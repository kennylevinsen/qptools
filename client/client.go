package client

import (
	"errors"
	"io"
	"sync"

	"github.com/joushou/qp"
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
)

func toError(m qp.Message) error {
	if eresp, ok := m.(*qp.ErrorResponse); ok {
		return errors.New(eresp.Error)
	}
	return nil
}

// DirectClient allows for wrapped access to the low-level 9P primitives, but
// without having to deal with concerns about actual serialization.
type Client struct {
	fids    map[qp.Fid]*DirectFid
	fidLock sync.Mutex
	client  *RawClient
	nextFid qp.Fid
}

// NewDirectClient returns an initialized DirectClient.
func New(rw io.ReadWriter) *Client {
	c := NewRawClient(rw)
	return &Client{
		fids:   make(map[qp.Fid]*DirectFid),
		client: c,
	}
}

// Start starts the underlying client.
func (dc *Client) Start() error {
	return dc.client.Start()
}

// getFid allocates and returns a new Fid.
func (dc *Client) getFid() (*DirectFid, error) {
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
			f := &DirectFid{
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
func (dc *Client) rmFid(f *DirectFid) error {
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
	dc.client.Stop()
}

// FlushAll flushes all current requests.
func (dc *Client) FlushAll() {
	tags := dc.client.PendingTags()
	for _, t := range tags {
		dc.Flush(t)
		dc.client.Ditch(t)
	}
}

// Flush sends Tflush.
func (dc *Client) Flush(oldtag qp.Tag) error {
	t, err := dc.client.Tag()
	if err != nil {
		return err
	}
	_, err = dc.client.Send(&qp.FlushRequest{
		Tag:    t,
		OldTag: oldtag,
	})

	return err
}

// Version sends Tversion.
func (dc *Client) Version(maxsize uint32, version string) (uint32, string, error) {
	resp, err := dc.client.Send(&qp.VersionRequest{
		Tag:     qp.NOTAG,
		MaxSize: maxsize,
		Version: qp.Version,
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

	return vresp.MaxSize, vresp.Version, nil
}

// Auth sends Tauth.
func (dc *Client) Auth(user, service string) (Fid, qp.Qid, error) {
	t, err := dc.client.Tag()
	if err != nil {
		return nil, qp.Qid{}, err
	}

	nfid, err := dc.getFid()
	if err != nil {
		dc.rmFid(nfid)
		return nil, qp.Qid{}, err
	}

	resp, err := dc.client.Send(&qp.AuthRequest{
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

// Attach sends Tattch.
func (dc *Client) Attach(authfid Fid, user, service string) (Fid, qp.Qid, error) {
	t, err := dc.client.Tag()
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

	resp, err := dc.client.Send(&qp.AttachRequest{
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

// Fid represents a fid, implementing all 9P features that operate on a fid.
type DirectFid struct {
	fid    qp.Fid
	parent *Client
}

func (f *DirectFid) ID() qp.Fid {
	return f.fid
}

// Walk sends Twalk.
func (f *DirectFid) Walk(names []string) (Fid, []qp.Qid, error) {
	t, err := f.parent.client.Tag()
	if err != nil {
		return nil, nil, err
	}

	nfid, err := f.parent.getFid()
	if err != nil {
		f.parent.rmFid(nfid)
		return nil, nil, err
	}

	resp, err := f.parent.client.Send(&qp.WalkRequest{
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
		nfid = nil
	}
	return nfid, wresp.Qids, nil
}

// Clunk sends Tclunk.
func (f *DirectFid) Clunk() error {
	t, err := f.parent.client.Tag()
	if err != nil {
		return err
	}

	resp, err := f.parent.client.Send(&qp.ClunkRequest{
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
func (f *DirectFid) Remove() error {
	t, err := f.parent.client.Tag()
	if err != nil {
		return err
	}

	resp, err := f.parent.client.Send(&qp.RemoveRequest{
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
func (f *DirectFid) Open(mode qp.OpenMode) (qp.Qid, uint32, error) {
	t, err := f.parent.client.Tag()
	if err != nil {
		return qp.Qid{}, 0, err
	}

	resp, err := f.parent.client.Send(&qp.OpenRequest{
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
func (f *DirectFid) Create(name string, perm qp.FileMode, mode qp.OpenMode) (qp.Qid, uint32, error) {
	t, err := f.parent.client.Tag()
	if err != nil {
		return qp.Qid{}, 0, err
	}

	resp, err := f.parent.client.Send(&qp.CreateRequest{
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

// Read sends Tread.
func (f *DirectFid) Read(offset uint64, count uint32) ([]byte, error) {
	t, err := f.parent.client.Tag()
	if err != nil {
		return nil, err
	}

	resp, err := f.parent.client.Send(&qp.ReadRequest{
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

// Write sends Twrite.
func (f *DirectFid) Write(offset uint64, data []byte) (uint32, error) {
	t, err := f.parent.client.Tag()
	if err != nil {
		return 0, err
	}

	resp, err := f.parent.client.Send(&qp.WriteRequest{
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
func (f *DirectFid) Stat() (qp.Stat, error) {
	t, err := f.parent.client.Tag()
	if err != nil {
		return qp.Stat{}, err
	}

	resp, err := f.parent.client.Send(&qp.StatRequest{
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
func (f *DirectFid) WriteStat(stat qp.Stat) error {
	t, err := f.parent.client.Tag()
	if err != nil {
		return err
	}

	resp, err := f.parent.client.Send(&qp.WriteStatRequest{
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
