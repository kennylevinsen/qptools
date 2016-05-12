package fileserver

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/joushou/qp"
	"github.com/joushou/qptools/fileserver/trees"
)

const TestVerbosity = Quiet

func testStructure1() trees.Dir {
	root := trees.NewSyntheticDir("root", 0777, "", "")

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

	dir3 := trees.NewSyntheticDir("dir3", 0777, "", "")
	file4 := trees.NewSyntheticFile("file4", 0777, "", "")
	dir3.Add("file4", file4)
	file5 := trees.NewSyntheticFile("file5", 0777, "", "")
	whdir3 := newWalkHook(dir3, file5, nil)
	root.Add("dir3", whdir3)

	dir4 := trees.NewSyntheticDir("dir4", 0777, "", "")
	file6 := trees.NewSyntheticFile("file6", 0777, "", "")
	dir4.Add("file6", file6)
	whdir4 := newWalkHook(dir4, nil, fmt.Errorf("omg"))
	root.Add("dir4", whdir4)

	dir5 := trees.NewSyntheticDir("dir5", 0777, "", "")
	root.Add("dir5", dir5)

	file7 := trees.NewSyntheticFile("file7", 0777, "", "")
	file8 := trees.NewSyntheticFile("file8", 0777, "", "")
	afile7 := newArrivedHook(file7, file8, nil)
	dir5.Add("file7", afile7)

	file9 := trees.NewSyntheticFile("file9", 0777, "", "")
	afile9 := newArrivedHook(file9, nil, fmt.Errorf("omg"))
	dir5.Add("file9", afile9)

	return root
}

func verifyWalkSuccess(s *fidState, names []string, target string) error {
	f1, q1, err := walkTo(s, names)
	if err != nil {
		return fmt.Errorf("walk to %s failed: %v", target, err)
	}
	if len(q1) != len(names) {
		return fmt.Errorf("expected %d qid, got %d", len(names), len(q1))
	}
	f := f1.location.Current()
	name, err := f.Name()
	if err != nil {
		return fmt.Errorf("could not get destination file name: %v", err)
	}
	if name != target {
		return fmt.Errorf("expected file name %s, got %s", target, name)
	}

	return nil
}

func verifyWalkFailure(s *fidState, names []string, expectedQids int, expectNilQids, expectError bool) error {
	f1, q1, err := walkTo(s, names)
	if expectError && err == nil {
		return fmt.Errorf("walk did not return error as expected")
	}
	if expectNilQids && q1 != nil {
		return fmt.Errorf("walk did not return nil qids as expected")
	}
	if expectedQids != len(q1) {
		return fmt.Errorf("expected %d qid, got %d", expectedQids, len(q1))
	}
	if f1 != nil {
		return fmt.Errorf("walk did not return nil *fidState as expected")
	}

	return nil
}

func TestWalkToFile(t *testing.T) {
	root := testStructure1()
	s := &fidState{location: FilePath{root}}
	if err := verifyWalkSuccess(s, []string{"file1"}, "file1"); err != nil {
		t.Errorf("1: %v", err)
	}
	if err := verifyWalkSuccess(s, []string{"dir1", "dir2", "file3"}, "file3"); err != nil {
		t.Errorf("2: %v", err)
	}
}

func TestWalkUp(t *testing.T) {
	root := testStructure1()
	s1 := &fidState{location: FilePath{root}}
	if err := verifyWalkSuccess(s1, []string{"file1", ".."}, "root"); err != nil {
		t.Errorf("1: %v", err)
	}

	f, _ := root.Walk("", "file1")
	s2 := &fidState{location: FilePath{root, f}}
	if err := verifyWalkSuccess(s2, []string{".."}, "root"); err != nil {
		t.Errorf("2: %v", err)
	}

	if err := verifyWalkSuccess(s1, []string{".."}, "root"); err != nil {
		t.Errorf("3: %v", err)
	}
}

