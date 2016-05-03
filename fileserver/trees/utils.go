package trees

import (
	"errors"
	"io"
	"sync"

	"github.com/joushou/qp"
)

// Common errors.
var (
	ErrPermissionDenied  = errors.New("permission denied")
	ErrFileAlreadyExists = errors.New("file already exists")
	ErrNoSuchFile        = errors.New("no such file")
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
	Open(user string, mode qp.OpenMode) (ReadWriteAtCloser, error)

	// Qid returns the qid of the file.
	Qid() (qp.Qid, error)

	// Stat returns the stat structure of the file.
	Stat() (qp.Stat, error)

	// SetLength sets the length of the file, if possible.
	SetLength(user string, length uint64) error

	// SetName sets the name of the file. This must only be called from the
	// Dir's Rename method, to ensure agreement between dir and file.
	SetName(user, name string) error

	// SetOwner changes the user and group of the file.
	SetOwner(user, UID, GID string) error

	// SetMode changes the mode and permissions of the file.
	SetMode(user string, mode qp.FileMode) error

	// IsDir returns whether or not the file is a directory.
	IsDir() (bool, error)

	// CanRemove returns if the file can be removed. An example of a negative
	// response would be a directory with content.
	CanRemove() (bool, error)

	// Arrived is called whenever a file is walked to, allowing access time to be
	// updated. It returns a file and an error, permitting the walk to be
	// "magic", with the file changing the walk destination. Unless a file
	// intends to be magic, it should update its access time and return itself
	// with a nil error.
	Arrived(user string) (File, error)
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

// ReadWriteAtCloser is an interface that allows reading and writing at offset,
// as well as closing.
type ReadWriteAtCloser interface {
	io.ReaderAt
	io.WriterAt
	io.Closer
}

// Authenticator describes a handle that implements authentication service.
type Authenticator interface {
	ReadWriteAtCloser

	// Authenticated informs if the user is authenticated to the service.
	Authenticated(user, service string) (bool, error)
}

// Lister allows for ListHandle to read the directory entries, so that a
// directory does not have to implement reading.
type Lister interface {
	Dir

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

func PermCheck(owner, group bool, permissions qp.FileMode, mode qp.OpenMode) bool {
	var offset uint8
	if group {
		offset = 3
	}
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
