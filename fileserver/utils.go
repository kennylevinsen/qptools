package fileserver

import (
	"errors"

	"github.com/joushou/qp"
	"github.com/joushou/qptools/fileserver/trees"
)

// FilePath is used to maintain the current position in a tree.
type FilePath []trees.File

// Current returns the current location, which is the last element of the
// path.
func (fp FilePath) Current() trees.File {
	if len(fp) == 0 {
		return nil
	}
	return fp[len(fp)-1]
}

// Parent returns the parent directory, or root if there is no parent. It
// returns nil if the path is empty.
func (fp FilePath) Parent() trees.File {
	if len(fp) == 0 {
		return nil
	} else if len(fp) == 1 {
		return fp[len(fp)-1]
	}
	return fp[len(fp)-2]
}

// Clone creates a clone of the path.
func (fp FilePath) Clone() FilePath {
	n := make(FilePath, len(fp))
	for i := range fp {
		n[i] = fp[i]
	}
	return n
}

func setStat(user string, e trees.File, parent trees.Dir, nstat qp.Stat) error {
	ostat, err := e.Stat()
	if err != nil {
		return err
	}

	length := false
	name := false
	mode := false
	owner := false

	if nstat.Type != ^uint16(0) && nstat.Type != ostat.Type {
		return errors.New("it is illegal to modify type")
	}
	if nstat.Dev != ^uint32(0) && nstat.Dev != ostat.Dev {
		return errors.New("it is illegal to modify dev")
	}
	if nstat.MUID != "" && nstat.MUID != ostat.MUID {
		return errors.New("it is illegal to modify muid")
	}
	if nstat.Atime != ^uint32(0) && nstat.Atime != ostat.Atime {
		return errors.New("it is illegal to modify atime")
	}
	if parent == nil && nstat.Name != "" && nstat.Name != ostat.Name {
		return errors.New("it is illegal to rename root")
	}
	if nstat.Length != ^uint64(0) && nstat.Length != ostat.Length {
		if ostat.Mode&qp.DMDIR != 0 {
			return errors.New("cannot set length of directory")
		}
		if nstat.Length > ostat.Length {
			return errors.New("cannot extend length")
		}
		length = true
	}
	if nstat.Name != "" && nstat.Name != ostat.Name {
		name = true
	}
	if (nstat.UID != "" && nstat.UID != ostat.UID) || (nstat.GID != "" && nstat.GID != ostat.GID) {
		owner = true
	}
	if nstat.Mode != ^qp.FileMode(0) && nstat.Mode != ostat.Mode {
		mode = true
	}

	if nstat.Mtime != ^uint32(0) && nstat.Mtime != ostat.Mtime {
		// We ignore mtime changes ATM. Only owner can change mtime.
	}

	if mode {
		if err := e.SetMode(user, ostat.Mode&qp.DMDIR | nstat.Mode & ^qp.DMDIR); err != nil {
			return err
		}
	}
	if owner {
		if err := e.SetOwner(user, nstat.UID, nstat.GID); err != nil {
			e.SetMode(user, ostat.Mode)
			return err
		}
	}
	if name {
		if err := parent.Rename(user, ostat.Name, nstat.Name); err != nil {
			e.SetMode(user, ostat.Mode)
			e.SetOwner(user, ostat.UID, ostat.GID)
			return err
		}
	}
	if length {
		if err := e.SetLength(user, nstat.Length); err != nil {
			e.SetMode(user, ostat.Mode)
			e.SetOwner(user, ostat.UID, ostat.GID)
			parent.Rename(user, nstat.Name, ostat.Name)
			return err
		}
	}
	return nil
}
