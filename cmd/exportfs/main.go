package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/kennylevinsen/qptools/fileserver"
	"github.com/kennylevinsen/qptools/fileserver/trees"
)

func usage() {
	fmt.Printf(`Usage: %s path UID GID address [verbosity]

      path           Path to serve
      UID            Owning user of /
      GID            Owning group of /
      address        Address to bind to
      verbosity      One of "quiet", "chatty", "loud", "obnoxious" or "debug"
`, os.Args[0])
}

func main() {
	if len(os.Args) < 5 {
		fmt.Printf("Too few arguments\n")
		usage()
		return
	}

	path := os.Args[1]
	user := os.Args[2]
	group := os.Args[3]
	addr := os.Args[4]

	var verbosity = fileserver.Quiet
	if len(os.Args) > 5 {
		switch os.Args[5] {
		case "quiet":
			verbosity = fileserver.Quiet
		case "chatty":
			verbosity = fileserver.Chatty
		case "loud":
			verbosity = fileserver.Loud
		case "obnoxious":
			verbosity = fileserver.Obnoxious
		case "debug":
			verbosity = fileserver.Debug
		default:
			fmt.Printf("Unknown verbosity level %s\n", os.Args[4])
			usage()
			return
		}
	}

	root := trees.NewProxyFile(path, "", user, group)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Unable to listen: %v", err)
	}

	log.Printf("Starting exportfs at %s", addr)

	for {
		conn, err := l.Accept()
		if err != nil {
			log.Printf("Error: %v", err)
			return
		}

		f := fileserver.New(conn, root, nil)
		f.Verbosity = verbosity
		go f.Serve()
	}
}
