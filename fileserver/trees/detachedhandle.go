package trees

import (
	"errors"
	"sync"
)

// DetachedHandle is like SyntheticHandle, but instead of enquiring about
// content from the file itself, DetachedHandle manipulates a local content
// slice, detached from the original file. This is useful for making things
// like files with unique content for each opener. Access does not affect
// Atime, Mtime, MUID or Version of the original file.
type DetachedHandle struct {
	sync.RWMutex
	Content    []byte
	Readable   bool
	Writable   bool
	AppendOnly bool
}

// ReadAt reads from the provided offset.
func (h *DetachedHandle) ReadAt(p []byte, offset int64) (int, error) {
	h.RLock()
	defer h.RUnlock()
	if !h.Readable {
		return 0, errors.New("file not open for read")
	}
	if offset > int64(len(h.Content)) {
		return 0, nil
	}

	maxRead := int64(len(p))
	remaining := int64(len(h.Content)) - offset
	if maxRead > remaining {
		maxRead = remaining
	}

	copy(p, h.Content[offset:maxRead+offset])
	return int(maxRead), nil
}

// WriteAt writes at the provided offset.
func (h *DetachedHandle) WriteAt(p []byte, offset int64) (int, error) {
	h.Lock()
	defer h.Unlock()

	if !h.Writable {
		return 0, errors.New("file not open for write")
	}

	if h.AppendOnly || offset > int64(len(h.Content)) {
		offset = int64(len(h.Content))
	}

	wlen := int64(len(p))
	l := int(wlen + offset)

	if l > cap(h.Content) {
		c := l * 2
		if l < 10240 {
			c = 10240
		}
		b := make([]byte, l, c)
		copy(b, h.Content[:offset])
		h.Content = b
	} else if l > len(h.Content) {
		h.Content = h.Content[:l]
	}

	copy(h.Content[offset:], p)

	return int(wlen), nil
}

// Close closes the handle.
func (h *DetachedHandle) Close() error {
	h.Lock()
	defer h.Unlock()
	h.Readable = false
	h.Writable = false
	return nil
}

// NewDetachedHandle creates a new DetachedHandle.
func NewDetachedHandle(cnt []byte, readable, writable, appendOnly bool) *DetachedHandle {
	return &DetachedHandle{
		Content:    cnt,
		Readable:   readable,
		Writable:   writable,
		AppendOnly: appendOnly,
	}
}
