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
	FidInUse           = "fid already in use"
	TagInUse           = "tag already in use"
	AuthNotSupported   = "authentication not supported"
	NoSuchService      = "no such service"
	ResponseTooLong    = "response too long"
	UnknownFid         = "unknown fid"
	FidOpen            = "fid is open"
	FidNotDirectory    = "fid is not a directory"
	NoSuchFile         = "file does not exist"
	InvalidFileName    = "invalid file name"
	NotOpenForRead     = "file not opened for reading"
	NotOpenForWrite    = "file not opened for writing"
	UnsupportedMessage = "message not supported"
)

const (
	// MaxSize is the maximum negotiable message size.
	MaxSize = 10 * 1024 * 1024

	// MinSize is the minimum size that will be accepted.
	MinSize = 128
)

// Verbosity is the verbosity level of the server.
type Verbosity int

// Verbosity levels
const (
	Quiet Verbosity = iota
	Chatty
	Loud
	Obnoxious
	Debug
)

// fidState is the internal state associated with a fid.
type fidState struct {
	// It is important that the lock is only hold during the immediate
	// manipulation of the state. That also includes the read lock. Holding it for
	// the full duration of a potentially blocking call such as read/write will
	// lead to unwanted queueing of requests, including Flush and Clunk.
	sync.RWMutex

	location FilePath

	open     trees.ReadWriteSeekCloser
	mode     qp.OpenMode
	username string
}

// FileServer serves an io.ReadWriter with the provided qp.Protocol,
// navigating the provided trees.Dir.
//
// While intended to operate by the specs, FileServer breaks spec, sometimes
// for good, sometimes for bad in the following scenarios:
//
// 1. FileServer does not enforce MAXWELEM (fcall(3)). A client providing more
// than 16 names in a walk has already broken protocol, and there is no reason
// for FileServer to enforce a protocol limit against a client known to not
// comply with said limit.
//
// 2. FileServer responds to a request on a currently occupied tag with an
// Rerror, but handles it internally as an implicit flush of the old request,
// as the client would most likely not be able to map the response on this tag
// to its request. There is, however, a small race in the current
// implementation for this behaviour: When a tag collision is detected, an
// error is created to send a response. If, however, the original request is
// pending when the error is returned, but sends the response before the tag
// collision error is sent, the tag collision error will be flushed instead.
// Additional locking would seemingly be required to solve this issue, adding
// unnecessary complexity in order to provide a better definition of a broken
// request.
//
// 3. FileServer clunks all fids when the io.ReadWriter returns an error to
// ensure that state is cleaned up properly.
//
// 4. FileServer does not care if the tag of a Version request is NOTAG.
//
// 5. FileServer permits explicit walks to ".".
//
// 6. FileServer does not enforce maxsize (hopefully a temporary limitation).
type FileServer struct {
	RW          io.ReadWriter
	Verbosity   Verbosity
	DefaultRoot trees.Dir
	Roots       map[string]trees.Dir

	// internal
	writeLock sync.Mutex
	p         qp.Protocol
	dead      error

	// It is important that the locks below are only held during the immediate
	// manipulation of the maps they are associated with. That also includes
	// the read locks. Holding it for the full duration of a potentially
	// blocking call such as read/write will lead to unwanted queueing of
	// requests, including Flush and Clunk.

	// session data
	maxsize uint32
	fids    map[qp.Fid]*fidState
	fidLock sync.RWMutex
	tags    map[qp.Tag]bool
	tagLock sync.Mutex
}

// cleanup handles post-execution cleanup.
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

// logreq prints the request, formatted after the verbosity level.
func (fs *FileServer) logreq(t qp.Tag, m qp.Message) {
	switch fs.Verbosity {
	case Chatty, Loud:
		log.Printf("-> [%04X]%T", t, m)
	case Obnoxious, Debug:
		log.Printf("-> [%04X]%T    \t%+v", t, m, m)
	}
}

