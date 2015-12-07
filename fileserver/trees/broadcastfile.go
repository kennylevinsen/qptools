package trees

import (
	"errors"
	"sync"
	"time"

	"github.com/joushou/qp"
)

var ErrTerminatedRead = errors.New("read terminated")

type BroadcastOpenFile struct {
	sync.RWMutex
	f *BroadcastFile

	queue     [][]byte
	queueCond *sync.Cond

	curbuf     []byte
	curbufLock sync.RWMutex
}

func (of *BroadcastOpenFile) Seek(int64, int) (int64, error) {
	return 0, nil
}

func (of *BroadcastOpenFile) Read(p []byte) (int, error) {
	of.RLock()
	defer of.RUnlock()
	if of.f == nil {
		return 0, errors.New("file not open")
	}
	of.curbufLock.Lock()
	defer of.curbufLock.Unlock()
	if len(of.curbuf) == 0 {
		var err error
		of.curbuf, err = of.fetch()
		if err != nil {
			return 0, err
		}
	}

	m := len(of.curbuf)
	if len(p) < m {
		m = len(p)
	}

	copy(p, of.curbuf[:m])
	of.curbuf = of.curbuf[m:]

	return m, nil
}

func (of *BroadcastOpenFile) Write(p []byte) (int, error) {
	of.RLock()
	defer of.RUnlock()
	if of.f == nil {
		return 0, errors.New("file not open")
	}
	of.f.Push(p)
	return len(p), nil
}

func (of *BroadcastOpenFile) fetch() ([]byte, error) {
	// We always read from the channel to avoid complex locking schemes.
	of.queueCond.L.Lock()
	defer of.queueCond.L.Unlock()

	if len(of.queue) == 0 {
		of.queueCond.Wait()
	}

	// If we got woken and the queue is still zero, assume we're not wanted
	// anymore.
	if len(of.queue) == 0 {
		return nil, ErrTerminatedRead
	}

	b := of.queue[0]
	of.queue = of.queue[1:]
	return b, nil
}

func (of *BroadcastOpenFile) push(b []byte) {
	of.queueCond.L.Lock()
	defer of.queueCond.L.Unlock()
	of.queue = append(of.queue, b)
	of.queueCond.Signal()
}

func (of *BroadcastOpenFile) Close() error {
	of.Lock()
	defer of.Unlock()
	of.queueCond.Broadcast()
	if of.f != nil {
		of.f.deregister(of)
		of.f = nil
	}
	return nil
}

func NewBroadcastOpenFile(f *BroadcastFile) *BroadcastOpenFile {
	var l sync.Mutex
	return &BroadcastOpenFile{
		f:         f,
		queueCond: sync.NewCond(&l),
	}
}

type BroadcastFile struct {
	sync.RWMutex
	files []*BroadcastOpenFile

	id          uint64
	name        string
	user        string
	group       string
	muser       string
	atime       time.Time
	mtime       time.Time
	version     uint32
	permissions qp.FileMode
}

func (f *BroadcastFile) Name() (string, error) {
	return f.name, nil
}

func (f *BroadcastFile) Qid() (qp.Qid, error) {
	return qp.Qid{
		Type:    qp.QTFILE,
		Version: f.version,
		Path:    f.id,
	}, nil
}

func (f *BroadcastFile) WriteStat(s qp.Stat) error {
	f.name = s.Name
	f.user = s.UID
	f.group = s.GID
	f.permissions = s.Mode
	f.mtime = time.Now()
	f.atime = f.mtime
	f.version++
	return nil
}
func (f *BroadcastFile) Stat() (qp.Stat, error) {
	q, err := f.Qid()
	if err != nil {
		return qp.Stat{}, err
	}
	return qp.Stat{
		Qid:    q,
		Mode:   f.permissions,
		Name:   f.name,
		Length: 0,
		UID:    f.user,
		GID:    f.group,
		MUID:   f.muser,
		Atime:  uint32(f.atime.Unix()),
		Mtime:  uint32(f.mtime.Unix()),
	}, nil
}

func (f *BroadcastFile) Open(user string, mode qp.OpenMode) (OpenFile, error) {
	owner := f.user == user
	if !permCheck(owner, f.permissions, mode) {
		return nil, errors.New("access denied")
	}

	f.Lock()
	defer f.Unlock()
	f.atime = time.Now()

	x := NewBroadcastOpenFile(f)

	f.files = append(f.files, x)
	return x, nil
}

func (f *BroadcastFile) IsDir() (bool, error) {
	return false, nil
}

func (f *BroadcastFile) CanRemove() (bool, error) {
	return true, nil
}

func (f *BroadcastFile) Push(b []byte) error {
	f.Lock()
	f.version++
	f.Unlock()
	f.RLock()
	defer f.RUnlock()
	for _, of := range f.files {
		of.push(b)
	}
	return nil
}

func (f *BroadcastFile) deregister(of *BroadcastOpenFile) {
	f.Lock()
	defer f.Unlock()
	for i := range f.files {
		if f.files[i] == of {
			f.files = append(f.files[:i], f.files[i+1:]...)
			return
		}
	}
}

func NewBroadcastFile(name string, permissions qp.FileMode, user, group string) *BroadcastFile {
	return &BroadcastFile{
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
