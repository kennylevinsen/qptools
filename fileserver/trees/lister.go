package trees

import "errors"

type ListOpenTree struct {
	t      Lister
	user   string
	buffer []byte
	offset int64
}

func (ot *ListOpenTree) Update() error {
	s, err := ot.t.List(ot.user)
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
	ot.buffer = bb
	return nil
}

func (ot *ListOpenTree) Seek(offset int64, whence int) (int64, error) {
	if ot.t == nil {
		return 0, errors.New("file not open")
	}
	length := int64(len(ot.buffer))
	switch whence {
	case 0:
	case 1:
		offset = ot.offset + offset
	case 2:
		offset = length + offset
	default:
		return ot.offset, errors.New("invalid whence value")
	}

	if offset < 0 {
		return ot.offset, errors.New("negative seek invalid")
	}

	if offset != 0 && offset != ot.offset {
		return ot.offset, errors.New("seek to other than 0 on dir illegal")
	}

	ot.offset = offset
	err := ot.Update()
	if err != nil {
		return 0, err
	}
	if a, ok := ot.t.(AccessLogger); ok {
		a.Accessed(ot)
	}
	return ot.offset, nil
}

func (ot *ListOpenTree) Read(p []byte) (int, error) {
	if ot.t == nil {
		return 0, errors.New("file not open")
	}
	rlen := int64(len(p))
	if rlen > int64(len(ot.buffer))-ot.offset {
		rlen = int64(len(ot.buffer)) - ot.offset
	}
	copy(p, ot.buffer[ot.offset:rlen+ot.offset])
	ot.offset += rlen
	if a, ok := ot.t.(AccessLogger); ok {
		a.Accessed(ot)
	}
	return int(rlen), nil
}

func (ot *ListOpenTree) Write(p []byte) (int, error) {
	return 0, errors.New("cannot write to directory")
}

func (ot *ListOpenTree) Close() error {
	if a, ok := ot.t.(AccessLogger); ok {
		a.Closed(ot)
	}
	ot.t = nil
	return nil
}
