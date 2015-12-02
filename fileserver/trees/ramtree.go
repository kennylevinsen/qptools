package trees

import (
	"bytes"
	"errors"
	"sync"
	"time"

	"github.com/joushou/qp"
)

type RAMOpenTree struct {
	t      *RAMTree
	buffer []byte
	offset int64
}

func (ot *RAMOpenTree) update() error {
	ot.t.RLock()
	defer ot.t.RUnlock()
	buf := new(bytes.Buffer)
	for _, i := range ot.t.tree {
		y, err := i.Stat()
		if err != nil {
			return err
		}
		y.Encode(buf)
	}
	ot.buffer = buf.Bytes()
	return nil
}

func (ot *RAMOpenTree) Seek(offset int64, whence int) (int64, error) {
	if ot.t == nil {
		return 0, errors.New("file not open")
	}
	ot.t.RLock()
	defer ot.t.RUnlock()
	length := int64(len(ot.buffer))
	switch whence {
	case 0:
	case 1:
		offset = ot.offset + offset
	case 2:
		offset = length + offset
	default:
		return ot.offset, errors.New("invalid whence value")
	}

	if offset < 0 {
		return ot.offset, errors.New("negative seek invalid")
	}

	if offset != 0 && offset != ot.offset {
		return ot.offset, errors.New("seek to other than 0 on dir illegal")
	}

	ot.offset = offset
	err := ot.update()
	if err != nil {
		return 0, err
	}
	ot.t.atime = time.Now()
	return ot.offset, nil
}

func (ot *RAMOpenTree) Read(p []byte) (int, error) {
	if ot.t == nil {
		return 0, errors.New("file not open")
	}
	ot.t.RLock()
	defer ot.t.RUnlock()
	rlen := int64(len(p))
	if rlen > int64(len(ot.buffer))-ot.offset {
		rlen = int64(len(ot.buffer)) - ot.offset
	}
	copy(p, ot.buffer[ot.offset:rlen+ot.offset])
	ot.offset += rlen
	ot.t.atime = time.Now()
	return int(rlen), nil
}

func (ot *RAMOpenTree) Write(p []byte) (int, error) {
	return 0, errors.New("cannot write to directory")
}

func (ot *RAMOpenTree) Close() error {
	ot.t.Lock()
	defer ot.t.Unlock()
	ot.t.opens--
	ot.t = nil
	return nil
}

type RAMTree struct {
	sync.RWMutex
	parent      Dir
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

func (t *RAMTree) SetParent(d Dir) error {
	t.parent = d
	return nil
}

func (t *RAMTree) Parent() (Dir, error) {
	if t.parent == nil {
		return t, nil
	}
	return t.parent, nil
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
	return &RAMOpenTree{t: t}, nil
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
		d = NewRAMTree(name, perms, t.user, t.group)
	} else {
		perms = perms & (^qp.FileMode(0666) | (t.permissions & 0666))
		d = NewRAMFile(name, perms, t.user, t.group)
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

func (t *RAMTree) Walk(user string, name string) (File, error) {
	t.Lock()
	defer t.Unlock()
	owner := t.user == user
	if !permCheck(owner, t.permissions, qp.OEXEC) {
		return nil, errors.New("access denied")
	}

	t.atime = time.Now()
	for i := range t.tree {
		if i == name {
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
