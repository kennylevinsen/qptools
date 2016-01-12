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
	// DefaultMessageSize is the default size used during protocol negotiation.
	DefaultMessageSize = 128 * 1024
)

// SimpleClient errors
var (
	ErrNotADirectory          = errors.New("not a directory")
	ErrInvalidPath            = errors.New("invalid path")
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
	c       Connection
	msgsize uint32
	root    Fid
}

// setup initializes the 9P connection.
func (c *SimpleClient) setup(username, servicename string) error {
	if c.c == nil {
		return ErrSimpleClientNotStarted
	}

	var version string
	var err error
	c.msgsize, version, err = c.c.Version(DefaultMessageSize, qp.Version)
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

// walkTo splits the provided filepath on "/", performing the filewalk.
func (c *SimpleClient) walkTo(file string) (Fid, []qp.Qid, error) {
	s := strings.Split(file, "/")

	var strs []string
	for _, str := range s {
		if str != "" {
			strs = append(strs, str)
		}
	}
	s = strs

	if len(s) == 0 {
		return nil, nil, ErrInvalidPath
	}

	fid, qids, err := c.root.Walk(s)
	if err != nil {
		return nil, nil, err
	}
	if fid == nil {
		return nil, nil, ErrNoSuchFile
	}

	return fid, qids, nil
}

// Stat returns the qp.Stat structure of the file.
func (c *SimpleClient) Stat(file string) (qp.Stat, error) {
	if c.root == nil {
		return qp.Stat{}, ErrSimpleClientNotStarted
	}
	fid, _, err := c.walkTo(file)
	if err != nil {
		return qp.Stat{}, err
	}
	defer fid.Clunk()

	return fid.Stat()
}

// Read reads the entire file content of the file specified.
func (c *SimpleClient) Read(file string) ([]byte, error) {
	if c.root == nil {
		return nil, ErrSimpleClientNotStarted
	}
	fid, _, err := c.walkTo(file)
	if err != nil {
		return nil, err
	}
	if fid == nil {
		return nil, ErrNoSuchFile
	}
	defer fid.Clunk()
	_, _, err = fid.Open(qp.OREAD)
	if err != nil {
		return nil, err
	}

	sfid := &WrappedFid{Fid: fid}

	return sfid.ReadAll()
}

// Write writes the content specified to the file specified.
func (c *SimpleClient) Write(content []byte, file string) error {
	if c.root == nil {
		return ErrSimpleClientNotStarted
	}
	fid, _, err := c.walkTo(file)
	if err != nil {
		return err
	}
	if fid == nil {
		return ErrNoSuchFile
	}
	defer fid.Clunk()
	_, _, err = fid.Open(qp.OWRITE)
	if err != nil {
		return err
	}

	sfid := &WrappedFid{Fid: fid}

	return sfid.WriteAll(content)
}

// List returns a list of qp.Stats for each file in the directory. It does not
// verify if the file is a directory before trying to decode the content - the
// result will most likely be decoding errors.
func (c *SimpleClient) List(file string) ([]qp.Stat, error) {
	if c.root == nil {
		return nil, ErrSimpleClientNotStarted
	}
	fid, _, err := c.walkTo(file)
	if err != nil {
		return nil, err
	}
	defer fid.Clunk()
	_, _, err = fid.Open(qp.OREAD)
	if err != nil {
		return nil, err
	}

	sfid := &WrappedFid{Fid: fid}

	b, err := sfid.ReadAll()
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

// Create creates either a file or a directory at the specified path. It fails
// if the file already exists, or permissions did not permit it.
func (c *SimpleClient) Create(name string, directory bool) error {
	if c.root == nil {
		return ErrSimpleClientNotStarted
	}
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

// Rename renames a file. It can only rename within a directory. It fails if
// the target name already exists.
func (c *SimpleClient) Rename(oldname, newname string) error {
	if c.root == nil {
		return ErrSimpleClientNotStarted
	}
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

// Remove removes a file, if permitted.
func (c *SimpleClient) Remove(name string) error {
	if c.root == nil {
		return ErrSimpleClientNotStarted
	}
	fid, _, err := c.walkTo(name)
	if err != nil {
		return err
	}
	defer fid.Remove()
	return nil
}

// Dial calls the provided address on the provided network, connecting to the
// provided service as the provided user. It does not support authentication.
func (c *SimpleClient) Dial(network, address, username, servicename string) error {
	conn, err := net.Dial(network, address)
	if err != nil {
		return err
	}

	x := New(conn)
	go x.Serve()
	c.c = x

	err = c.setup(username, servicename)
	if err != nil {
		return err
	}
	return nil
}

// Connect connects to the provided service as the provided user over the
// provided io.ReadWriter. It does not support authentication.
func (c *SimpleClient) Connect(rw io.ReadWriter, username, servicename string) error {
	x := New(rw)
	go x.Serve()

	c.c = x

	err := c.setup(username, servicename)
	if err != nil {
		return err
	}
	return nil
}

// Stop stops the underlying client.
func (c *SimpleClient) Stop() {
	if c.c != nil {
		c.c.Stop()
	}
	c.c = nil
}
