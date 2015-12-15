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

	// ErrTagPoolDepleted indicates that all possible tags are currently in
	// use.
	ErrTagPoolDepleted = errors.New("tag pool depleted")

	// ErrStopped indicate that the client was stopped.
	ErrStopped = errors.New("stopped")
)

type Client struct {
	RW        io.ReadWriter
	Proto     qp.Protocol
	dead      error
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

func (c *Client) killChannel(t qp.Tag) error {
	c.queueLock.Lock()
	defer c.queueLock.Unlock()
	ch, exists := c.queue[t]
	if !exists {
		return ErrNoSuchTag
	}
	ch <- nil
	close(ch)
	delete(c.queue, t)
	return nil
}

func (c *Client) write(t qp.Tag, m qp.Message) error {
	c.writeLock.Lock()
	defer c.writeLock.Unlock()
	return c.Proto.Encode(c.RW, m)
}

func (c *Client) received(m qp.Message) error {
	c.queueLock.Lock()
	defer c.queueLock.Unlock()
	t := m.GetTag()
	if ch, ok := c.queue[t]; ok {
		ch <- m
		close(ch)
		delete(c.queue, t)
		return nil
	}
	return ErrNoSuchTag
}

// Tag retrieves the next valid tag.
func (c *Client) Tag() (qp.Tag, error) {
	c.queueLock.RLock()
	defer c.queueLock.RUnlock()

	// No need to loop 0xFFFF times to figure out that the thing is full.
	if len(c.queue) == 0x10000 {
		return 0, ErrTagPoolDepleted
	}

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

	// We can fail this check if the only valid tag was qp.NOTAG.
	if exists {
		return 0, ErrTagPoolDepleted
	}

	return t, nil
}

// Send sends a message and retrieves the response.
func (c *Client) Send(m qp.Message) (qp.Message, error) {
	t := m.GetTag()
	ch, err := c.getChannel(t)
	if err != nil {
		c.killChannel(t)
		return nil, err
	}

	err = c.write(t, m)
	if err != nil {
		c.dead = err
		return nil, err
	}

	return <-ch, nil
}

// Ditch throws a pending request state away.
func (c *Client) Ditch(t qp.Tag) error {
	return c.killChannel(t)
}

// PendingTags return tags that are currently in use.
func (c *Client) PendingTags() []qp.Tag {
	c.queueLock.RLock()
	defer c.queueLock.RUnlock()
	var t []qp.Tag
	for tag := range c.queue {
		t = append(t, tag)
	}
	return t
}

// Start starts the response parsing loop.
func (c *Client) Start() error {
	for c.dead == nil {
		m, err := c.Proto.Decode(c.RW)
		if err != nil {
			c.dead = err
			return err
		}
		c.received(m)
	}
	return c.dead
}

func (c *Client) Stop() {
	c.dead = ErrStopped
}

// New creates a new client.
func New(rw io.ReadWriter) *Client {
	return &Client{
		RW:    rw,
		Proto: qp.NineP2000,
		queue: make(map[qp.Tag]chan qp.Message),
	}
}
