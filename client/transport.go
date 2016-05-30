package client

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"

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

	// ErrUnexpectedTagPoolDepleted indicates that all possible tags are
	// currently in use, but that this should not have been the case.
	ErrUnexpectedTagPoolDepleted = errors.New("unexpected tag pool depletion")

	// ErrStopped indicate that the client was stopped.
	ErrStopped = errors.New("stopped")
)

// Transport implements a 9P client exposing a blocking rpc-like Send call,
// while still processing responses out-of-order. A blocking Send can be
// unblocked by using Ditch.
//
// Apart from managing available tags, matching them on response and
// encoding/decoding messages, RawClient does not concern itself with any
// protocol details.
type Transport struct {
	encoder *qp.Encoder
	decoder *qp.Decoder

	// error stores any run-loop errors.
	error     error
	errorLock sync.Mutex
	errorCnt  uint32

	// queue stores a response channel per message tag.
	queue     map[qp.Tag]chan qp.Message
	queueLock sync.RWMutex
	nextTag   qp.Tag

	// msgsize is the maximum set message size.
	msgsize uint32
}

func (c *Transport) die(err error) error {
	c.errorLock.Lock()
	defer c.errorLock.Unlock()
	if c.error == nil {
		c.error = err
	}
	atomic.AddUint32(&c.errorCnt, 1)
	return c.error
}

// getChannel retrieves the response channel for a given tag.
func (c *Transport) getChannel(t qp.Tag) chan qp.Message {
	c.queueLock.RLock()
	defer c.queueLock.RUnlock()

	if m, exists := c.queue[t]; exists {
		return m
	}

	return nil
}

// killChannel closes a response channel and removes it from the map.
func (c *Transport) killChannel(t qp.Tag) error {
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
func (c *Transport) received(m qp.Message) error {
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
func (c *Transport) Tag() (qp.Tag, error) {
	c.queueLock.Lock()
	defer c.queueLock.Unlock()

	// No need to loop 0xFFFF times to figure out that the thing is full.
	if len(c.queue) > 0xFFFF {
		// No tags available.
		return qp.NOTAG, ErrTagPoolDepleted
	} else if len(c.queue) == 0xFFFF {
		if _, exists := c.queue[qp.NOTAG]; !exists {
			// The only tag available is qp.NOTAG.
			return qp.NOTAG, ErrTagPoolDepleted
		}
	}

	// There is a tag available *somewhere*. Find it.
	var t qp.Tag
	var exists bool
	for i := 0; i < 0xFFFF; i++ {
		t = c.nextTag
		c.nextTag++
		if c.nextTag == qp.NOTAG {
			c.nextTag = 0
		}

		if _, exists = c.queue[t]; !exists {
			c.queue[t] = make(chan qp.Message, 1)
			return t, nil
		}
	}

	return qp.NOTAG, ErrUnexpectedTagPoolDepleted
}

// TakeTag allocates a specific tag if available. The tag MUST be used or
// ditched afterwards, in order not to leak tags.
func (c *Transport) TakeTag(t qp.Tag) error {
	c.queueLock.Lock()
	defer c.queueLock.Unlock()

	if _, exists := c.queue[t]; exists {
		return ErrTagInUse
	}

	c.queue[t] = make(chan qp.Message, 1)
	return nil
}

// Send sends a message and retrieves the response.
func (c *Transport) Send(m qp.Message) (qp.Message, error) {
	t := m.GetTag()
	ch := c.getChannel(t)
	if ch == nil {
		return nil, ErrNoSuchTag
	}

	err := c.encoder.WriteMessage(m)
	if err != nil {
		return nil, c.die(err)
	}

	return <-ch, nil
}

// Ditch throws a pending request state away, and unblocks any Send on the tag.
// It does not send Tflush, and should only be used after such a message has
// been sent, or after a connection has been deemed dead.
func (c *Transport) Ditch(t qp.Tag) error {
	return c.killChannel(t)
}

// PendingTags return tags that are currently in use.
func (c *Transport) PendingTags() []qp.Tag {
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
func (c *Transport) SetProtocol(p qp.Protocol) {
	// We take the writeLock to ensure that no one is trying to write
	// messages.
	c.encoder.Protocol = p
	c.decoder.Protocol = p
}

// SetReadWriter sets the io.ReadWriter used for message de/encoding. It is
// safe assuming the safe conditions for SetProtocol are present and the
// greedy decoding flag have not been enabled.
func (c *Transport) SetReadWriter(rw io.ReadWriter) {
	c.encoder.Writer = rw
	c.decoder.Reader = rw
}

// SetMessageSize sets the maximum message size. It does not reallocate the
// decoding buffer.
func (c *Transport) SetMessageSize(ms uint32) {
	c.msgsize = ms
	c.encoder.MessageSize = ms
	c.decoder.MessageSize = ms
	c.decoder.Reset()
}

// SetGreedyDecoding sets the greedy flag for the decoder. When set, changing
// the readwriter becomes unsafe.
func (c *Transport) SetGreedyDecoding(g bool) {
	c.decoder.Greedy = g
}

// MessageSize returns the maximum message size.
func (c *Transport) MessageSize() uint32 {
	return c.msgsize
}

// Serve executes the response parsing loop.
func (c *Transport) Serve() error {
	for atomic.LoadUint32(&c.errorCnt) == 0 {
		m, err := c.decoder.ReadMessage()
		if err != nil {
			return c.die(err)
		}
		if err = c.received(m); err != nil {
			return c.die(err)
		}
	}

	c.errorLock.Lock()
	defer c.errorLock.Unlock()
	return c.error
}

// Stop terminates the reading loop.
func (c *Transport) Stop() {
	c.die(nil)
}

// NewTransport creates a new client.
func NewTransport(rw io.ReadWriter) *Transport {
	c := &Transport{
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
