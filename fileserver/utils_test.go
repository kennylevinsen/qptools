package fileserver

import (
	"bytes"
	"encoding/hex"
	"io"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/kennylevinsen/qp"
	"github.com/kennylevinsen/qptools/fileserver/trees"
)

// This file contains test helpers.

type walkHook struct {
	walkDest trees.File
	walkErr  error
	trees.Dir
}

func (w *walkHook) Walk(user, name string) (trees.File, error) {
	return w.walkDest, w.walkErr
}

func newWalkHook(d trees.Dir, f trees.File, e error) *walkHook {
	return &walkHook{
		walkDest: f,
		walkErr:  e,
		Dir:      d,
	}
}

type arrivedHook struct {
	arrivedDest trees.File
	arrivedErr  error
	trees.File
}

func (a *arrivedHook) Arrived(user string) (trees.File, error) {
	return a.arrivedDest, a.arrivedErr
}

func newArrivedHook(f1, f2 trees.File, e error) *arrivedHook {
	return &arrivedHook{
		arrivedDest: f2,
		arrivedErr:  e,
		File:        f1,
	}
}

// pipeRWC allows for tying two io.Pipe's into two io.ReadWriteClosers that can
// be used to simulate things like network behaviour.
type pipeRWC struct {
	r io.Reader
	w io.WriteCloser
}

func (prwc *pipeRWC) Read(p []byte) (int, error) {
	return prwc.r.Read(p)
}

func (prwc *pipeRWC) Write(p []byte) (int, error) {
	return prwc.w.Write(p)
}

func (prwc *pipeRWC) Close() error {
	return prwc.w.Close()
}

func newPipePair() (*pipeRWC, *pipeRWC) {
	p1r, p1w := io.Pipe()
	p2r, p2w := io.Pipe()

	return &pipeRWC{r: p1r, w: p2w}, &pipeRWC{r: p2r, w: p1w}
}

// debugThing ties a qp.Encode and qp.Decoder together over an io.ReadWriter.
// Useful for integration tests.
type debugThing struct {
	writeLock sync.Mutex
	*qp.Encoder
	*qp.Decoder
}

func (dt *debugThing) WriteMessage(m qp.Message) error {
	dt.writeLock.Lock()
	defer dt.writeLock.Unlock()
	return dt.Encoder.WriteMessage(m)
}

func newDebugThing(rw io.ReadWriter) *debugThing {
	return &debugThing{
		Encoder: &qp.Encoder{
			Protocol:    qp.NineP2000,
			Writer:      rw,
			MessageSize: 16 * 1024,
		},
		Decoder: &qp.Decoder{
			Protocol:    qp.NineP2000,
			Reader:      rw,
			MessageSize: 16 * 1024,
		},
	}
}

// fakeHandle keeps track of opens, and takes locks on read and write to check
// behaviour when blocking on I/O. It also also implements
// trees.Authenticator, returning true if authed is set on the associated
// fakeFile.
type fakeHandle struct {
	f *fakeFile
}

func (f *fakeHandle) Close() error {
	f.f.openLock.Lock()
	defer f.f.openLock.Unlock()

	f.f.opened--
	return nil
}

func (f *fakeHandle) ReadAt([]byte, int64) (int, error) {
	f.f.rwLock.RLock()
	defer f.f.rwLock.RUnlock()
	return 0, nil
}

func (f *fakeHandle) WriteAt([]byte, int64) (int, error) {
	f.f.rwLock.RLock()
	defer f.f.rwLock.RUnlock()
	return 0, nil
}

func (f *fakeHandle) Authenticated(user, service string) (bool, error) {
	return f.f.authed, nil
}

type fakeFile struct {
	trees.SyntheticFile
	opened   int
	openLock sync.Mutex
	rwLock   sync.RWMutex
	authed   bool
}

func (f *fakeFile) Open(user string, mode qp.OpenMode) (trees.ReadWriteAtCloser, error) {
	f.openLock.Lock()
	defer f.openLock.Unlock()

	f.opened++
	return &fakeHandle{
		f: f,
	}, nil
}

