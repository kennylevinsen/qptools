# fileserver

`fileserver` is the reference server implementation using qp. `trees` contains the filesystem interface that `fileserver` navigates, as well as a bunch of bundled tools and reference implementations.

See the `trees` package for more information about the filesystem abstraction (`trees.SyntheticFile`, `trees.SyntheticDir` and `trees.ProxyFile` are those of most interest). Futhermore, see ramfs and exportfs at the root of qptools for complete fileserver examples.
