package fileserver

import (
	"encoding/binary"
	"errors"
	"fmt"
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
	MessagesDuringVersion  = "version: messages sent during version negotiation"
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

var (
	errMsgTooBig = errors.New("message too big")
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

// requestState stores data regarding this specific request.
type requestState struct {
	// tag must not be modified after construction of requestState.
	tag qp.Tag

	// cancelled most only be read or modified while holding FileServer.tagLock.
	cancelled bool
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
// to its request.
//
// 3. FileServer permits explicit walks to ".".
type FileServer struct {
	// RW is the read writer for the fileserver.
	RW io.ReadWriter

	// Verbosity is the verbosity level.
	Verbosity Verbosity

	// DefaultRoot is the root to use if the service isn't in the Roots map.
	DefaultRoot trees.File

	// Roots is the map of services to roots to use.
	Roots map[string]trees.File

	// AuthFile is a special file used for auth. The handle of AuthFile must
	// implement trees.Authenticator.
	AuthFile trees.File

	// The message size that the server will suggest.
	SuggestedMessageSize uint32

	// It is important that the locks below are only held during the immediate
	// manipulation of the maps they are associated with. That also includes
	// the read locks. Holding it for the full duration of a potentially
	// blocking call such as read/write will lead to unwanted queuing of
	// requests, including Flush and Clunk.
	fidLock    sync.RWMutex
	tagLock    sync.Mutex
	errorLock  sync.Mutex
	configLock sync.RWMutex

	// internal state
	error    error
	errorCnt uint32
	fids     map[qp.Fid]*fidState
	tags     map[qp.Tag]*requestState
	msgSize  uint32
	proto    qp.Protocol

	// Codecs
	decoder *qp.Decoder
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

// register registers a request state to its tag as a pending request. If
// cancelAndOverwrite is set to true, a collision is solved by cancelling the
// old requestState and assigning a new one, then to return an error. Otherwise,
// an error is just returned.
func (fs *FileServer) register(rs *requestState, cancelAndOverwrite bool) error {
	if rs.tag == qp.NOTAG {
		return nil
	}

	fs.tagLock.Lock()
	defer fs.tagLock.Unlock()
	if oldRS, exists := fs.tags[rs.tag]; exists {
		if cancelAndOverwrite {
			oldRS.cancelled = true
			fs.tags[rs.tag] = rs
		}
		return errors.New(TagInUse)
	}

	fs.tags[rs.tag] = rs
	return nil
}

// serialize serializes a message. It returns an error if serialization
// failed, or if the message was too big to serialize based on the current
// msgSize. The config lock is held during serialize.
func (fs *FileServer) serialize(m qp.Message) ([]byte, []byte, error) {
	var (
		mt             qp.MessageType
		msgbuf, header []byte
		err            error
	)

	if mt, err = fs.proto.MessageType(m); err != nil {
		return nil, nil, err
	}

	if msgbuf, err = m.MarshalBinary(); err != nil {
		return nil, nil, err
	}

	if len(msgbuf)+qp.HeaderSize > int(fs.msgSize) {
		return nil, nil, errMsgTooBig
	}

	header = make([]byte, qp.HeaderSize)
	binary.LittleEndian.PutUint32(header[0:4], uint32(len(msgbuf)+qp.HeaderSize))
	header[4] = byte(mt)

	return header, msgbuf, nil
}

// writeMsg writes the header and message body to the connection.
func (fs *FileServer) writeMsg(header, msgbuf []byte) error {
	if _, err := fs.RW.Write(header); err != nil {
		return err
	}

	if _, err := fs.RW.Write(msgbuf); err != nil {
		return err
	}

	return nil
}

// respond sends a response if the tag is still queued. The tag is removed
// immediately after checking its existence. It marks the fileserver as broken
// on error by setting fs.dead to the error, which must break the server loop.
func (fs *FileServer) respond(rs *requestState, m qp.Message) {
	fs.configLock.RLock()
	defer fs.configLock.RUnlock()

	header, msgbuf, err := fs.serialize(m)
	switch err {
	case errMsgTooBig:
		header, msgbuf, err = fs.serialize(&qp.ErrorResponse{
			Tag:   rs.tag,
			Error: ResponseTooBig,
		})
		if err != nil {
			fs.die(ErrCouldNotSendErr)
		}
		return
	default:
		fs.die(err)
		return
	case nil:
	}

	// We cannot let go of the tag lock until the repsonse has been sent if
	// applicable. Otherwise, we risk that the tag gets flushed between deciding
	// that it is okay to send the response, and actually sending it.
	// This lock is also used to serialize the write calls.
	fs.tagLock.Lock()
	if rs.cancelled {
		fs.tagLock.Unlock()
		return
	}

	fs.logresp(rs.tag, m)
	err = fs.writeMsg(header, msgbuf)
	delete(fs.tags, rs.tag)

	fs.tagLock.Unlock()

	if err != nil {
		fs.die(err)
	}
}

func (fs *FileServer) sendError(rs *requestState, str string) {
	fs.respond(rs, &qp.ErrorResponse{
		Tag:   rs.tag,
		Error: str,
	})
}

func (fs *FileServer) version(r *qp.VersionRequest, rs *requestState) {
	defer fs.handlePanic()
	if r.Tag != qp.NOTAG {
		// Be compliant!
		fs.sendError(rs, IncorrectTagForVersion)
		return
	}

	versionstr := r.Version
	msgsize := r.MessageSize
	if msgsize > fs.SuggestedMessageSize {
		msgsize = fs.SuggestedMessageSize
	} else if msgsize < MinSize {
		// This makes no sense. Error out.
		fs.sendError(rs, MessageSizeTooSmall)
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
	fs.tags = make(map[qp.Tag]*requestState)
	fs.tagLock.Unlock()
	fs.cleanup()

	fs.configLock.Lock()

	fs.proto = proto
	fs.msgSize = msgsize

	fs.decoder.Protocol = fs.proto
	fs.decoder.Reader = fs.RW
	fs.decoder.MessageSize = fs.msgSize
	fs.decoder.Greedy = true

	err := fs.decoder.Reset()
	fs.configLock.Unlock()

	if err != nil {
		fs.die(fmt.Errorf("could not reset decoder: %v", err))
		fs.sendError(rs, MessagesDuringVersion)
		return
	}

	fs.respond(rs, &qp.VersionResponse{
		Tag:         r.Tag,
		MessageSize: fs.msgSize,
		Version:     versionstr,
	})
}

func (fs *FileServer) auth(r *qp.AuthRequest, rs *requestState) {
	defer fs.handlePanic()
	if r.AuthFid == qp.NOFID {
		fs.sendError(rs, InvalidFid)
		return
	}

	if fs.AuthFile == nil {
		fs.sendError(rs, AuthNotSupported)
		return
	}

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	if _, exists := fs.fids[r.AuthFid]; exists {
		fs.sendError(rs, FidInUse)
		return
	}

	handle, err := fs.AuthFile.Open(r.Username, qp.ORDWR)
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	fs.fids[r.AuthFid] = &fidState{
		username: r.Username,
		handle:   handle,
	}

	qid := qp.Qid{
		Version: ^uint32(0),
		Path:    ^uint64(0),
		Type:    qp.QTAUTH,
	}

	fs.respond(rs, &qp.AuthResponse{
		Tag:     r.Tag,
		AuthQid: qid,
	})
}

func (fs *FileServer) attach(r *qp.AttachRequest, rs *requestState) {
	defer fs.handlePanic()
	if r.Fid == qp.NOFID {
		fs.sendError(rs, InvalidFid)
		return
	}

	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	if _, exists := fs.fids[r.Fid]; exists {
		fs.sendError(rs, FidInUse)
		return
	}

	if fs.AuthFile != nil {
		if r.AuthFid == qp.NOFID {
			// There's an authfile, but no authfid was provided.
			fs.sendError(rs, AuthRequired)
			return
		}

		as, exists := fs.fids[r.AuthFid]
		if !exists {
			fs.sendError(rs, UnknownFid)
			return
		}

		if as.handle == nil {
			fs.sendError(rs, FidNotOpen)
			return
		}

		auther, ok := as.handle.(trees.Authenticator)
		if !ok {
			fs.sendError(rs, AfidNotAuthFile)
			return
		}

		authed, err := auther.Authenticated(r.Username, r.Service)
		if err != nil {
			fs.sendError(rs, err.Error())
			return
		}

		if !authed {
			fs.sendError(rs, PermissionDenied)
			return
		}
	} else if r.AuthFid != qp.NOFID {
		// There's no authfile, but an authfid was provided.
		fs.sendError(rs, AuthNotSupported)
		return
	}

	var root trees.File
	if x, exists := fs.Roots[r.Service]; exists {
		root = x
	} else {
		root = fs.DefaultRoot
	}

	if root == nil {
		fs.sendError(rs, NoSuchService)
		return
	}

	fs.fids[r.Fid] = &fidState{
		username: r.Username,
		location: FilePath{root},
	}

	qid, err := root.Qid()
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	fs.respond(rs, &qp.AttachResponse{
		Tag: r.Tag,
		Qid: qid,
	})
}

func (fs *FileServer) flush(r *qp.FlushRequest, rs *requestState) {
	defer fs.handlePanic()
	fs.tagLock.Lock()
	if oldRS, exists := fs.tags[r.OldTag]; exists {
		oldRS.cancelled = true
		delete(fs.tags, r.OldTag)
	}
	fs.tagLock.Unlock()
	fs.respond(rs, &qp.FlushResponse{
		Tag: r.Tag,
	})
}

// walkTo handles the walking logic. Walk returns a fidState, len(names) qids
// and a nil error if the walk succeeded. If the walk was partially successful,
// it returns a nil fidState, less than len(names) qids and a nil error. If the
// walk was completely unsuccessful, a nil fidState, nil qid slice and a non-nil
// error is returned.
func walkTo(oldState *fidState, names []string) (*fidState, []qp.Qid, error) {
	var (
		handle          trees.ReadWriteAtCloser
		isdir, addToLoc bool
		root, temproot  trees.File
		username, name  string
		newloc          FilePath
		qids            []qp.Qid
		d               trees.Dir
		err             error
		q               qp.Qid
	)

	// Walk and Arrived can block, so we don't want to be holding locks. Copy
	// what we need.
	oldState.RLock()
	handle = oldState.handle
	newloc = oldState.location.Clone()
	username = oldState.username
	oldState.RUnlock()

	root = newloc.Current()

	if root == nil {
		return nil, nil, errors.New(InvalidOpOnFid)
	}

	if handle != nil {
		// Can't walk on an open fid.
		return nil, nil, errors.New(FidOpen)
	}

	for i := range names {
		addToLoc = false
		name = names[i]
		switch name {
		case ".":
			// This always succeeds, but we don't want to add it to our location
			// list.
		case "..":
			// This also always succeeds, and it either does nothing or shortens
			// our location list. We don't want anything added to the list
			// regardless.
			root = newloc.Parent()
			if len(newloc) > 1 {
				newloc = newloc[:len(newloc)-1]
			}
		default:
			// A regular file name. In this case, walking to the name is only
			// legal if the current file is a directory.
			addToLoc = true

			isdir, err = root.IsDir()
			if err != nil {
				return nil, nil, err
			}

			if !isdir {
				// Root isn't a dir, so we can't walk.
				err = errors.New(FidNotDirectory)
				goto done
			}

			d = root.(trees.Dir)
			if root, err = d.Walk(username, name); err != nil {
				// The walk failed for some arbitrary reason.
				goto done
			} else if root == nil {
				// The file did not exist
				err = errors.New(NoSuchFile)
				goto done
			}

			if temproot, err = root.Arrived(username); err != nil {
				// The Arrived callback failed for some arbitrary reason.
				goto done
			}

			if temproot != nil {
				root = temproot
			}
		}

		if addToLoc {
			newloc = append(newloc, root)
		}

		q, err = root.Qid()
		if err != nil {
			return nil, nil, err
		}

		qids = append(qids, q)
	}

done:
	if err != nil && len(qids) == 0 {
		return nil, nil, err
	}
	if len(qids) < len(names) {
		return nil, qids, nil
	}

	s := &fidState{
		username: username,
		location: newloc,
	}

	return s, qids, nil
}

func (fs *FileServer) walk(r *qp.WalkRequest, rs *requestState) {
	defer fs.handlePanic()
	if r.NewFid == qp.NOFID {
		fs.sendError(rs, InvalidFid)
		return
	}

	fs.fidLock.RLock()
	state, existsOld := fs.fids[r.Fid]
	_, existsNew := fs.fids[r.NewFid]
	fs.fidLock.RUnlock()

	if !existsOld {
		fs.sendError(rs, UnknownFid)
		return
	}

	if existsNew && r.Fid != r.NewFid {
		fs.sendError(rs, FidInUse)
		return
	}

	newfidState, qids, err := walkTo(state, r.Names)
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	if newfidState != nil {
		fs.fidLock.Lock()
		// We have to check if the fid is still available. The walk could block,
		// or another thread could have beaten us to the race and won the fid
		// allocation. This implementation avoids temporarily using the fid if
		// the walk failed, at the cost of an additional map lookup in the
		// general case, and full walk too much in the worst case. A bit of
		// research into whether or not it is okay to temporarily consume a fid,
		// even if the walk fails, is necessary in order to determine the
		// correct implementation. If it is okay, then pre-allocating the fid
		// and removing it again if the walk fails should do the trick.
		state, existsNew = fs.fids[r.NewFid]
		if r.Fid == r.NewFid {
			if state.handle != nil {
				fs.fidLock.Unlock()
				fs.sendError(rs, FidOpen)
				return
			}
		} else if existsNew {
			fs.fidLock.Unlock()
			fs.sendError(rs, FidInUse)
			return
		}

		fs.fids[r.NewFid] = newfidState
		fs.fidLock.Unlock()
	}

	fs.respond(rs, &qp.WalkResponse{
		Tag:  r.Tag,
		Qids: qids,
	})
}

func (fs *FileServer) open(r *qp.OpenRequest, rs *requestState) {
	defer fs.handlePanic()
	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(rs, UnknownFid)
		return
	}

	state.Lock()
	defer state.Unlock()

	if state.handle != nil {
		fs.sendError(rs, FidOpen)
		return
	}

	l := state.location.Current()
	if l == nil {
		fs.sendError(rs, InvalidOpOnFid)
		return
	}

	isdir, err := l.IsDir()
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	if isdir {
		switch r.Mode & 3 {
		case qp.OWRITE, qp.ORDWR:
			fs.sendError(rs, OpenWriteOnDir)
			return
		}
	}

	qid, err := l.Qid()
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	openfile, err := l.Open(state.username, r.Mode)
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	state.handle = openfile
	state.mode = r.Mode
	fs.respond(rs, &qp.OpenResponse{
		Tag: r.Tag,
		Qid: qid,
	})
}

func (fs *FileServer) create(r *qp.CreateRequest, rs *requestState) {
	defer fs.handlePanic()
	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(rs, UnknownFid)
		return
	}

	state.Lock()
	defer state.Unlock()

	if state.handle != nil {
		fs.sendError(rs, FidOpen)
		return
	}

	if r.Name == "." || r.Name == ".." {
		fs.sendError(rs, InvalidFileName)
		return
	}

	cur := state.location.Current()
	if cur == nil {
		fs.sendError(rs, InvalidOpOnFid)
		return
	}

	isdir, err := cur.IsDir()
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}
	if !isdir {
		fs.sendError(rs, FidNotDirectory)
		return
	}

	dir := cur.(trees.Dir)

	l, err := dir.Create(state.username, r.Name, r.Permissions)
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	qid, err := l.Qid()
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	openfile, err := l.Open(state.username, r.Mode)
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	state.location = append(state.location, l)
	state.handle = openfile
	state.mode = r.Mode

	fs.respond(rs, &qp.CreateResponse{
		Tag:    r.Tag,
		Qid:    qid,
		IOUnit: 0,
	})
}

func (fs *FileServer) read(r *qp.ReadRequest, rs *requestState) {
	defer fs.handlePanic()
	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(rs, UnknownFid)
		return
	}

	state.RLock()
	handle := state.handle
	mode := state.mode
	state.RUnlock()

	if handle == nil {
		fs.sendError(rs, FidNotOpen)
		return
	}

	if (mode&3 != qp.OREAD) && (mode&3 != qp.ORDWR) {
		fs.sendError(rs, NotOpenForRead)
		return
	}

	// We try to cap things to the negotiated maxsize.
	fs.configLock.RLock()
	count := int(fs.msgSize) - qp.ReadOverhead
	fs.configLock.RUnlock()
	if count > int(r.Count) {
		count = int(r.Count)
	}

	b := make([]byte, count)
	n, err := handle.ReadAt(b, int64(r.Offset))
	if err != nil && err != io.EOF {
		fs.sendError(rs, err.Error())
		return
	}

	fs.respond(rs, &qp.ReadResponse{
		Tag:  r.Tag,
		Data: b[:n],
	})
}

func (fs *FileServer) write(r *qp.WriteRequest, rs *requestState) {
	defer fs.handlePanic()
	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(rs, UnknownFid)
		return
	}

	state.RLock()
	handle := state.handle
	mode := state.mode
	state.RUnlock()

	if handle == nil {
		fs.sendError(rs, FidNotOpen)
		return
	}

	if (mode&3 != qp.OWRITE) && (mode&3 != qp.ORDWR) {
		fs.sendError(rs, NotOpenForWrite)
		return
	}

	n, err := handle.WriteAt(r.Data, int64(r.Offset))
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	fs.respond(rs, &qp.WriteResponse{
		Tag:   r.Tag,
		Count: uint32(n),
	})
}

