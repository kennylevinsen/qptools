package trees

import (
	"errors"
	"sync"
	"time"

	"github.com/joushou/qp"
)

// ErrTerminatedRead indicates that a read was terminated because the file was
// closed.
var ErrTerminatedRead = errors.New("read terminated")

// BroadcastHandle implements the R/W access to the broadcasting mechanism of
// BroadcastFile.
type BroadcastHandle struct {
	sync.RWMutex
	f *BroadcastFile

	Readable bool
	Writable bool

	queue     [][]byte
	queueCond *sync.Cond

	curbuf     []byte
	curbufLock sync.RWMutex

	unwanted bool
}

// Seek is a noop on a BroadcastHandle.
func (h *BroadcastHandle) Seek(int64, int) (int64, error) {
	return 0, nil
}

// Read reads the current message, or if the end is reached, retrieves a new
// message. If no new messages are available, Read blocks to wait for one. It
// is woken up, returning ErrTerminatedRead if Close is called on the handle.
func (h *BroadcastHandle) Read(p []byte) (int, error) {
	h.RLock()
	if !h.Readable || h.f == nil {
		h.RUnlock()
		return 0, errors.New("file not open for reading")
	}
	h.RUnlock()

	h.curbufLock.Lock()
	defer h.curbufLock.Unlock()

	if h.curbuf == nil {
		// If we don't have a buffer, wait for one
		var err error
		h.curbuf, err = h.fetch()
		if err != nil {
			return 0, err
		}
	} else if len(h.curbuf) == 0 {
		// If our buffer is empty, clear it - next read will fetch us a new one.
		h.curbuf = nil
		return 0, nil
	}

	m := len(h.curbuf)
	if len(p) < m {
		m = len(p)
	}

	copy(p, h.curbuf[:m])
	h.curbuf = h.curbuf[m:]

	return m, nil
}

// Write adds a message to the BroadcastFile.
func (h *BroadcastHandle) Write(p []byte) (int, error) {
	h.RLock()
	defer h.RUnlock()
	if !h.Writable || h.f == nil {
		return 0, errors.New("file not open for writing")
	}
	h.f.Push(p)
	return len(p), nil
}

// fetch waits for a new message.
func (h *BroadcastHandle) fetch() ([]byte, error) {
	// We always read from the channel to avoid complex locking schemes.
	h.queueCond.L.Lock()
	defer h.queueCond.L.Unlock()

	for len(h.queue) == 0 && h.unwanted == false {
		h.queueCond.Wait()
	}

	if h.unwanted {
		return nil, ErrTerminatedRead
	}

	b := h.queue[0]
	h.queue = h.queue[1:]
	return b, nil
}

// push pushes a message to the local queue, waking up fetch as necessary.
func (h *BroadcastHandle) push(b []byte) {
	h.queueCond.L.Lock()
	defer h.queueCond.L.Unlock()
	h.queue = append(h.queue, b)
	h.queueCond.Signal()
}

// Close closes the handle, waking up any blocked reads.
func (h *BroadcastHandle) Close() error {
	h.Lock()
	defer h.Unlock()
	h.queue = nil
	h.unwanted = true
	h.queueCond.Broadcast()

	if h.Readable && h.f != nil {
		// Only readable files are registered
		h.f.deregister(h)
	}
	h.f = nil
	return nil
}

// BroadcastFile provides broadcast functionality. Writing to the file allows
// everyone who had the file open for reading during the write to read and
// receive the message.
type BroadcastFile struct {
	sync.RWMutex
	files []*BroadcastHandle

	*SyntheticFile
}

// Open returns a new BroadcastHandle. If it is readable, it is also
// registered on the list of open files to receive broadcast messages. Do note
// that BroadcastFile keeps a reference to all listening handles. A handle can
// therefore only be garbage collected if Close has been called on it.
func (f *BroadcastFile) Open(user string, mode qp.OpenMode) (ReadWriteSeekCloser, error) {
	if !f.CanOpen(user, mode) {
		return nil, errors.New("access denied")
	}

	f.Lock()
	defer f.Unlock()
	f.Atime = time.Now()

	readable := mode&3 == qp.OREAD || mode&3 == qp.OEXEC || mode&3 == qp.ORDWR
	writable := mode&3 == qp.OWRITE || mode&3 == qp.ORDWR

	x := &BroadcastHandle{
		f:         f,
		queueCond: sync.NewCond(&sync.Mutex{}),
		Readable:  readable,
		Writable:  writable,
	}

	if readable {
		// We only register readable files for the queue
		f.files = append(f.files, x)
	}

	return x, nil
}

// Push pushes a new broadcast message for all current listeners.
func (f *BroadcastFile) Push(b []byte) error {
	f.Lock()
	f.Version++
	f.Unlock()
	f.RLock()
	defer f.RUnlock()
	for _, h := range f.files {
		h.push(b)
	}
	return nil
}

// deregister removes a handle from the listener list.
func (f *BroadcastFile) deregister(h *BroadcastHandle) {
	f.Lock()
	defer f.Unlock()
	for i := range f.files {
		if f.files[i] == h {
			f.files = append(f.files[:i], f.files[i+1:]...)
			return
		}
	}
}

// NewBroadcastFile returns a new BroadcastFile.
func NewBroadcastFile(name string, permissions qp.FileMode, user, group string) *BroadcastFile {
	return &BroadcastFile{
		SyntheticFile: NewSyntheticFile(name, permissions, user, group),
	}
}
