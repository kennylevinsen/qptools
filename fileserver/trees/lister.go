package trees

import "errors"

type ListHandle struct {
	t      Lister
	user   string
	buffer []byte
	offset int64
}

func (h *ListHandle) update() error {
	s, err := h.t.List(h.user)
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

func (h *ListHandle) Seek(offset int64, whence int) (int64, error) {
	if h.t == nil {
		return 0, errors.New("file not open")
	}
	length := int64(len(h.buffer))
	switch whence {
	case 0:
	case 1:
		offset = h.offset + offset
	case 2:
		offset = length + offset
	default:
		return h.offset, errors.New("invalid whence value")
	}

	if offset < 0 {
		return h.offset, errors.New("negative seek invalid")
	}

	if offset != 0 && offset != h.offset {
		return h.offset, errors.New("seek to other than 0 on dir illegal")
	}

	h.offset = offset
	err := h.update()
	if err != nil {
		return 0, err
	}
	if a, ok := h.t.(AccessLogger); ok {
		a.Accessed()
	}
	return h.offset, nil
}

func (h *ListHandle) Read(p []byte) (int, error) {
	if h.t == nil {
		return 0, errors.New("file not open")
	}
	rlen := int64(len(p))
	if rlen > int64(len(h.buffer))-h.offset {
		rlen = int64(len(h.buffer)) - h.offset
	}
	copy(p, h.buffer[h.offset:rlen+h.offset])
	h.offset += rlen
	if a, ok := h.t.(AccessLogger); ok {
		a.Accessed()
	}
	return int(rlen), nil
}

func (h *ListHandle) Write(p []byte) (int, error) {
	return 0, errors.New("cannot write to directory")
}

func (h *ListHandle) Close() error {
	if a, ok := h.t.(AccessLogger); ok {
		a.Closed()
	}
	h.t = nil
	return nil
}
