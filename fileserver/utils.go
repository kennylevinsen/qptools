package fileserver

import (
	"errors"

	"github.com/joushou/qp"
	"github.com/joushou/qptools/fileserver/trees"
)

type FilePath []trees.File

func (fp FilePath) Current() trees.File {
	if len(fp) == 0 {
		return nil
	}
	return fp[len(fp)-1]
}

func (fp FilePath) Parent() trees.File {
	if len(fp) == 0 {
		return nil
	} else if len(fp) == 1 {
		return fp[len(fp)-1]
	}
	return fp[len(fp)-2]
}

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

	needWrite := false
	rename := false
	curname := ""
	newname := ""

	if nstat.Type != ^uint16(0) && nstat.Type != ostat.Type {
		return errors.New("it is illegal to modify type")
	}
	if nstat.Dev != ^uint32(0) && nstat.Dev != ostat.Dev {
		return errors.New("it is illegal to modify dev")
	}
	if nstat.Mode != ^qp.FileMode(0) && nstat.Mode != ostat.Mode {
		// TODO Ensure we don't flip DMDIR
		if user != ostat.UID {
			return errors.New("only owner can change mode")
		}
		ostat.Mode = ostat.Mode&qp.DMDIR | nstat.Mode & ^qp.DMDIR
	}
	if nstat.Atime != ^uint32(0) && nstat.Atime != ostat.Atime {
		return errors.New("it is illegal to modify atime")
	}
	if nstat.Mtime != ^uint32(0) && nstat.Mtime != ostat.Mtime {
		if user != ostat.UID {
			return errors.New("only owner can change mtime")
		}
		needWrite = true
		ostat.Mtime = nstat.Mtime
	}
	if nstat.Length != ^uint64(0) && nstat.Length != ostat.Length {
		if ostat.Mode&qp.DMDIR != 0 {
			return errors.New("cannot set length of directory")
		}
		if nstat.Length > ostat.Length {
			return errors.New("cannot extend length")
		}
		ostat.Length = nstat.Length
	}
	if nstat.Name != "" && nstat.Name != ostat.Name {
		if parent != nil {
			curname = ostat.Name
			newname = nstat.Name
			ostat.Name = nstat.Name
			rename = true
		} else {
			return errors.New("it is illegal to rename root")
		}
	}
	if nstat.UID != "" && nstat.UID != ostat.UID {
		// NOTE: It is normally illegal to change the file owner, but we are a bit more relaxed.
		ostat.UID = nstat.UID
		needWrite = true
	}
	if nstat.GID != "" && nstat.GID != ostat.GID {
		ostat.GID = nstat.GID
		needWrite = true
	}
	if nstat.MUID != "" && nstat.MUID != ostat.MUID {
		return errors.New("it is illegal to modify muid")
	}

	if needWrite {
		x, err := e.Open(user, qp.OWRITE)
		if err != nil {
			return err
		}
		x.Close()
	}

	// Try to perform the rename
	if rename {
		if err := parent.Rename(user, curname, newname); err != nil {
			return err
		}
	}

	return e.WriteStat(ostat)
}
