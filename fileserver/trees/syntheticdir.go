package trees

import (
	"errors"
	"sync"
	"time"

	"github.com/joushou/qp"
)

// SyntheticDir represents an in-memory directory. Create on this directory
// will add an in-memory SyntheticFile. It is capable of containing any item
// implementing the File interface, in-memory or not. It supports basic
// permission checking at owner and global, but not group level due to not
// having a group database. Access and modified time is kept track of as well.
type SyntheticDir struct {
	sync.RWMutex
	ID          uint64
	Filename    string
	UID         string
	GID         string
	MUID        string
	Atime       time.Time
	Mtime       time.Time
	Version     uint32
	Permissions qp.FileMode
	Opens       uint
	Tree        map[string]File
}

// Name returns the name of the synthetic dir. This cannot fail.
func (d *SyntheticDir) Name() (string, error) {
	d.RLock()
	defer d.RUnlock()
	if d.Filename == "" {
		return "/", nil
	}
	return d.Filename, nil
}

// Qid returns the qid of the synthetic dir. This cannot fail.
func (d *SyntheticDir) Qid() (qp.Qid, error) {
	return qp.Qid{
		Type:    qp.QTDIR,
		Version: d.Version,
		Path:    d.ID,
	}, nil
}

// SetLength returns an error if length is non-zero, as it is not legal to set
// the length of a directory.
func (d *SyntheticDir) SetLength(user string, length uint64) error {
	if length != 0 {
		return errors.New("cannot set length of directory")
	}
	return nil
}

// SetName sets the name of the dir. This must only be called from Rename on a
// directory.
func (d *SyntheticDir) SetName(user, name string) error {
	d.Lock()
	defer d.Unlock()

	d.Filename = name
	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
	return nil
}

// SetOwner sets the owner.
func (d *SyntheticDir) SetOwner(user, UID, GID string) error {
	if !d.CanOpen(user, qp.OWRITE) {
		return ErrPermissionDenied
	}
	d.Lock()
	defer d.Unlock()

	if UID != "" {
		d.UID = UID
	}
	if GID != "" {
		d.GID = GID
	}
	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
	return nil
}

// SetMode sets the mode and permissions.
func (d *SyntheticDir) SetMode(user string, mode qp.FileMode) error {
	if user != d.UID || !d.CanOpen(user, qp.OWRITE) {
		return ErrPermissionDenied
	}
	d.Lock()
	defer d.Unlock()

	d.Permissions = mode | qp.DMDIR
	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
	return nil
}

// Stat returns the stat struct. This cannot fail.
func (d *SyntheticDir) Stat() (qp.Stat, error) {
	d.RLock()
	defer d.RUnlock()
	q, _ := d.Qid()
	return qp.Stat{
		Qid:   q,
		Mode:  d.Permissions | qp.DMDIR,
		Name:  d.Filename,
		UID:   d.UID,
		GID:   d.GID,
		MUID:  d.MUID,
		Atime: uint32(d.Atime.Unix()),
		Mtime: uint32(d.Mtime.Unix()),
	}, nil
}

// Accessed updates the access time.
func (d *SyntheticDir) Accessed() {
	d.Lock()
	defer d.Unlock()
	d.Atime = time.Now()
}

// Modified updates the modified time.
func (d *SyntheticDir) Modified() {
	d.Lock()
	defer d.Unlock()
	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
}

// Closed decreases the open count.
func (d *SyntheticDir) Closed() {
	d.Lock()
	defer d.Unlock()
	d.Opens--
}

// List returns a stat struct for each file in the dir.
func (d *SyntheticDir) List(user string) ([]qp.Stat, error) {
	d.RLock()
	defer d.RUnlock()
	owner := d.UID == user

	if !PermCheck(owner, false, d.Permissions, qp.OREAD) {
		return nil, ErrPermissionDenied
	}

	var s []qp.Stat
	for _, i := range d.Tree {
		y, err := i.Stat()
		if err != nil {
			return nil, err
		}
		s = append(s, y)
	}
	return s, nil
}

// CanOpen checks if a user may perform the requested open.
func (d *SyntheticDir) CanOpen(user string, mode qp.OpenMode) bool {
	d.RLock()
	defer d.RUnlock()
	owner := d.UID == user
	return PermCheck(owner, false, d.Permissions, mode)
}

