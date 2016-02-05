package trees

import "github.com/joushou/qp"

// MixDir allows for access two two directories on top of eachother. Top takes
// priority: Creation is done in the Top dir, and the MixTree identifies as
// the Top dir. Operations only pass down to Bottom if the files manipulated
// are already present in Bottom and not in Top. The result is somewhat
// equivalent to a plan9 "bind".
type MixDir struct {
	Top    Dir
	Bottom Dir
}

// Name lists Top's name,
func (mt *MixDir) Name() (string, error) {
	return mt.Top.Name()
}

// List combines the list of Top and Bottom directories, returning a directory
// listing where entries in Top have priority over Bottom.
func (mt *MixDir) List(user string) ([]qp.Stat, error) {
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
func (mt *MixDir) Open(user string, mode qp.OpenMode) (ReadWriteSeekCloser, error) {
	return &ListHandle{
		Dir:  mt,
		User: user,
	}, nil
}

// Qid returns Top's qid.
func (mt *MixDir) Qid() (qp.Qid, error) {
	return mt.Top.Qid()
}

// Stat returns Top's stat.
func (mt *MixDir) Stat() (qp.Stat, error) {
	return mt.Top.Stat()
}

// SetLength sets the content length of Top.
func (mt *MixDir) SetLength(user string, length uint64) error {
	return mt.Top.SetLength(user, length)
}

// SetName sets the name of Top. This must only be called from Rename on a
// directory.
func (mt *MixDir) SetName(user, name string) error {
	return mt.Top.SetName(user, name)
}

// SetOwner sets the owner of Top.
func (mt *MixDir) SetOwner(user, UID, GID string) error {
	return mt.Top.SetOwner(user, UID, GID)
}

// SetMode sets the mode and permissions of Top.
func (mt *MixDir) SetMode(user string, mode qp.FileMode) error {
	return mt.Top.SetMode(user, mode)
}

// IsDir always returns true.
func (mt *MixDir) IsDir() (bool, error) {
	return true, nil
}

// CanRemove always returns false.
func (mt *MixDir) CanRemove() (bool, error) {
	return false, nil
}

// Walk first tries to walk in Top, then in Bottom, returning results and
// errors as they are met.
func (mt *MixDir) Walk(user, name string) (File, error) {
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
func (mt *MixDir) Create(user, name string, perms qp.FileMode) (File, error) {
	return mt.Top.Create(user, name, perms)
}

// Remove first tries to remove in Top, then in Bottom, returning results and
// errors as they are met.
func (mt *MixDir) Remove(user, name string) error {
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
func (mt *MixDir) Rename(user, oldname, newname string) error {
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