func TestWalkDot(t *testing.T) {
	root := testStructure1()
	s1 := &fidState{location: FilePath{root}}
	if err := verifyWalkSuccess(s1, []string{"."}, "root"); err != nil {
		t.Errorf("1: %v", err)
	}
	if err := verifyWalkSuccess(s1, []string{"file1", "."}, "file1"); err != nil {
		t.Errorf("2: %v", err)
	}

	f, _ := root.Walk("", "file1")
	s2 := &fidState{location: FilePath{root, f}}
	if err := verifyWalkSuccess(s2, []string{"."}, "file1"); err != nil {
		t.Errorf("3: %v", err)
	}
}

func TestWalkNil(t *testing.T) {
	root := testStructure1()
	s1 := &fidState{location: FilePath{root}}
	if err := verifyWalkSuccess(s1, nil, "root"); err != nil {
		t.Errorf("1: %v", err)
	}

	f, _ := root.Walk("", "file1")
	s2 := &fidState{location: FilePath{root, f}}
	if err := verifyWalkSuccess(s2, nil, "file1"); err != nil {
		t.Errorf("2: %v", err)
	}
}

func TestWalkComplicated(t *testing.T) {
	root := testStructure1()
	s := &fidState{location: FilePath{root}}
	if err := verifyWalkSuccess(s, []string{"dir1", "dir2", "file3", "..", ".", "..", "..", "file1", "..", "file2"}, "file2"); err != nil {
		t.Errorf("1: %v", err)
	}
}

func TestWalkToNonExistantFile(t *testing.T) {
	root := testStructure1()
	s := &fidState{location: FilePath{root}}

	if err := verifyWalkFailure(s, []string{"fakeFile"}, 0, true, true); err != nil {
		t.Errorf("1: %v", err)
	}
	if err := verifyWalkFailure(s, []string{"fakeDir", "fakeFile"}, 0, true, true); err != nil {
		t.Errorf("2: %v", err)
	}
	if err := verifyWalkFailure(s, []string{"dir1", "fakeDir"}, 1, false, false); err != nil {
		t.Errorf("3: %v", err)
	}
	if err := verifyWalkFailure(s, []string{"dir1", "fakeDir", "fakeFile"}, 1, false, false); err != nil {
		t.Errorf("4: %v", err)
	}
}

func TestWalkNonDirectory(t *testing.T) {
	root := testStructure1()
	s1 := &fidState{location: FilePath{root}}

	if err := verifyWalkFailure(s1, []string{"file1", "fakeFile"}, 1, false, false); err != nil {
		t.Errorf("1: %v", err)
	}

	if err := verifyWalkFailure(s1, []string{"file1", "fakeFile", ".."}, 1, false, false); err != nil {
		t.Errorf("2: %v", err)
	}

	f, _ := root.Walk("", "file1")
	s2 := &fidState{location: FilePath{f}}

	if err := verifyWalkFailure(s2, []string{"fakeFile"}, 0, true, true); err != nil {
		t.Errorf("3: %v", err)
	}
}

func TestWalkOpenFid(t *testing.T) {
	root := testStructure1()
	f, _ := root.Walk("", "dir1")
	h, _ := f.Open("", qp.OREAD)
	s1 := &fidState{location: FilePath{root, f}, handle: h}

	if err := verifyWalkFailure(s1, []string{"."}, 0, true, true); err != nil {
		t.Errorf("1: %v", err)
	}
}

func TestWalkEmptyLocation(t *testing.T) {
	s1 := &fidState{location: FilePath{}}

	if err := verifyWalkFailure(s1, nil, 0, true, true); err != nil {
		t.Errorf("1: %v", err)
	}
	if err := verifyWalkFailure(s1, []string{"."}, 0, true, true); err != nil {
		t.Errorf("2: %v", err)
	}
	if err := verifyWalkFailure(s1, []string{".."}, 0, true, true); err != nil {
		t.Errorf("3: %v", err)
	}
	if err := verifyWalkFailure(s1, []string{"fakeFile"}, 0, true, true); err != nil {
		t.Errorf("4: %v", err)
	}
}

