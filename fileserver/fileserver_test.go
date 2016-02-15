package fileserver

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/joushou/qp"
	"github.com/joushou/qptools/fileserver/trees"
)

const TestVerbosity = Quiet

//
// Utility types
//

// debugRW stores writes and EOFs on reads. It is used to satisfy the
// FileServer, and making the Start loop terminate immediately with an I/O
// error.
type debugRW struct {
	buf     *bytes.Buffer
	dec     *qp.Decoder
	buflock sync.Mutex
}

func (*debugRW) Read([]byte) (int, error) { return 0, io.EOF }

func (d *debugRW) Write(p []byte) (int, error) {
	d.buflock.Lock()
	defer d.buflock.Unlock()
	return d.buf.Write(p)
}

func (d *debugRW) NextMessage() (qp.Message, error) {
	d.buflock.Lock()
	defer d.buflock.Unlock()

	errchan := make(chan error, 0)
	respchan := make(chan qp.Message, 0)

	go func() {
		resp, err := d.dec.NextMessage()
		if err != nil {
			errchan <- err
		} else {
			respchan <- resp
		}
	}()

	select {
	case <-time.After(5 * time.Second):
		return nil, errors.New("message not received within timeout")
	case err := <-errchan:
		return nil, err
	case resp := <-respchan:
		return resp, nil
	}
}

