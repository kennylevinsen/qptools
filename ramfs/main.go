package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/joushou/qptools/fileserver"
	"github.com/joushou/qptools/fileserver/trees"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Printf("Too few arguments\n")
		fmt.Printf("%s UID GID address\n", os.Args[0])
		fmt.Printf("UID and GID are the user/group that owns /\n")
		return
	}

	user := os.Args[1]
	group := os.Args[2]
	addr := os.Args[3]

	root := trees.NewRAMTree("/", 0777, user, group)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}

	log.Printf("Starting ramfs at %s", addr)
	for {
		conn, err := l.Accept()
		if err != nil {
			log.Printf("Error: %v", err)
			return
		}

		f := fileserver.New(conn, root, nil, fileserver.Debug)
		go f.Start()
	}
}
