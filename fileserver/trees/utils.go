package trees

import (
	"io"
	"sync"

	"github.com/joushou/qp"
)

var (
	globalIDLock sync.Mutex
	globalID     uint64 = 0
)

func nextID() uint64 {
	globalIDLock.Lock()
	defer globalIDLock.Unlock()
	id := globalID
	globalID++
	return id
}

type File interface {
	Name() (string, error)

	Open(user string, mode qp.OpenMode) (ReadWriteSeekCloser, error)

	Qid() (qp.Qid, error)
	Stat() (qp.Stat, error)
	WriteStat(qp.Stat) error

	IsDir() (bool, error)
	CanRemove() (bool, error)
}

type Dir interface {
	File

	Walk(user, name string) (File, error)
	Create(user, name string, perms qp.FileMode) (File, error)
	Remove(user, name string) error
	Rename(user, oldname, newname string) error
}

type ReadWriteSeekCloser interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
}

type Lister interface {
	List(user string) ([]qp.Stat, error)
}

type AccessLogger interface {
	Accessed()
	Modified()
	Closed()
}

func permCheck(owner bool, permissions qp.FileMode, mode qp.OpenMode) bool {
	var offset uint8
	if owner {
		offset = 6
	}

	switch mode & 3 {
	case qp.OREAD:
		return permissions&(1<<(2+offset)) != 0
	case qp.OWRITE:
		return permissions&(1<<(1+offset)) != 0
	case qp.ORDWR:
		return (permissions&(1<<(2+offset)) != 0) && (permissions&(1<<(1+offset)) != 0)
	case qp.OEXEC:
		return permissions&(1<<offset) != 0
	default:
		return false
	}
}
