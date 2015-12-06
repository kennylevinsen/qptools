package fileserver

import (
	"errors"
	"io"
	"log"
	"sync"
	"time"

	"github.com/joushou/qp"
	"github.com/joushou/qptools/fileserver/trees"
)

// These are the error strings used by the fileserver itself. Do note that the
// fileserver will blindly return errors from the directory tree to the 9P
// client.
const (
	FidInUse         = "fid already in use"
	TagInUse         = "tag already in use"
	AuthNotSupported = "authentication not supported"
	NoSuchService    = "no such service"
	ResponseTooLong  = "response too long"
	UnknownFid       = "unknown fid"
	FidOpen          = "fid is open"
	FidNotDirectory  = "fid is not a directory"
	NoSuchFile       = "file does not exist"
	InvalidFileName  = "invalid file name"
	NotOpenForRead   = "file not opened for reading"
	NotOpenForWrite  = "file not opened for writing"
)

// Verbosity is the verbosity level
type Verbosity int

const (
	Quiet Verbosity = iota
	Chatty
	Loud
	Obnoxious
	Debug
)

type fidState struct {
	sync.RWMutex
	location FilePath

	open     trees.OpenFile
	mode     qp.OpenMode
	username string
}

// FileServer serves a ReadWriter with a given handler.
type FileServer struct {
	RW          io.ReadWriter
	Verbosity   Verbosity
	DefaultRoot trees.Dir
	Roots       map[string]trees.Dir

	// internal
	writeLock sync.Mutex
	p         qp.Protocol
	dead      error

	// session data
	maxsize uint32
	fids    map[qp.Fid]*fidState
	fidLock sync.RWMutex
	tags    map[qp.Tag]bool
	tagLock sync.Mutex
}

func (fs *FileServer) cleanup() {
	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	for _, s := range fs.fids {
		if s.open != nil {
			s.open.Close()
			s.open = nil
		}
	}
}

func (fs *FileServer) logreq(t qp.Tag, m qp.Message) {
	switch fs.Verbosity {
	case Chatty, Loud:
		log.Printf("-> [%04X]%T", t, m)
	case Obnoxious, Debug:
		log.Printf("-> [%04X]%T    \t%+v", t, m, m)
	}
}

func (fs *FileServer) logresp(t qp.Tag, m qp.Message) {
	switch fs.Verbosity {
	case Loud:
		log.Printf("<- [%04X]%T", t, m)
	case Obnoxious, Debug:
		log.Printf("<- [%04X]%T    \t%+v", t, m, m)
	}
}

func (fs *FileServer) sendError(t qp.Tag, str string) {
	e := &qp.ErrorResponse{
		Tag:   t,
		Error: str,
	}
	fs.respond(t, e)
}

func (fs *FileServer) respond(t qp.Tag, m qp.Message) {
	fs.tagLock.Lock()
	defer fs.tagLock.Unlock()
	_, tagPresent := fs.tags[t]

	if t != qp.NOTAG && !tagPresent {
		return
	}

	delete(fs.tags, t)

	/* maxsize conformance temporarily disabled.
	l := uint32(m.EncodedLength())
	if l > fs.maxsize {
		if mx, ok := m.(*qp.ErrorResponse); ok {
			diff := l - fs.maxsize
			mx.Error = mx.Error[:uint32(len(mx.Error))-diff]
		} else {
			m = &qp.ErrorResponse{
				Tag:   t,
				Error: ResponseTooLong,
			}
		}
	}*/

	fs.logresp(t, m)

	fs.writeLock.Lock()
	defer fs.writeLock.Unlock()
	err := fs.p.Encode(fs.RW, m)
	if err != nil {
		fs.dead = err
	}
}

func (fs *FileServer) addTag(t qp.Tag) error {
	if t == qp.NOTAG {
		return nil
	}

	fs.tagLock.Lock()
	defer fs.tagLock.Unlock()
	_, exists := fs.tags[t]
	if exists {
		return errors.New(TagInUse)
	}
	fs.tags[t] = true
	return nil
}

