package client

import (
	"bytes"
	"errors"
	"io"
	"net"
	"path"
	"strings"

	"github.com/joushou/qp"
)

const (
	DefaultMaxSize = 128 * 1024
)

var (
	ErrUnknownProtocol        = errors.New("unknown protocol")
	ErrSimpleClientNotStarted = errors.New("client not started")
	ErrNoSuchFile             = errors.New("no such file")
	ErrNotADirectory          = errors.New("not a directory")
	ErrWeirdResponse          = errors.New("weird response")
)

func toError(m qp.Message) error {
	if eresp, ok := m.(*qp.ErrorResponse); ok {
		return errors.New(eresp.Error)
	}
	return nil
}

type SimpleClient struct {
	c       *Client
	maxSize uint32
	root    qp.Fid
	nextFid qp.Fid
}

func (c *SimpleClient) getFid() qp.Fid {
	// We need to skip NOFID (highest value) and 0 (our root)
	if c.nextFid == qp.NOFID {
		c.nextFid++
	}
	if c.nextFid == 0 {
		c.nextFid++
	}
	f := c.nextFid
	c.nextFid++
	return f
}

func (c *SimpleClient) setup(username, servicename string) error {
	if c.c == nil {
		return ErrSimpleClientNotStarted
	}

	resp, err := c.c.Send(&qp.VersionRequest{
		Tag:     qp.NOTAG,
		MaxSize: DefaultMaxSize,
		Version: qp.Version,
	})
	if err != nil {
		c.c.Stop()
		c.c = nil
		return err
	}
	if err = toError(resp); err != nil {
		c.c.Stop()
		c.c = nil
		return err
	}

	vresp, ok := resp.(*qp.VersionResponse)
	if !ok {
		return ErrWeirdResponse
	}

	if vresp.Version != qp.Version {
		return ErrUnknownProtocol
	}

	c.maxSize = vresp.MaxSize

	t, _ := c.c.Tag()

	resp, err = c.c.Send(&qp.AttachRequest{
		Tag:      t,
		Fid:      c.root,
		AuthFid:  qp.NOFID,
		Username: username,
		Service:  servicename,
	})

	if err != nil {
		c.c.Stop()
		c.c = nil
		return err
	}
	if err = toError(resp); err != nil {
		c.c.Stop()
		c.c = nil
		return err
	}

	return nil
}