// logresp prints the response, formatted after the verbosity level.
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

// respond sends a response if the tag is still queued. The tag is removed
// immediately after checking its existence. It marks the fileserver as broken
// on error by setting fs.dead to the error, which must break the server loop.
func (fs *FileServer) respond(t qp.Tag, m qp.Message) {
	// We're holding writeLock during the full duration of respond in order to
	// ensure that flush handling cannot end up sending an Rflush for the tag.
	// If writeLock was only held during write, and tagLock was only held
	// during tag map access, an Rflush could end up being sent in between
	// those two locks, breaking flush protocol, which guarantees that a tag is
	// immediately reusable after an Rflush for it has been received.
	fs.writeLock.Lock()
	defer fs.writeLock.Unlock()

	fs.tagLock.Lock()
	_, tagPresent := fs.tags[t]

	if t != qp.NOTAG && !tagPresent {
		fs.tagLock.Unlock()
		return
	}

	delete(fs.tags, t)
	fs.tagLock.Unlock()

	fs.logresp(t, m)

	err := fs.p.Encode(fs.RW, m)
	if err != nil {
		fs.dead = err
	}
}

// addTag registers a tag as a pending request. Removing the tag prior to its
// response being processed results in the response not being sent.
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

// flushTag removes the tag.
func (fs *FileServer) flushTag(t qp.Tag) {
	fs.tagLock.Lock()
	defer fs.tagLock.Unlock()
	if _, exists := fs.tags[t]; exists {
		delete(fs.tags, t)
	}
}