func (fs *FileServer) walkTo(state *fidState, names []string) (*fidState, []qp.Qid, error) {
	state.Lock()
	defer state.Unlock()

	if state.open != nil {
		return nil, nil, errors.New(FidOpen)
	}

	if len(names) == 0 {
		x := &fidState{
			username: state.username,
			location: state.location,
		}
		return x, nil, nil
	}

	cur := state.location.Current()
	isdir, err := cur.IsDir()
	if err != nil {
		return nil, nil, err
	}
	if !isdir {
		return nil, nil, errors.New(FidNotDirectory)
	}

	root := cur
	newloc := state.location
	first := true
	var qids []qp.Qid
	for i := range names {
		x, err := root.Open(state.username, qp.OEXEC)
		if err != nil {
			goto done
		}
		x.Close()

		addToLoc := true
		name := names[i]
		switch name {
		case ".":
			addToLoc = false
		case "..":
			root = newloc.Parent()
			if len(newloc) > 1 {
				newloc = newloc[:len(newloc)-1]
				addToLoc = false
			}
		default:
			isdir, err = root.IsDir()
			if err != nil {
				return nil, nil, err
			}
			if !isdir {
				goto done
			}

			d := root.(trees.Dir)
			root, err = d.Walk(state.username, name)
			if err != nil {
				goto done
			}

			if root == nil {
				if first {
					return nil, nil, errors.New(NoSuchFile)
				}
				goto done
			}
		}

		if addToLoc {
			newloc = append(newloc, root)
		}

		q, err := root.Qid()
		if err != nil {
			return nil, nil, err
		}

		qids = append(qids, q)

		first = false
	}

done:
	if len(qids) < len(names) {
		return nil, qids, nil
	}

	s := &fidState{
		username: state.username,
		location: newloc,
	}

	return s, qids, nil
}

func (fs *FileServer) version(r *qp.VersionRequest) {
	fs.logreq(r.Tag, r)
	maxsize := r.MaxSize
	if maxsize > 1024*1024*10 {
		maxsize = 1024 * 1024 * 10
	} else if maxsize < 128 {
		// This makes no sense. Try to force the client up a bit.
		maxsize = 128
	}

	fs.maxsize = maxsize

	versionstr := r.Version
	switch versionstr {
	// case qp.VersionDote:
	// 	fs.p = qp.NineP2000Dote
	case qp.Version:
		fs.p = qp.NineP2000
	default:
		fs.p = qp.NineP2000
		versionstr = qp.UnknownVersion
	}

	// VersionRequest resets everything
	fs.cleanup()
	fs.fids = make(map[qp.Fid]*fidState)
	fs.tags = make(map[qp.Tag]bool)
	fs.addTag(r.Tag)

	fs.respond(r.Tag, &qp.VersionResponse{
		Tag:     r.Tag,
		MaxSize: maxsize,
		Version: versionstr,
	})
}

func (fs *FileServer) auth(r *qp.AuthRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.sendError(r.Tag, AuthNotSupported)
}

func (fs *FileServer) attach(r *qp.AttachRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	if _, exists := fs.fids[r.Fid]; exists {
		fs.sendError(r.Tag, FidInUse)
		return
	}

	var root trees.Dir
	if x, exists := fs.Roots[r.Service]; exists {
		root = x
	} else {
		root = fs.DefaultRoot
	}

	if root == nil {
		fs.sendError(r.Tag, NoSuchService)
		return
	}

	s := &fidState{
		username: r.Username,
		location: FilePath{root},
	}

	fs.fids[r.Fid] = s

	qid, err := root.Qid()
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	fs.respond(r.Tag, &qp.AttachResponse{
		Tag: r.Tag,
		Qid: qid,
	})
}

func (fs *FileServer) flush(r *qp.FlushRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.tagLock.Lock()
	if _, exists := fs.tags[r.OldTag]; exists {
		delete(fs.tags, r.OldTag)
	}
	fs.tagLock.Unlock()
	fs.respond(r.Tag, &qp.FlushResponse{
		Tag: r.Tag,
	})
}