// Open returns a ListHandle if the open was permitted.
func (d *SyntheticDir) Open(user string, mode qp.OpenMode) (ReadWriteAtCloser, error) {
	if !d.CanOpen(user, mode) {
		return nil, ErrPermissionDenied
	}

	d.Lock()
	defer d.Unlock()
	d.Atime = time.Now()
	d.Opens++
	return &ListHandle{
		Dir:  d,
		User: user,
	}, nil
}

// IsDir returns true. This cannot fail.
func (d *SyntheticDir) IsDir() (bool, error) {
	return true, nil
}

// CanRemove returns true if the directory is empty. This cannot fail.
func (d *SyntheticDir) CanRemove() (bool, error) {
	return len(d.Tree) == 0, nil
}

// Arrived returns the directory itself, after updating the access time.
func (d *SyntheticDir) Arrived(user string) (File, error) {
	d.Lock()
	defer d.Unlock()
	d.Atime = time.Now()
	return nil, nil
}

// Create adds either a synthetic dir or file, depending on the file
// permissions, if the create is permitted and a file with that name doesn't
// already exist. It also updates mtime, mtime and version for the dir.
func (d *SyntheticDir) Create(user, name string, perms qp.FileMode) (File, error) {
	d.Lock()
	defer d.Unlock()
	owner := d.UID == user
	if !PermCheck(owner, false, d.Permissions, qp.OWRITE) {
		return nil, ErrPermissionDenied
	}

	_, ok := d.Tree[name]
	if ok {
		return nil, ErrFileAlreadyExists
	}

	var f File
	if perms&qp.DMDIR != 0 {
		perms = perms & (^qp.FileMode(0777) | (d.Permissions & 0777))
		f = NewSyntheticDir(name, perms, d.UID, d.GID)
	} else {
		perms = perms & (^qp.FileMode(0666) | (d.Permissions & 0666))
		f = NewSyntheticFile(name, perms, d.UID, d.GID)
	}

	d.Tree[name] = f

	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
	return f, nil
}

// Add adds a file directly to the dir.
func (d *SyntheticDir) Add(name string, f File) error {
	d.Lock()
	defer d.Unlock()
	_, ok := d.Tree[name]
	if ok {
		return ErrFileAlreadyExists
	}
	d.Tree[name] = f
	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
	return nil
}

// Rename renames a file, assuming the rename is permitted, the file exists and
// the new name does not collide with another file. It updates atime, mtime and
// version.
func (d *SyntheticDir) Rename(user, oldname, newname string) error {
	d.Lock()
	defer d.Unlock()
	_, ok := d.Tree[oldname]
	if !ok {
		return ErrNoSuchFile
	}
	_, ok = d.Tree[newname]
	if ok {
		return ErrFileAlreadyExists
	}

	owner := d.UID == user
	if !PermCheck(owner, false, d.Permissions, qp.OWRITE) {
		return ErrPermissionDenied
	}

	elem := d.Tree[oldname]
	if err := elem.SetName(user, newname); err != nil {
		return err
	}
	d.Tree[newname] = elem
	delete(d.Tree, oldname)
	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
	return nil
}

// Remove removes a file if it exists, and is permitted both by the directory
// permissions and by the file itself. It updates atime, mtime and version.
func (d *SyntheticDir) Remove(user, name string) error {
	d.Lock()
	defer d.Unlock()
	owner := d.UID == user
	if !PermCheck(owner, false, d.Permissions, qp.OWRITE) {
		return ErrPermissionDenied
	}

	f, exists := d.Tree[name]
	if !exists {
		return ErrNoSuchFile
	}

	rem, err := f.CanRemove()
	if err != nil {
		return err
	}
	if !rem {
		return errors.New("file could not be removed")
	}
	delete(d.Tree, name)
	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
	return nil
}

// Walk goes to a file, if permitted.
func (d *SyntheticDir) Walk(user, name string) (File, error) {
	d.Lock()
	defer d.Unlock()
	owner := d.UID == user
	if !PermCheck(owner, false, d.Permissions, qp.OEXEC) {
		return nil, ErrPermissionDenied
	}

	d.Atime = time.Now()
	x, exists := d.Tree[name]
	if !exists {
		return nil, nil
	}

	return x, nil
}

// NewSyntheticDir returns a new SyntheticDir.
func NewSyntheticDir(name string, permissions qp.FileMode, user, group string) *SyntheticDir {
	return &SyntheticDir{
		Filename:    name,
		Tree:        make(map[string]File),
		Permissions: permissions,
		UID:         user,
		GID:         group,
		MUID:        user,
		ID:          nextID(),
		Atime:       time.Now(),
		Mtime:       time.Now(),
	}
}