func (fs *FileServer) clunk(r *qp.ClunkRequest, rs *requestState) {
	defer fs.handlePanic()
	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	state, exists := fs.fids[r.Fid]
	if !exists {
		fs.sendError(rs, UnknownFid)
		return
	}

	delete(fs.fids, r.Fid)

	state.Lock()
	defer state.Unlock()

	if state.handle != nil {
		state.handle.Close()
		state.handle = nil
	}

	fs.respond(rs, &qp.ClunkResponse{
		Tag: r.Tag,
	})
}

func (fs *FileServer) remove(r *qp.RemoveRequest, rs *requestState) {
	defer fs.handlePanic()
	fs.fidLock.Lock()
	defer fs.fidLock.Unlock()

	state, exists := fs.fids[r.Fid]
	if !exists {
		fs.sendError(rs, UnknownFid)
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
		fs.respond(rs, &qp.RemoveResponse{
			Tag: r.Tag,
		})
		return
	}

	cur := state.location.Current()
	p := state.location.Parent()
	n, err := cur.Name()
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	p.(trees.Dir).Remove(state.username, n)

	fs.respond(rs, &qp.RemoveResponse{
		Tag: r.Tag,
	})
}

func (fs *FileServer) stat(r *qp.StatRequest, rs *requestState) {
	defer fs.handlePanic()
	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(rs, UnknownFid)
		return
	}

	state.RLock()
	l := state.location.Current()
	state.RUnlock()

	if l == nil {
		fs.sendError(rs, InvalidOpOnFid)
		return
	}

	st, err := l.Stat()
	if err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	fs.respond(rs, &qp.StatResponse{
		Tag:  r.Tag,
		Stat: st,
	})
}

