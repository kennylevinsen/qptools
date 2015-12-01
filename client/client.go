package client

import (
	"errors"
	"io"
	"sync"

	"github.com/joushou/qp"
)

var (
	// ErrTagInUse indicates that a tag is already being used for still active
	// request.
	ErrTagInUse = errors.New("tag already in use")

	// ErrNoSuchTag indicates that the tag in a response from a server was not
	// known to be associated with a pending request.
	ErrNoSuchTag = errors.New("tag does not exist")

	// ErrInvalidResponse indicates that the response type did not make sense
	// for the request.
	ErrInvalidResponse = errors.New("invalid response")

	ErrTagPoolDepleted = errors.New("tag pool depleted")
)

type Client struct {
	rw io.ReadWriter

	p         qp.Protocol
	queueLock sync.RWMutex
	queue     map[qp.Tag]chan qp.Message
	writeLock sync.RWMutex
	nextTag   qp.Tag
}

func (c *Client) getChannel(t qp.Tag) (chan qp.Message, error) {
	c.queueLock.Lock()
	defer c.queueLock.Unlock()

	if _, exists := c.queue[t]; exists {
		return nil, ErrTagInUse
	}

	ch := make(chan qp.Message, 1)
	c.queue[t] = ch
	return ch, nil
}

func (c *Client) write(t qp.Tag, m qp.Message) error {
	c.writeLock.Lock()
	defer c.writeLock.Unlock()

	if err := c.p.Encode(c.rw, m); err != nil {
		if _, ok := c.queue[t]; ok {
			delete(c.queue, t)
		}
		return err
	}
	return nil
}

func (c *Client) received(m qp.Message) error {
	c.queueLock.Lock()
	defer c.queueLock.Unlock()
	t := m.GetTag()
	if ch, ok := c.queue[t]; ok {
		ch <- m
		delete(c.queue, t)
		return nil
	}
	return ErrNoSuchTag
}

func (c *Client) Tag() qp.Tag {
	c.queueLock.RLock()
	defer c.queueLock.RUnlock()

	exists := true
	var t qp.Tag
	for i := 0; exists && i < 0xFFFF; i++ {
		t = c.nextTag
		c.nextTag++
		if c.nextTag == qp.NOTAG {
			c.nextTag = 0
		}

		_, exists = c.queue[t]
	}

	if !exists {
		return nil, ErrTagPoolDepleted
	}

	return t
}

func (c *Client) Send(m qp.Message) (qp.Message, error) {
	t := m.GetTag()
	ch, err := c.getChannel(t)
	if err != nil {
		return nil, err
	}

	c.write(t, m)
	resp := <-ch
	if resp == nil {
		return nil, nil
	}

	return resp, nil
}

func (c *Client) Ditch(t qp.Tag) error {
	c.queueLock.Lock()
	defer c.queueLock.Unlock()
	if _, exists := c.queue[t]; !exists {
		return nil, ErrNoSuchTag
	}
	delete(c.queue, t)
	return nil
}

func (c *Client) SetCodec(p qp.Protocol) {
	c.p = p
}

func (c *Client) Start() error {
	for {
		m, err := c.p.Decode(c.rw)
		if err != nil {
			return err
		}
		c.received(m)
	}
}
