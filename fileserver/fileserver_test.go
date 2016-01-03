package fileserver

import (
	"io"
	"sync"
	"testing"

	"github.com/joushou/qp"
	"github.com/joushou/qptools/fileserver/trees"
)

const TestVerbosity = Quiet

// TODO(kl): Verify the response messages, rather than just the side-effects.

// dummyRW absorbs writes, and EOFs on reads. It is used to satisfy the
// FileServer, and making the Start loop terminate immediately with an I/O
// error.
type dummyRW struct{}

func (dummyRW) Read([]byte) (int, error)    { return 0, io.EOF }
func (dummyRW) Write(p []byte) (int, error) { return len(p), nil }

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
	trees.SyntheticDir
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

// TestClunkRemove tests if a file is closed on clunk or remove. It does not
// test if remove actually removes the file.
func TestClunkRemove(t *testing.T) {
	buf := dummyRW{}
	ff := &fakeFile{}
	fs := New(buf, ff, nil, TestVerbosity)

	fs.version(&qp.VersionRequest{
		Tag:     qp.NOTAG,
		Version: qp.Version,
		MaxSize: 4096,
	})

	fs.attach(&qp.AttachRequest{
		Tag:     1,
		AuthFid: qp.NOFID,
		Fid:     0,
	})

	fs.open(&qp.OpenRequest{
		Tag:  1,
		Fid:  0,
		Mode: qp.OREAD,
	})

	if ff.opened != 1 {
		t.Errorf("open count was %d, expected 1", ff.opened)
	}

	fs.clunk(&qp.ClunkRequest{
		Tag: 1,
		Fid: 0,
	})

	if ff.opened != 0 {
		t.Errorf("open count was %d, expected 0", ff.opened)
	}

	fs.attach(&qp.AttachRequest{
		Tag:     1,
		AuthFid: qp.NOFID,
		Fid:     0,
	})

	fs.open(&qp.OpenRequest{
		Tag:  1,
		Fid:  0,
		Mode: qp.OREAD,
	})

	if ff.opened != 1 {
		t.Errorf("open count was %d, expected 1", ff.opened)
	}

	fs.remove(&qp.RemoveRequest{
		Tag: 1,
		Fid: 0,
	})

	if ff.opened != 0 {
		t.Errorf("open count was %d, expected 0", ff.opened)
	}
}

// TestCleanup tests that when I/O errors occur, all open files are properly
// closed and cleaned up, even if blocked in read or write calls.
func TestCleanup(t *testing.T) {
	buf := dummyRW{}
	ff := &fakeFile{}
	fs := New(buf, ff, nil, TestVerbosity)

	fs.version(&qp.VersionRequest{
		Tag:     qp.NOTAG,
		Version: qp.Version,
		MaxSize: 4096,
	})

	fs.attach(&qp.AttachRequest{
		Tag:     1,
		AuthFid: qp.NOFID,
		Fid:     0,
	})

	// Clone so we can have two opens
	fs.walk(&qp.WalkRequest{
		Tag:    1,
		Fid:    0,
		NewFid: 1,
	})

	fs.open(&qp.OpenRequest{
		Tag:  1,
		Fid:  0,
		Mode: qp.OREAD,
	})

	fs.open(&qp.OpenRequest{
		Tag:  2,
		Fid:  1,
		Mode: qp.OWRITE,
	})

	ff.rwLock.Lock()

	var wg1 sync.WaitGroup
	var wg2 sync.WaitGroup

	wg1.Add(2)
	wg2.Add(2)

	go func() {
		wg1.Done()
		fs.read(&qp.ReadRequest{
			Tag:    3,
			Fid:    0,
			Offset: 0,
			Count:  1024,
		})
		wg2.Done()
	}()

	go func() {
		wg1.Done()
		fs.write(&qp.WriteRequest{
			Tag:    4,
			Fid:    1,
			Offset: 0,
			Data:   []byte("Hello, world!"),
		})
		wg2.Done()
	}()

	if ff.opened != 2 {
		t.Errorf("open count was %d, expected 2", ff.opened)
	}

	wg1.Wait()

	err := fs.Start()
	if err != io.EOF {
		t.Errorf("start error was %v, expected %v", err, io.EOF)
	}

	if ff.opened != 0 {
		t.Errorf("open count was %d, expected 0", ff.opened)
	}

	ff.rwLock.Unlock()

	wg2.Wait()
}

