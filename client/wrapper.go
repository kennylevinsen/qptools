package client

import (
	"errors"
	"io"
	"sync/atomic"

	"github.com/joushou/qp"
	"github.com/joushou/qptools/utils"
)

// WrappedFid provides implementations of various common I/O interfaces, using
// ReadOnce/WriteOnce of the Fid interface.
type WrappedFid struct {
	offset int64
	Fid
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

// Read implements io.Reader.
func (wf *WrappedFid) Read(p []byte) (int, error) {
	cur := atomic.LoadInt64(&wf.offset)
	n, err := wf.ReadAt(p, cur)
	atomic.AddInt64(&wf.offset, int64(n))
	return n, err
}

// ReadAt implements io.ReaderAt.
func (wf *WrappedFid) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("cannot read from negative offset")
	}

	b, err := wf.Fid.ReadOnce(uint64(off), uint32(len(p)))
	copy(p, b)
	if err == nil && len(b) == 0 {
		err = io.EOF
	}
	return len(b), err
}

// ReadAll reads the full content of a file, starting at offset 0.
func (wf *WrappedFid) ReadAll() ([]byte, error) {
	var (
		p, b []byte
		o    uint64
		err  error
	)

	for {
		b, err = wf.Fid.ReadOnce(o, wf.Fid.MessageSize())
		if err != nil || len(b) == 0 {
			break
		}
		p = append(p, b...)
		o += uint64(len(b))
	}

	return p, err
}

// Readdir reads a directory listing from the fid.
func (wf *WrappedFid) Readdir() ([]qp.Stat, error) {
	b, err := wf.ReadAll()
	if err != nil {
		return nil, err
	}

	return utils.Readdir(b)
}

// Write implements io.Writer.
func (wf *WrappedFid) Write(p []byte) (int, error) {
	cur := atomic.LoadInt64(&wf.offset)
	n, err := wf.WriteAt(p, cur)
	atomic.AddInt64(&wf.offset, int64(n))
	return n, err
}

// WriteAt implements io.WriterAt.
func (wf *WrappedFid) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("cannot read from negative offset")
	}

	var (
		ms  = wf.Fid.MessageSize() - qp.WriteOverhead
		o   = uint64(off)
		n   uint32
		err error
	)

	if ms > uint32(len(p)) {
		ms = uint32(len(p))
	}

	for len(p) > 0 {
		n, err = wf.Fid.WriteOnce(o, p[:ms])
		if err != nil {
			break
		}
		if n == 0 {
			err = io.ErrShortWrite
			break
		}
		p = p[n:]
		o += uint64(n)
	}

	return int(o) - int(off), err
}

// Close clunks the fid.
func (wf *WrappedFid) Close() error {
	return wf.Fid.Clunk()
}

// WrapFid returns a *WrappedFid.
func WrapFid(f Fid) *WrappedFid {
	return &WrappedFid{Fid: f}
}
