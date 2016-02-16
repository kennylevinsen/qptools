package trees

import (
	"errors"
	"sync"
)

// ListHandle is a special handle used to list directories that implement the
// Lister interface. It also provides access logging for directories
// implementing AccessLogger.
type ListHandle struct {
	sync.Mutex

	Dir    Lister
	User   string
	buffer []byte
}

func (h *ListHandle) update() error {
	s, err := h.Dir.List(h.User)
	if err != nil {
		return err
	}
	bb := make([]byte, 0, len(s)*64)
	for _, i := range s {
		b, err := i.MarshalBinary()
		bb = append(bb, b...)
		if err != nil {
			return err
		}
	}
	h.buffer = bb
	return nil
}

// ReadAt reads the directory listing.
func (h *ListHandle) ReadAt(p []byte, offset int64) (int, error) {
	h.Lock()
	defer h.Unlock()
	if h.Dir == nil {
		return 0, errors.New("file not open")
	}

	if a, ok := h.Dir.(AccessLogger); ok {
		a.Accessed()
	}

	// TODO(kl): Enforce seek restrictions
	if offset == 0 {
		err := h.update()
		if err != nil {
			return 0, err
		}
	} else if offset > int64(len(h.buffer)) {
		return 0, nil
	}

	rlen := int64(len(p))
	if rlen > int64(len(h.buffer))-offset {
		rlen = int64(len(h.buffer)) - offset
	}
	copy(p, h.buffer[offset:rlen+offset])
	return int(rlen), nil
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
	h.Dir = nil
	return nil
}
