package trees

import (
	"errors"
	"sync"
	"time"

	"github.com/joushou/qp"
)

// SyntheticOpenFile implements locked R/W access to a SyntheticFile's
// internal Content byteslice. It updates atime, mtime, version and open
// count.
type SyntheticOpenFile struct {
	sync.Mutex
	offset     int64
	f          *SyntheticFile
	user       string
	read       bool
	write      bool
	appendOnly bool
}

func (of *SyntheticOpenFile) Seek(offset int64, whence int) (int64, error) {
	of.Lock()
	defer of.Unlock()
	if (!of.read && !of.write) || of.f == nil {
		return 0, errors.New("file not open")
	}
	of.f.RLock()
	defer of.f.RUnlock()

	cnt := of.f.Content
	length := int64(len(cnt))

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

	if offset > length {
		offset = length
	}

	of.offset = offset
	of.f.Atime = time.Now()
	return of.offset, nil
}

func (of *SyntheticOpenFile) Read(p []byte) (int, error) {
	of.Lock()
	defer of.Unlock()
	if !of.read || of.f == nil {
		return 0, errors.New("file not open for read")
	}

	of.f.RLock()
	defer of.f.RUnlock()

	cnt := of.f.Content
	maxRead := int64(len(p))
	remaining := int64(len(cnt)) - of.offset
	if maxRead > remaining {
		maxRead = remaining
	}

	copy(p, cnt[of.offset:maxRead+of.offset])
	of.offset += maxRead
	of.f.Atime = time.Now()
	return int(maxRead), nil
}

func (of *SyntheticOpenFile) Write(p []byte) (int, error) {
	of.Lock()
	defer of.Unlock()
	if !of.write || of.f == nil {
		return 0, errors.New("file not open for write")
	}

	of.f.Lock()
	defer of.f.Unlock()

	cnt := of.f.Content
	if of.appendOnly {
		of.offset = int64(len(cnt))
	}
	wlen := int64(len(p))
	l := int(wlen + of.offset)

	if l > cap(cnt) {
		c := l * 2
		if l < 10240 {
			c = 10240
		}
		b := make([]byte, l, c)
		copy(b, cnt[:of.offset])
		of.f.Content = b
	} else if l > len(cnt) {
		of.f.Content = cnt[:l]
	}

	copy(of.f.Content[of.offset:], p)

	of.offset += wlen
	of.f.Mtime = time.Now()
	of.f.Atime = of.f.Mtime
	of.f.MUID = of.user
	of.f.Version++
	return int(wlen), nil
}

func (of *SyntheticOpenFile) Close() error {
	of.Lock()
	defer of.Unlock()
	of.f.Lock()
	defer of.f.Unlock()
	of.f.Opens--
	of.f = nil
	return nil
}

// SyntheticROOpenFile is an OpenFile implementation that serves a static
// byte-slice for reads, rather than SyntheticFile's built-in slice. It's
// useful for making unique content available only to the user that opened the
// file.
type SyntheticROOpenFile struct {
	Content []byte
	offset  int64
}

func (of *SyntheticROOpenFile) Seek(offset int64, whence int) (int64, error) {
	length := int64(len(of.Content))
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

	if offset > int64(len(of.Content)) {
		offset = int64(len(of.Content))
	}

	of.offset = offset
	return of.offset, nil
}

func (of *SyntheticROOpenFile) Read(p []byte) (int, error) {
	maxRead := int64(len(p))
	remaining := int64(len(of.Content)) - of.offset
	if maxRead > remaining {
		maxRead = remaining
	}

	copy(p, of.Content[of.offset:maxRead+of.offset])
	of.offset += maxRead
	return int(maxRead), nil
}

func (of *SyntheticROOpenFile) Write(p []byte) (int, error) {
	return 0, errors.New("cannot write to session file")
}

func (of *SyntheticROOpenFile) Close() error {
	return nil
}

// NewSyntheticROOpenFile creates a new SyntheticROOpenFile.
func NewSyntheticROOpenFile(cnt []byte) *SyntheticROOpenFile {
	return &SyntheticROOpenFile{
		Content: cnt,
	}
}

// SynetheticFile is a File implementation that takes care of the more boring
// aspects of a file implementation, such as permission-handling and qid/stat
// generation. By default, it serves the Content slice through a
// SyntheticROOpenFile. In most cases, one would embed SyntheticFile and
// provide their own Open implementation for more interesting functionality.
type SyntheticFile struct {
	sync.RWMutex
	ID          uint64
	Filename    string
	UID         string
	GID         string
	MUID        string
	Atime       time.Time
	Mtime       time.Time
	Version     uint32
	Length      uint64
	FakeLength  bool
	Permissions qp.FileMode
	Content     []byte
	Opens       uint
}

