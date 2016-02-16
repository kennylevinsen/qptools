package trees

import (
	"errors"
	"sync"

	"github.com/joushou/qp"
)

// LockedHandle is a handle wrapper for use by LockedFile.
type LockedHandle struct {
	ReadWriteAtCloser
	Locker sync.Locker
}

// Close closes the wrapped handle, and unlocks the LockedFile.
func (of *LockedHandle) Close() error {
	err := of.ReadWriteAtCloser.Close()
	of.Locker.Unlock()
	return err
}

// LockedFile provides a lock on open for read and write, using a
// sync.RWMutex. The result is that open will block if file is opened for
// write, or if its opened for read and attempted opened for write.
type LockedFile struct {
	File
	OpenLock sync.RWMutex
}

// Open returns a LockedHandle if the open was permitted, holding either the
// read or the write lock, depending on the opening mode.
func (f *LockedFile) Open(user string, mode qp.OpenMode) (ReadWriteAtCloser, error) {
	of, err := f.File.Open(user, mode)
	if err != nil {
		return of, err
	}
	read := mode&3 == qp.OREAD || mode&3 == qp.OEXEC || mode&3 == qp.ORDWR
	write := mode&3 == qp.OWRITE || mode&3 == qp.ORDWR

	var l sync.Locker
	switch {
	case write:
		l = f.OpenLock.RLocker()
	case read:
		l = &f.OpenLock
	default:
		of.Close()
		return nil, errors.New("locked file can only be opened for read or write")
	}

	l.Lock()
	return &LockedHandle{
		ReadWriteAtCloser: of,
		Locker:            l,
	}, nil
}

// NewLockedFile wraps a file in a LockedFile.
func NewLockedFile(f File) *LockedFile {
	return &LockedFile{
		File: f,
	}
}
