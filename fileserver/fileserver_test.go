package fileserver

import (
	"bytes"
	"encoding/hex"
	"io"
	"sync"
	"testing"

	"github.com/joushou/qp"
	"github.com/joushou/qptools/fileserver/trees"
)

const TestVerbosity = Quiet

func TestWalkTo(t *testing.T) {
	//
	//	/
	//		file1
	// 		file2
	//      ...
	// 		dir1/
	// 			dir2/
	// 				file3
	//
	root := trees.NewSyntheticDir("", 0777, "", "")
	file1 := trees.NewSyntheticFile("file1", 0777, "", "")
	root.Add("file1", file1)
	file2 := trees.NewSyntheticFile("file2", 0777, "", "")
	root.Add("file2", file2)
	ddd := trees.NewSyntheticFile("...", 0777, "", "")
	root.Add("...", ddd)
	dir1 := trees.NewSyntheticDir("dir1", 0777, "", "")
	root.Add("dir1", dir1)
	dir2 := trees.NewSyntheticDir("dir2", 0777, "", "")
	dir1.Add("dir2", dir2)
	file3 := trees.NewSyntheticFile("file3", 0777, "", "")
	dir2.Add("file3", file3)

	s := &fidState{
		username: "",
		location: FilePath{root},
	}

	// Go to file1
	f1, q1, err := walkTo(s, []string{"file1"})
	if err != nil {
		t.Errorf("walk to file1 failed: %v", err)
	}
	if len(q1) != 1 {
		t.Errorf("expected 1 qid, got %d", len(q1))
	}
	if f1.location.Current() != file1 {
		t.Errorf("expected file1, got %v", f1)
	}

	// Go back up
	f2, q2, err := walkTo(f1, []string{".."})
	if err != nil {
		t.Errorf("walk to .. failed: %v", err)
	}
	if len(q2) != 1 {
		t.Errorf("expected 1 qid, got %d", len(q2))
	}
	if f2.location.Current() != root {
		t.Errorf("expected root, got %v", f2)
	}

	// Try to go up again
	f3, q3, err := walkTo(s, []string{".."})
	if err != nil {
		t.Errorf("walk to .. failed: %v", err)
	}
	if len(q3) != 1 {
		t.Errorf("expected 1 qid, got %d", len(q3))
	}
	if f3.location.Current() != root {
		t.Errorf("expected root, got %v", f3)
	}

	// Go to ...
	f4, q4, err := walkTo(s, []string{"..."})
	if err != nil {
		t.Errorf("walk to ... failed: %v", err)
	}
	if len(q4) != 1 {
		t.Errorf("expected 1 qid, got %d", len(q4))
	}
	if f4.location.Current() != ddd {
		t.Errorf("expected ddd, got %v", f4)
	}

	// Traverse multiple files
	f5, q5, err := walkTo(s, []string{"dir1", "dir2", "file3"})
	if err != nil {
		t.Errorf("walk to dir1/dir2/file3 failed: %v", err)
	}
	if len(q5) != 3 {
		t.Errorf("expected 3 qid, got %d", len(q5))
	}
	if f5.location.Current() != file3 {
		t.Errorf("expected file3, got %v", f5)
	}

	// Go to .
	f6, q6, err := walkTo(f1, []string{"."})
	if err != nil {
		t.Errorf("walk to . failed: %v", err)
	}
	if len(q6) != 1 {
		t.Errorf("expected 1 qid, got %d", len(q6))
	}
	if f6.location.Current() != file1 {
		t.Errorf("expected file1, got %v", f6)
	}

	// Complicated walk
	f7, q7, err := walkTo(s, []string{"dir1", "dir2", "file3", "..", ".", "..", "..", "file1", "..", "file2"})
	if err != nil {
		t.Errorf("walk to dir1/dir2/file3/.././../file1 failed: %v", err)
	}
	if len(q7) != 10 {
		t.Errorf("expected 10 qid, got %d", len(q7))
	}
	if f7.location.Current() != file2 {
		t.Errorf("expected file2, got %v", f7)
	}

	// Partial walk
	f8, q8, err := walkTo(s, []string{"dir1", "dir3"})
	if err != nil {
		t.Errorf("walk to dir1/dir3 failed: %v", err)
	}
	if len(q8) != 1 {
		t.Errorf("expected 1 qid, got %d", len(q8))
	}
	if f8 != nil {
		t.Errorf("expected nil file, got %v", f8)
	}

	// Fail on first walk
	f9, q9, err := walkTo(s, []string{"dir3", "dir3"})
	if err == nil {
		t.Errorf("walk to dir3/dir3 succeeded, expected failure")
	}
	if q9 != nil {
		t.Errorf("expected nil qids, got %v", q9)
	}
	if f9 != nil {
		t.Errorf("expected nil file, got %v", f9)
	}

	// nil walk
	f10, q10, err := walkTo(s, nil)
	if err != nil {
		t.Errorf("nil walk failed: %v", err)
	}
	if len(q10) != 0 {
		t.Errorf("expected 0 qid, got %d", len(q10))
	}
	if f10.location.Current() != root {
		t.Errorf("expected root, got %v", f10)
	}

	// Fail on second walk
	f11, q11, err := walkTo(s, []string{"dir1", "dir3", "file3"})
	if err != nil {
		t.Errorf("walk to dir1/dir3/file3 failed: %v", err)
	}
	if len(q11) != 1 {
		t.Errorf("expected nil qids, got %v", q11)
	}
	if f11 != nil {
		t.Errorf("expected nil file, got %v", f11)
	}

	// Fail due to non-directory walk
	f12, q12, err := walkTo(s, []string{"file1", "file2", "file3"})
	if err != nil {
		t.Errorf("walk to file1/file2/file3 failed: %v", err)
	}
	if len(q12) != 1 {
		t.Errorf("expected nil qids, got %v", q12)
	}
	if f12 != nil {
		t.Errorf("expected nil file, got %v", f12)
	}
}

