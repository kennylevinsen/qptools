package trees

import (
	"errors"
	"sync"
	"time"

	"github.com/joushou/qp"
)

// MagicWalk allows a file to override the returned file on walk.
type MagicWalk interface {
	MagicWalk(user string) (File, error)
}

// RAMTree represents an in-memory directory. Create on this directory will
// add an in-memory SyntheticFile. It is capable of containing any item
// implementing the File interface, in-memory or not. It supports basic
// permission checking at owner and global, but not group level due to not
// having a group database. Access and modified time is kept track of as well.
type RAMTree struct {
	sync.RWMutex
	tree        map[string]File
	id          uint64
	name        string
	user        string
	group       string
	muser       string
	version     uint32
	atime       time.Time
	mtime       time.Time
	permissions qp.FileMode
	opens       uint
}

func (t *RAMTree) Qid() (qp.Qid, error) {
	return qp.Qid{
		Type:    qp.QTDIR,
		Version: t.version,
		Path:    t.id,
	}, nil
}

func (t *RAMTree) Name() (string, error) {
	t.RLock()
	defer t.RUnlock()
	if t.name == "" {
		return "/", nil
	}
	return t.name, nil
}

func (t *RAMTree) WriteStat(s qp.Stat) error {
	t.Lock()
	defer t.Unlock()
	t.name = s.Name
	t.user = s.UID
	t.group = s.GID
	t.permissions = s.Mode
	t.atime = time.Now()
	t.mtime = time.Now()
	t.version++
	return nil
}

func (t *RAMTree) Stat() (qp.Stat, error) {
	t.RLock()
	defer t.RUnlock()
	q, err := t.Qid()
	if err != nil {
		return qp.Stat{}, err
	}
	n, err := t.Name()
	if err != nil {
		return qp.Stat{}, err
	}
	return qp.Stat{
		Qid:   q,
		Mode:  t.permissions | qp.DMDIR,
		Name:  n,
		UID:   t.user,
		GID:   t.group,
		MUID:  t.muser,
		Atime: uint32(t.atime.Unix()),
		Mtime: uint32(t.mtime.Unix()),
	}, nil
}

func (t *RAMTree) Accessed(_ OpenFile) {
	t.Lock()
	defer t.Unlock()
	t.atime = time.Now()
}

func (t *RAMTree) Modified(_ OpenFile) {
	t.Lock()
	defer t.Unlock()
	t.mtime = time.Now()
	t.atime = t.mtime
	t.version++
}

func (t *RAMTree) Closed(_ OpenFile) {
	t.Lock()
	defer t.Unlock()
	t.opens--
}

func (t *RAMTree) List(user string) ([]qp.Stat, error) {
	t.RLock()
	defer t.RUnlock()
	owner := t.user == user

	if !permCheck(owner, t.permissions, qp.OREAD) {
		return nil, errors.New("access denied")
	}

	var s []qp.Stat
	for _, i := range t.tree {
		y, err := i.Stat()
		if err != nil {
			return nil, err
		}
		s = append(s, y)
	}
	return s, nil
}

func (t *RAMTree) Open(user string, mode qp.OpenMode) (OpenFile, error) {
	t.Lock()
	defer t.Unlock()
	owner := t.user == user

	if !permCheck(owner, t.permissions, mode) {
		return nil, errors.New("access denied")
	}

	t.atime = time.Now()
	t.opens++
	return &ListOpenTree{
		t:    t,
		user: user,
	}, nil
}

func (t *RAMTree) CanRemove() (bool, error) {
	return len(t.tree) == 0, nil
}

func (t *RAMTree) Create(user, name string, perms qp.FileMode) (File, error) {
	t.Lock()
	defer t.Unlock()
	owner := t.user == user
	if !permCheck(owner, t.permissions, qp.OWRITE) {
		return nil, errors.New("access denied")
	}

	_, ok := t.tree[name]
	if ok {
		return nil, errors.New("file already exists")
	}

	var d File
	if perms&qp.DMDIR != 0 {
		perms = perms & (^qp.FileMode(0777) | (t.permissions & 0777))
		d = NewSyntheticFile(name, perms, t.user, t.group)
	} else {
		perms = perms & (^qp.FileMode(0666) | (t.permissions & 0666))
		d = NewSyntheticFile(name, perms, t.user, t.group)
	}

	t.tree[name] = d

	t.mtime = time.Now()
	t.atime = t.mtime
	t.version++
	return d, nil
}

func (t *RAMTree) Add(name string, f File) error {
	t.Lock()
	defer t.Unlock()
	_, ok := t.tree[name]
	if ok {
		return errors.New("file already exists")
	}
	t.tree[name] = f
	t.mtime = time.Now()
	t.atime = t.mtime
	t.version++
	return nil
}

func (t *RAMTree) Rename(user, oldname, newname string) error {
	t.Lock()
	defer t.Unlock()
	_, ok := t.tree[oldname]
	if !ok {
		return errors.New("file not found")
	}
	_, ok = t.tree[newname]
	if ok {
		return errors.New("file already exists")
	}

	owner := t.user == user
	if !permCheck(owner, t.permissions, qp.OWRITE) {
		return errors.New("access denied")
	}

	t.tree[newname] = t.tree[oldname]
	delete(t.tree, oldname)
	return nil
}

func (t *RAMTree) Remove(user, name string) error {
	t.Lock()
	defer t.Unlock()
	owner := t.user == user
	if !permCheck(owner, t.permissions, qp.OWRITE) {
		return errors.New("access denied")
	}

	if f, ok := t.tree[name]; ok {
		rem, err := f.CanRemove()
		if err != nil {
			return err
		}
		if !rem {
			return errors.New("file could not be removed")
		}
		delete(t.tree, name)
		t.mtime = time.Now()
		t.atime = t.mtime
		t.version++
		return nil
	}

	return errors.New("no such file")
}

func (t *RAMTree) Walk(user, name string) (File, error) {
	t.Lock()
	defer t.Unlock()
	owner := t.user == user
	if !permCheck(owner, t.permissions, qp.OEXEC) {
		return nil, errors.New("access denied")
	}

	t.atime = time.Now()
	for i := range t.tree {
		if i == name {
			if o, ok := t.tree[i].(MagicWalk); ok {
				// We could potentially end up trying to retake these locks.
				t.Unlock()
				r, err := o.MagicWalk(user)
				t.Lock()
				return r, err
			}
			return t.tree[i], nil
		}
	}
	return nil, nil
}

func (t *RAMTree) IsDir() (bool, error) {
	return true, nil
}

func NewRAMTree(name string, permissions qp.FileMode, user, group string) *RAMTree {
	return &RAMTree{
		name:        name,
		tree:        make(map[string]File),
		permissions: permissions,
		user:        user,
		group:       group,
		muser:       user,
		id:          nextID(),
		atime:       time.Now(),
		mtime:       time.Now(),
	}
}
