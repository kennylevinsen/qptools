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
	sync.Mutex
	Content    []byte
	Offset     int64
	Readable   bool
	Writable   bool
	AppendOnly bool
}

// Seek changes the offset of the handle.
func (h *DetachedHandle) Seek(offset int64, whence int) (int64, error) {
	h.Lock()
	defer h.Unlock()
	if !h.Readable && !h.Writable {
		return 0, errors.New("file not open")
	}
	length := int64(len(h.Content))
	switch whence {
	case 0:
	case 1:
		offset = h.Offset + offset
	case 2:
		offset = length + offset
	default:
		return h.Offset, errors.New("invalid whence value")
	}

	if offset < 0 {
		return h.Offset, errors.New("negative seek invalid")
	}

	if offset > int64(len(h.Content)) {
		offset = int64(len(h.Content))
	}

	h.Offset = offset
	return h.Offset, nil
}

// Read reads from the current offset.
func (h *DetachedHandle) Read(p []byte) (int, error) {
	h.Lock()
	defer h.Unlock()
	if !h.Readable {
		return 0, errors.New("file not open for read")
	}
	maxRead := int64(len(p))
	remaining := int64(len(h.Content)) - h.Offset
	if maxRead > remaining {
		maxRead = remaining
	}

	copy(p, h.Content[h.Offset:maxRead+h.Offset])
	h.Offset += maxRead
	return int(maxRead), nil
}

// Write writes at the current offset.
func (h *DetachedHandle) Write(p []byte) (int, error) {
	h.Lock()
	defer h.Unlock()

	if !h.Writable {
		return 0, errors.New("file not open for write")
	}

	if h.AppendOnly {
		h.Offset = int64(len(h.Content))
	}
	wlen := int64(len(p))
	l := int(wlen + h.Offset)

	if l > cap(h.Content) {
		c := l * 2
		if l < 10240 {
			c = 10240
		}
		b := make([]byte, l, c)
		copy(b, h.Content[:h.Offset])
		h.Content = b
	} else if l > len(h.Content) {
		h.Content = h.Content[:l]
	}

	copy(h.Content[h.Offset:], p)

	h.Offset += wlen
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