// walkTo handles the walking logic. It is isolated to make implementation of
// .e with its additional walk-performing requests simpler.
func (fs *FileServer) walkTo(state *fidState, names []string) (*fidState, []qp.Qid, error) {
	// We could theoretically release the state lock after copying the content,
	// which might be saner due to Open or Walk on the files potentially
	// blocking, but we are currently holding the lock during the full walk for
	// simplicity.
	state.Lock()
	defer state.Unlock()

	if state.open != nil {
		// Can't walk on an open fid.
		return nil, nil, errors.New(FidOpen)
	}

	if len(names) == 0 {
		// A 0-length walk is equivalent to walking to ".", which effectively
		// just clones the fid.
		x := &fidState{
			username: state.username,
			location: state.location,
		}
		return x, nil, nil
	}

	cur := state.location.Current()
	isdir, err := cur.IsDir()
	if err != nil {
		// Yes, checking if the file is a directory can fail if the file is
		// backed to disk.
		return nil, nil, err
	}

	if !isdir {
		// NOTE(kl): Isn't a walk to "." legal on a file? Pretty sure this check
		// should just be killed.
		return nil, nil, errors.New(FidNotDirectory)
	}

	root := cur
	newloc := state.location.Clone()
	first := true
	var qids []qp.Qid
	for i := range names {
		// We open the file to check permissions, as walking would otherwise not
		// necessarily require OEXEC permissions. This is a bit ugly and an
		// unnecessarily high amount of work.
		x, err := root.Open(state.username, qp.OEXEC)
		if err != nil {
			goto done
		}
		x.Close()

		addToLoc := true
		name := names[i]
		switch name {
		case ".":
			// This always succeeds, but we don't want to add it to our location
			// list.
			addToLoc = false
		case "..":
			// This also always succeeds, and is either does nothing or shortens
			// our location list. We don't want anything added to the list
			// regardless.
			addToLoc = false
			root = newloc.Parent()
			if len(newloc) > 1 {
				newloc = newloc[:len(newloc)-1]
			}
		default:
			// A regular file name. In this case, walking to the name is only
			// legal if the current file is a directory.
			isdir, err = root.IsDir()
			if err != nil {
				return nil, nil, err
			}

			if !isdir {
				// Root isn't a dir, so we can't walk.
				if first {
					return nil, nil, errors.New(FidNotDirectory)
				}
				goto done
			}

			d := root.(trees.Dir)
			root, err = d.Walk(state.username, name)
			if err != nil {
				if first {
					return nil, nil, err
				}
				// The walk failed for some arbitrary reason.
				goto done
			}

			if root == nil {
				// The file did not exist
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

	versionstr := r.Version
	maxsize := r.MaxSize
	if maxsize > MaxSize {
		maxsize = MaxSize
	} else if maxsize < 128 {
		// This makes no sense. Try to force the client up a bit.
		// NOTE(kl): Should we rather just return an unknown version response?
		maxsize = 128
	}

	fs.maxsize = maxsize

	// We change the protocol codec here if necessary. This only works because
	// the server loop is currently blocked. Had it continued ahead, blocking
	// in its Decode call again, we would only be able to change the protocol
	// for the next-next request. This would be an issue for .u, which change
	// the Tattach message, as well as for .e, which might follow up with a
	// Tsession immediately after our Rversion.
	switch versionstr {
	case qp.Version:
		fs.p = qp.NineP2000
	default:
		fs.p = qp.NineP2000
		versionstr = qp.UnknownVersion
	}

	// Tversion resets everything
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

// auth currently just sends an error to inform the client that auth is not
// required. In the near future, it should allow external implementation of an
// auth algo, exposed through the file abstraction.
func (fs *FileServer) auth(r *qp.AuthRequest) {
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}
	fs.logreq(r.Tag, r)
	fs.sendError(r.Tag, AuthNotSupported)
}

func (fs *FileServer) attach(r *qp.AttachRequest) {
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}

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
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}

	fs.logreq(r.Tag, r)
	fs.flushTag(r.OldTag)
	fs.respond(r.Tag, &qp.FlushResponse{
		Tag: r.Tag,
	})
}

func (fs *FileServer) walk(r *qp.WalkRequest) {
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}

	fs.logreq(r.Tag, r)

	fs.fidLock.Lock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.Unlock()

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
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}

	fs.logreq(r.Tag, r)

	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

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
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}

	fs.logreq(r.Tag, r)

	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

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
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}

	fs.logreq(r.Tag, r)

	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.RLock()
	open := state.open
	mode := state.mode
	state.RUnlock()

	if open == nil || ((mode&3 != qp.OREAD) && (mode&3 != qp.ORDWR)) {
		fs.sendError(r.Tag, NotOpenForRead)
		return
	}

	// We try to cap things to the negotiated maxsize
	count := int(fs.maxsize) - (4 + qp.HeaderSize)
	if count > int(r.Count) {
		count = int(r.Count)
	}

	b := make([]byte, count)

	_, err := open.Seek(int64(r.Offset), 0)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	n, err := open.Read(b)
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
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}

	fs.logreq(r.Tag, r)

	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.RLock()
	open := state.open
	mode := state.mode
	state.RUnlock()

	if open == nil || ((mode&3 != qp.OWRITE) && (mode&3 != qp.ORDWR)) {
		fs.sendError(r.Tag, NotOpenForWrite)
		return
	}

	_, err := open.Seek(int64(r.Offset), 0)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	n, err := open.Write(r.Data)
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
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}

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
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}

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
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}

	fs.logreq(r.Tag, r)

	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.RLock()
	l := state.location.Current()
	state.RUnlock()

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
	if err := fs.addTag(r.Tag); err != nil {
		fs.sendError(r.Tag, TagInUse)
		return
	}

	fs.logreq(r.Tag, r)

	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

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

func (fs *FileServer) unsupported(r qp.Message) {
	t := r.GetTag()
	if err := fs.addTag(t); err != nil {
		fs.sendError(t, TagInUse)
		return
	}
	fs.sendError(t, UnsupportedMessage)
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
			fs.auth(mx)
		case *qp.AttachRequest:
			fs.attach(mx)
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
		default:
			fs.unsupported(m)
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