// TestVersionCleanup tests that when a new Tversion message is sent, all open
// files are properly closed and cleaned up, even if blocked in read or write
// calls.
func TestVersionCleanup(t *testing.T) {
	buf := dummyRW{}
	ff := &fakeFile{}
	fs := New(buf, ff, nil, TestVerbosity)

	fs.version(&qp.VersionRequest{
		Tag:     qp.NOTAG,
		Version: qp.Version,
		MaxSize: 4096,
	})

	fs.attach(&qp.AttachRequest{
		Tag:     1,
		AuthFid: qp.NOFID,
		Fid:     0,
	})

	// Clone so we can have two opens
	fs.walk(&qp.WalkRequest{
		Tag:    1,
		Fid:    0,
		NewFid: 1,
	})

	fs.open(&qp.OpenRequest{
		Tag:  1,
		Fid:  0,
		Mode: qp.OREAD,
	})

	fs.open(&qp.OpenRequest{
		Tag:  2,
		Fid:  1,
		Mode: qp.OWRITE,
	})

	ff.rwLock.Lock()

	var wg1 sync.WaitGroup
	var wg2 sync.WaitGroup

	wg1.Add(2)
	wg2.Add(2)

	go func() {
		wg1.Done()
		fs.read(&qp.ReadRequest{
			Tag:    3,
			Fid:    0,
			Offset: 0,
			Count:  1024,
		})
		wg2.Done()
	}()

	go func() {
		wg1.Done()
		fs.write(&qp.WriteRequest{
			Tag:    4,
			Fid:    1,
			Offset: 0,
			Data:   []byte("Hello, world!"),
		})
		wg2.Done()
	}()

	if ff.opened != 2 {
		t.Errorf("open count was %d, expected 2", ff.opened)
	}

	wg1.Wait()

	fs.version(&qp.VersionRequest{
		Tag:     qp.NOTAG,
		Version: qp.Version,
		MaxSize: 4096,
	})

	if ff.opened != 0 {
		t.Errorf("open count was %d, expected 0", ff.opened)
	}

	ff.rwLock.Unlock()

	wg2.Wait()
}

// TestAuth tests if the authentication file behaves properly.
func TestAuth(t *testing.T) {
	buf := dummyRW{}
	af := &fakeFile{}
	ff := &fakeFile{}
	fs := New(buf, ff, nil, TestVerbosity)
	fs.AuthFile = af

	fs.version(&qp.VersionRequest{
		Tag:     qp.NOTAG,
		Version: qp.Version,
		MaxSize: 4096,
	})

	fs.auth(&qp.AuthRequest{
		Tag:     qp.NOTAG,
		AuthFid: 0,
	})

	if af.opened != 1 {
		t.Errorf("open count for authfile was %d, expected 1", ff.opened)
	}

	if ff.opened != 0 {
		t.Errorf("open count for root file was %d, expected 0", ff.opened)
	}

	// This should not work, as the auth file did not permit it.
	fs.attach(&qp.AttachRequest{
		Tag:     1,
		AuthFid: 0,
		Fid:     1,
	})

	fs.open(&qp.OpenRequest{
		Tag:  1,
		Fid:  1,
		Mode: qp.OREAD,
	})

	if af.opened != 1 {
		t.Errorf("open count for authfile was %d, expected 1", ff.opened)
	}

	if ff.opened != 0 {
		t.Errorf("open count for root file was %d, expected 0", ff.opened)
	}

	// This open shouldn't be allowed
	fs.attach(&qp.AttachRequest{
		Tag:     1,
		AuthFid: qp.NOFID,
		Fid:     2,
	})

	fs.open(&qp.OpenRequest{
		Tag:  1,
		Fid:  2,
		Mode: qp.OREAD,
	})

	if af.opened != 1 {
		t.Errorf("open count for authfile was %d, expected 1", ff.opened)
	}

	if ff.opened != 0 {
		t.Errorf("open count for root file was %d, expected 0", ff.opened)
	}

	af.authed = true

	// The auth file permits it now, so it should work
	fs.attach(&qp.AttachRequest{
		Tag:     1,
		AuthFid: 0,
		Fid:     1,
	})

	fs.open(&qp.OpenRequest{
		Tag:  1,
		Fid:  1,
		Mode: qp.OREAD,
	})

	if af.opened != 1 {
		t.Errorf("open count for authfile was %d, expected 1", ff.opened)
	}

	if ff.opened != 1 {
		t.Errorf("open count for root file was %d, expected 1", ff.opened)
	}
}
