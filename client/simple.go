package client

import (
	"encoding/binary"
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
	ErrNotADirectory          = errors.New("not a directory")
	ErrNoSuchFile             = errors.New("no such file")
	ErrUnknownProtocol        = errors.New("unknown protocol")
	ErrSimpleClientNotStarted = errors.New("client not started")
)

func emptyStat() qp.Stat {
	return qp.Stat{
		Type:   ^uint16(0),
		Dev:    ^uint32(0),
		Mode:   ^qp.FileMode(0),
		Atime:  ^uint32(0),
		Mtime:  ^uint32(0),
		Length: ^uint64(0),
	}
}

// SimpleClient provides a simple API for working with 9P servers. It is not
// the most efficient way to use 9P, but allows using such servers with little
// to no clue about what exactly 9P is.
type SimpleClient struct {
	c       *DirectClient
	maxSize uint32
	root    *Fid
}

func (c *SimpleClient) setup(username, servicename string) error {
	if c.c == nil {
		return ErrSimpleClientNotStarted
	}

	var version string
	var err error
	c.maxSize, version, err = c.c.Version(DefaultMaxSize, qp.Version)
	if err != nil {
		return err
	}
	if version != qp.Version {
		return ErrUnknownProtocol
	}

	c.root, _, err = c.c.Attach(nil, username, servicename)
	if err != nil {
		return err
	}
	return nil
}

func (c *SimpleClient) readAll(fid *Fid) ([]byte, error) {
	var b []byte

	for {
		// 9 is the size of a read response
		data, err := fid.Read(uint64(len(b)), c.maxSize-9)
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			break
		}
		b = append(b, data...)
	}

	return b, nil
}

func (c *SimpleClient) writeAll(fid *Fid, data []byte) error {
	var offset uint64
	for {
		count := int(c.maxSize - 20)
		if len(data[offset:]) < count {
			count = len(data[offset:])
		}

		if count == 0 {
			break
		}

		wcount, err := fid.Write(offset, data[offset:offset+uint64(count)])
		if err != nil {
			return err
		}
		offset += uint64(wcount)
	}

	return nil
}

func (c *SimpleClient) walkTo(file string) (*Fid, qp.Qid, error) {
	s := strings.Split(file, "/")

	var strs []string
	for _, str := range s {
		if str != "" {
			strs = append(strs, str)
		}
	}
	s = strs

	fid, qids, err := c.root.Walk(s)
	if err != nil {
		return nil, qp.Qid{}, err
	}

	if len(qids) != len(s) {
		return nil, qp.Qid{}, ErrNoSuchFile
	}

	q := qp.Qid{}
	if len(qids) > 0 {
		end := len(qids) - 1
		for i, q := range qids {
			if i == end {
				break
			}
			if q.Type&qp.QTDIR == 0 {
				return nil, qp.Qid{}, ErrNotADirectory
			}
		}
		q = qids[end]
	}
	return fid, q, nil
}

func (c *SimpleClient) Stat(file string) (qp.Stat, error) {
	fid, _, err := c.walkTo(file)
	if err != nil {
		return qp.Stat{}, err
	}
	defer fid.Clunk()

	return fid.Stat()
}

func (c *SimpleClient) ReadSome(file string, offset uint64) ([]byte, error) {
	fid, _, err := c.walkTo(file)
	if err != nil {
		return nil, err
	}
	defer fid.Clunk()
	_, _, err = fid.Open(qp.OREAD)
	if err != nil {
		return nil, err
	}
	return fid.Read(offset, c.maxSize-9)
}

func (c *SimpleClient) Read(file string) ([]byte, error) {
	fid, _, err := c.walkTo(file)
	if err != nil {
		return nil, err
	}
	defer fid.Clunk()
	_, _, err = fid.Open(qp.OREAD)
	if err != nil {
		return nil, err
	}

	return c.readAll(fid)
}

func (c *SimpleClient) Write(content []byte, file string) error {
	fid, _, err := c.walkTo(file)
	if err != nil {
		return err
	}
	defer fid.Clunk()
	_, _, err = fid.Open(qp.OWRITE)
	if err != nil {
		return err
	}

	return c.writeAll(fid, content)
}

func (c *SimpleClient) List(file string) ([]qp.Stat, error) {
	fid, _, err := c.walkTo(file)
	if err != nil {
		return nil, err
	}
	defer fid.Clunk()
	_, _, err = fid.Open(qp.OREAD)
	if err != nil {
		return nil, err
	}

	b, err := c.readAll(fid)
	if err != nil {
		return nil, err
	}

	var stats []qp.Stat
	for len(b) > 0 {
		x := qp.Stat{}
		l := binary.LittleEndian.Uint16(b[0:2])
		if err := x.UnmarshalBinary(b[0 : 2+l]); err != nil {
			return nil, err
		}
		b = b[2+l:]
		stats = append(stats, x)
	}

	return stats, nil
}

func (c *SimpleClient) Create(name string, directory bool) error {
	dir := path.Dir(name)
	file := path.Base(name)

	fid, _, err := c.walkTo(dir)
	if err != nil {
		return err
	}
	defer fid.Clunk()

	perms := qp.FileMode(0755)
	if directory {
		perms |= qp.DMDIR
	}

	_, _, err = fid.Create(file, perms, qp.OREAD)
	if err != nil {
		return err
	}
	return nil
}

func (c *SimpleClient) Rename(oldname, newname string) error {
	sold := strings.Split(oldname, "/")
	snew := strings.Split(newname, "/")
	if len(sold) != len(snew) {
		return errors.New("invalid rename")
	}

	for i := 0; i < len(sold)-1; i++ {
		if sold[i] != snew[i] {
			return errors.New("invalid rename")
		}
	}

	fid, _, err := c.walkTo(oldname)
	if err != nil {
		return err
	}
	defer fid.Clunk()

	s := emptyStat()
	s.Name = snew[len(snew)-1]

	return fid.WriteStat(s)
}

func (c *SimpleClient) Remove(name string) error {
	fid, _, err := c.walkTo(name)
	if err != nil {
		return err
	}
	defer fid.Remove()
	return nil
}

func (c *SimpleClient) Dial(network, address, username, servicename string) error {
	conn, err := net.Dial(network, address)
	if err != nil {
		return err
	}

	c.c = &DirectClient{}
	c.c.Connect(conn)

	err = c.setup(username, servicename)
	if err != nil {
		return err
	}
	return nil
}

func (c *SimpleClient) Connect(rw io.ReadWriter, username, servicename string) error {
	c.c = &DirectClient{}
	c.c.Connect(rw)

	err := c.setup(username, servicename)
	if err != nil {
		return err
	}
	return nil
}
