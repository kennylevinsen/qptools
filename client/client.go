package client

import (
	"errors"
	"io"
	"sync"

	"github.com/kennylevinsen/qp"
)

var (
	// ErrWeirdResponse indicates that a response type was unexpected. That is,
	// not the response fitting the request or ErrorResponse.
	ErrWeirdResponse = errors.New("weird response")

	// ErrNoFidsAvailable indicate that the pool of fids have been depleted,
	// due to 0xFFFE files being open.
	ErrNoFidsAvailable = errors.New("no available fids")

	// ErrNoSuchFid indicates that the fid does not exist.
	ErrNoSuchFid = errors.New("no such fid")

	// ErrNoSuchFile indicates that the file didn't exist, although the walk was
	// a success.
	ErrNoSuchFile = errors.New("no such file or directory")
)

func toError(m qp.Message) error {
	if eresp, ok := m.(*qp.ErrorResponse); ok {
		return errors.New(eresp.Error)
	}
	return nil
}

// Client implements a high-level interface to 9P. It uses Transport internally
// for serialization.
type Client struct {
	fids      map[qp.Fid]*ClientFid
	fidLock   sync.Mutex
	transport *Transport
	nextFid   qp.Fid
}

// New returns an initialized Client.
func New(rw io.ReadWriter) *Client {
	t := NewTransport(rw)
	return &Client{
		fids:      make(map[qp.Fid]*ClientFid),
		transport: t,
	}
}

// Serve runs the underlying client.
func (dc *Client) Serve() error {
	return dc.transport.Serve()
}

// getFid allocates and returns a new Fid.
func (dc *Client) getFid() (*ClientFid, error) {
	dc.fidLock.Lock()
	defer dc.fidLock.Unlock()
	for i := qp.Fid(0); i < qp.NOFID; i++ {
		taken := false
		for key := range dc.fids {
			if key == i {
				taken = true
				break
			}
		}
		if !taken {
			f := &ClientFid{
				fid:    i,
				parent: dc,
			}
			dc.fids[i] = f
			return f, nil
		}
	}
	return nil, ErrNoFidsAvailable
}

// rmFid removes a Fid from the usage pool.
func (dc *Client) rmFid(f *ClientFid) error {
	dc.fidLock.Lock()
	defer dc.fidLock.Unlock()
	_, ok := dc.fids[f.fid]
	if ok {
		delete(dc.fids, f.fid)
	}
	return ErrNoSuchFid
}

// Stop clunks all fids and terminates the client.
func (dc *Client) Stop() {
	for _, fid := range dc.fids {
		fid.Clunk()
	}
	dc.fids = nil
	dc.transport.Stop()
}

// FlushAll flushes all current requests.
func (dc *Client) FlushAll() {
	tags := dc.transport.PendingTags()
	for _, t := range tags {
		dc.Flush(t)
		dc.transport.Ditch(t)
	}
}

// Flush sends Tflush.
func (dc *Client) Flush(oldtag qp.Tag) error {
	t, err := dc.transport.Tag()
	if err != nil {
		return err
	}
	_, err = dc.transport.Send(&qp.FlushRequest{
		Tag:    t,
		OldTag: oldtag,
	})

	return err
}

// Version initializes the connection with the provided protocol and message
// size parameters. A successful version negotiation returns a final msgsize
// lower or equal to the suggested msgsize, version string equal to the
// suggested version string and no error. If the version string is "unknown",
// the server is denying the protocol. A consequence of a successful protocol
// negotiation is that any prior state on the protocol is cleared - that is, all
// fids that may have been opened previously will implicitly be clunked.
func (dc *Client) Version(msgsize uint32, version string) (uint32, string, error) {
	if err := dc.transport.TakeTag(qp.NOTAG); err != nil {
		return 0, "", err
	}

	resp, err := dc.transport.Send(&qp.VersionRequest{
		Tag:         qp.NOTAG,
		MessageSize: msgsize,
		Version:     qp.Version,
	})

	if err != nil {
		return 0, "", err
	}
	if err = toError(resp); err != nil {
		return 0, "", err
	}

	vresp, ok := resp.(*qp.VersionResponse)
	if !ok {
		return 0, "", ErrWeirdResponse
	}

	dc.transport.SetMessageSize(msgsize)
	dc.transport.SetGreedyDecoding(true)

	return vresp.MessageSize, vresp.Version, nil
}

