package trees

import (
	"errors"
	"sync"
	"time"

	"github.com/joushou/qp"
)

type RAMOpenFile struct {
	offset int64
	f      *RAMFile
}

func (of *RAMOpenFile) Seek(offset int64, whence int) (int64, error) {
	if of.f == nil {
		return 0, errors.New("file not open")
	}
	of.f.RLock()
	defer of.f.RUnlock()
	length := int64(len(of.f.content))
	switch whence {
	case 0:
	case 1:
		offset = of.offset + offset
	case 2:
		offset = length + offset
	default:
		return of.offset, errors.New("invalid whence value")
	}

	if offset < 0 {
		return of.offset, errors.New("negative seek invalid")
	}

	if offset > int64(len(of.f.content)) {
		offset = int64(len(of.f.content))
	}

	of.offset = offset
	of.f.atime = time.Now()
	return of.offset, nil
}

func (of *RAMOpenFile) Read(p []byte) (int, error) {
	if of.f == nil {
		return 0, errors.New("file not open")
	}
	of.f.RLock()
	defer of.f.RUnlock()
	maxRead := int64(len(p))
	remaining := int64(len(of.f.content)) - of.offset
	if maxRead > remaining {
		maxRead = remaining
	}

	copy(p, of.f.content[of.offset:maxRead+of.offset])
	of.offset += maxRead
	of.f.atime = time.Now()
	return int(maxRead), nil
}

func (of *RAMOpenFile) Write(p []byte) (int, error) {
	if of.f == nil {
		return 0, errors.New("file not open")
	}

	of.f.Lock()
	defer of.f.Unlock()

	// TODO(kl): handle append-only
	wlen := int64(len(p))
	l := int(wlen + of.offset)

	if l > cap(of.f.content) {
		c := l * 2
		if l < 10240 {
			c = 10240
		}
		b := make([]byte, l, c)
		copy(b, of.f.content[:of.offset])
		of.f.content = b
	} else if l > len(of.f.content) {
		of.f.content = of.f.content[:l]
	}

	copy(of.f.content[of.offset:], p)

	of.offset += wlen
	of.f.mtime = time.Now()
	of.f.atime = of.f.mtime
	of.f.version++
	return int(wlen), nil
}

func (of *RAMOpenFile) Close() error {
	of.f.Lock()
	defer of.f.Unlock()
	of.f.opens--
	of.f = nil
	return nil
}

// RAMFile represents an in-memory file. It is usually created by calling
// Create on a RAMTree. Like RAMTree, it contains basic permission checking at
// owner and global, but not group level due to not having a group database.
// Access and modified time is kept track of as well.
type RAMFile struct {
	sync.RWMutex
	content     []byte
	id          uint64
	name        string
	user        string
	group       string
	muser       string
	atime       time.Time
	mtime       time.Time
	version     uint32
	permissions qp.FileMode
	opens       uint
}

func (f *RAMFile) Name() (string, error) {
	return f.name, nil
}

func (f *RAMFile) Qid() (qp.Qid, error) {
	return qp.Qid{
		Type:    qp.QTFILE,
		Version: f.version,
		Path:    f.id,
	}, nil
}

func (f *RAMFile) WriteStat(s qp.Stat) error {
	if s.Length != ^uint64(0) {
		if s.Length > uint64(len(f.content)) {
			return errors.New("cannot extend length")
		}
		f.content = f.content[:s.Length]
	}
	f.name = s.Name
	f.user = s.UID
	f.group = s.GID
	f.permissions = s.Mode
	f.mtime = time.Now()
	f.atime = f.mtime
	f.version++
	return nil
}

func (f *RAMFile) Stat() (qp.Stat, error) {
	q, err := f.Qid()
	if err != nil {
		return qp.Stat{}, err
	}
	n, err := f.Name()
	if err != nil {
		return qp.Stat{}, err
	}
	return qp.Stat{
		Qid:    q,
		Mode:   f.permissions,
		Name:   n,
		Length: uint64(len(f.content)),
		UID:    f.user,
		GID:    f.user,
		MUID:   f.user,
		Atime:  uint32(f.atime.Unix()),
		Mtime:  uint32(f.mtime.Unix()),
	}, nil
}

func (f *RAMFile) Open(user string, mode qp.OpenMode) (OpenFile, error) {
	owner := f.user == user
	if !permCheck(owner, f.permissions, mode) {
		return nil, errors.New("access denied")
	}

	f.atime = time.Now()

	f.Lock()
	defer f.Unlock()
	f.opens++

	return &RAMOpenFile{f: f}, nil
}

func (f *RAMFile) IsDir() (bool, error) {
	return false, nil
}

func (f *RAMFile) CanRemove() (bool, error) {
	return true, nil
}

func NewRAMFile(name string, permissions qp.FileMode, user, group string) *RAMFile {
	return &RAMFile{
		name:        name,
		permissions: permissions,
		user:        user,
		group:       group,
		muser:       user,
		id:          nextID(),
		atime:       time.Now(),
		mtime:       time.Now(),
	}
}