func (fs *FileServer) walk(r *qp.WalkRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()
	state, exists := fs.fids[r.Fid]
	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	if _, exists = fs.fids[r.NewFid]; exists {
		fs.sendError(r.Tag, FidInUse)
		return
	}

	newfidState, qids, err := fs.walkTo(state, r.Names)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	if newfidState != nil {
		fs.fids[r.NewFid] = newfidState
	}

	fs.respond(r.Tag, &qp.WalkResponse{
		Tag:  r.Tag,
		Qids: qids,
	})
}

func (fs *FileServer) open(r *qp.OpenRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()

	state, exists := fs.fids[r.Fid]
	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.Lock()
	defer state.Unlock()

	if state.open != nil {
		fs.sendError(r.Tag, FidOpen)
		return
	}

	l := state.location.Current()
	qid, err := l.Qid()
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	openfile, err := l.Open(state.username, r.Mode)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	state.open = openfile
	state.mode = r.Mode
	fs.respond(r.Tag, &qp.OpenResponse{
		Tag: r.Tag,
		Qid: qid,
	})
}

func (fs *FileServer) create(r *qp.CreateRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()

	state, exists := fs.fids[r.Fid]
	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.Lock()
	defer state.Unlock()

	if state.open != nil {
		fs.sendError(r.Tag, FidOpen)
		return
	}

	if r.Name == "." || r.Name == ".." {
		fs.sendError(r.Tag, InvalidFileName)
		return
	}

	cur := state.location.Current()
	isdir, err := cur.IsDir()
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}
	if !isdir {
		fs.sendError(r.Tag, FidNotDirectory)
		return
	}

	dir := cur.(trees.Dir)

	l, err := dir.Create(state.username, r.Name, r.Permissions)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	qid, err := l.Qid()
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	x, err := l.Open(state.username, r.Mode)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	state.location = append(state.location, l)
	state.open = x
	state.mode = r.Mode

	fs.respond(r.Tag, &qp.CreateResponse{
		Tag:    r.Tag,
		Qid:    qid,
		IOUnit: 0,
	})
}

func (fs *FileServer) read(r *qp.ReadRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()

	state, exists := fs.fids[r.Fid]
	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.RLock()
	defer state.RUnlock()

	if state.open == nil || ((state.mode&3 != qp.OREAD) && (state.mode&3 != qp.ORDWR)) {
		fs.sendError(r.Tag, NotOpenForRead)
		return
	}

	// We try to cap things to the negotiated maxsize
	count := int(fs.maxsize) - (4 + int(r.Count) + qp.HeaderSize)
	if count > int(r.Count) {
		count = int(r.Count)
	}

	b := make([]byte, count)

	_, err := state.open.Seek(int64(r.Offset), 0)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	n, err := state.open.Read(b)
	if err == io.EOF {
		n = 0
	} else if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	b = b[:n]

	fs.respond(r.Tag, &qp.ReadResponse{
		Tag:  r.Tag,
		Data: b,
	})
}

func (fs *FileServer) write(r *qp.WriteRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()

	state, exists := fs.fids[r.Fid]
	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.RLock()
	defer state.RUnlock()

	if state.open == nil || ((state.mode&3 != qp.OWRITE) && (state.mode&3 != qp.ORDWR)) {
		fs.sendError(r.Tag, NotOpenForWrite)
		return
	}

	_, err := state.open.Seek(int64(r.Offset), 0)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	n, err := state.open.Write(r.Data)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	fs.respond(r.Tag, &qp.WriteResponse{
		Tag:   r.Tag,
		Count: uint32(n),
	})
}

func (fs *FileServer) clunk(r *qp.ClunkRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	state, exists := fs.fids[r.Fid]
	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	delete(fs.fids, r.Fid)

	state.Lock()
	defer state.Unlock()

	if state.open != nil {
		state.open.Close()
		state.open = nil
	}

	fs.respond(r.Tag, &qp.ClunkResponse{
		Tag: r.Tag,
	})
}

