package trees

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/kennylevinsen/qp"
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

// proxyFileListHandle implements fast directory listing of a ProxyFile.
type proxyFileListHandle struct {
	sync.Mutex
	Dir  *ProxyFile
	list []os.FileInfo
}

// Close is a no-op.
func (h *proxyFileListHandle) Close() error { return nil }

// WriteAt on proxyFileListHandle returns an error.
func (h *proxyFileListHandle) WriteAt(p []byte, offset int64) (int, error) {
	return 0, errors.New("cannot write to directory")
}

// ReadAt handles serialization of directory listing on demand.
func (h *proxyFileListHandle) ReadAt(p []byte, offset int64) (int, error) {
	h.Lock()
	defer h.Unlock()
	if offset == 0 {
		list, err := h.Dir.DirectList()
		if err != nil {
			return 0, err
		}
		h.list = list
	}

	var copied int
	for {
		if len(h.list) == 0 {
			break
		}

		f := h.list[0]

		tpf := &ProxyFile{
			root:  h.Dir.root,
			path:  f.Name(),
			user:  h.Dir.user,
			group: h.Dir.group,
		}

		s := tpf.stat(f)
		l := s.EncodedSize()
		if len(p)-copied < l {
			if copied == 0 {
				return 0, errors.New("read: message size too small: stat does not fit")
			}
			break
		}

		if err := s.Marshal(p[copied:]); err != nil {
			return copied, err
		}

		copied += l
		h.list = h.list[1:]
	}

	return copied, nil
}

// ProxyFile provides access to a local filesystem. Permissions aren't
// checked, but assumed enforced by the OS. All owners are reported as the
// provided user and group, not read from the filesystem due to portability
// issues.
type ProxyFile struct {
	caching  int32
	root     string
	path     string
	infoLock sync.RWMutex
	info     os.FileInfo
	user     string
	group    string
}

// updateInfo updates the local file information, unless caching is currently
// enabled.
func (pf *ProxyFile) updateInfo() error {
	if atomic.LoadInt32(&pf.caching) > 0 {
		return nil
	}

	var err error
	pf.infoLock.Lock()
	defer pf.infoLock.Unlock()
	pf.info, err = os.Stat(filepath.Join(pf.root, pf.path))
	return err
}

// cache enables the fileinfo cache. It is used to avoid updating the file
// information multiple times during a single call to ProxyFile for no reason.
func (pf *ProxyFile) cache(t bool) {
	if t {
		atomic.AddInt32(&pf.caching, 1)
	} else {
		atomic.AddInt32(&pf.caching, -1)
	}
}

