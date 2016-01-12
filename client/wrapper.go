package client

import (
	"errors"
	"sync/atomic"

	"github.com/joushou/qp"
)

// WrappedFid provides implementations of various common I/O interfaces, using
// ReadOnce/WriteOnce of the Fid interface.
type WrappedFid struct {
	Fid
	offset int64
}

// Seek changes the current offset.
func (wf *WrappedFid) Seek(offset int64, whence int) (int64, error) {
	cur := atomic.LoadInt64(&wf.offset)
	switch whence {
	case 0:
	case 1:
		offset += cur
	case 2:
		return cur, errors.New("cannot seek from end of file")
	}

	if offset < 0 {
		return cur, errors.New("negative offset invalid")
	}
	atomic.StoreInt64(&wf.offset, offset)
	return offset, nil
}

// Read reads to the provided slice from the current offset.
func (wf *WrappedFid) Read(p []byte) (int, error) {
	cur := atomic.LoadInt64(&wf.offset)
	n, err := wf.ReadAt(p, cur)
	atomic.AddInt64(&wf.offset, int64(n))
	return n, err
}

// ReadAt reads to the provided byte slice from the specified offset,
// splitting the read into multiple messages as needed.
func (wf *WrappedFid) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("cannot read from negative offset")
	}
	o := uint64(off)
	read := 0
	toread := len(p)
	for read < toread {
		b, err := wf.Fid.ReadOnce(o, uint32(toread))
		if err != nil {
			return read, err
		}
		copy(p[read:], b)
		read += len(b)
		o += uint64(len(b))
	}

	return read, nil
}

// ReadAll reads the full content of a file, starting at offset 0.
func (wf *WrappedFid) ReadAll() ([]byte, error) {
	var (
		p    []byte
		b    []byte
		o    uint64
		read int
		err  error
	)
	for {
		b, err = wf.Fid.ReadOnce(o, 16*1026)
		if err != nil {
			return p, err
		}
		if len(b) == 0 {
			break
		}
		copy(p[read:], b)
		read += len(b)
		o += uint64(len(b))
	}

	return p, nil
}

// Write writes to the provided slice at the current offset.
func (wf *WrappedFid) Write(p []byte) (int, error) {
	cur := atomic.LoadInt64(&wf.offset)
	n, err := wf.WriteAt(p, cur)
	atomic.AddInt64(&wf.offset, int64(n))
	return n, err
}

// WriteAt writes the provided byte slice to the specified offset, splitting
// the write into multiple messages as needed.
func (wf *WrappedFid) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("cannot read from negative offset")
	}
	ms := wf.Fid.MessageSize() - qp.WriteOverhead
	o := uint64(off)
	for len(p) > 0 {
		n, err := wf.Fid.WriteOnce(o, p[:ms])
		if err != nil {
			return int(o) - int(off), err
		}
		p = p[n:]
		o += uint64(n)
	}

	return int(o) - int(off), nil
}

// WriteAll writes the entire slice to the file, starting at offset 0.
func (wf *WrappedFid) WriteAll(p []byte) error {
	ms := wf.Fid.MessageSize() - qp.WriteOverhead
	var o uint64
	for len(p) > 0 {
		n, err := wf.Fid.WriteOnce(o, p[:ms])
		if err != nil {
			return err
		}
		p = p[n:]
		o += uint64(n)
	}

	return nil
}

// Close clunks the fid.
func (wf *WrappedFid) Close() error {
	return wf.Fid.Clunk()
}
