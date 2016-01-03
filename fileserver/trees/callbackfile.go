package trees

import (
	"errors"
	"sync"

	"github.com/joushou/qp"
)

type CallbackHandle struct {
	*DetachedHandle
	cf        *CallbackFile
	cmdbuffer []byte
	cmdlock   sync.Mutex
}

func (ch *CallbackHandle) Seek(offset int64, whence int) (int64, error) {
	r, err := ch.DetachedHandle.Seek(offset, whence)
	if ch.Offset == 0 {
		ch.Lock()
		defer ch.Unlock()
		ch.Content = ch.cf.UpdateHook()
	}
	return r, err
}

func (ch *CallbackHandle) Write(p []byte) (int, error) {
	ch.cmdlock.Lock()
	defer ch.cmdlock.Unlock()
	ch.cmdbuffer = ch.cf.WriteHook(append(ch.cmdbuffer, p...))
	return len(p), nil
}

type CallbackFile struct {
	*SyntheticFile
	UpdateHook func() []byte
	WriteHook  func([]byte) []byte
}

func (f *CallbackFile) Open(user string, mode qp.OpenMode) (ReadWriteSeekCloser, error) {
	if !f.CanOpen(user, mode) {
		return nil, errors.New("permission denied")
	}
	return &CallbackHandle{
		DetachedHandle: NewDetachedHandle(nil, true, true, true),
		cf:             f,
	}, nil
}

func NewCallbackFile(name string, permissions qp.FileMode, user, group string, updatehook func() []byte, writehook func([]byte) []byte) *CallbackFile {
	return &CallbackFile{
		SyntheticFile: NewSyntheticFile(name, permissions, user, group),
		UpdateHook:    updatehook,
		WriteHook:     writehook,
	}
}
