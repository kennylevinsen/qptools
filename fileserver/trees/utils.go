package trees

import (
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

	Open(user string, mode qp.OpenMode) (OpenFile, error)

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

type OpenFile interface {
	Seek(offset int64, whence int) (int64, error)
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
}

type Lister interface {
	List(user string) ([]qp.Stat, error)
}

type AccessLogger interface {
	Accessed(OpenFile)
	Modified(OpenFile)
	Closed(OpenFile)
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
