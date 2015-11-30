package trees

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/joushou/qp"
	"github.com/joushou/qptools/fileserver"
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

type ProxyOpenTree struct {
	t      *ProxyFile
	f      *os.File
	path   string
	buffer []byte
	offset int64
}

func (ot *ProxyOpenTree) update() error {
	_, err := ot.f.Seek(0, 0)
	if err != nil {
		return err
	}

	buf := new(bytes.Buffer)
	dir, err := ot.f.Readdir(-1)
	if err != nil {
		return err
	}

	for _, f := range dir {
		pf := &ProxyFile{
			path: f.Name(),
			info: f,
		}

		// We gave it a stat, we just need the encoding
		pf.cache(true)
		y, err := pf.Stat()
		if err != nil {
			return err
		}
		y.Encode(buf)
	}
	ot.buffer = buf.Bytes()
	return nil
}

func (ot *ProxyOpenTree) Seek(offset int64, whence int) (int64, error) {
	if ot.t == nil {
		return 0, errors.New("file not open")
	}
	length := int64(len(ot.buffer))
	switch whence {
	case 0:
	case 1:
		offset = ot.offset + offset
	case 2:
		offset = length + offset
	default:
		return ot.offset, errors.New("invalid whence value")
	}

	if offset < 0 {
		return ot.offset, errors.New("negative seek invalid")
	}

	if offset != 0 && offset != ot.offset {
		return ot.offset, errors.New("seek to other than 0 on dir illegal")
	}

	ot.offset = offset
	ot.update()
	return ot.offset, nil
}

func (ot *ProxyOpenTree) Read(p []byte) (int, error) {
	if ot.t == nil {
		return 0, errors.New("file not open")
	}
	rlen := int64(len(p))
	if rlen > int64(len(ot.buffer))-ot.offset {
		rlen = int64(len(ot.buffer)) - ot.offset
	}
	copy(p, ot.buffer[ot.offset:rlen+ot.offset])
	ot.offset += rlen
	return int(rlen), nil
}

func (ot *ProxyOpenTree) Write(p []byte) (int, error) {
	return 0, errors.New("cannot write to directory")
}

func (ot *ProxyOpenTree) Close() error {
	ot.t = nil
	return ot.f.Close()
}

type ProxyFile struct {
	sync.RWMutex
	root    string
	path    string
	info    os.FileInfo
	caching int
	user    string
	group   string
}

func (pf *ProxyFile) updateInfo() error {
	if pf.caching > 0 {
		return nil
	}
	var err error
	pf.info, err = os.Stat(filepath.Join(pf.root, pf.path))
	return err
}

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

func (pf *ProxyFile) WriteStat(s qp.Stat) error {
	n := filepath.Base(pf.path)
	if s.Name != "" && s.Name != n {
		d := filepath.Dir(pf.path)
		pf.path = filepath.Join(d, s.Name)
	}

	// NOTE(kl): We ignore everything else. This is incorrect, but most things
	// don't make sense to touch, and the actual rename has already ocurred
	// anyway.
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
	st.Atime = uint32(pf.info.ModTime().Unix())
	st.Mtime = st.Mtime
	st.Length = uint64(pf.info.Size())
	st.Name = filepath.Base(pf.path)
	st.UID = pf.user
	st.GID = pf.user
	st.MUID = pf.user

	return st, nil
}

func (pf *ProxyFile) Open(_ string, mode qp.OpenMode) (fileserver.OpenFile, error) {
	if err := pf.updateInfo(); err != nil {
		return nil, err
	}
	pf.cache(true)
	defer pf.cache(false)

	f, err := os.OpenFile(filepath.Join(pf.root, pf.path), openMode2Flag(mode), 0)
	if err != nil {
		return nil, err
	}

	if p, _ := pf.IsDir(); p {
		return &ProxyOpenTree{
			t:    pf,
			f:    f,
			path: filepath.Join(pf.root, pf.path),
		}, nil
	}

	return f, nil
}

func (pf *ProxyFile) CanRemove() (bool, error) {
	return true, nil
}

func (pf *ProxyFile) Walk(_, name string) (fileserver.File, error) {
	p := filepath.Join(pf.path, name)

	if _, err := os.Stat(filepath.Join(pf.root, p)); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	return &ProxyFile{
		root: pf.root,
		path: p,
	}, nil
}

func (pf *ProxyFile) Create(_, name string, perms qp.FileMode) (fileserver.File, error) {
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
		root: pf.root,
		path: p,
	}, nil
}

func (pf *ProxyFile) Remove(_, name string) error {
	p := filepath.Join(pf.path, name)
	return os.Remove(filepath.Join(pf.root, p))
}

func (pf *ProxyFile) Rename(_, oldname, newname string) error {
	op := filepath.Join(pf.root, filepath.Join(pf.path, oldname))
	np := filepath.Join(pf.root, filepath.Join(pf.path, newname))
	return os.Rename(op, np)
}

func (pf *ProxyFile) IsDir() (bool, error) {
	if err := pf.updateInfo(); err != nil {
		return false, err
	}
	return pf.info.IsDir(), nil
}

func NewProxyTree(root, path, user, group string) fileserver.Dir {
	return &ProxyFile{
		root:  root,
		path:  path,
		user:  user,
		group: group,
	}
}
