package trees

import "github.com/joushou/qp"

// MixTree allows for access two two directories on top of eachother. Top
// takes priority: Creation is done in the Top dir, and the MixTree identifies
// as the Top dir. Operations only pass down to Bottom if the files
// manipulated are already present in Bottom and not in Top. The result is
// somewhat equivalent to a plan9 "bind".
type MixTree struct {
	Top    Dir
	Bottom Dir
}

// Name lists Top's name,
func (mt *MixTree) Name() (string, error) {
	return mt.Top.Name()
}

// List combines the list of Top and Bottom directories, returning a directory
// listing where entries in Top have priority over Bottom.
func (mt *MixTree) List(user string) ([]qp.Stat, error) {
	m := make(map[string]qp.Stat)

	l1, err := mt.Bottom.(Lister).List(user)
	if err != nil {
		return nil, err
	}
	for _, s := range l1 {
		m[s.Name] = s
	}

	l2, err := mt.Top.(Lister).List(user)
	if err != nil {
		return nil, err
	}
	for _, s := range l2 {
		m[s.Name] = s
	}

	var l []qp.Stat
	for _, v := range m {
		l = append(l, v)
	}
	return l, nil
}

// Open return an OpenFile for the MixTree directory.
func (mt *MixTree) Open(user string, mode qp.OpenMode) (OpenFile, error) {
	return &ListOpenTree{
		t:    mt,
		user: user,
	}, nil
}

// Qid returns Top's qid.
func (mt *MixTree) Qid() (qp.Qid, error) {
	return mt.Top.Qid()
}

// Stat returns Top's stat.
func (mt *MixTree) Stat() (qp.Stat, error) {
	return mt.Top.Stat()
}

// WriteStat writes to Top's stat.
func (mt *MixTree) WriteStat(s qp.Stat) error {
	return mt.Top.WriteStat(s)
}

// IsDir always returns true.
func (mt *MixTree) IsDir() (bool, error) {
	return true, nil
}

// CanRemove always returns false.
func (mt *MixTree) CanRemove() (bool, error) {
	return false, nil
}

// Walk first tries to walk in Top, then in Bottom, returning results and
// errors as they are met.
func (mt *MixTree) Walk(user, name string) (File, error) {
	f, err := mt.Top.Walk(user, name)
	if err != nil {
		return nil, err
	}
	if f != nil {
		return f, nil
	}
	f, err = mt.Bottom.Walk(user, name)
	if err != nil {
		return nil, err
	}
	if f != nil {
		return f, nil
	}
	return nil, nil
}

// Create creates a file in Top.
func (mt *MixTree) Create(user, name string, perms qp.FileMode) (File, error) {
	return mt.Top.Create(user, name, perms)
}

// Remove first tries to remove in Top, then in Bottom, returning results and
// errors as they are met.
func (mt *MixTree) Remove(user, name string) error {
	f, err := mt.Top.Walk(user, name)
	if err != nil {
		return err
	}
	if f != nil {
		return mt.Top.Remove(user, name)
	}

	f, err = mt.Bottom.Walk(user, name)
	if err != nil {
		return err
	}
	if f != nil {

		return mt.Bottom.Remove(user, name)
	}

	return nil
}

// Rename first tries to rename in Top, then in Bottom, returnign results and
// errors as they are met.
func (mt *MixTree) Rename(user, oldname, newname string) error {
	f, err := mt.Top.Walk(user, oldname)
	if err != nil {
		return err
	}
	if f != nil {
		return mt.Top.Rename(user, oldname, newname)
	}

	f, err = mt.Bottom.Walk(user, oldname)
	if err != nil {
		return err
	}
	if f != nil {
		return mt.Bottom.Rename(user, oldname, newname)
	}

	return nil
}