func (f *SyntheticFile) Name() (string, error) {
	return f.Filename, nil
}

func (f *SyntheticFile) Qid() (qp.Qid, error) {
	return qp.Qid{
		Type:    qp.QTFILE,
		Version: f.Version,
		Path:    f.ID,
	}, nil
}

func (f *SyntheticFile) WriteStat(s qp.Stat) error {
	f.Lock()
	defer f.Unlock()
	if s.Length != ^uint64(0) {
		if s.Length > uint64(len(f.Content)) {
			return errors.New("cannot extend length")
		}
		if f.FakeLength {
			return errors.New("cannot change fake length")
		}
		f.Content = f.Content[:s.Length]
	}
	f.Filename = s.Name
	f.UID = s.UID
	f.GID = s.GID
	f.Permissions = s.Mode
	f.Mtime = time.Now()
	f.Atime = f.Mtime
	f.Version++
	return nil
}

func (f *SyntheticFile) Stat() (qp.Stat, error) {
	f.RLock()
	defer f.RUnlock()
	q, err := f.Qid()
	if err != nil {
		return qp.Stat{}, err
	}

	l := f.Length
	if !f.FakeLength {
		l = uint64(len(f.Content))
	}

	return qp.Stat{
		Qid:    q,
		Mode:   f.Permissions,
		Name:   f.Filename,
		Length: l,
		UID:    f.UID,
		GID:    f.GID,
		MUID:   f.MUID,
		Atime:  uint32(f.Atime.Unix()),
		Mtime:  uint32(f.Mtime.Unix()),
	}, nil
}

func (f *SyntheticFile) CanOpen(user string, mode qp.OpenMode) bool {
	f.RLock()
	defer f.RUnlock()
	owner := f.UID == user
	return permCheck(owner, f.Permissions, mode)
}

func (f *SyntheticFile) Open(user string, mode qp.OpenMode) (OpenFile, error) {
	if !f.CanOpen(user, mode) {
		return nil, errors.New("access denied")
	}

	f.Lock()
	defer f.Unlock()
	f.Atime = time.Now()
	f.Opens++

	return &SyntheticOpenFile{
		f:          f,
		user:       user,
		read:       mode&3 == qp.OREAD || mode&3 == qp.OEXEC || mode&3 == qp.ORDWR,
		write:      mode&3 == qp.OWRITE || mode&3 == qp.ORDWR,
		appendOnly: f.Permissions&qp.DMAPPEND != 0,
	}, nil
}

func (f *SyntheticFile) IsDir() (bool, error) {
	return false, nil
}

func (f *SyntheticFile) CanRemove() (bool, error) {
	return true, nil
}

func (f *SyntheticFile) SetContent(cnt []byte) {
	f.Content = cnt
	f.Length = uint64(len(cnt))
}

// NewSyntheticFile returns a new SyntheticFile.
func NewSyntheticFile(name string, permissions qp.FileMode, user, group string) *SyntheticFile {
	return &SyntheticFile{
		Filename:    name,
		Permissions: permissions,
		UID:         user,
		GID:         group,
		MUID:        user,
		ID:          nextID(),
		Atime:       time.Now(),
		Mtime:       time.Now(),
	}
}

type LockedOpenFile struct {
	OpenFile
	Locker sync.Locker
}

func (of *LockedOpenFile) Close() error {
	err := of.OpenFile.Close()
	of.Locker.Unlock()
	return err
}

// LockedFile provides a lock on open for read and write, using a
// sync.RWMutex. The result is that open will block if file is opened for
// write, or if its opened for read and attempted opened for write.
type LockedFile struct {
	File
	OpenLock sync.RWMutex
}

func (f *LockedFile) Open(user string, mode qp.OpenMode) (OpenFile, error) {
	of, err := f.File.Open(user, mode)
	if err != nil {
		return of, err
	}
	read := mode&3 == qp.OREAD || mode&3 == qp.OEXEC || mode&3 == qp.ORDWR
	write := mode&3 == qp.OWRITE || mode&3 == qp.ORDWR

	var l sync.Locker
	switch {
	case write:
		l = f.OpenLock.RLocker()
	case read:
		l = &f.OpenLock
	default:
		of.Close()
		return nil, errors.New("locked file can only be opened for read or write")
	}

	l.Lock()
	return &LockedOpenFile{
		OpenFile: of,
		Locker:   l,
	}, nil
}

// NewLockedFile wraps a file in a LockedFile.
func NewLockedFile(f File) *LockedFile {
	return &LockedFile{
		File: f,
	}
}