// Auth returns a fid and qid for the auth file of the requested user and
// service. This fid can be used to execute an authentication protocol. When
// done, use the fid as parameter to Attach. If no authentication is required,
// an error will be returned.
func (dc *Client) Auth(user, service string) (Fid, qp.Qid, error) {
	t, err := dc.transport.Tag()
	if err != nil {
		return nil, qp.Qid{}, err
	}

	nfid, err := dc.getFid()
	if err != nil {
		dc.rmFid(nfid)
		return nil, qp.Qid{}, err
	}

	resp, err := dc.transport.Send(&qp.AuthRequest{
		Tag:      t,
		AuthFid:  nfid.fid,
		Username: user,
		Service:  service,
	})
	if err != nil {
		dc.rmFid(nfid)
		return nil, qp.Qid{}, err
	}
	if err = toError(resp); err != nil {
		dc.rmFid(nfid)
		return nil, qp.Qid{}, err
	}

	aresp, ok := resp.(*qp.AuthResponse)
	if !ok {
		dc.rmFid(nfid)
		return nil, qp.Qid{}, ErrWeirdResponse
	}

	return nfid, aresp.AuthQid, nil
}

// Attach returns a fid and qid for the request user and service. If
// authentication is required, provide the fid from the Auth message. Otherwise,
// use a nil fid.
func (dc *Client) Attach(authfid Fid, user, service string) (Fid, qp.Qid, error) {
	t, err := dc.transport.Tag()
	if err != nil {
		return nil, qp.Qid{}, err
	}

	nfid, err := dc.getFid()
	if err != nil {
		dc.rmFid(nfid)
		return nil, qp.Qid{}, err
	}

	afid := qp.NOFID
	if authfid != nil {
		afid = authfid.ID()
	}

	resp, err := dc.transport.Send(&qp.AttachRequest{
		Tag:      t,
		Fid:      nfid.fid,
		AuthFid:  afid,
		Username: user,
		Service:  service,
	})
	if err != nil {
		dc.rmFid(nfid)
		return nil, qp.Qid{}, err
	}
	if err = toError(resp); err != nil {
		dc.rmFid(nfid)
		return nil, qp.Qid{}, err
	}

	aresp, ok := resp.(*qp.AttachResponse)
	if !ok {
		dc.rmFid(nfid)
		return nil, qp.Qid{}, ErrWeirdResponse
	}
	return nfid, aresp.Qid, nil
}

// ClientFid represents a fid, implementing all 9P features that operate on a
// fid.
type ClientFid struct {
	fid        qp.Fid
	parent     *Client
	offsetLock sync.Mutex
}

// ID returns the integer value of the fid as a qp.Fid.
func (f *ClientFid) ID() qp.Fid {
	return f.fid
}

// MessageSize returns the message size of the parent connections client.
func (f *ClientFid) MessageSize() uint32 {
	return f.parent.transport.MessageSize()
}

// Walk sends Twalk.
func (f *ClientFid) Walk(names []string) (Fid, []qp.Qid, error) {
	t, err := f.parent.transport.Tag()
	if err != nil {
		return nil, nil, err
	}

	nfid, err := f.parent.getFid()
	if err != nil {
		f.parent.rmFid(nfid)
		return nil, nil, err
	}

	resp, err := f.parent.transport.Send(&qp.WalkRequest{
		Tag:    t,
		Fid:    f.fid,
		NewFid: nfid.fid,
		Names:  names,
	})

	if err != nil {
		return nil, nil, err
	}
	if err = toError(resp); err != nil {
		f.parent.rmFid(nfid)
		return nil, nil, err
	}

	wresp, ok := resp.(*qp.WalkResponse)
	if !ok {
		f.parent.rmFid(nfid)
		return nil, nil, ErrWeirdResponse
	}

	if len(wresp.Qids) != len(names) {
		f.parent.rmFid(nfid)
		return nil, wresp.Qids, ErrNoSuchFile
	}
	return nfid, wresp.Qids, nil
}

// Clunk sends Tclunk.
func (f *ClientFid) Clunk() error {
	t, err := f.parent.transport.Tag()
	if err != nil {
		return err
	}

	resp, err := f.parent.transport.Send(&qp.ClunkRequest{
		Tag: t,
		Fid: f.fid,
	})
	if err != nil {
		return err
	}
	if err = toError(resp); err != nil {
		return err
	}
	_, ok := resp.(*qp.ClunkResponse)
	if !ok {
		return ErrWeirdResponse
	}
	f.parent.rmFid(f)
	return nil
}

