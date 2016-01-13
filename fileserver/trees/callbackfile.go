package trees

import (
	"errors"
	"sync"

	"github.com/joushou/qp"
)

// CallbackHandle is the handle for CallbackFile. It calls into CallbackFile
// on seek to 0 and write.
type CallbackHandle struct {
	*DetachedHandle
	cf        *CallbackFile
	cmdbuffer []byte
	cmdlock   sync.Mutex
}

// Seek changes the offset in the handle. Seeking to 0 causes the handle to
// update its content by calling the CallbackFile's updatehook.
func (ch *CallbackHandle) Seek(offset int64, whence int) (int64, error) {
	r, err := ch.DetachedHandle.Seek(offset, whence)
	if ch.Offset == 0 {
		ch.Lock()
		defer ch.Unlock()
		ch.Content = ch.cf.UpdateHook()
	}
	return r, err
}

// Write calls the CallbackFile's writehook.
func (ch *CallbackHandle) Write(p []byte) (int, error) {
	ch.cmdlock.Lock()
	defer ch.cmdlock.Unlock()
	ch.cmdbuffer = ch.cf.WriteHook(append(ch.cmdbuffer, p...))
	return len(p), nil
}

// CallbackFile is a synthetic file that handles it content through an update
// hook and a write hook. The update hook is called when the handle seeks to 0
// for fresh reading. The write hook is called when data is written to the
// handle.
type CallbackFile struct {
	*SyntheticFile

	// UpdateHook is called when the handle prepares for a fresh read by
	// seeking to 0. The returned content becomes the content of the file.
	UpdateHook func() []byte

	// WriteHook is called when the handle writes. The input is the current
	// command buffer, the output being the new command buffer.
	WriteHook func([]byte) []byte
}

// Open returns a new CallbackHandle, assuming the user is permitted to access
// the file.
func (f *CallbackFile) Open(user string, mode qp.OpenMode) (ReadWriteSeekCloser, error) {
	if !f.CanOpen(user, mode) {
		return nil, errors.New("permission denied")
	}
	return &CallbackHandle{
		DetachedHandle: NewDetachedHandle(nil, true, true, true),
		cf:             f,
	}, nil
}

// NewCallbackFile returns an initialized CallbackFile.
func NewCallbackFile(name string, permissions qp.FileMode, user, group string, updatehook func() []byte, writehook func([]byte) []byte) *CallbackFile {
	return &CallbackFile{
		SyntheticFile: NewSyntheticFile(name, permissions, user, group),
		UpdateHook:    updatehook,
		WriteHook:     writehook,
	}
}
