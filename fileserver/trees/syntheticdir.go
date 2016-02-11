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

func (d *SyntheticDir) Qid() (qp.Qid, error) {
	return qp.Qid{
		Type:    qp.QTDIR,
		Version: d.Version,
		Path:    d.ID,
	}, nil
}

func (d *SyntheticDir) Name() (string, error) {
	d.RLock()
	defer d.RUnlock()
	if d.Filename == "" {
		return "/", nil
	}
	return d.Filename, nil
}

func (d *SyntheticDir) SetLength(user string, length uint64) error {
	if length != 0 {
		return errors.New("cannot set length of directory")
	}
	return nil
}

func (d *SyntheticDir) SetName(user, name string) error {
	d.Lock()
	defer d.Unlock()

	d.Filename = name
	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
	return nil
}

func (d *SyntheticDir) SetOwner(user, UID, GID string) error {
	if !d.CanOpen(user, qp.OWRITE) {
		return errors.New("permission denied")
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

func (d *SyntheticDir) SetMode(user string, mode qp.FileMode) error {
	if user != d.UID || !d.CanOpen(user, qp.OWRITE) {
		return errors.New("permission denied")
	}
	d.Lock()
	defer d.Unlock()

	d.Permissions = mode | qp.DMDIR
	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
	return nil
}

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

func (d *SyntheticDir) Accessed() {
	d.Lock()
	defer d.Unlock()
	d.Atime = time.Now()
}

func (d *SyntheticDir) Modified() {
	d.Lock()
	defer d.Unlock()
	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
}

func (d *SyntheticDir) Closed() {
	d.Lock()
	defer d.Unlock()
	d.Opens--
}

func (d *SyntheticDir) List(user string) ([]qp.Stat, error) {
	d.RLock()
	defer d.RUnlock()
	owner := d.UID == user

	if !PermCheck(owner, false, d.Permissions, qp.OREAD) {
		return nil, errors.New("access denied")
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

func (d *SyntheticDir) CanOpen(user string, mode qp.OpenMode) bool {
	d.RLock()
	defer d.RUnlock()
	owner := d.UID == user
	return PermCheck(owner, false, d.Permissions, mode)
}

func (d *SyntheticDir) Open(user string, mode qp.OpenMode) (ReadWriteSeekCloser, error) {
	if !d.CanOpen(user, mode) {
		return nil, errors.New("access denied")
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

func (d *SyntheticDir) CanRemove() (bool, error) {
	return len(d.Tree) == 0, nil
}

func (d *SyntheticDir) Create(user, name string, perms qp.FileMode) (File, error) {
	d.Lock()
	defer d.Unlock()
	owner := d.UID == user
	if !PermCheck(owner, false, d.Permissions, qp.OWRITE) {
		return nil, errors.New("access denied")
	}

	_, ok := d.Tree[name]
	if ok {
		return nil, errors.New("file already exists")
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

func (d *SyntheticDir) Add(name string, f File) error {
	d.Lock()
	defer d.Unlock()
	_, ok := d.Tree[name]
	if ok {
		return errors.New("file already exists")
	}
	d.Tree[name] = f
	d.Mtime = time.Now()
	d.Atime = d.Mtime
	d.Version++
	return nil
}

func (d *SyntheticDir) Rename(user, oldname, newname string) error {
	d.Lock()
	defer d.Unlock()
	_, ok := d.Tree[oldname]
	if !ok {
		return errors.New("file not found")
	}
	_, ok = d.Tree[newname]
	if ok {
		return errors.New("file already exists")
	}

	if !d.CanOpen(user, qp.OWRITE) {
		return errors.New("permission denied")
	}

	elem := d.Tree[oldname]
	if err := elem.SetName(user, newname); err != nil {
		return err
	}
	d.Tree[newname] = elem
	delete(d.Tree, oldname)
	return nil
}

func (d *SyntheticDir) Remove(user, name string) error {
	d.Lock()
	defer d.Unlock()
	owner := d.UID == user
	if !PermCheck(owner, false, d.Permissions, qp.OWRITE) {
		return errors.New("access denied")
	}

	f, exists := d.Tree[name]
	if !exists {
		return errors.New("no such file")
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

func (d *SyntheticDir) Walk(user, name string) (File, error) {
	d.Lock()
	defer d.Unlock()
	owner := d.UID == user
	if !PermCheck(owner, false, d.Permissions, qp.OEXEC) {
		return nil, errors.New("access denied")
	}

	d.Atime = time.Now()
	x, exists := d.Tree[name]
	if !exists {
		return nil, nil
	}

	return x, nil
}

func (d *SyntheticDir) Arrived(user string) (File, error) {
	d.Lock()
	defer d.Unlock()
	d.Atime = time.Now()
	return d, nil
}

func (d *SyntheticDir) IsDir() (bool, error) {
	return true, nil
}

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
