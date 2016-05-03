package fileserver

import (
	"errors"
	"io"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/joushou/qp"
	"github.com/joushou/qptools/fileserver/trees"
)

// These are the error strings used by the fileserver itself. Do note that the
// fileserver will blindly return errors from the directory tree to the 9P
// client.
const (
	FidInUse               = "fid already in use"
	TagInUse               = "tag already in use"
	AuthNotSupported       = "authentication not supported"
	AuthRequired           = "authentication required"
	NoSuchService          = "no such service"
	ResponseTooLong        = "response too long"
	InvalidFid             = "invalid fid for operation"
	UnknownFid             = "unknown fid"
	FidOpen                = "fid is open"
	FidNotOpen             = "fid is not open"
	FidNotDirectory        = "fid is not a directory"
	NoSuchFile             = "file does not exist"
	InvalidFileName        = "invalid file name"
	NotOpenForRead         = "file not opened for reading"
	NotOpenForWrite        = "file not opened for writing"
	UnsupportedMessage     = "message not supported"
	InvalidOpOnFid         = "invalid operation on file"
	AfidNotAuthFile        = "afid is not a valid auth file"
	PermissionDenied       = "permission denied"
	ResponseTooBig         = "response too big"
	MessageSizeTooSmall    = "version: message size too small"
	IncorrectTagForVersion = "version: tag must be NOTAG"
	OpenWriteOnDir         = "open: cannot open dir for write"
)

var (
	// ErrCouldNotSendErr indicates that we were unable to send an error
	// response.
	ErrCouldNotSendErr = errors.New("could not send error")

	// ErrEncMessageSizeMismatch indicates that the message encoder and the
	// fileserver disagrees on the max msgsize. That is, the encoder thought
	// the message violated the max msgsize, but the fileserver thoguht it
	// didn't.
	ErrEncMessageSizeMismatch = errors.New("encoder and fileserver disagrees on messagesize")

	// ErrHandlerPanic indicates that a handler panicked.
	ErrHandlerPanic = errors.New("handler panicked")
)

const (
	// MessageSize is the maximum negotiable message size.
	MessageSize = 10 * 1024 * 1024

	// MinSize is the minimum size that will be accepted.
	MinSize = 256
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
	// It is important that the lock is only held during the immediate
	// manipulation of the state. That also includes the read lock. Holding it
	// for the full duration of a potentially blocking call such as read/write
	// will lead to unwanted queuing of requests, including Flush and Clunk.
	sync.RWMutex

	location FilePath

	handle   trees.ReadWriteAtCloser
	mode     qp.OpenMode
	username string
}

// FileServer serves an io.ReadWriter, navigating the provided file tree.
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
// 3. FileServer permits explicit walks to ".".
type FileServer struct {
	// Verbosity is the verbosity level.
	Verbosity Verbosity

	// DefaultRoot is the root to use if the service isn't in the Roots map.
	DefaultRoot trees.File

	// Roots is the map of services to roots to use.
	Roots map[string]trees.File

	// AuthFile is a special file used for auth. The handle of AuthFile must
	// implement trees.Authenticator.
	AuthFile trees.File

	// It is important that the locks below are only held during the immediate
	// manipulation of the maps they are associated with. That also includes
	// the read locks. Holding it for the full duration of a potentially
	// blocking call such as read/write will lead to unwanted queuing of
	// requests, including Flush and Clunk.
	fidLock   sync.RWMutex
	tagLock   sync.Mutex
	errorLock sync.Mutex

	// internal state
	error       error
	errorCnt    uint32
	MessageSize uint32
	fids        map[qp.Fid]*fidState
	tags        map[qp.Tag]bool

	// Codecs
	Encoder *qp.Encoder
	Decoder *qp.Decoder
}

