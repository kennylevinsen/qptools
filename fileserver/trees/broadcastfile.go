package trees

import "sync"

type BroadcastOpenFile struct {
	sync.RWMutex
	b *BroadcastFile

	queue     [][]byte
	queueLock sync.Mutex

	curbuf     []byte
	curbufoff  int64
	curbufLock sync.RWMutex
}

func (of *BroadcastOpenFile) Read(p []byte) (int, error) {
	of.curbufLock.Lock()
	defer of.curbufLock.Unlock()
	if of.curbuf == nil {

	}
}

func (of *BroadcastOpenFile) push(b []byte) {
	of.queueLock.Lock()
	defer of.queueLock.Unlock()
	of.queue = append(of.queue, b)
}

func (of *BroadcastOpenFile) Close() {
	if b != nil {
		b.deregister(of)
	}
	b = nil
}

type BroadcastFile struct {
	sync.RWMutex
	files []*BroadcastOpenFile
}

func (bf *BroadcastFile) Push(b []byte) error {
	bf.RLock()
	defer bf.RUnlock()
	for _, of := range bf.files {
		of.push(b)
	}
}

func (bf *BroadcastFile) deregister(of *BroadcastOpenFile) {
	bf.Lock()
	defer bf.Unlock()
	for i := range bf.files {
		if bf.files[i] == of {
			bf.files = append(bf.files[:i], bf.files[i+1:]...)
			return
		}
	}
}
