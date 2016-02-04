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

// RawClient implements a 9P client exposing a blocking rpc-like Send call,
// while still processing responses out-of-order. A blocking Send can be
// unblocked by using Ditch.
//
// Apart from managing available tags, matching them on response and
// encoding/decoding messages, RawClient does not concern itself with any
// protocol details.
type RawClient struct {
	encoder *qp.Encoder
	decoder *qp.Decoder

	// error stores any run-loop errors.
	error     error
	errorLock sync.Mutex

	// queue stores a response channel per message tag.
	queue     map[qp.Tag]chan qp.Message
	queueLock sync.RWMutex

	// nextTag is the next tag to allocate.
	nextTag qp.Tag

	// msgsize is the maximum set message size.
	msgsize uint32
}

func (c *RawClient) die(err error) error {
	c.errorLock.Lock()
	defer c.errorLock.Unlock()
	if c.error == nil {
		c.error = err
	}
	return c.error
}

func (c *RawClient) setChannel(t qp.Tag) chan qp.Message {
	c.queueLock.Lock()
	defer c.queueLock.Unlock()

	if _, exists := c.queue[t]; exists {
		return nil
	}

	x := make(chan qp.Message, 1)
	c.queue[t] = x

	return x
}

// getChannel retrieves the response channel for a given tag.
func (c *RawClient) getChannel(t qp.Tag) chan qp.Message {
	c.queueLock.RLock()
	defer c.queueLock.RUnlock()

	if m, exists := c.queue[t]; exists {
		return m
	}

	return nil
}

// killChannel closes a response channel and removes it from the map.
func (c *RawClient) killChannel(t qp.Tag) error {
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

// received is the callback for messages decoded from the io.ReadWriter.
func (c *RawClient) received(m qp.Message) error {
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

// Tag allocates and retrieves a tag. The tag MUST be used or ditched
// afterwards, in order not to leak tags.
func (c *RawClient) Tag() (qp.Tag, error) {
	c.queueLock.Lock()
	defer c.queueLock.Unlock()

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

	c.queue[t] = make(chan qp.Message, 1)

	return t, nil
}

// Send sends a message and retrieves the response.
func (c *RawClient) Send(m qp.Message) (qp.Message, error) {
	t := m.GetTag()
	ch := c.getChannel(t)
	if ch == nil {
		if t == qp.NOTAG {
			ch = c.setChannel(qp.NOTAG)
			if ch == nil {
				return nil, ErrTagInUse
			}
		} else {
			return nil, ErrNoSuchTag
		}
	}

	err := c.encoder.WriteMessage(m)
	if err != nil {
		return nil, c.die(err)
	}

	return <-ch, nil
}

// Ditch throws a pending request state away, and unblocks any Send on the tag.
func (c *RawClient) Ditch(t qp.Tag) error {
	return c.killChannel(t)
}

// PendingTags return tags that are currently in use.
func (c *RawClient) PendingTags() []qp.Tag {
	c.queueLock.RLock()
	defer c.queueLock.RUnlock()
	var t []qp.Tag
	for tag := range c.queue {
		t = append(t, tag)
	}
	return t
}

// SetProtocol changes the protocol used for message de/encoding. Calling this
// method is only safe if there is a guarantee that the client and server will
// not exchange any messages before the change has been complete, to ensure
// proper de/encoding of messages. Calling this method while the Decoder is
// decoding messages may results in a decoder error, terminating the run loop.
func (c *RawClient) SetProtocol(p qp.Protocol) {
	// We take the writeLock to ensure that no one is trying to write
	// messages.
	c.encoder.Protocol = p
	c.decoder.Protocol = p
}

// SetReadWriter sets the io.ReadWriter used for message de/encoding. It is
// safe assuming the safe conditions for SetProtocol are present and the
// greedy decoding flag have not been enabled.
func (c *RawClient) SetReadWriter(rw io.ReadWriter) {
	c.encoder.Writer = rw
	c.decoder.Reader = rw
}

// SetMessageSize sets the maximum message size. It does not reallocate the
// decoding buffer.
func (c *RawClient) SetMessageSize(ms uint32) {
	c.msgsize = ms
	c.encoder.MessageSize = ms
	c.decoder.MessageSize = ms
	c.decoder.Reset()
}

// SetGreedyDecoding sets the greedy flag for the decoder. When set, changing
// the readwriter becomes unsafe.
func (c *RawClient) SetGreedyDecoding(g bool) {
	c.decoder.Greedy = g
}

// MessageSize returns the maximum message size.
func (c *RawClient) MessageSize() uint32 {
	return c.msgsize
}

// Serve executes the response parsing loop.
func (c *RawClient) Serve() error {
	for {
		m, err := c.decoder.NextMessage()
		if err != nil {
			return c.die(err)
		}
		if err = c.received(m); err != nil {
			return c.die(err)
		}
	}
}

// Stop terminates the reading loop.
func (c *RawClient) Stop() {
	c.die(nil)
}

// NewRawClient creates a new client.
func NewRawClient(rw io.ReadWriter) *RawClient {
	c := &RawClient{
		queue:   make(map[qp.Tag]chan qp.Message),
		msgsize: 16384,
	}

	c.encoder = &qp.Encoder{
		Protocol:    qp.NineP2000,
		Writer:      rw,
		MessageSize: 16384,
	}

	c.decoder = &qp.Decoder{
		Protocol:    qp.NineP2000,
		Reader:      rw,
		MessageSize: 16384,
	}

	return c
}
