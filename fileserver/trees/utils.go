package trees

import (
	"io"
	"sync"

	"github.com/joushou/qp"
)

var (
	globalIDLock sync.Mutex
	globalID     uint64
)

func nextID() uint64 {
	globalIDLock.Lock()
	defer globalIDLock.Unlock()
	id := globalID
	globalID++
	return id
}

// File is a node in the tree abstraction.
type File interface {
	// Name returns the name of the file.
	Name() (string, error)

	// Open returns a handle to the file in form of a ReadWriteSeekCloser in
	// the mode requested if the user is permitted to do so.
	Open(user string, mode qp.OpenMode) (ReadWriteSeekCloser, error)

	// QId returns the qid of the file.
	Qid() (qp.Qid, error)

	// Stat returns the stat structure of the file.
	Stat() (qp.Stat, error)

	// WriteStat changes the stat structure of the file.
	WriteStat(qp.Stat) error

	// IsDir returns whether or not the file is a directory.
	IsDir() (bool, error)

	// CanRemove returns if the file can be removed.
	CanRemove() (bool, error)
}

// Dir is a file that also sports directory features. Directory detection must
// not occur by asserting Dir, but should be done by using IsDir.
type Dir interface {
	File

	// Walk finds a file by name "file" and returns it if it exists and the
	// user is allowed to execute the directory. The name is the name of the
	// file, without any "/" in it.
	Walk(user, name string) (File, error)

	// Create creats a file of a default type defined by teh directory
	// implementation itself, with the permissions required and returns it if
	// the file does not already exist and the user is permitted to do so.
	Create(user, name string, perms qp.FileMode) (File, error)

	// Remove removes the file if it exists and the user is permitted to do so.
	Remove(user, name string) error

	// Rename renames the file in the local directory if the old name exists,
	// the new name does not already exist, and the user is permitted to do so.
	Rename(user, oldname, newname string) error
}

// ReadWriteSeekCloser is an interface that allows reading, writing, seeking
// and closing.
type ReadWriteSeekCloser interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
}

// Lister allows for ListHandle to read the directory entries, so that a
// directory does not have to implement reading.
type Lister interface {
	List(user string) ([]qp.Stat, error)
}

// AccessLogger defines a file that can log access
type AccessLogger interface {
	// Accessed logs access.
	Accessed()

	// Modified logs modification.
	Modified()

	// Closed logs closure.
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