func newDebugRW() *debugRW {
	buf := new(bytes.Buffer)
	return &debugRW{
		buf: buf,
		dec: &qp.Decoder{
			Reader:      buf,
			Protocol:    qp.NineP2000,
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

func (f *fakeHandle) Read([]byte) (int, error) {
	f.f.rwLock.RLock()
	defer f.f.rwLock.RUnlock()
	return 0, nil
}

func (f *fakeHandle) Write([]byte) (int, error) {
	f.f.rwLock.RLock()
	defer f.f.rwLock.RUnlock()
	return 0, nil
}

func (f *fakeHandle) Seek(int64, int) (int64, error) {
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

func (f *fakeFile) Open(user string, mode qp.OpenMode) (trees.ReadWriteSeekCloser, error) {
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

func version(version string, tag qp.Tag, msize int, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.version(&qp.VersionRequest{
		Tag:         qp.NOTAG,
		Version:     qp.Version,
		MessageSize: 4096,
	})

	m, err := dbg.NextMessage()
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

func auth(authfid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.auth(&qp.AuthRequest{
		Tag:     tag,
		AuthFid: authfid,
	})

	m, err := dbg.NextMessage()
	if err != nil {
		t.Fatalf("%s:%d: attach failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.AuthResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.AuthResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}
}

func authfail(authfid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.auth(&qp.AuthRequest{
		Tag:     tag,
		AuthFid: authfid,
	})

	m, err := dbg.NextMessage()
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

func attach(fid, authfid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.attach(&qp.AttachRequest{
		Tag:     tag,
		AuthFid: authfid,
		Fid:     fid,
	})

	m, err := dbg.NextMessage()
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

func attachfail(fid, authfid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.attach(&qp.AttachRequest{
		Tag:     tag,
		AuthFid: authfid,
		Fid:     fid,
	})

	m, err := dbg.NextMessage()
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

func open(mode qp.OpenMode, fid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.open(&qp.OpenRequest{
		Tag:  tag,
		Fid:  fid,
		Mode: mode,
	})

	m, err := dbg.NextMessage()
	if err != nil {
		t.Fatalf("%s:%d: attach failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.OpenResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.OpenResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}
}

func openfail(mode qp.OpenMode, fid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.open(&qp.OpenRequest{
		Tag:  tag,
		Fid:  fid,
		Mode: mode,
	})

	m, err := dbg.NextMessage()
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

func walk(names []string, newfid, fid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.walk(&qp.WalkRequest{
		Tag:    tag,
		Fid:    fid,
		NewFid: newfid,
		Names:  names,
	})

	m, err := dbg.NextMessage()
	if err != nil {
		t.Fatalf("%s:%d: attach failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.WalkResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.WalkResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}
}

func walkfail(names []string, newfid, fid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.walk(&qp.WalkRequest{
		Tag:    tag,
		Fid:    fid,
		NewFid: newfid,
		Names:  names,
	})

	m, err := dbg.NextMessage()
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

func read(offset uint64, count uint32, fid qp.Fid, tag qp.Tag, expected []byte, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.read(&qp.ReadRequest{
		Tag:    tag,
		Fid:    fid,
		Offset: offset,
		Count:  count,
	})

	m, err := dbg.NextMessage()
	if err != nil {
		t.Fatalf("%s:%d: attach failed: %v", filepath.Base(file), line, err)
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

func readfail(offset uint64, count uint32, fid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.read(&qp.ReadRequest{
		Tag:    tag,
		Fid:    fid,
		Offset: offset,
		Count:  count,
	})

	m, err := dbg.NextMessage()
	if err != nil {
		t.Fatalf("%s:%d: attach failed: %v", filepath.Base(file), line, err)
	}

	em, ok := m.(*qp.ErrorResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.AttachResponse, got %#v", filepath.Base(file), line, m)
	}

	if em.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, em.Tag)
	}

	if em.Error != errstr {
		t.Fatalf("%s:%d: error response incorrect: expected %s, got %s", filepath.Base(file), line, errstr, em.Error)
	}
}

func write(offset uint64, payload []byte, fid qp.Fid, tag qp.Tag, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.write(&qp.WriteRequest{
		Tag:    tag,
		Fid:    fid,
		Offset: offset,
		Data:   payload,
	})

	m, err := dbg.NextMessage()
	if err != nil {
		t.Fatalf("%s:%d: attach failed: %v", filepath.Base(file), line, err)
	}

	am, ok := m.(*qp.WriteResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.ReadResponse, got %#v", filepath.Base(file), line, m)
	}

	if am.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, am.Tag)
	}

	if am.Count != uint32(len(payload)) {
		t.Fatalf("%s:%d: response data incorrected: expected %d, got %d", filepath.Base(file), line, len(payload), am.Count)
	}
}

func writefail(offset uint64, payload []byte, fid qp.Fid, tag qp.Tag, errstr string, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.write(&qp.WriteRequest{
		Tag:    tag,
		Fid:    fid,
		Offset: offset,
		Data:   payload,
	})

	m, err := dbg.NextMessage()
	if err != nil {
		t.Fatalf("%s:%d: attach failed: %v", filepath.Base(file), line, err)
	}

	em, ok := m.(*qp.ErrorResponse)
	if !ok {
		t.Fatalf("%s:%d: wrong response: expected a *qp.AttachResponse, got %#v", filepath.Base(file), line, m)
	}

	if em.Tag != tag {
		t.Fatalf("%s:%d: response tag incorrect: expected %d, got %d", filepath.Base(file), line, tag, em.Tag)
	}

	if em.Error != errstr {
		t.Fatalf("%s:%d: error response incorrect: expected %s, got %s", filepath.Base(file), line, errstr, em.Error)
	}
}

func stat(fid qp.Fid, tag qp.Tag, expected qp.Stat, fs *FileServer, dbg *debugRW, t *testing.T) {
	_, file, line, _ := runtime.Caller(1)
	fs.stat(&qp.StatRequest{
		Tag: tag,
		Fid: fid,
	})

	m, err := dbg.NextMessage()
	if err != nil {
		t.Fatalf("%s:%d: attach failed: %v", filepath.Base(file), line, err)
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

//
// Tests
//

// TestUknownFid checks if unknown fids are denied.
func TestUnknownFid(t *testing.T) {
	dbg := newDebugRW()
	ff := &fakeFile{}
	fs := New(dbg, ff, nil)
	fs.Verbosity = TestVerbosity

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	openfail(qp.OREAD, 0, 1, UnknownFid, fs, dbg, t)
	openfail(qp.OREAD, qp.NOFID, 1, UnknownFid, fs, dbg, t)
	walkfail(nil, 2, 1, 1, UnknownFid, fs, dbg, t)
}

// TestUseOfNOFID checks if NOFID is denied as new fid. It may only be used to
// represent that a field has not been set.
func TestUseOfNOFID(t *testing.T) {
	dbg := newDebugRW()
	ff := &fakeFile{}
	fs := New(dbg, ff, nil)
	fs.Verbosity = TestVerbosity

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	authfail(qp.NOFID, 1, InvalidFid, fs, dbg, t)
	attachfail(qp.NOFID, qp.NOFID, 1, InvalidFid, fs, dbg, t)
	walkfail(nil, qp.NOFID, qp.NOFID, 1, InvalidFid, fs, dbg, t)
}

// TestClunkRemove tests if a file is closed on clunk or remove. It does not
// test if remove actually removes the file.
func TestClunkRemove(t *testing.T) {
	dbg := newDebugRW()
	ff := &fakeFile{}
	fs := New(dbg, ff, nil)
	fs.Verbosity = TestVerbosity

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(0, qp.NOFID, 1, fs, dbg, t)
	open(qp.OREAD, 0, 1, fs, dbg, t)

	if ff.opened != 1 {
		t.Errorf("open count was %d, expected 1", ff.opened)
	}

	fs.clunk(&qp.ClunkRequest{
		Tag: 1,
		Fid: 0,
	})
	dbg.NextMessage()

	if ff.opened != 0 {
		t.Errorf("open count was %d, expected 0", ff.opened)
	}

	attach(0, qp.NOFID, 1, fs, dbg, t)
	open(qp.OREAD, 0, 1, fs, dbg, t)

	if ff.opened != 1 {
		t.Errorf("open count was %d, expected 1", ff.opened)
	}

	fs.remove(&qp.RemoveRequest{
		Tag: 1,
		Fid: 0,
	})
	dbg.NextMessage()

	if ff.opened != 0 {
		t.Errorf("open count was %d, expected 0", ff.opened)
	}
}

// TestCleanup tests that when I/O errors occur, all open files are properly
// closed and cleaned up, even if blocked in read or write calls.
func TestCleanup(t *testing.T) {
	dbg := newDebugRW()
	ff := &fakeFile{}
	fs := New(dbg, ff, nil)
	fs.Verbosity = TestVerbosity

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(0, qp.NOFID, 1, fs, dbg, t)
	walk(nil, 1, 0, 1, fs, dbg, t)
	open(qp.OREAD, 0, 1, fs, dbg, t)
	open(qp.OWRITE, 1, 1, fs, dbg, t)

	// Make it so read and write will block.
	ff.rwLock.Lock()

	var wg1 sync.WaitGroup
	var wg2 sync.WaitGroup

	wg1.Add(2)
	wg2.Add(2)

	// Issue a read that will block.
	go func() {
		wg1.Done()
		fs.read(&qp.ReadRequest{
			Tag:    3,
			Fid:    0,
			Offset: 0,
			Count:  1024,
		})
		dbg.NextMessage()
		wg2.Done()
	}()

	// Issue a write that will block.
	go func() {
		wg1.Done()
		fs.write(&qp.WriteRequest{
			Tag:    4,
			Fid:    1,
			Offset: 0,
			Data:   []byte("Hello, world!"),
		})
		dbg.NextMessage()
		wg2.Done()
	}()

	if ff.opened != 2 {
		t.Errorf("open count was %d, expected 2", ff.opened)
	}

	// Wait to ensure that the calls are being issued.
	wg1.Wait()

	err := fs.Serve()
	if err != io.EOF {
		t.Errorf("start error was %v, expected %v", err, io.EOF)
	}

	if ff.opened != 0 {
		t.Errorf("open count was %d, expected 0", ff.opened)
	}

	// Unblock.
	ff.rwLock.Unlock()

	wg2.Wait()
}

// TestVersionCleanup tests that when a new Tversion message is sent, all open
// files are properly closed and cleaned up, even if blocked in read or write
// calls.
func TestVersionCleanup(t *testing.T) {
	dbg := newDebugRW()
	ff := &fakeFile{}
	fs := New(dbg, ff, nil)
	fs.Verbosity = TestVerbosity

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(0, qp.NOFID, 1, fs, dbg, t)
	walk(nil, 1, 0, 1, fs, dbg, t)
	open(qp.OREAD, 0, 1, fs, dbg, t)
	open(qp.OWRITE, 1, 1, fs, dbg, t)

	// Make it so read and write will block.
	ff.rwLock.Lock()

	var wg1 sync.WaitGroup
	var wg2 sync.WaitGroup

	wg1.Add(2)
	wg2.Add(2)

	// Issue a read that will block.
	go func() {
		wg1.Done()
		fs.read(&qp.ReadRequest{
			Tag:    3,
			Fid:    0,
			Offset: 0,
			Count:  1024,
		})
		dbg.NextMessage()
		wg2.Done()
	}()

	// Issue a write that will block.
	go func() {
		wg1.Done()
		fs.write(&qp.WriteRequest{
			Tag:    4,
			Fid:    1,
			Offset: 0,
			Data:   []byte("Hello, world!"),
		})
		dbg.NextMessage()
		wg2.Done()
	}()

	if ff.opened != 2 {
		t.Errorf("open count was %d, expected 2", ff.opened)
	}

	// Wait to ensure that the calls are being issued.
	wg1.Wait()

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)

	if ff.opened != 0 {
		t.Errorf("open count was %d, expected 0", ff.opened)
	}

	// Unblock.
	ff.rwLock.Unlock()

	wg2.Wait()
}

// TestNoAuth tests if the authentication is declined when no authfile is present.
func TestNoAuth(t *testing.T) {
	dbg := newDebugRW()
	ff := &fakeFile{}
	fs := New(dbg, ff, nil)
	fs.Verbosity = TestVerbosity

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	authfail(0, qp.NOTAG, AuthNotSupported, fs, dbg, t)
}

// TestAuth tests if the authentication file behaves properly.
func TestAuth(t *testing.T) {
	dbg := newDebugRW()
	af := &fakeFile{}
	ff := &fakeFile{}
	fs := New(dbg, ff, nil)
	fs.Verbosity = TestVerbosity
	fs.AuthFile = af

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	auth(0, qp.NOTAG, fs, dbg, t)

	if af.opened != 1 {
		t.Errorf("open count for authfile was %d, expected 1", ff.opened)
	}

	if ff.opened != 0 {
		t.Errorf("open count for root file was %d, expected 0", ff.opened)
	}

	// This should not work, as the auth file did not permit it.
	attachfail(1, 0, 1, PermissionDenied, fs, dbg, t)

	// This should not work as the fid does not exist.
	attachfail(1, 2, 1, UnknownFid, fs, dbg, t)

	// This shouldn't work either, as we need.
	attachfail(1, qp.NOFID, 2, AuthRequired, fs, dbg, t)

	af.authed = true

	// The auth file permits it now, so it should work.
	attach(1, 0, 1, fs, dbg, t)

	open(qp.OREAD, 1, 1, fs, dbg, t)

	if af.opened != 1 {
		t.Errorf("open count for authfile was %d, expected 1", ff.opened)
	}

	if ff.opened != 1 {
		t.Errorf("open count for root file was %d, expected 1", ff.opened)
	}
}

// TestRead tests if a file and directory can be successfully read.
func TestRead(t *testing.T) {
	dbg := newDebugRW()
	root := trees.NewSyntheticDir("", 0777, "", "")
	file1 := trees.NewSyntheticFile("file1", 0777, "", "")
	file1.SetContent([]byte("Some content"))
	root.Add("file1", file1)
	fs := New(dbg, root, nil)

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(1, qp.NOFID, 1, fs, dbg, t)
	walk([]string{"file1"}, 2, 1, 1, fs, dbg, t)
	readfail(0, 1024, 2, 1, FidNotOpen, fs, dbg, t)

	open(qp.OREAD, 2, 1, fs, dbg, t)
	read(0, 1024, 2, 1, []byte("Some content"), fs, dbg, t)
	read(5, 1024, 2, 1, []byte("content"), fs, dbg, t)
	read(11, 1024, 2, 1, []byte("t"), fs, dbg, t)
	read(12, 1024, 2, 1, nil, fs, dbg, t)
	read(1024, 1024, 2, 1, nil, fs, dbg, t)

	s1, _ := file1.Stat()
	sb1, _ := s1.MarshalBinary()

	walk(nil, 3, 1, 1, fs, dbg, t)
	open(qp.OREAD, 3, 1, fs, dbg, t)
	read(0, 1024, 3, 1, sb1, fs, dbg, t)
	readfail(1, 1024, 3, 1, "seek to other than 0 on dir illegal", fs, dbg, t)
}

// TestWrite tests if a file can be succesfully written to.
func TestWrite(t *testing.T) {
	dbg := newDebugRW()
	root := trees.NewSyntheticDir("", 0777, "", "")
	file1 := trees.NewSyntheticFile("file1", 0777, "", "")
	root.Add("file1", file1)
	fs := New(dbg, root, nil)

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(1, qp.NOFID, 1, fs, dbg, t)
	walk([]string{"file1"}, 2, 1, 1, fs, dbg, t)

	writefail(0, []byte("Some content"), 2, 1, FidNotOpen, fs, dbg, t)
	open(qp.OWRITE, 2, 1, fs, dbg, t)
	write(0, []byte("Some"), 2, 1, fs, dbg, t)
	write(4, []byte(" cont"), 2, 1, fs, dbg, t)
	write(1024, []byte("ent"), 2, 1, fs, dbg, t)

	if bytes.Compare(file1.Content, []byte("Some content")) != 0 {
		t.Errorf("content did not match: expected %s, got %s", "Some content", file1.Content)
	}

	openfail(qp.OWRITE, 1, 1, OpenWriteOnDir, fs, dbg, t)
}

func TestStat(t *testing.T) {
	dbg := newDebugRW()
	root := trees.NewSyntheticDir("", 0777, "", "")
	file1 := trees.NewSyntheticFile("file1", 0777, "", "")
	file1.SetContent([]byte("Some content"))
	root.Add("file1", file1)
	fs := New(dbg, root, nil)

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(1, qp.NOFID, 1, fs, dbg, t)
	walk([]string{"file1"}, 2, 1, 1, fs, dbg, t)

	s1, _ := file1.Stat()
	stat(2, 1, s1, fs, dbg, t)

	walk(nil, 3, 1, 1, fs, dbg, t)

	s2, _ := root.Stat()
	stat(3, 1, s2, fs, dbg, t)
}
