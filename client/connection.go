package client

import "github.com/joushou/qp"

type Connection interface {
	Version(maxsize uint32, version string) (uint32, string, error)
	Auth(user, service string) (Fid, qp.Qid, error)
	Attach(authfid Fid, user, service string) (Fid, qp.Qid, error)

	FlushAll()
	Stop()
}

type Fid interface {
	ID() qp.Fid

	Walk(names []string) (Fid, []qp.Qid, error)
	Open(mode qp.OpenMode) (qp.Qid, uint32, error)
	Create(name string, perm qp.FileMode, mode qp.OpenMode) (qp.Qid, uint32, error)
	Read(offset uint64, count uint32) ([]byte, error)
	Write(offset uint64, data []byte) (uint32, error)
	Stat() (qp.Stat, error)
	WriteStat(stat qp.Stat) error
	Clunk() error
	Remove() error
}
