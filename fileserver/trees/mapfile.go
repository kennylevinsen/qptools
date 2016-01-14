package trees

import (
	"sync"

	"github.com/joushou/qp"
)

// MapFile provides locked map[string]string storage. Reading the file dumps
// the map, with each entry being written as "key=val", each item separated by
// newlines. Writing "key=val\n" updates "key" to "val". Writing "key=\n"
// deletes key. All characters are considered legal, with the exception that a
// key cannot contain "=", and that a value cannot contain "\n".
type MapFile struct {
	*CallbackFile
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

// NewMapFile returns an initialized MapFile.
func NewMapFile(name string, perm qp.FileMode, user, group string) *MapFile {
	m := &MapFile{
		store: make(map[string]string),
	}
	m.CallbackFile = NewCallbackFile(name, 0666, user, group, m.renderMap, m.query)
	return m
}