func (c *SimpleClient) readAll(fid qp.Fid) ([]byte, error) {
	var b []byte

	for {
		t, _ := c.c.Tag()
		resp, err := c.c.Send(&qp.ReadRequest{
			Tag:    t,
			Fid:    fid,
			Offset: uint64(len(b)),
			Count:  c.maxSize - 9, // The size of a response
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

		if len(rresp.Data) == 0 {
			break
		}
		b = append(b, rresp.Data...)
	}

	return b, nil
}

func (c *SimpleClient) writeAll(fid qp.Fid, data []byte) error {
	var offset uint64
	for {
		count := int(c.maxSize - 20)
		if len(data[offset:]) < count {
			count = len(data[offset:])
		}
		t, _ := c.c.Tag()
		resp, err := c.c.Send(&qp.WriteRequest{
			Tag:    t,
			Fid:    fid,
			Offset: offset,
			Data:   data[offset : offset+uint64(count)],
		})
		if err != nil {
			return err
		}
		if err = toError(resp); err != nil {
			return err
		}

		wresp, ok := resp.(*qp.WriteResponse)
		if !ok {
			return ErrWeirdResponse
		}
		offset += uint64(wresp.Count)
	}

	return nil
}

func (c *SimpleClient) walkTo(file string) (qp.Fid, qp.Qid, error) {
	s := strings.Split(file, "/")

	var strs []string
	for _, str := range s {
		if str != "" {
			strs = append(strs, str)
		}
	}
	s = strs

	f := c.getFid()
	t, _ := c.c.Tag()
	resp, err := c.c.Send(&qp.WalkRequest{
		Tag:    t,
		Fid:    c.root,
		NewFid: f,
		Names:  s,
	})
	if err != nil {
		return qp.NOFID, qp.Qid{}, err
	}
	if err = toError(resp); err != nil {
		return qp.NOFID, qp.Qid{}, err
	}

	wresp, ok := resp.(*qp.WalkResponse)
	if !ok {
		return qp.NOFID, qp.Qid{}, ErrWeirdResponse
	}

	if len(wresp.Qids) != len(s) {
		return qp.NOFID, qp.Qid{}, ErrNoSuchFile
	}

	q := qp.Qid{}
	if len(wresp.Qids) > 0 {
		end := len(wresp.Qids) - 1
		for i, q := range wresp.Qids {
			if i == end {
				break
			}
			if q.Type&qp.QTDIR == 0 {
				return qp.NOFID, qp.Qid{}, ErrNotADirectory
			}
		}
		q = wresp.Qids[end]
	}

	return f, q, nil
}

func (c *SimpleClient) clunk(fid qp.Fid) {
	t, _ := c.c.Tag()
	c.c.Send(&qp.ClunkRequest{
		Tag: t,
		Fid: fid,
	})
}

func (c *SimpleClient) Read(file string) ([]byte, error) {
	fid, _, err := c.walkTo(file)
	if err != nil {
		return nil, err
	}
	defer c.clunk(fid)

	t, _ := c.c.Tag()
	resp, err := c.c.Send(&qp.OpenRequest{
		Tag:  t,
		Fid:  fid,
		Mode: qp.OREAD,
	})
	if err != nil {
		return nil, err
	}
	if err = toError(resp); err != nil {
		return nil, err
	}

	return c.readAll(fid)
}

func (c *SimpleClient) Write(content []byte, file string) error {
	fid, _, err := c.walkTo(file)
	if err != nil {
		return err
	}
	defer c.clunk(fid)

	t, _ := c.c.Tag()
	resp, err := c.c.Send(&qp.OpenRequest{
		Tag:  t,
		Fid:  fid,
		Mode: qp.OWRITE,
	})
	if err != nil {
		return err
	}
	if err = toError(resp); err != nil {
		return err
	}

	return c.writeAll(fid, content)
}

func (c *SimpleClient) List(file string) ([]string, error) {
	fid, _, err := c.walkTo(file)
	if err != nil {
		return nil, err
	}
	defer c.clunk(fid)

	t, _ := c.c.Tag()
	resp, err := c.c.Send(&qp.OpenRequest{
		Tag:  t,
		Fid:  fid,
		Mode: qp.OREAD,
	})
	if err != nil {
		return nil, err
	}
	if err = toError(resp); err != nil {
		return nil, err
	}

	b, err := c.readAll(fid)
	if err != nil {
		return nil, err
	}

	buf := bytes.NewBuffer(b)
	var strs []string
	for buf.Len() > 0 {
		x := &qp.Stat{}
		if err := x.Decode(buf); err != nil {
			return nil, err
		}
		if x.Mode&qp.DMDIR == 0 {
			strs = append(strs, x.Name)
		} else {
			strs = append(strs, x.Name+"/")
		}
	}

	return strs, nil
}

func (c *SimpleClient) Create(name string, directory bool) error {
	dir := path.Dir(name)
	file := path.Base(name)

	fid, _, err := c.walkTo(dir)
	if err != nil {
		return err
	}
	defer c.clunk(fid)

	perms := qp.FileMode(0755)
	if directory {
		perms |= qp.DMDIR
	}

	t, _ := c.c.Tag()
	resp, err := c.c.Send(&qp.CreateRequest{
		Tag:         t,
		Fid:         fid,
		Name:        file,
		Permissions: perms,
		Mode:        qp.OREAD,
	})
	if err != nil {
		return err
	}
	if err = toError(resp); err != nil {
		return err
	}
	return nil
}

func (c *SimpleClient) Remove(name string) error {
	fid, _, err := c.walkTo(name)
	if err != nil {
		return err
	}

	t, _ := c.c.Tag()
	resp, err := c.c.Send(&qp.RemoveRequest{
		Tag: t,
		Fid: fid,
	})
	if err != nil {
		return err
	}
	if err = toError(resp); err != nil {
		return err
	}
	return nil
}

func (c *SimpleClient) Dial(network, address, username, servicename string) error {
	conn, err := net.Dial(network, address)
	if err != nil {
		return err
	}

	c.c = New(conn)
	go c.c.Start()

	err = c.setup(username, servicename)
	if err != nil {
		return err
	}
	return nil
}

func (c *SimpleClient) Connect(rw io.ReadWriter, username, servicename string) error {
	c.c = New(rw)
	go c.c.Start()

	err := c.setup(username, servicename)
	if err != nil {
		return err
	}
	return nil
}