//
// Standard operations reused by many tests
//

func version(version string, tag qp.Tag, msize int, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.VersionRequest{
		Tag:         qp.NOTAG,
		Version:     qp.Version,
		MessageSize: 4096,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: version failed: %v", filepath.Base(file), line, err)
	}

	vm, ok := m.(*qp.VersionResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.VersionResponse, got %#v", filepath.Base(file), line, m)
	}

	if vm.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, vm.Tag)
	}

	if vm.Version != version {
		t.Fatalf("%s:%d: version response string incorrect: expected %s, got %s", filepath.Base(file), line, version, qp.Version)
	}
}

func auth(authfid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.AuthRequest{
		Tag:     tag,
		AuthFid: authfid,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: auth failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.AuthResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.AuthResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}
}

func authfail(authfid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.AuthRequest{
		Tag:     tag,
		AuthFid: authfid,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: auth failed: %v", filepath.Base(file), line, err)
	}

	em, ok := m.(*qp.ErrorResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.ErrorResponse, got %#v", filepath.Base(file), line, m)
	}

	if em.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, em.Tag)
	}

	if em.Error != errstr {
		t.Fatalf("%s:%d: error response incorrect: expected %s, got %s", filepath.Base(file), line, errstr, em.Error)
	}
}

func attach(fid, authfid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.AttachRequest{
		Tag:     tag,
		AuthFid: authfid,
		Fid:     fid,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: attach failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.AttachResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.AttachResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}
}

func attachfail(fid, authfid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.AttachRequest{
		Tag:     tag,
		AuthFid: authfid,
		Fid:     fid,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: attach failed: %v", filepath.Base(file), line, err)
	}

	em, ok := m.(*qp.ErrorResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.ErrorResponse, got %#v", filepath.Base(file), line, m)
	}

	if em.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, em.Tag)
	}

	if em.Error != errstr {
		t.Fatalf("%s:%d: error response incorrect: expected %s, got %s", filepath.Base(file), line, errstr, em.Error)
	}
}

func open(mode qp.OpenMode, fid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.OpenRequest{
		Tag:  tag,
		Fid:  fid,
		Mode: mode,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: open failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.OpenResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.OpenResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}
}

func openfail(mode qp.OpenMode, fid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.OpenRequest{
		Tag:  tag,
		Fid:  fid,
		Mode: mode,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: open failed: %v", filepath.Base(file), line, err)
	}

	em, ok := m.(*qp.ErrorResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.ErrorResponse, got %#v", filepath.Base(file), line, m)
	}

	if em.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, em.Tag)
	}

	if em.Error != errstr {
		t.Fatalf("%s:%d: error response incorrect: expected %s, got %s", filepath.Base(file), line, errstr, em.Error)
	}
}

func create(name string, mode qp.OpenMode, perm qp.FileMode, fid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.CreateRequest{
		Tag:         tag,
		Fid:         fid,
		Name:        name,
		Permissions: perm,
		Mode:        mode,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: create failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.CreateResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.CreateResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}
}

func createfail(name string, mode qp.OpenMode, perm qp.FileMode, fid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.CreateRequest{
		Tag:         tag,
		Fid:         fid,
		Name:        name,
		Permissions: perm,
		Mode:        mode,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: create failed: %v", filepath.Base(file), line, err)
	}

	em, ok := m.(*qp.ErrorResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.ErrorResponse, got %#v", filepath.Base(file), line, m)
	}

	if em.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, em.Tag)
	}

	if em.Error != errstr {
		t.Fatalf("%s:%d: error response incorrect: expected %s, got %s", filepath.Base(file), line, errstr, em.Error)
	}
}

func wstat(stat qp.Stat, fid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.WriteStatRequest{
		Tag:  tag,
		Fid:  fid,
		Stat: stat,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: create failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.WriteStatResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.CreateResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}
}

