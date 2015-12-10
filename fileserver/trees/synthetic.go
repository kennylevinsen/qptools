package trees

import (
	"errors"
	"time"

	"github.com/joushou/qp"
)

// SyntheticROOpenFile is an OpenFile implementation that serves a static
// byte-slice for reads.
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
	ID          uint64
	Filename    string
	UID         string
	GID         string
	MUID        string
	Atime       time.Time
	Mtime       time.Time
	Version     uint32
	Length      uint64
	Permissions qp.FileMode
	Content     []byte
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
	q, err := f.Qid()
	if err != nil {
		return qp.Stat{}, err
	}
	return qp.Stat{
		Qid:    q,
		Mode:   f.Permissions,
		Name:   f.Filename,
		Length: f.Length,
		UID:    f.UID,
		GID:    f.GID,
		MUID:   f.MUID,
		Atime:  uint32(f.Atime.Unix()),
		Mtime:  uint32(f.Mtime.Unix()),
	}, nil
}

func (f *SyntheticFile) CanOpen(user string, mode qp.OpenMode) bool {
	owner := f.UID == user
	return permCheck(owner, f.Permissions, mode)
}

func (f *SyntheticFile) Open(user string, mode qp.OpenMode) (OpenFile, error) {
	if !f.CanOpen(user, mode) {
		return nil, errors.New("access denied")
	}

	return NewSyntheticROOpenFile([]byte(f.Content)), nil
}

func (f *SyntheticFile) IsDir() (bool, error) {
	return false, nil
}

func (f *SyntheticFile) CanRemove() (bool, error) {
	return true, nil
}

func (f *SyntheticFile) SetContent(cnt []byte) {
	f.Content = cnt
	f.Length = len(cnt)
}

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
