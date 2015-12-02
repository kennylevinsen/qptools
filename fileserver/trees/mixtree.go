package trees

import (
	"errors"

	"github.com/joushou/qp"
)

type Lister interface {
	List(string) ([]qp.Stat, error)
}

type MixTree struct {
	Over  Dir
	Under Dir
	name  string
}

func (mt *MixTree) Name() (string, error) {
	return mt.name, nil
}

func (mt *MixTree) Open(user string, mode qp.OpenMode) (OpenFile, error) {
	if mode != qp.OREAD && mode != qp.ORDWR {
		return nil, errors.New("invalid mode for directory")
	}

	l1, err := mt.Under.(Lister).List(user)
	if err != nil {
		return nil, err
	}
	l2, err := mt.Over.(Lister).List(user)
	if err != nil {
		return nil, err
	}
	_, _ = l1, l2
	return nil, nil
}

func (mt *MixTree) Qid() (qp.Qid, error) {
	return qp.Qid{}, nil
}

func (mt *MixTree) Stat() (qp.Stat, error) {
	return qp.Stat{}, nil
}

func (mt *MixTree) WriteStat(qp.Stat) error {
	return nil
}

func (mt *MixTree) IsDir() (bool, error) {
	return true, nil
}

func (mt *MixTree) CanRemove() (bool, error) {
	return false, nil
}

func (mt *MixTree) Walk(user, name string) (File, error) {
	return nil, nil
}

func (mt *MixTree) Create(user, name string, perms qp.FileMode) (File, error) {
	return mt.Over.Create(user, name, perms)
}

func (mt *MixTree) Remove(user, name string) error {
	return nil
}

func (mt *MixTree) Rename(user, oldname, newname string) error {
	return nil
}