func TestWalkToWalkHook(t *testing.T) {
	root := testStructure1()
	s1 := &fidState{location: FilePath{root}}

	if err := verifyWalkSuccess(s1, []string{"dir3", "file4"}, "file5"); err != nil {
		t.Errorf("1: %v", err)
	}

	if err := verifyWalkFailure(s1, []string{"dir4", "file4"}, 1, false, false); err != nil {
		t.Errorf("2: %v", err)
	}

	dir4, _ := root.Walk("", "dir4")
	s2 := &fidState{location: FilePath{root, dir4}}

	if err := verifyWalkFailure(s2, []string{"file4"}, 0, true, true); err != nil {
		t.Errorf("3: %v", err)
	}
}

func TestWalkToArrivedHook(t *testing.T) {
	root := testStructure1()
	s1 := &fidState{location: FilePath{root}}

	if err := verifyWalkSuccess(s1, []string{"dir5", "file7"}, "file8"); err != nil {
		t.Errorf("1: %v", err)
	}

	if err := verifyWalkFailure(s1, []string{"dir5", "file9"}, 1, false, false); err != nil {
		t.Errorf("2: %v", err)
	}

	dir5, _ := root.Walk("", "dir5")
	s2 := &fidState{location: FilePath{root, dir5}}

	if err := verifyWalkFailure(s2, []string{"file9"}, 0, true, true); err != nil {
		t.Errorf("3: %v", err)
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
	dbg.ReadMessage()

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
	dbg.ReadMessage()

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

func TestWalk(t *testing.T) {
	root := trees.NewSyntheticDir("", 0777, "", "")
	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	fs := New(p2, root, nil)
	fs.Verbosity = TestVerbosity
	go fs.Serve()

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(1, qp.NOFID, 1, fs, dbg, t)
	walkfail([]string{"file1"}, 2, 1, 1, NoSuchFile, fs, dbg, t)
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

// TestWrite tests if a file can be successfully written to.
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

func TestCreate(t *testing.T) {
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
	walk(nil, 2, 1, 1, fs, dbg, t)
	createfail("file1", qp.OREAD, 0777, 1, 1, trees.ErrFileAlreadyExists.Error(), fs, dbg, t)
	create("file2", qp.OREAD, 0777, 2, 1, fs, dbg, t)
	walk(nil, 3, 1, 1, fs, dbg, t)
	create("dir1", qp.OREAD, 0777|qp.DMDIR, 3, 1, fs, dbg, t)
	createfail("file1", qp.OREAD, 0777, 3, 1, FidOpen, fs, dbg, t)
}

func emptyStat() qp.Stat {
	return qp.Stat{
		Type: ^uint16(0),
		Dev:  ^uint32(0),
		Qid: qp.Qid{
			Type:    ^qp.QidType(0),
			Version: ^uint32(0),
			Path:    ^uint64(0),
		},
		Mode:   ^qp.FileMode(0),
		Atime:  ^uint32(0),
		Mtime:  ^uint32(0),
		Length: ^uint64(0),
	}
}

func TestWriteStat(t *testing.T) {
	root := trees.NewSyntheticDir("", 0777, "", "")
	file1 := trees.NewSyntheticFile("file1", 0777, "", "")
	file1.SetContent([]byte("Some content"))
	root.Add("file1", file1)
	file2 := trees.NewSyntheticFile("file2", 0777, "", "")
	file2.SetContent([]byte("Some content"))
	root.Add("file2", file2)

	p1, p2 := newPipePair()
	defer p1.Close()
	defer p2.Close()

	dbg := newDebugThing(p1)
	fs := New(p2, root, nil)
	fs.Verbosity = TestVerbosity
	go fs.Serve()

	version(qp.Version, qp.NOTAG, 4096, fs, dbg, t)
	attach(1, qp.NOFID, 1, fs, dbg, t)

	s1 := emptyStat()
	s1.Name = "file2"
	wstatfail(s1, 1, 1, "it is illegal to rename root", fs, dbg, t)

	walk([]string{"file1"}, 2, 1, 1, fs, dbg, t)

	wstatfail(s1, 2, 1, trees.ErrFileAlreadyExists.Error(), fs, dbg, t)

	s2 := emptyStat()
	s2.Name = "file3"
	wstat(s2, 2, 1, fs, dbg, t)

}