// Qid implements File.
func (pf *ProxyFile) Qid() (qp.Qid, error) {
	if err := pf.updateInfo(); err != nil {
		return qp.Qid{}, err
	}

	pf.infoLock.RLock()
	defer pf.infoLock.RUnlock()

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

// Name implements File.
func (pf *ProxyFile) Name() (string, error) {
	if pf.path == "" {
		return "/", nil
	}
	return filepath.Base(pf.path), nil
}

// SetLength implements File.
func (pf *ProxyFile) SetLength(user string, length uint64) error {
	return nil
}

// SetName implements File.
func (pf *ProxyFile) SetName(user, name string) error {
	n := filepath.Base(pf.path)
	if name != "" && name != n {
		d := filepath.Dir(pf.path)
		pf.path = filepath.Join(d, name)
	}
	return nil
}

// SetOwner implements File.
func (pf *ProxyFile) SetOwner(user, UID, GID string) error {
	return nil
}

// SetMode implements File.
func (pf *ProxyFile) SetMode(user string, mode qp.FileMode) error {
	return nil
}

// Stat implements File.
func (pf *ProxyFile) Stat() (qp.Stat, error) {
	if err := pf.updateInfo(); err != nil {
		return qp.Stat{}, err
	}
	pf.infoLock.RLock()
	defer pf.infoLock.RUnlock()
	return pf.stat(pf.info), nil
}

// stat implements a faster qp.Stat generation mechanism.
func (pf *ProxyFile) stat(info os.FileInfo) qp.Stat {
	// Qid() inlined
	chk := sha256.Sum224([]byte(filepath.Join(pf.root, pf.path)))
	path := binary.LittleEndian.Uint64(chk[:8])
	mtime := info.ModTime()
	stMtime := uint32(mtime.Unix())

	st := qp.Stat{
		Qid: qp.Qid{
			Path:    path,
			Version: uint32(mtime.UnixNano() / 1000000),
		},
		Mode:   qp.FileMode(info.Mode() & 0777),
		Mtime:  stMtime,
		Atime:  stMtime,
		Length: uint64(info.Size()),
		Name:   filepath.Base(pf.path),
		UID:    pf.user,
		GID:    pf.group,
		MUID:   pf.user,
	}

	if info.IsDir() {
		st.Length = 0
		st.Qid.Type |= qp.QTDIR
		st.Mode |= qp.DMDIR
	}

	return st
}

// List implements Lister.
func (pf *ProxyFile) List(_ string) ([]qp.Stat, error) {
	isdir, err := pf.IsDir()
	if err != nil {
		return nil, fmt.Errorf("could not inspect file: %v", err)
	}
	if !isdir {
		return nil, errors.New("not a directory")
	}

	f, err := os.OpenFile(filepath.Join(pf.root, pf.path), openMode2Flag(qp.OREAD), 0)
	if err != nil {
		return nil, fmt.Errorf("could not open directory for reading: %v", err)
	}
	defer f.Close()

	dir, err := f.Readdir(-1)
	if err != nil {
		return nil, fmt.Errorf("could not read directory listing: %v", err)
	}

	s := make([]qp.Stat, len(dir))
	for idx, f := range dir {
		tpf := &ProxyFile{
			root:  pf.root,
			path:  f.Name(),
			user:  pf.user,
			group: pf.group,
		}

		s[idx] = tpf.stat(f)
	}

	return s, nil
}

// DirectList returns the []os.FileInfo for this directory, allowing
// implementations of fast directory listing.
func (pf *ProxyFile) DirectList() ([]os.FileInfo, error) {
	isdir, err := pf.IsDir()
	if err != nil {
		return nil, fmt.Errorf("could not inspect file: %v", err)
	}
	if !isdir {
		return nil, errors.New("not a directory")
	}

	f, err := os.OpenFile(filepath.Join(pf.root, pf.path), openMode2Flag(qp.OREAD), 0)
	if err != nil {
		return nil, fmt.Errorf("could not open directory for reading: %v", err)
	}
	defer f.Close()

	dir, err := f.Readdir(-1)
	if err != nil {
		return nil, fmt.Errorf("could not read directory listing: %v", err)
	}

	return dir, err
}

// Open implements File.
func (pf *ProxyFile) Open(user string, mode qp.OpenMode) (ReadWriteAtCloser, error) {
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
		return &proxyFileListHandle{
			Dir: pf,
		}, nil
	}

	return os.OpenFile(filepath.Join(pf.root, pf.path), openMode2Flag(mode), 0)
}

// CanRemove implements File.
func (pf *ProxyFile) CanRemove() (bool, error) {
	return true, nil
}

// Arrived implements File.
func (pf *ProxyFile) Arrived(_ string) (File, error) {
	return nil, nil
}

// IsDir implements File.
func (pf *ProxyFile) IsDir() (bool, error) {
	if err := pf.updateInfo(); err != nil {
		return false, err
	}
	pf.infoLock.RLock()
	defer pf.infoLock.RUnlock()
	return pf.info.IsDir(), nil
}

// Walk implements Dir.
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

// Create implements Dir.
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

// Remove implements Dir.
func (pf *ProxyFile) Remove(_, name string) error {
	return os.Remove(filepath.Join(pf.root, pf.path, name))
}

// Rename implements Dir.
func (pf *ProxyFile) Rename(_, oldname, newname string) error {
	op := filepath.Join(pf.root, pf.path, oldname)
	np := filepath.Join(pf.root, pf.path, newname)
	return os.Rename(op, np)
}

// NewProxyFile returns a new proxy file.
func NewProxyFile(root, path, user, group string) *ProxyFile {
	return &ProxyFile{
		root:  root,
		path:  path,
		user:  user,
		group: group,
	}
}
