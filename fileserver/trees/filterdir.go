package trees

import (
	"errors"
	"sync"

	"github.com/joushou/qp"
)

type FilterDir struct {
	Lister

	UpdateHook    func(string, *FilterDir)
	FilteredNames map[string]bool
	Whitelist     bool
	FilterLock    sync.RWMutex
	Filename      string
}

func (fd *FilterDir) Name() (string, error) {
	return fd.Filename, nil
}

func (fd *FilterDir) Stat() (qp.Stat, error) {
	s, err := fd.Lister.Stat()
	if err != nil {
		return s, err
	}
	s.Name = fd.Filename
	return s, err
}

// Things to mask:
// 	List (files must be filtered)
// 	Walk (name must be permitted)
// 	Rename (oldname must be permitted)
// 	Remove (name must be permitted)

func (fd *FilterDir) update(name string) {
	if fd.UpdateHook != nil {
		fd.UpdateHook(name, fd)
	}
}

func (fd *FilterDir) filePermitted(name string) bool {
	fd.FilterLock.RLock()
	defer fd.FilterLock.RUnlock()
	_, inset := fd.FilteredNames[name]

	// If the file is on the list and the list is a blacklist, or it is not on
	// the list and the list is a whitelist, the access is not permitted.
	return inset == fd.Whitelist
}

func (fd *FilterDir) List(user string) ([]qp.Stat, error) {
	fd.update("")
	l, err := fd.Lister.List(user)
	if err != nil {
		return nil, err
	}

	fd.FilterLock.RLock()
	defer fd.FilterLock.RUnlock()
	for i := 0; i < len(l); i++ {
		n := l[i].Name
		_, inset := fd.FilteredNames[n]
		if inset != fd.Whitelist {
			// Delete from list without preserving order.
			l[i] = l[len(l)-1]
			l[len(l)-1] = qp.Stat{}
			l = l[:len(l)-1]

			// There's a new element on our position - we want to process that
			// too.
			i--
		}
	}

	return l, nil
}

func (fd *FilterDir) Walk(user, name string) (File, error) {
	fd.update(name)
	if !fd.filePermitted(name) {
		return nil, nil
	}
	return fd.Lister.Walk(user, name)
}

func (fd *FilterDir) Rename(user, oldname, newname string) error {
	fd.update(oldname)
	if !fd.filePermitted(oldname) {
		return errors.New("permission denied")
	}
	return fd.Lister.Rename(user, oldname, newname)
}

func (fd *FilterDir) Remove(user, name string) error {
	fd.update(name)
	if !fd.filePermitted(name) {
		return errors.New("permission denied")
	}
	return fd.Lister.Remove(user, name)
}

func NewFilterDir(name string, dir Lister) *FilterDir {
	return &FilterDir{
		Filename: name,
		Lister:   dir,
	}
}
