package main

import (
	"fmt"
	"log"
	"net"
	"os"

	"github.com/joushou/qptools/fileserver"
	"github.com/joushou/qptools/fileserver/trees"

	"net/http"
	_ "net/http/pprof"
)

func usage() {
	fmt.Printf(`Usage: %s UID GID address [verbosity]

      UID            Owning user of /
      GID            Owning group of /
      address        Address to bind to
      verbosity      One of "quiet", "chatty", "loud", "obnoxious" or "debug"
`, os.Args[0])
}

func main() {
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()
	if len(os.Args) < 4 {
		fmt.Printf("Too few arguments\n")
		usage()
		return
	}

	user := os.Args[1]
	group := os.Args[2]
	addr := os.Args[3]

	var verbosity = fileserver.Quiet
	if len(os.Args) > 4 {
		switch os.Args[4] {
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

		f := fileserver.New(conn, root, nil, verbosity)
		go f.Start()
	}
}
