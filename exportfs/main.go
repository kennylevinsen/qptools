package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/joushou/g9p"
	"github.com/joushou/g9ptools/exportfs/proxytree"
	"github.com/joushou/g9ptools/fileserver"
)

func main() {
	if len(os.Args) < 6 {
		fmt.Printf("Too few arguments\n")
		fmt.Printf("%s path service UID GID address\n", os.Args[0])
		fmt.Printf("UID and GID are the user/group that owns /\n")
		return
	}

	path := os.Args[1]
	service := os.Args[2]
	user := os.Args[3]
	group := os.Args[4]
	addr := os.Args[5]

	root := proxytree.NewProxyTree(path, "", user, group)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}

	h := func() g9p.Handler {
		m := make(map[string]fileserver.Dir)
		m[service] = root
		return fileserver.NewFileServer(nil, m, 10*1024*1024, fileserver.Obnoxious)
	}

	log.Printf("Starting proxy at %s", addr)
	g9p.ServeListener(l, h)
}
