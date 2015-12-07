package trees

import (
	"time"

	"github.com/joushou/qp"
)

type SyntheticFile struct {
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

func (f *SyntheticFile) Name() (string, error) {
	return f.name, nil
}

func (f *SyntheticFile) Qid() (qp.Qid, error) {
	return qp.Qid{
		Type:    qp.QTFILE,
		Version: f.version,
		Path:    f.id,
	}, nil
}

func (f *SyntheticFile) WriteStat(s qp.Stat) error {
	f.name = s.Name
	f.user = s.UID
	f.group = s.GID
	f.permissions = s.Mode
	f.mtime = time.Now()
	f.atime = f.mtime
	f.version++
	return nil
}

func (f *SyntheticFile) Stat() (qp.Stat, error) {
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

func (f *SyntheticFile) CanOpen(user string, mode qp.OpenMode) bool {
	owner := f.user == user
	return permCheck(owner, f.permissions, mode)
}

func (f *SyntheticFile) IsDir() (bool, error) {
	return false, nil
}

func (f *SyntheticFile) CanRemove() (bool, error) {
	return true, nil
}

func NewSyntheticFile(name string, permissions qp.FileMode, user, group string) *SyntheticFile {
	return &SyntheticFile{
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
