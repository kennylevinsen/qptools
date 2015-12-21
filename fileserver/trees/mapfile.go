package trees

import (
	"errors"
	"sync"

	"github.com/joushou/qp"
)

// MapHandle provides the reading/writing interface to a MapFile.
type MapHandle struct {
	*DetachedHandle
	f         *MapFile
	cmdbuffer []byte
}

// Seek performs the requested seek, but updates the representation of the map
// when seeking to 0.
func (h *MapHandle) Seek(offset int64, whence int) (int64, error) {
	off, err := h.DetachedHandle.Seek(offset, whence)
	if off == 0 {
		// We update the map when you seek to 0
		h.Content = h.f.renderMap()
	}
	return off, err
}

// Write adds the written data to the internal command buffer, processing all
// complete assignments.
func (h *MapHandle) Write(p []byte) (int, error) {
	h.Lock()
	defer h.Unlock()
	h.cmdbuffer = h.f.query(append(h.cmdbuffer, p...))
	return len(p), nil
}

// MapFile provides locked map[string]string storage. Reading the file dumps
// the map, with each entry being written as "key=val", each item separated by
// newlines. Writing "key=val\n" updates "key" to "val". Writing "key=\n"
// deletes key.
type MapFile struct {
	*SyntheticFile
	store     map[string]string
	storelock sync.Mutex
}

// renderMap returns the current file representation of the map.
func (f *MapFile) renderMap() []byte {
	f.storelock.Lock()
	defer f.storelock.Unlock()

	var b []byte
	for i := range f.store {
		b = append(b, []byte(i)...)
		b = append(b, '=')
		b = append(b, []byte(f.store[i])...)
		b = append(b, '\n')
	}
	return b
}

// query takes a command buffer, processes all complete and valid map
// assignments, and returns the remaining buffer.
func (f *MapFile) query(cmd []byte) []byte {
	f.storelock.Lock()
	defer f.storelock.Unlock()

	startIdx := 0
	key := ""
	gotKey := false
	consumed := 0

	for i := range cmd {
		if i < startIdx {
			continue
		}

		if gotKey {
			if cmd[i] == '\n' {
				data := string(cmd[startIdx:i])
				f.store[key] = data
				if len(data) == 0 {
					delete(f.store, key)
				}
				gotKey = false
				startIdx = i + 1
				consumed += len(key) + len(data) + 2
			}
		} else {
			if cmd[i] == '=' {
				key = string(cmd[startIdx:i])
				gotKey = true
				startIdx = i + 1
			}
		}
	}

	return cmd[consumed:]
}

// Open returns a MapHandle.
func (f *MapFile) Open(user string, mode qp.OpenMode) (ReadWriteSeekCloser, error) {
	if !f.CanOpen(user, mode) {
		return nil, errors.New("access denied")
	}

	return &MapHandle{
		DetachedHandle: &DetachedHandle{
			Readable:   true,
			Writable:   true,
			AppendOnly: true,
		},
		f: f,
	}, nil
}

// NewMapFile returns an initialised MapFile.
func NewMapFile(name string, perm qp.FileMode, user, group string) *MapFile {
	return &MapFile{
		SyntheticFile: NewSyntheticFile(name, 0666, user, group),
		store:         make(map[string]string),
	}
}
