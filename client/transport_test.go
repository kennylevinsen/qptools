package client

import (
	"testing"

	"github.com/kennylevinsen/qp"
)

func TestTagAllocation(t *testing.T) {
	transport := NewTransport(nil)

	// Allocate everything.
	for i := 0; i < 0xFFFF; i++ {
		tag, err := transport.Tag()
		if err != nil {
			t.Fatalf("transport tag allocator failed: %v", err)
		}
		if tag == qp.NOTAG {
			t.Fatalf("transport tag allocator allocated NOTAG on iteration %d", i)
		}
	}

	// Test that everything is allocated.
	if _, err := transport.Tag(); err != ErrTagPoolDepleted {
		t.Fatalf("expected ErrTagPoolDepleted, got %v", err)
	}

	// Try to take NOTAG.
	if err := transport.TakeTag(qp.NOTAG); err != nil {
		t.Fatalf("allocating NOTAG failed: %v", err)
	}

	// Try to take a taken tag.
	if err := transport.TakeTag(0); err != ErrTagInUse {
		t.Fatalf("expected ErrTagInUse, got %v", err)
	}

	// Try to take another taken tag.
	if err := transport.TakeTag(qp.NOTAG); err != ErrTagInUse {
		t.Fatalf("expected ErrTagInUse, got %v", err)
	}

	// Ditch all tags.
	for i := 0; i < 0x10000; i++ {
		transport.Ditch(qp.Tag(i))
	}

	// Try to take the first tag again.
	if err := transport.TakeTag(0); err != nil {
		t.Fatalf("allocating tag 0 failed: %v", err)
	}
}

func TestMessageReception(t *testing.T) {
	transport := NewTransport(nil)

	tag, err := transport.Tag()
	if err != nil {
		t.Fatalf("could not allocate tag: %v", err)
	}
	rversion := &qp.VersionResponse{Tag: tag}

	// Try a valid message
	if err := transport.received(rversion); err != nil {
		t.Fatalf("message reception on valid tag failed: %v", err)
	}

	// Try the tag again - this should fail
	if err := transport.received(rversion); err != ErrNoSuchTag {
		t.Fatalf("expected ErrNoSuchTag, got: %v", err)
	}
}
