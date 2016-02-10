package trees

import (
	"errors"
	"sync"
	"time"

	"github.com/joushou/qp"
)

// SyntheticHandle implements locked R/W access to a SyntheticFile's internal
// Content byteslice. It updates atime, mtime, version and open count.
type SyntheticHandle struct {
	sync.Mutex
	f          *SyntheticFile
	User       string
	Offset     int64
	Readable   bool
	Writable   bool
	AppendOnly bool
}

// Seek changes the offset of the handle.
func (h *SyntheticHandle) Seek(offset int64, whence int) (int64, error) {
	h.Lock()
	defer h.Unlock()
	if (!h.Readable && !h.Writable) || h.f == nil {
		return 0, errors.New("file not open")
	}
	h.f.Lock()
	defer h.f.Unlock()

	cnt := h.f.Content
	length := int64(len(cnt))

	switch whence {
	case 0:
	case 1:
		offset = h.Offset + offset
	case 2:
		offset = length + offset
	default:
		return h.Offset, errors.New("invalid whence value")
	}

	if offset < 0 {
		return h.Offset, errors.New("negative seek invalid")
	}

	if offset > length {
		offset = length
	}

	h.Offset = offset
	h.f.Atime = time.Now()
	return h.Offset, nil
}

// Read reads from the current offset.
func (h *SyntheticHandle) Read(p []byte) (int, error) {
	h.Lock()
	defer h.Unlock()
	if !h.Readable || h.f == nil {
		return 0, errors.New("file not open for read")
	}

	h.f.Lock()
	defer h.f.Unlock()

	cnt := h.f.Content
	maxRead := int64(len(p))
	remaining := int64(len(cnt)) - h.Offset
	if maxRead > remaining {
		maxRead = remaining
	}

	copy(p, cnt[h.Offset:maxRead+h.Offset])
	h.Offset += maxRead
	h.f.Atime = time.Now()
	return int(maxRead), nil
}

// Write writes at the current offset.
func (h *SyntheticHandle) Write(p []byte) (int, error) {
	h.Lock()
	defer h.Unlock()
	if !h.Writable || h.f == nil {
		return 0, errors.New("file not open for write")
	}

	h.f.Lock()
	defer h.f.Unlock()

	cnt := h.f.Content
	if h.AppendOnly {
		h.Offset = int64(len(cnt))
	}
	wlen := int64(len(p))
	l := int(wlen + h.Offset)

	if l > cap(cnt) {
		c := l * 2
		if l < 10240 {
			c = 10240
		}
		b := make([]byte, l, c)
		copy(b, cnt[:h.Offset])
		h.f.Content = b
	} else if l > len(cnt) {
		h.f.Content = cnt[:l]
	}

	copy(h.f.Content[h.Offset:], p)

	h.Offset += wlen
	h.f.Mtime = time.Now()
	h.f.Atime = h.f.Mtime
	h.f.MUID = h.User
	h.f.Version++
	return int(wlen), nil
}

// Close closes the handle, decrementing the open counter of the
// SyntheticFile.
func (h *SyntheticHandle) Close() error {
	h.Lock()
	defer h.Unlock()
	h.f.Lock()
	defer h.f.Unlock()
	h.f.Opens--
	h.f = nil
	return nil
}

// SyntheticFile is a File implementation that takes care of the more boring
// aspects of a file implementation, such as permission-handling and qid/stat
// generation. By default, it serves the Content slice through a
// SyntheticROHandle. In most cases, one would embed SyntheticFile and provide
// their own Open implementation for more interesting functionality.
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

// Name returns the name of the synthetic file. This cannot fail.
func (f *SyntheticFile) Name() (string, error) {
	return f.Filename, nil
}

// Qid returns the qid of the synthetic file. This cannot fail.
func (f *SyntheticFile) Qid() (qp.Qid, error) {
	return qp.Qid{
		Type:    qp.QTFILE,
		Version: f.Version,
		Path:    f.ID,
	}, nil
}

// SetLength sets the length of the file content.
func (f *SyntheticFile) SetLength(user string, length uint64) error {
	if !f.CanOpen(user, qp.OWRITE) {
		return errors.New("permission denied")
	}
	f.Lock()
	defer f.Unlock()
	if length > uint64(len(f.Content)) {
		return errors.New("cannot extend length")
	}
	if f.FakeLength {
		return errors.New("cannot change fake length")
	}
	f.Content = f.Content[:length]
	f.Mtime = time.Now()
	f.Atime = f.Mtime
	f.Version++
	return nil
}

// SetName sets the name of the file. This must only be called from Rename on
// a directory.
func (f *SyntheticFile) SetName(user, name string) error {
	f.Lock()
	defer f.Unlock()

	f.Filename = name
	f.Mtime = time.Now()
	f.Atime = f.Mtime
	f.Version++
	return nil
}

// SetOwner sets the owner.
func (f *SyntheticFile) SetOwner(user, UID, GID string) error {
	if !f.CanOpen(user, qp.OWRITE) {
		return errors.New("permission denied")
	}
	f.Lock()
	defer f.Unlock()

	if UID != "" {
		f.UID = UID
	}
	if GID != "" {
		f.GID = GID
	}

	f.Mtime = time.Now()
	f.Atime = f.Mtime
	f.Version++
	return nil
}

// SetMode sets the mode and permissions.
func (f *SyntheticFile) SetMode(user string, mode qp.FileMode) error {
	if user != f.UID || !f.CanOpen(user, qp.OWRITE) {
		return errors.New("permission denied")
	}
	f.Lock()
	defer f.Unlock()

	f.Permissions = mode
	f.Mtime = time.Now()
	f.Atime = f.Mtime
	f.Version++
	return nil
}

// Stat returns the stat struct. This cannot fail.
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

// CanOpen checks if a user may perform the requested open.
func (f *SyntheticFile) CanOpen(user string, mode qp.OpenMode) bool {
	f.RLock()
	defer f.RUnlock()
	owner := f.UID == user
	return PermCheck(owner, false, f.Permissions, mode)
}

// Open returns a SyntheticHandle if the open was permitted.
func (f *SyntheticFile) Open(user string, mode qp.OpenMode) (ReadWriteSeekCloser, error) {
	if !f.CanOpen(user, mode) {
		return nil, errors.New("access denied")
	}

	f.Lock()
	defer f.Unlock()
	f.Atime = time.Now()
	f.Opens++

	if f.Content != nil && mode&qp.OTRUNC != 0 && mode&qp.OWRITE != 0 {
		f.SetContent(nil)
	}

	return &SyntheticHandle{
		f:          f,
		User:       user,
		Readable:   mode&3 == qp.OREAD || mode&3 == qp.OEXEC || mode&3 == qp.ORDWR,
		Writable:   mode&3 == qp.OWRITE || mode&3 == qp.ORDWR,
		AppendOnly: f.Permissions&qp.DMAPPEND != 0,
	}, nil
}

// IsDir returns false. This cannot fail.
func (f *SyntheticFile) IsDir() (bool, error) {
	return false, nil
}

// CanRemove returns true. This cannot fail.
func (f *SyntheticFile) CanRemove() (bool, error) {
	return true, nil
}

// Arrived returns the file itself, after updating the access time.
func (f *SyntheticFile) Arrived(user string) (File, error) {
	// TODO(kl): Use atomic for atime access.
	f.Lock()
	defer f.Unlock()
	f.Atime = time.Now()
	return f, nil
}

// SetContent sets the content and length
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