func wstatfail(stat qp.Stat, fid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.WriteStatRequest{
		Tag:  tag,
		Fid:  fid,
		Stat: stat,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: create failed: %v", filepath.Base(file), line, err)
	}

	em, ok := m.(*qp.ErrorResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.ErrorResponse, got %#v", filepath.Base(file), line, m)
	}

	if em.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, em.Tag)
	}

	if em.Error != errstr {
		t.Fatalf("%s:%d: error response incorrect: expected %s, got %s", filepath.Base(file), line, errstr, em.Error)
	}
}

func walk(names []string, newfid, fid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.WalkRequest{
		Tag:    tag,
		Fid:    fid,
		NewFid: newfid,
		Names:  names,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: walk failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.WalkResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.WalkResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}
}

func walkfail(names []string, newfid, fid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.WalkRequest{
		Tag:    tag,
		Fid:    fid,
		NewFid: newfid,
		Names:  names,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: walk failed: %v", filepath.Base(file), line, err)
	}

	em, ok := m.(*qp.ErrorResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.ErrorResponse, got %#v", filepath.Base(file), line, m)
	}

	if em.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, em.Tag)
	}

	if em.Error != errstr {
		t.Fatalf("%s:%d: error response incorrect: expected %s, got %s", filepath.Base(file), line, errstr, em.Error)
	}
}

func read(offset uint64, count uint32, fid qp.Fid, tag qp.Tag, expected []byte, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.ReadRequest{
		Tag:    tag,
		Fid:    fid,
		Offset: offset,
		Count:  count,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: read failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.ReadResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.ReadResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}

	if bytes.Compare(am.Data, expected) != 0 {
		t.Fatalf("%s:%d: response data incorrected:\nExpected: %s\nGot: %s\n", filepath.Base(file), line, hex.Dump(expected), hex.Dump(am.Data))
	}
}

func readfail(offset uint64, count uint32, fid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.ReadRequest{
		Tag:    tag,
		Fid:    fid,
		Offset: offset,
		Count:  count,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: read failed: %v", filepath.Base(file), line, err)
	}

	em, ok := m.(*qp.ErrorResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.ErrorResponse, got %#v", filepath.Base(file), line, m)
	}

	if em.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, em.Tag)
	}

	if em.Error != errstr {
		t.Fatalf("%s:%d: error response incorrect: expected %s, got %s", filepath.Base(file), line, errstr, em.Error)
	}
}

func write(offset uint64, payload []byte, fid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.WriteRequest{
		Tag:    tag,
		Fid:    fid,
		Offset: offset,
		Data:   payload,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: write failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.WriteResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.WriteResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}

	if am.Count != uint32(len(payload)) {
		t.Fatalf("%s:%d: response data incorrected: expected %d, got %d", filepath.Base(file), line, len(payload), am.Count)
	}
}

func writefail(offset uint64, payload []byte, fid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.WriteRequest{
		Tag:    tag,
		Fid:    fid,
		Offset: offset,
		Data:   payload,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: write failed: %v", filepath.Base(file), line, err)
	}

	em, ok := m.(*qp.ErrorResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.ErrorResponse, got %#v", filepath.Base(file), line, m)
	}

	if em.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, em.Tag)
	}

	if em.Error != errstr {
		t.Fatalf("%s:%d: error response incorrect: expected %s, got %s", filepath.Base(file), line, errstr, em.Error)
	}
}

func stat(fid qp.Fid, tag qp.Tag, expected qp.Stat, fs *FileServer, dbg *debugThing, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	dbg.WriteMessage(&qp.StatRequest{
		Tag: tag,
		Fid: fid,
	})

	m, err := dbg.ReadMessage()
	if err != nil {
		t.Fatalf("%s:%d: stat failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.StatResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.StatResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}

	if am.Stat != expected {
		t.Fatalf("%s:%d: response data incorrected:\nExpected: %#v\nGot: %#v\n", filepath.Base(file), line, expected, am.Stat)
	}
}
