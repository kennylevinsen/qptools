package trees

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/joushou/qp"
)

func openMode2Flag(fm qp.OpenMode) int {
	var nfm int

	switch fm & 0xF {
	case qp.OREAD:
		nfm = os.O_RDONLY
	case qp.OWRITE:
		nfm = os.O_WRONLY
	case qp.ORDWR:
		nfm = os.O_RDWR
	case qp.OEXEC:
		// We most likely don't want to enter here, ever
		nfm = os.O_RDONLY
	}

	switch fm & 0xF0 {
	case qp.OTRUNC:
		nfm |= os.O_TRUNC
	}

	return nfm
}

// ProxyFile provides access to a local filesystem. Permissions aren't
// checked, but assumed enforced by the OS. All owners are reported as the
// provided user and group, not read from the filesystem due to portability
// issues.
type ProxyFile struct {
	sync.RWMutex
	root    string
	path    string
	info    os.FileInfo
	caching int
	user    string
	group   string
}

// updateInfo updates the local file information, unless caching is currently
// enabled.
func (pf *ProxyFile) updateInfo() error {
	if pf.caching > 0 {
		return nil
	}
	var err error
	pf.info, err = os.Stat(filepath.Join(pf.root, pf.path))
	return err
}

// cache enables the fileinfo cache. It is used to avoid updating the file
// information multiple times during a single call to ProxyFile for no reason.
func (pf *ProxyFile) cache(t bool) {
	if t {
		pf.caching++
	} else {
		pf.caching--
	}
}

func (pf *ProxyFile) Qid() (qp.Qid, error) {
	if err := pf.updateInfo(); err != nil {
		return qp.Qid{}, err
	}

	var tp qp.QidType
	if pf.info.IsDir() {
		tp |= qp.QTDIR
	}

	// This is not entirely correct as a path, as removing and recreating the
	// file should give a new path, but... What the hell.
	chk := sha256.Sum224([]byte(filepath.Join(pf.root, pf.path)))
	path := binary.LittleEndian.Uint64(chk[:8])

	return qp.Qid{
		Path:    path,
		Version: uint32(pf.info.ModTime().UnixNano() / 1000000),
		Type:    tp,
	}, nil
}

func (pf *ProxyFile) Name() (string, error) {
	if pf.path == "" {
		return "/", nil
	}
	return filepath.Base(pf.path), nil
}

func (pf *ProxyFile) SetLength(user string, length uint64) error {
	return nil
}

func (pf *ProxyFile) SetName(user, name string) error {
	n := filepath.Base(pf.path)
	if name != "" && name != n {
		d := filepath.Dir(pf.path)
		pf.path = filepath.Join(d, name)
	}
	return nil
}

func (pf *ProxyFile) SetOwner(user, UID, GID string) error {
	return nil
}

func (pf *ProxyFile) SetMode(user string, mode qp.FileMode) error {
	return nil
}

func (pf *ProxyFile) Stat() (qp.Stat, error) {
	if err := pf.updateInfo(); err != nil {
		return qp.Stat{}, err
	}
	pf.cache(true)
	defer pf.cache(false)

	var err error
	st := qp.Stat{}

	st.Qid, err = pf.Qid()
	if err != nil {
		return qp.Stat{}, err
	}
	st.Mode = qp.FileMode(pf.info.Mode() & 0777)
	if pf.info.IsDir() {
		st.Mode |= qp.DMDIR
	}
	st.Mtime = uint32(pf.info.ModTime().Unix())
	st.Atime = st.Mtime
	st.Length = uint64(pf.info.Size())
	if pf.info.IsDir() {
		st.Length = 0
	}
	st.Name = filepath.Base(pf.path)
	st.UID = pf.user
	st.GID = pf.group
	st.MUID = pf.user
	return st, nil
}

func (pf *ProxyFile) List(_ string) ([]qp.Stat, error) {
	if err := pf.updateInfo(); err != nil {
		return nil, err
	}
	pf.cache(true)
	defer pf.cache(false)

	isdir, err := pf.IsDir()
	if err != nil {
		return nil, err
	}
	if !isdir {
		return nil, errors.New("not a directory")
	}

	f, err := os.OpenFile(filepath.Join(pf.root, pf.path), openMode2Flag(qp.OREAD), 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dir, err := f.Readdir(-1)
	if err != nil {
		return nil, err
	}

	var s []qp.Stat
	for _, f := range dir {
		tpf := &ProxyFile{
			path:  f.Name(),
			info:  f,
			user:  pf.user,
			group: pf.group,
		}

		tpf.cache(true)
		y, err := tpf.Stat()
		if err != nil {
			return nil, err
		}
		s = append(s, y)
		tpf.cache(false)
	}

	return s, nil
}

func (pf *ProxyFile) Open(user string, mode qp.OpenMode) (ReadWriteSeekCloser, error) {
	if err := pf.updateInfo(); err != nil {
		return nil, err
	}
	pf.cache(true)
	defer pf.cache(false)

	isdir, err := pf.IsDir()
	if err != nil {
		return nil, err
	}

	if isdir {
		return &ListHandle{
			Dir:  pf,
			User: user,
		}, nil
	}

	return os.OpenFile(filepath.Join(pf.root, pf.path), openMode2Flag(mode), 0)
}

func (pf *ProxyFile) CanRemove() (bool, error) {
	return true, nil
}

func (pf *ProxyFile) Walk(_, name string) (File, error) {
	p := filepath.Join(pf.path, name)

	if _, err := os.Stat(filepath.Join(pf.root, p)); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	return &ProxyFile{
		root:  pf.root,
		path:  p,
		user:  pf.user,
		group: pf.group,
	}, nil
}

func (pf *ProxyFile) Arrived(_ string) (File, error) {
	return pf, nil
}

func (pf *ProxyFile) Create(_, name string, perms qp.FileMode) (File, error) {
	p := filepath.Join(pf.path, name)
	if perms&qp.DMDIR != 0 {
		err := os.Mkdir(filepath.Join(pf.root, p), os.FileMode(perms&0777))
		if err != nil {
			return nil, err
		}
	} else {
		f, err := os.OpenFile(filepath.Join(pf.root, p), os.O_CREATE|os.O_EXCL, os.FileMode(perms&0777))
		if err != nil {
			return nil, err
		}
		f.Close()
	}

	return &ProxyFile{
		root:  pf.root,
		path:  p,
		user:  pf.user,
		group: pf.group,
	}, nil
}

func (pf *ProxyFile) Remove(_, name string) error {
	return os.Remove(filepath.Join(pf.root, pf.path, name))
}

func (pf *ProxyFile) Rename(_, oldname, newname string) error {
	op := filepath.Join(pf.root, pf.path, oldname)
	np := filepath.Join(pf.root, pf.path, newname)
	return os.Rename(op, np)
}

func (pf *ProxyFile) IsDir() (bool, error) {
	if err := pf.updateInfo(); err != nil {
		return false, err
	}
	return pf.info.IsDir(), nil
}

func NewProxyFile(root, path, user, group string) *ProxyFile {
	return &ProxyFile{
		root:  root,
		path:  path,
		user:  user,
		group: group,
	}
}
