package trees

import (
	"errors"
	"sync"
	"time"

	"github.com/joushou/qp"
)

// ErrTerminatedRead indicates that a read was terminated because the file was
// closed.
var (
	ErrTerminatedRead = errors.New("read terminated")
	ErrMessageTooBig  = errors.New("message bigger than requested read")
)

// BroadcastHandle implements the R/W access to the broadcasting mechanism of
// BroadcastFile.
type BroadcastHandle struct {
	sync.RWMutex
	f *BroadcastFile

	consumer bool

	queue     [][]byte
	queueCond *sync.Cond

	unwanted bool
}

// ReadAt retrieves a message. If no new messages are available, Read blocks to
// wait for one. It is woken up, returning ErrTerminatedRead if Close is called
// on the handle. If the message was bigger than the requested read size,
// ErrMessageTooBig is returned.
func (h *BroadcastHandle) ReadAt(p []byte, offset int64) (int, error) {
	buf, err := h.fetch()
	if err != nil {
		return 0, err
	}

	if len(p) < len(buf) {
		return 0, ErrMessageTooBig
	}

	copy(p, buf)

	return len(p), nil
}

// WriteAt adds a message to the BroadcastFile.
func (h *BroadcastHandle) WriteAt(p []byte, offset int64) (int, error) {
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
	h.queueCond.L.Lock()
	defer h.queueCond.L.Unlock()

	h.queue = nil
	h.unwanted = true
	h.queueCond.Broadcast()

	if h.consumer {
		// Only readable files are registered
		h.f.deregister(h)
	}
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
func (f *BroadcastFile) Open(user string, mode qp.OpenMode) (ReadWriteAtCloser, error) {
	if !f.CanOpen(user, mode) {
		return nil, ErrPermissionDenied
	}

	f.Lock()
	defer f.Unlock()
	f.Atime = time.Now()

	readable := mode&3 == qp.OREAD || mode&3 == qp.OEXEC || mode&3 == qp.ORDWR

	x := &BroadcastHandle{
		f:         f,
		consumer:  readable,
		queueCond: sync.NewCond(&sync.Mutex{}),
	}

	if readable {
		// We only register readable files for the queue
		f.files = append(f.files, x)
	}

	return x, nil
}

// Push pushes a new broadcast message for all current listeners.
func (f *BroadcastFile) Push(b []byte) error {
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