func (fs *FileServer) writeStat(r *qp.WriteStatRequest, rs *requestState) {
	defer fs.handlePanic()
	fs.fidLock.RLock()
	state, exists := fs.fids[r.Fid]
	fs.fidLock.RUnlock()

	if !exists {
		fs.sendError(rs, UnknownFid)
		return
	}

	state.Lock()
	defer state.Unlock()

	l := state.location.Current()
	if l == nil {
		fs.sendError(rs, InvalidOpOnFid)
		return
	}

	var p trees.Dir
	if len(state.location) > 1 {
		p = state.location.Parent().(trees.Dir)
	}

	if err := setStat(state.username, l, p, r.Stat); err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	fs.respond(rs, &qp.WriteStatResponse{
		Tag: r.Tag,
	})
}

func (fs *FileServer) received(m qp.Message) {
	rs := &requestState{tag: m.GetTag()}
	fs.logreq(rs.tag, m)
	if err := fs.register(rs, true); err != nil {
		fs.sendError(rs, err.Error())
		return
	}

	switch mx := m.(type) {
	// Basic messages
	case *qp.VersionRequest:
		fs.version(mx, rs)
	case *qp.AuthRequest:
		fs.auth(mx, rs)
	case *qp.AttachRequest:
		fs.attach(mx, rs)
	case *qp.FlushRequest:
		fs.flush(mx, rs)
	case *qp.WalkRequest:
		go fs.walk(mx, rs)
	case *qp.OpenRequest:
		go fs.open(mx, rs)
	case *qp.CreateRequest:
		go fs.create(mx, rs)
	case *qp.ReadRequest:
		go fs.read(mx, rs)
	case *qp.WriteRequest:
		go fs.write(mx, rs)
	case *qp.ClunkRequest:
		go fs.clunk(mx, rs)
	case *qp.RemoveRequest:
		go fs.remove(mx, rs)
	case *qp.StatRequest:
		go fs.stat(mx, rs)
	case *qp.WriteStatRequest:
		go fs.writeStat(mx, rs)
	default:
		fs.sendError(rs, UnsupportedMessage)
	}
}

// Serve starts the response parsing loop.
func (fs *FileServer) Serve() error {
	for atomic.LoadUint32(&fs.errorCnt) == 0 {
		m, err := fs.decoder.ReadMessage()
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
		RW:                   rw,
		DefaultRoot:          defaultRoot,
		Roots:                roots,
		Verbosity:            Quiet,
		SuggestedMessageSize: MessageSize,
		tags:                 make(map[qp.Tag]*requestState),
		decoder: &qp.Decoder{
			Protocol:    qp.NineP2000,
			Reader:      rw,
			MessageSize: MessageSize,
		},
	}

	return fs
}
