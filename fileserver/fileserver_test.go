package fileserver

import (
	"io"
	"sync"
	"testing"

	"github.com/joushou/qp"
	"github.com/joushou/qptools/fileserver/trees"
)

// dummyRW absorbs writes, and EOFs on reads. It is used to satisfy the
// FileServer, and making the Start loop terminate immediately with an I/O
// error.
type dummyRW struct{}

func (dummyRW) Read([]byte) (int, error)    { return 0, io.EOF }
func (dummyRW) Write(p []byte) (int, error) { return len(p), nil }

type fakeHandle struct {
	trees.SyntheticHandle
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

type fakeFile struct {
	trees.RAMTree
	opened   int
	openLock sync.Mutex
	rwLock   sync.RWMutex
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
	fs := New(buf, ff, nil, Quiet)

	fs.version(&qp.VersionRequest{
		Tag:     qp.NOTAG,
		Version: qp.Version,
		MaxSize: 4096,
	})

	fs.attach(&qp.AttachRequest{
		Tag: 1,
		Fid: 0,
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
		Tag: 1,
		Fid: 0,
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
	fs := New(buf, ff, nil, Quiet)

	fs.version(&qp.VersionRequest{
		Tag:     qp.NOTAG,
		Version: qp.Version,
		MaxSize: 4096,
	})

	fs.attach(&qp.AttachRequest{
		Tag: 1,
		Fid: 0,
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

	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		fs.read(&qp.ReadRequest{
			Tag:    3,
			Fid:    0,
			Offset: 0,
			Count:  1024,
		})
		wg.Done()
	}()

	go func() {
		fs.write(&qp.WriteRequest{
			Tag:    4,
			Fid:    1,
			Offset: 0,
			Data:   []byte("Hello, world!"),
		})
		wg.Done()
	}()

	if ff.opened != 2 {
		t.Errorf("open count was %d, expected 2", ff.opened)
	}

	err := fs.Start()
	if err != io.EOF {
		t.Errorf("start error was %v, expected %v", err, io.EOF)
	}

	if ff.opened != 0 {
		t.Errorf("open count was %d, expected 0", ff.opened)
	}

	ff.rwLock.Unlock()

	wg.Wait()
}