// TestUknownFid checks if unknown fids are denied.
func TestUnknownFid(t *testing.T) {
	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	ff := &fakeFile{}
	fs := New(p2, ff, nil)
	fs.Verbosity = TestVerbosity
	go fs.Serve()

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	openfail(qp.OREAD, 0, 1, UnknownFid, fs, dbg, t)
	openfail(qp.OREAD, qp.NOFID, 1, UnknownFid, fs, dbg, t)
	walkfail(nil, 2, 1, 1, UnknownFid, fs, dbg, t)
}

// TestUseOfNOFID checks if NOFID is denied as new fid. It may only be used to
// represent that a field has not been set.
func TestUseOfNOFID(t *testing.T) {
	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	ff := &fakeFile{}
	fs := New(p2, ff, nil)
	fs.Verbosity = TestVerbosity
	go fs.Serve()

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	authfail(qp.NOFID, 1, InvalidFid, fs, dbg, t)
	attachfail(qp.NOFID, qp.NOFID, 1, InvalidFid, fs, dbg, t)
	walkfail(nil, qp.NOFID, qp.NOFID, 1, InvalidFid, fs, dbg, t)
}

// TestClunkRemove tests if a file is closed on clunk or remove. It does not
// test if remove actually removes the file.
func TestClunkRemove(t *testing.T) {
	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	ff := &fakeFile{}
	fs := New(p2, ff, nil)
	fs.Verbosity = TestVerbosity
	go fs.Serve()

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(0, qp.NOFID, 1, fs, dbg, t)
	open(qp.OREAD, 0, 1, fs, dbg, t)

	if ff.opened != 1 {
		t.Errorf("open count was %d, expected 1", ff.opened)
	}

	dbg.WriteMessage(&qp.ClunkRequest{
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

	dbg.WriteMessage(&qp.RemoveRequest{
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
	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	ff := &fakeFile{}
	fs := New(p2, ff, nil)
	fs.Verbosity = TestVerbosity
	reschan := make(chan error, 0)

	go func() { reschan <- fs.Serve() }()

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
		dbg.WriteMessage(&qp.ReadRequest{
			Tag:    3,
			Fid:    0,
			Offset: 0,
			Count:  1024,
		})
		wg2.Done()
	}()

	// Issue a write that will block.
	go func() {
		wg1.Done()
		dbg.WriteMessage(&qp.WriteRequest{
			Tag:    4,
			Fid:    1,
			Offset: 0,
			Data:   []byte("Hello, world!"),
		})
		wg2.Done()
	}()
	wg1.Wait()

	if ff.opened != 2 {
		t.Errorf("open count was %d, expected 2", ff.opened)
	}

	// Close channel and wait for the serve loop to die.
	p1.Close()
	p2.Close()
	err := <-reschan
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
	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	ff := &fakeFile{}
	fs := New(p2, ff, nil)
	fs.Verbosity = TestVerbosity
	go fs.Serve()

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
		dbg.WriteMessage(&qp.ReadRequest{
			Tag:    3,
			Fid:    0,
			Offset: 0,
			Count:  1024,
		})
		wg2.Done()
	}()

	// Issue a write that will block.
	go func() {
		wg1.Done()
		dbg.WriteMessage(&qp.WriteRequest{
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
	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	ff := &fakeFile{}
	fs := New(p2, ff, nil)
	fs.Verbosity = TestVerbosity
	go fs.Serve()

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	authfail(0, qp.NOTAG, AuthNotSupported, fs, dbg, t)
}

// TestAuth tests if the authentication file behaves properly.
func TestAuth(t *testing.T) {
	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	af := &fakeFile{}
	ff := &fakeFile{}
	fs := New(p2, ff, nil)
	fs.Verbosity = TestVerbosity
	fs.AuthFile = af
	go fs.Serve()

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
	root := trees.NewSyntheticDir("", 0777, "", "")
	file1 := trees.NewSyntheticFile("file1", 0777, "", "")
	file1.SetContent([]byte("Some content"))
	root.Add("file1", file1)

	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	fs := New(p2, root, nil)
	fs.Verbosity = TestVerbosity
	go fs.Serve()

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(1, qp.NOFID, 1, fs, dbg, t)
	walk([]string{"file1"}, 2, 1, 1, fs, dbg, t)
	readfail(0, 1024, 2, 1, FidNotOpen, fs, dbg, t)

	open(qp.OREAD, 2, 1, fs, dbg, t)
	read(0, 1024, 2, 1, []byte("Some content"), fs, dbg, t)
	read(0, 0, 2, 1, nil, fs, dbg, t)
	read(5, 1024, 2, 1, []byte("content"), fs, dbg, t)
	read(11, 1024, 2, 1, []byte("t"), fs, dbg, t)
	read(12, 1024, 2, 1, nil, fs, dbg, t)
	read(1024, 1024, 2, 1, nil, fs, dbg, t)

	s1, _ := file1.Stat()
	sb1, _ := s1.MarshalBinary()

	walk(nil, 3, 1, 1, fs, dbg, t)
	open(qp.OREAD, 3, 1, fs, dbg, t)
	read(0, 1024, 3, 1, sb1, fs, dbg, t)
}

// TestWrite tests if a file can be succesfully written to.
func TestWrite(t *testing.T) {
	root := trees.NewSyntheticDir("", 0777, "", "")
	file1 := trees.NewSyntheticFile("file1", 0777, "", "")
	root.Add("file1", file1)

	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	fs := New(p2, root, nil)
	fs.Verbosity = TestVerbosity
	go fs.Serve()

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(1, qp.NOFID, 1, fs, dbg, t)
	walk([]string{"file1"}, 2, 1, 1, fs, dbg, t)

	somecnt := []byte("Some content")

	writefail(0, somecnt, 2, 1, FidNotOpen, fs, dbg, t)
	open(qp.OWRITE, 2, 1, fs, dbg, t)
	write(0, []byte("Some"), 2, 1, fs, dbg, t)
	write(4, []byte(" cont"), 2, 1, fs, dbg, t)
	write(1024, []byte("ent"), 2, 1, fs, dbg, t)

	if bytes.Compare(file1.Content, somecnt) != 0 {
		t.Errorf("content did not match:\nExpected: %s\nGot: %s", hex.Dump(somecnt), hex.Dump(file1.Content))
	}

	openfail(qp.OWRITE, 1, 1, OpenWriteOnDir, fs, dbg, t)
}

// TestStat verifies that stat requests behave as expected.
func TestStat(t *testing.T) {
	root := trees.NewSyntheticDir("", 0777, "", "")
	file1 := trees.NewSyntheticFile("file1", 0777, "", "")
	file1.SetContent([]byte("Some content"))
	root.Add("file1", file1)

	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	fs := New(p2, root, nil)
	fs.Verbosity = TestVerbosity
	go fs.Serve()

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(1, qp.NOFID, 1, fs, dbg, t)
	walk([]string{"file1"}, 2, 1, 1, fs, dbg, t)

	s1, _ := file1.Stat()
	stat(2, 1, s1, fs, dbg, t)

	walk(nil, 3, 1, 1, fs, dbg, t)

	s2, _ := root.Stat()
	stat(3, 1, s2, fs, dbg, t)
}
