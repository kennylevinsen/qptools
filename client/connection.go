package client

import "github.com/joushou/qp"

// Connection describes the operations that can be performed on a 9P
// connection.
type Connection interface {
	// Version initializes the connection with the provided protocol and
	// message size parameters. A successful version negotiation returns a
	// final msgsize lower or equal to the suggested msgsize, version string
	// equal to the suggested version string and no error. If the version
	// string is "unknown", the server is denying the protocol. A consequence
	// of a successful protocol negotiation is that any prior state on the
	// protocol is cleared - that is, all fids that may have been opened
	// previously will implicitly be clunked.
	Version(msgsize uint32, version string) (uint32, string, error)

	// Auth returns a fid and qid for the auth file of the requested user and
	// service. This fid can be used to execute an authentication protocol.
	// When done, use the fid as parameter to Attach. If no authentication is
	// required, an error will be returned.
	Auth(user, service string) (Fid, qp.Qid, error)

	// Attach returns a fid and qid for the request user and service. If
	// authentication is required, provide the fid from the Auth message.
	// Otherwise, use a nil fid.
	Attach(authfid Fid, user, service string) (Fid, qp.Qid, error)

	// FlushAll flushes all current requests.
	FlushAll()

	// Stop stops the underlying clietn.
	Stop()
}

// Fid descibes the actions that can be performed on a fid.
type Fid interface {
	// ID returns the integer ID that this Fid object represents.
	ID() qp.Fid

	// MessageSize returns the message size for the connection the Fid is in
	// use on.
	MessageSize() uint32

	// Walk walks to the provided path. If successful, a fid is returned.
	// Regardless of success, the qids for all parts of the path successfully
	// walked is returned. A walk with a nil slice is equivalent to a walk to
	// ".", effectively copying the fid. A walk to "." should never be used.
	Walk(names []string) (Fid, []qp.Qid, error)

	// Open opens the fid for read and/or write operations, depending on mode.
	// The number returned is the iounit, a number representing what size read
	// or write the user may expect to succeed in a single message, or 0 if no
	// such information is provided.
	Open(mode qp.OpenMode) (qp.Qid, uint32, error)

	// Create cretes a file and opens it for read and/or write operations,
	// depending on mode. It is created with the provided permissions. An error
	// is returned if the file already exists. The number returned is the
	// iounit, a number representing what size read or write the user may
	// expect to succeed in a single message, or 0 if no such information is
	// provided.
	Create(name string, perm qp.FileMode, mode qp.OpenMode) (qp.Qid, uint32, error)

	// ReadOnce executes a single read operation on the fid with the provided
	// parameters.
	ReadOnce(offset uint64, count uint32) ([]byte, error)

	// WriteOnce executes a single write operation on the fid with the provided
	// parameters.
	WriteOnce(offset uint64, data []byte) (uint32, error)

	// Stat issues a stat request to the fid.
	Stat() (qp.Stat, error)

	// WriteStat writes the provided stat to the fid.
	WriteStat(stat qp.Stat) error

	// Clunk closes the fid.
	Clunk() error

	// Remove closes the fid, and if possible, removes it. Even if the file
	// isn't removed for whatever reason, the fid is still clunked.
	Remove() error
}
