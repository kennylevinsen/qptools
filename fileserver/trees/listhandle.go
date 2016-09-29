package trees

import (
	"errors"
	"sync"
)

// BUG(kl): ListHandle.ReadAt permits arbitrary seeking in the directory
// listing, which is out of spec.

// ListHandle is a special handle used to list directories that implement the
// Lister interface. It also provides access logging for directories
// implementing AccessLogger.
type ListHandle struct {
	sync.Mutex

	Dir  Lister
	User string

	buffer [][]byte
}

func (h *ListHandle) update() error {
	s, err := h.Dir.List(h.User)
	if err != nil {
		return err
	}

	var bb [][]byte
	for _, i := range s {
		b := make([]byte, i.EncodedSize())
		if err := i.Marshal(b); err != nil {
			return err
		}
		bb = append(bb, b)
	}

	h.buffer = bb
	return nil
}

// ReadAt reads the directory listing.
func (h *ListHandle) ReadAt(p []byte, offset int64) (int, error) {
	if offset == 0 {
		h.Lock()
		err := h.update()
		h.Unlock()
		if err != nil {
			return 0, err
		}
	}

	if a, ok := h.Dir.(AccessLogger); ok {
		a.Accessed()
	}

	h.Lock()
	defer h.Unlock()
	var copied int
	for {
		if len(h.buffer) == 0 {
			break
		}

		b := h.buffer[0]

		if copied+len(b) > len(p) {
			if copied == 0 {
				return 0, errors.New("read: message size too small: stat does not fit")
			}
			break
		}
		copy(p[copied:], b)
		copied += len(b)
		h.buffer = h.buffer[1:]
	}

	return copied, nil
}

// WriteAt returns an error, as writing to a directory is not legal.
func (h *ListHandle) WriteAt(p []byte, offset int64) (int, error) {
	return 0, errors.New("cannot write to directory")
}

// Close closes the handle.
func (h *ListHandle) Close() error {
	if a, ok := h.Dir.(AccessLogger); ok {
		a.Closed()
	}
	return nil
}
