package client

import (
	"errors"
	"io"
	"sync"

	"github.com/joushou/qp"
)

var (
	ErrWeirdResponse   = errors.New("weird response")
	ErrNoFidsAvailable = errors.New("no available fids")
	ErrNoSuchFid       = errors.New("no such fid")
)

func toError(m qp.Message) error {
	if eresp, ok := m.(*qp.ErrorResponse); ok {
		return errors.New(eresp.Error)
	}
	return nil
}

// DirectClient allows for wrapped access to the low-level 9P primitives, but
// without having to deal with concerns about actual serialization.
type DirectClient struct {
	fids    map[qp.Fid]*Fid
	fidLock sync.Mutex
	client  *Client
	nextFid qp.Fid
}

func NewDirectClient() *DirectClient {
	return &DirectClient{
		fids: make(map[qp.Fid]*Fid),
	}
}

func (dc *DirectClient) getFid() (*Fid, error) {
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
			f := &Fid{
				fid:    i,
				parent: dc,
			}
			dc.fids[i] = f
			return f, nil
		}
	}
	return nil, ErrNoFidsAvailable
}

func (dc *DirectClient) rmFid(f *Fid) error {
	dc.fidLock.Lock()
	defer dc.fidLock.Unlock()
	_, ok := dc.fids[f.fid]
	if ok {
		delete(dc.fids, f.fid)
	}
	return ErrNoSuchFid
}

func (dc *DirectClient) Connect(rw io.ReadWriter) {
	dc.client = New(rw)
	go dc.client.Start()
}

func (dc *DirectClient) Stop() {
	for _, fid := range dc.fids {
		fid.Clunk()
	}
	dc.fids = nil
	dc.client.Stop()
}

func (dc *DirectClient) FlushAll() {
	tags := dc.client.PendingTags()
	for _, t := range tags {
		dc.Flush(t)
		dc.client.Ditch(t)
	}
}

func (dc *DirectClient) Flush(oldtag qp.Tag) error {
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

func (dc *DirectClient) Version(maxsize uint32, version string) (uint32, string, error) {
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

func (dc *DirectClient) Auth(user, service string) (*Fid, qp.Qid, error) {
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

func (dc *DirectClient) Attach(authfid *Fid, user, service string) (*Fid, qp.Qid, error) {
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
		afid = authfid.fid
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

type Fid struct {
	fid    qp.Fid
	parent *DirectClient
}

func (f *Fid) Walk(names []string) (*Fid, []qp.Qid, error) {
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

func (f *Fid) Clunk() error {
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

func (f *Fid) Remove() error {
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

func (f *Fid) Open(mode qp.OpenMode) (qp.Qid, uint32, error) {
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

func (f *Fid) Create(name string, perm qp.FileMode, mode qp.OpenMode) (qp.Qid, uint32, error) {
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

func (f *Fid) Read(offset uint64, count uint32) ([]byte, error) {
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

func (f *Fid) Write(offset uint64, data []byte) (uint32, error) {
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

func (f *Fid) Stat() (qp.Stat, error) {
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

func (f *Fid) WriteStat(stat qp.Stat) error {
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