// Remove sends Tremove.
func (f *ClientFid) Remove() error {
	t, err := f.parent.transport.Tag()
	if err != nil {
		return err
	}

	resp, err := f.parent.transport.Send(&qp.RemoveRequest{
		Tag: t,
		Fid: f.fid,
	})
	if err != nil {
		return err
	}
	if err = toError(resp); err != nil {
		return err
	}
	_, ok := resp.(*qp.RemoveResponse)
	if !ok {
		return ErrWeirdResponse
	}
	f.parent.rmFid(f)
	return nil
}

// Open sends Topen.
func (f *ClientFid) Open(mode qp.OpenMode) (qp.Qid, uint32, error) {
	t, err := f.parent.transport.Tag()
	if err != nil {
		return qp.Qid{}, 0, err
	}

	resp, err := f.parent.transport.Send(&qp.OpenRequest{
		Tag:  t,
		Fid:  f.fid,
		Mode: mode,
	})

	if err != nil {
		return qp.Qid{}, 0, err
	}
	if err = toError(resp); err != nil {
		return qp.Qid{}, 0, err
	}

	oresp, ok := resp.(*qp.OpenResponse)
	if !ok {
		return qp.Qid{}, 0, ErrWeirdResponse
	}

	return oresp.Qid, oresp.IOUnit, nil
}

// Create sends Tcreate.
func (f *ClientFid) Create(name string, perm qp.FileMode, mode qp.OpenMode) (qp.Qid, uint32, error) {
	t, err := f.parent.transport.Tag()
	if err != nil {
		return qp.Qid{}, 0, err
	}

	resp, err := f.parent.transport.Send(&qp.CreateRequest{
		Tag:         t,
		Fid:         f.fid,
		Name:        name,
		Permissions: perm,
		Mode:        mode,
	})

	if err != nil {
		return qp.Qid{}, 0, err
	}
	if err = toError(resp); err != nil {
		return qp.Qid{}, 0, err
	}

	oresp, ok := resp.(*qp.CreateResponse)
	if !ok {
		return qp.Qid{}, 0, ErrWeirdResponse
	}

	return oresp.Qid, oresp.IOUnit, nil
}

// ReadOnce is the primitive API, and is directly equivalent to sending a Tread.
func (f *ClientFid) ReadOnce(offset uint64, count uint32) ([]byte, error) {
	t, err := f.parent.transport.Tag()
	if err != nil {
		return nil, err
	}

	resp, err := f.parent.transport.Send(&qp.ReadRequest{
		Tag:    t,
		Fid:    f.fid,
		Offset: offset,
		Count:  count,
	})

	if err != nil {
		return nil, err
	}
	if err = toError(resp); err != nil {
		return nil, err
	}
	rresp, ok := resp.(*qp.ReadResponse)
	if !ok {
		return nil, ErrWeirdResponse
	}
	return rresp.Data, nil
}

// WriteOnce is the primitive API, and is directly equivalent to sending a
// Twrite.
func (f *ClientFid) WriteOnce(offset uint64, data []byte) (uint32, error) {
	t, err := f.parent.transport.Tag()
	if err != nil {
		return 0, err
	}

	resp, err := f.parent.transport.Send(&qp.WriteRequest{
		Tag:    t,
		Fid:    f.fid,
		Offset: offset,
		Data:   data,
	})
	if err != nil {
		return 0, err
	}
	if err = toError(resp); err != nil {
		return 0, err
	}

	wresp, ok := resp.(*qp.WriteResponse)
	if !ok {
		return 0, ErrWeirdResponse
	}

	return wresp.Count, nil
}

// Stat sends Tstat.
func (f *ClientFid) Stat() (qp.Stat, error) {
	t, err := f.parent.transport.Tag()
	if err != nil {
		return qp.Stat{}, err
	}

	resp, err := f.parent.transport.Send(&qp.StatRequest{
		Tag: t,
		Fid: f.fid,
	})

	if err != nil {
		return qp.Stat{}, err
	}
	if err = toError(resp); err != nil {
		return qp.Stat{}, err
	}

	sresp, ok := resp.(*qp.StatResponse)
	if !ok {
		return qp.Stat{}, ErrWeirdResponse
	}

	return sresp.Stat, nil
}

// WriteStat sends Twstat.
func (f *ClientFid) WriteStat(stat qp.Stat) error {
	t, err := f.parent.transport.Tag()
	if err != nil {
		return err
	}

	resp, err := f.parent.transport.Send(&qp.WriteStatRequest{
		Tag:  t,
		Fid:  f.fid,
		Stat: stat,
	})

	if err != nil {
		return err
	}
	if err = toError(resp); err != nil {
		return err
	}

	_, ok := resp.(*qp.WriteStatResponse)
	if !ok {
		return ErrWeirdResponse
	}

	return nil
}