func (fs *FileServer) remove(r *qp.RemoveRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	state, exists := fs.fids[r.Fid]
	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	delete(fs.fids, r.Fid)

	state.Lock()
	defer state.Unlock()

	if state.open != nil {
		state.open.Close()
		state.open = nil
	}

	if len(state.location) <= 1 {
		fs.respond(r.Tag, &qp.RemoveResponse{
			Tag: r.Tag,
		})
		return
	}

	cur := state.location.Current()
	p := state.location.Parent()
	n, err := cur.Name()
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	p.(trees.Dir).Remove(state.username, n)

	fs.respond(r.Tag, &qp.RemoveResponse{
		Tag: r.Tag,
	})
}

func (fs *FileServer) stat(r *qp.StatRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()

	state, exists := fs.fids[r.Fid]
	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.RLock()
	defer state.RUnlock()

	l := state.location.Current()
	if l == nil {
		fs.sendError(r.Tag, NoSuchFile)
		return
	}

	st, err := l.Stat()
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	fs.respond(r.Tag, &qp.StatResponse{
		Tag:  r.Tag,
		Stat: st,
	})
}

func (fs *FileServer) writeStat(r *qp.WriteStatRequest) {
	fs.addTag(r.Tag)
	fs.logreq(r.Tag, r)
	fs.fidLock.RLock()
	defer fs.fidLock.RUnlock()

	state, exists := fs.fids[r.Fid]
	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.Lock()
	defer state.Unlock()

	l := state.location.Current()
	if l == nil {
		fs.sendError(r.Tag, NoSuchFile)
		return
	}

	var p trees.Dir
	if len(state.location) > 1 {
		p = state.location.Parent().(trees.Dir)
	}

	if err := setStat(state.username, l, p, r.Stat); err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	fs.respond(r.Tag, &qp.WriteStatResponse{
		Tag: r.Tag,
	})
}

// Start starts the server loop. The loop returns on I/O error.
func (fs *FileServer) Start() error {
	for fs.dead == nil {
		m, err := fs.p.Decode(fs.RW)
		if err != nil {
			fs.dead = err
			break
		}

		switch mx := m.(type) {
		// Basic messages
		case *qp.VersionRequest:
			fs.version(mx)
		case *qp.AuthRequest:
			go fs.auth(mx)
		case *qp.AttachRequest:
			go fs.attach(mx)
		case *qp.FlushRequest:
			fs.flush(mx)
		case *qp.WalkRequest:
			go fs.walk(mx)
		case *qp.OpenRequest:
			go fs.open(mx)
		case *qp.CreateRequest:
			go fs.create(mx)
		case *qp.ReadRequest:
			go fs.read(mx)
		case *qp.WriteRequest:
			go fs.write(mx)
		case *qp.ClunkRequest:
			go fs.clunk(mx)
		case *qp.RemoveRequest:
			go fs.remove(mx)
		case *qp.StatRequest:
			go fs.stat(mx)
		case *qp.WriteStatRequest:
			go fs.writeStat(mx)
		case *qp.SessionRequestDote:
			/* 9P2000.e */
		case *qp.SimpleReadRequestDote:
			/* 9P2000.e */
		case *qp.SimpleWriteRequestDote:
			/* 9P2000.e */
		case *qp.AuthRequestDotu:
			/* 9P2000.u */
		case *qp.AttachRequestDotu:
			/* 9P2000.u */
		case *qp.CreateRequestDotu:
			/* 9P2000.u */
		case *qp.WriteStatRequestDotu:
			/* 9P2000.u */
		}
	}

	fs.cleanup()
	return fs.dead
}

// New constructs a new FileServer. roots is the map where the fileserver
// should look for directory roots based on service name. defaultRoot is the
// root that will be used if the service wasn't in the map. If the service is
// not in the map, and there is no default set, attach will fail.
func New(rw io.ReadWriter, defaultRoot trees.Dir, roots map[string]trees.Dir, v Verbosity) *FileServer {
	fs := &FileServer{
		RW:          rw,
		DefaultRoot: defaultRoot,
		Roots:       roots,
		Verbosity:   v,
		p:           qp.NineP2000,
		tags:        make(map[qp.Tag]bool),
	}

	if v == Debug {
		go func() {
			t := time.Tick(10 * time.Second)
			for range t {
				if fs.dead != nil {
					return
				}

				log.Printf("Open fids: %d", len(fs.fids))
			}
		}()
	}

	return fs
}