// cleanup handles post-execution cleanup.
func (fs *FileServer) cleanup() {
	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	for _, s := range fs.fids {
		s.Lock()
		if s.handle != nil {
			s.handle.Close()
			s.handle = nil
			s.mode = 0
		}
		s.Unlock()
	}
	fs.fids = make(map[qp.Fid]*fidState)
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

// die stops the server and records the first error.
func (fs *FileServer) die(err error) error {
	fs.errorLock.Lock()
	defer fs.errorLock.Unlock()
	if fs.error == nil {
		fs.error = err
	}
	atomic.AddUint32(&fs.errorCnt, 1)

	fs.cleanup()
	return fs.error
}

// handlePanic logs and prints
func (fs *FileServer) handlePanic() {
	r := recover()
	if r != nil {
		log.Printf("fileserver: Panic while handling request: %v\n\n%s\n", r, debug.Stack())
		fs.die(ErrHandlerPanic)
	}
}

// respond sends a response if the tag is still queued. The tag is removed
// immediately after checking its existence. It marks the fileserver as broken
// on error by setting fs.dead to the error, which must break the server loop.
func (fs *FileServer) respond(t qp.Tag, m qp.Message) {
	// We cannot let go of the tag lock until the tag has been deleted if it
	// existed. Otherwise, we risk that the tag gets flushed while we're sending
	// the response, potentially sending an Rflush before we start sending the
	// response, which would violate protocol.
	fs.tagLock.Lock()

	if _, tagPresent := fs.tags[t]; t != qp.NOTAG && !tagPresent {
		fs.tagLock.Unlock()
		return
	}

	delete(fs.tags, t)
	fs.logresp(t, m)
	fs.tagLock.Unlock()

	err := fs.Encoder.WriteMessage(m)
	switch err {
	case qp.ErrMessageTooBig:
		errmsg := ResponseTooBig

		if e, ok := m.(*qp.ErrorResponse); ok {
			// We do a bit of special handling if the failed message was an
			// error. We're supposed to cut the size down.

			// Calc the size to chop the message up to.
			max := int(fs.Encoder.MessageSize - qp.HeaderSize - 4)

			switch {
			case e.Error == ResponseTooBig:
				// Okay, we're done for. We can't even say the message was too
				// big.
				fs.die(ErrCouldNotSendErr)
				return
			case max < 16:
				// We should only end up here if someone intentionally messed up
				// our maxsize.
				fs.die(ErrCouldNotSendErr)
				return
			case len(e.Error) > max:
				errmsg = e.Error[:max]
			default:
				// The message was actually okay, so the encoder must be
				// confused.
				fs.die(ErrEncMessageSizeMismatch)
				return
			}
		}

		if err = fs.addTag(t); err != nil {
			// BUG(kl): If a WriteMessage fails in FileServer.respond with
			// qp.ErrMessageTooBig, and the client breaks protocol, sending an
			// additional request on the same tag, we risk not being able to
			// acquire the tag to write the error. In this case, the new request
			// overwrites the old, and we simply dump the original response and
			// its error message. More complicated locking could improve this
			// behaviour, but the client broke the protocol completely, so we do
			// not particularly care.
			return
		}
		fs.sendError(t, errmsg)
		return
	default:
		fs.die(err)
	case nil:
		return
	}
}

func (fs *FileServer) sendError(t qp.Tag, str string) {
	e := &qp.ErrorResponse{
		Tag:   t,
		Error: str,
	}
	fs.respond(t, e)
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

func (fs *FileServer) version(r *qp.VersionRequest) {
	defer fs.handlePanic()
	if r.Tag != qp.NOTAG {
		// Be compliant!
		fs.sendError(r.Tag, IncorrectTagForVersion)
		return
	}

	versionstr := r.Version
	msgsize := r.MessageSize
	if msgsize > fs.MessageSize {
		msgsize = fs.MessageSize
	} else if msgsize < MinSize {
		// This makes no sense. Error out.
		fs.sendError(r.Tag, MessageSizeTooSmall)
		return
	}

	// We change the protocol codec here if necessary. This only works because
	// the server loop is currently blocked. Had it continued ahead, blocking
	// in its Decode call again, we would only be able to change the protocol
	// for the next-next request. This would be an issue for .u, which change
	// the Tattach message, as well as for .e, which might follow up with a
	// Tsession immediately after our Rversion.
	var proto qp.Protocol
	switch versionstr {
	case qp.Version:
		proto = qp.NineP2000
	default:
		proto = qp.NineP2000
		versionstr = qp.UnknownVersion
	}

	// Reset everything.
	fs.tagLock.Lock()
	fs.tags = make(map[qp.Tag]bool)
	fs.tagLock.Unlock()
	fs.cleanup()

	// BUG(kl): Race on msgsize in version handling that initializes the codecs.
	// It is not of great importance, as sending other messages after or while
	// sending a Tversion makes no sense, and this assignment is only
	// problematic if we use the codecs *while* processing a Tversion request.
	fs.MessageSize = msgsize
	fs.Encoder.MessageSize = msgsize
	fs.Decoder.MessageSize = msgsize

	// Modifying the Decoder protocol is safe, as the receiver loop is blocked.
	// Modifying the Encoder protocol is safe as long as an older asynchronous
	// request is not having its response written at the current time.
	fs.Encoder.Protocol = proto
	fs.Decoder.Protocol = proto

	fs.respond(r.Tag, &qp.VersionResponse{
		Tag:         r.Tag,
		MessageSize: msgsize,
		Version:     versionstr,
	})
}

func (fs *FileServer) auth(r *qp.AuthRequest) {
	defer fs.handlePanic()
	if r.AuthFid == qp.NOFID {
		fs.sendError(r.Tag, InvalidFid)
		return
	}

	if fs.AuthFile == nil {
		fs.sendError(r.Tag, AuthNotSupported)
		return
	}

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	if _, exists := fs.fids[r.AuthFid]; exists {
		fs.sendError(r.Tag, FidInUse)
		return
	}

	handle, err := fs.AuthFile.Open(r.Username, qp.ORDWR)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	s := &fidState{
		username: r.Username,
		handle:   handle,
	}

	fs.fids[r.AuthFid] = s

	qid := qp.Qid{
		Version: ^uint32(0),
		Path:    ^uint64(0),
		Type:    qp.QTAUTH,
	}

	fs.respond(r.Tag, &qp.AuthResponse{
		Tag:     r.Tag,
		AuthQid: qid,
	})
}

func (fs *FileServer) attach(r *qp.AttachRequest) {
	defer fs.handlePanic()
	if r.Fid == qp.NOFID {
		fs.sendError(r.Tag, InvalidFid)
		return
	}

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	if _, exists := fs.fids[r.Fid]; exists {
		fs.sendError(r.Tag, FidInUse)
		return
	}

	switch {
	case fs.AuthFile != nil && r.AuthFid != qp.NOFID:
		// There's an authfile and an authfid - check it.
		as, exists := fs.fids[r.AuthFid]
		if !exists {
			fs.sendError(r.Tag, UnknownFid)
			return
		}

		if as.handle == nil {
			fs.sendError(r.Tag, FidNotOpen)
			return
		}

		auther, ok := as.handle.(trees.Authenticator)
		if !ok {
			fs.sendError(r.Tag, AfidNotAuthFile)
			return
		}

		authed, err := auther.Authenticated(r.Username, r.Service)
		if err != nil {
			fs.sendError(r.Tag, err.Error())
			return
		}

		if !authed {
			fs.sendError(r.Tag, PermissionDenied)
			return
		}
	case fs.AuthFile == nil && r.AuthFid != qp.NOFID:
		// There's no authfile, but an authfid was provided.
		fs.sendError(r.Tag, AuthNotSupported)
		return
	case fs.AuthFile != nil && r.AuthFid == qp.NOFID:
		// There's an authfile, but no authfid was provided.
		fs.sendError(r.Tag, AuthRequired)
		return
	}

	var root trees.File
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
	defer fs.handlePanic()
	fs.flushTag(r.OldTag)
	fs.respond(r.Tag, &qp.FlushResponse{
		Tag: r.Tag,
	})
}

// walkTo handles the walking logic. Walk returns a fidState, len(names) qids
// and a nil error if the walk succeeded. If the walk was partially successful,
// it returns a nil fidState, less than len(names) qids and a nil error. If the
// walk was completely unsuccessful, a nil fidState, nil qid slice and a non-nil
// error is returned.
func walkTo(state *fidState, names []string) (*fidState, []qp.Qid, error) {
	// Walk and Arrived can block, so we don't want to be holding locks. Copy
	// what we need.
	state.RLock()
	handle := state.handle
	root := state.location.Current()
	newloc := state.location.Clone()
	username := state.username
	state.RUnlock()

	if root == nil {
		return nil, nil, errors.New(InvalidOpOnFid)
	}

	if handle != nil {
		// Can't walk on an open fid.
		return nil, nil, errors.New(FidOpen)
	}

	if len(names) == 0 {
		// A 0-length walk is equivalent to walking to ".", which effectively
		// just clones the fid.
		x := &fidState{
			username: username,
			location: newloc,
		}
		return x, nil, nil
	}

	first := true
	var isdir bool
	var err error
	var qids []qp.Qid
	for i := range names {
		addToLoc := true
		name := names[i]
		switch name {
		case ".":
			// This always succeeds, but we don't want to add it to our location
			// list.
			addToLoc = false
		case "..":
			// This also always succeeds, and it either does nothing or shortens
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
			if root, err = d.Walk(username, name); err != nil {
				// The walk failed for some arbitrary reason.
				if first {
					return nil, nil, err
				}
				goto done
			} else if root == nil {
				// The file did not exist
				if first {
					return nil, nil, errors.New(NoSuchFile)
				}
				goto done
			}

			var temproot trees.File
			if temproot, err = root.Arrived(username); err != nil {
				// The Arrived callback failed for some arbitrary reason.
				if first {
					return nil, nil, err
				}
				goto done
			}

			if temproot != nil {
				root = temproot
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
		username: username,
		location: newloc,
	}

	return s, qids, nil
}

func (fs *FileServer) walk(r *qp.WalkRequest) {
	defer fs.handlePanic()
	if r.NewFid == qp.NOFID {
		fs.sendError(r.Tag, InvalidFid)
		return
	}

	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	fs.fidLock.RLock()
	_, exists = fs.fids[r.NewFid]
	fs.fidLock.RUnlock()

	if exists {
		fs.sendError(r.Tag, FidInUse)
		return
	}

	newfidState, qids, err := walkTo(state, r.Names)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	if newfidState != nil {
		fs.fidLock.Lock()
		fs.fids[r.NewFid] = newfidState
		fs.fidLock.Unlock()
	}

	fs.respond(r.Tag, &qp.WalkResponse{
		Tag:  r.Tag,
		Qids: qids,
	})
}

func (fs *FileServer) open(r *qp.OpenRequest) {
	defer fs.handlePanic()
	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.Lock()
	defer state.Unlock()

	if state.handle != nil {
		fs.sendError(r.Tag, FidOpen)
		return
	}

	l := state.location.Current()
	if l == nil {
		fs.sendError(r.Tag, InvalidOpOnFid)
		return
	}

	isdir, err := l.IsDir()
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	if isdir {
		switch r.Mode & 3 {
		case qp.OWRITE, qp.ORDWR:
			fs.sendError(r.Tag, OpenWriteOnDir)
			return
		}
	}

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

	state.handle = openfile
	state.mode = r.Mode
	fs.respond(r.Tag, &qp.OpenResponse{
		Tag: r.Tag,
		Qid: qid,
	})
}

func (fs *FileServer) create(r *qp.CreateRequest) {
	defer fs.handlePanic()
	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.Lock()
	defer state.Unlock()

	if state.handle != nil {
		fs.sendError(r.Tag, FidOpen)
		return
	}

	if r.Name == "." || r.Name == ".." {
		fs.sendError(r.Tag, InvalidFileName)
		return
	}

	cur := state.location.Current()
	if cur == nil {
		fs.sendError(r.Tag, InvalidOpOnFid)
		return
	}

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

	openfile, err := l.Open(state.username, r.Mode)
	if err != nil {
		fs.sendError(r.Tag, err.Error())
		return
	}

	state.location = append(state.location, l)
	state.handle = openfile
	state.mode = r.Mode

	fs.respond(r.Tag, &qp.CreateResponse{
		Tag:    r.Tag,
		Qid:    qid,
		IOUnit: 0,
	})
}

func (fs *FileServer) read(r *qp.ReadRequest) {
	defer fs.handlePanic()
	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.RLock()
	handle := state.handle
	mode := state.mode
	state.RUnlock()

	if handle == nil {
		fs.sendError(r.Tag, FidNotOpen)
		return
	}

	if (mode&3 != qp.OREAD) && (mode&3 != qp.ORDWR) {
		fs.sendError(r.Tag, NotOpenForRead)
		return
	}

	// We try to cap things to the negotiated maxsize.
	count := int(fs.Encoder.MessageSize) - qp.ReadOverhead
	if count > int(r.Count) {
		count = int(r.Count)
	}

	b := make([]byte, count)
	n, err := handle.ReadAt(b, int64(r.Offset))
	if err != nil && err != io.EOF {
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
	defer fs.handlePanic()
	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(r.Tag, UnknownFid)
		return
	}

	state.RLock()
	handle := state.handle
	mode := state.mode
	state.RUnlock()

	if handle == nil {
		fs.sendError(r.Tag, FidNotOpen)
		return
	}

	if (mode&3 != qp.OWRITE) && (mode&3 != qp.ORDWR) {
		fs.sendError(r.Tag, NotOpenForWrite)
		return
	}

	n, err := handle.WriteAt(r.Data, int64(r.Offset))
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
	defer fs.handlePanic()
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

	if state.handle != nil {
		state.handle.Close()
		state.handle = nil
	}

	fs.respond(r.Tag, &qp.ClunkResponse{
		Tag: r.Tag,
	})
}

func (fs *FileServer) remove(r *qp.RemoveRequest) {
	defer fs.handlePanic()
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

	if state.handle != nil {
		state.handle.Close()
		state.handle = nil
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
	defer fs.handlePanic()
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
		fs.sendError(r.Tag, InvalidOpOnFid)
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
	defer fs.handlePanic()
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
		fs.sendError(r.Tag, InvalidOpOnFid)
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

func (fs *FileServer) received(m qp.Message) error {
	t := m.GetTag()
	if err := fs.addTag(t); err != nil {
		fs.sendError(t, TagInUse)
		return nil
	}
	fs.logreq(t, m)

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
		fs.sendError(t, UnsupportedMessage)
	}
	return nil
}

// Serve starts the response parsing loop.
func (fs *FileServer) Serve() error {
	for atomic.LoadUint32(&fs.errorCnt) == 0 {
		m, err := fs.Decoder.ReadMessage()
		if err != nil {
			return fs.die(err)
		}
		fs.received(m)
	}
	fs.errorLock.Lock()
	defer fs.errorLock.Unlock()
	return fs.error
}

// New constructs a new FileServer. roots is the map where the fileserver should
// look for file roots based on service name. defaultRoot is the root that will
// be used if the service wasn't in the map. If the service is not in the map,
// and there is no default set, attach will fail.
func New(rw io.ReadWriter, defaultRoot trees.File, roots map[string]trees.File) *FileServer {
	fs := &FileServer{
		DefaultRoot: defaultRoot,
		Roots:       roots,
		Verbosity:   Quiet,
		MessageSize: MessageSize,
		tags:        make(map[qp.Tag]bool),
	}

	fs.Encoder = &qp.Encoder{
		Protocol:    qp.NineP2000,
		Writer:      rw,
		MessageSize: MessageSize,
	}

	fs.Decoder = &qp.Decoder{
		Protocol:    qp.NineP2000,
		Reader:      rw,
		MessageSize: MessageSize,
		Greedy:      true,
	}

	return fs
}
