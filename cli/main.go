package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"strings"

	"github.com/chzyer/readline"
	"github.com/joushou/qp"
	"github.com/joushou/qptools/client"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	service = kingpin.Flag("service", "service name to use when connecting (aname)").Short('s').String()
	user    = kingpin.Flag("user", "username to use when connecting (uname)").Short('u').String()
	address = kingpin.Arg("address", "address to connect to").Required().String()
	command = stringList(kingpin.Arg("command", "command to execute (disables interactive mode)"))
)

type slist []string

func (i *slist) Set(value string) error {
	*i = append(*i, value)
	return nil
}

func (i *slist) String() string {
	return ""
}

func (i *slist) IsCumulative() bool {
	return true
}

func stringList(s kingpin.Settings) (target *[]string) {
	target = new([]string)
	s.SetValue((*slist)(target))
	return
}

func permToString(m qp.FileMode) string {
	x := []byte("drwxrwxrwx")
	if m&qp.DMDIR == 0 {
		x[0] = '-'
	}

	m = m & 0777
	for idx := uint(0); idx < 9; idx++ {

		if m&(1<<(8-idx)) == 0 {
			x[idx+1] = '-'
		}
	}
	return string(x)
}

func parsepath(path string) ([]string, bool) {
	if len(path) == 0 {
		return nil, false
	}
	absolute := path[0] == '/'
	s := strings.Split(path, "/")
	var strs []string
	for _, str := range s {
		switch str {
		case "", ".":
		case "..":
			if len(strs) > 0 && strs[len(strs)-1] != ".." {
				strs = strs[:len(strs)-1]
			} else {
				strs = append(strs, str)
			}
		default:
			strs = append(strs, str)
		}
	}
	return strs, absolute
}

func readdir(b []byte) ([]qp.Stat, error) {
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

var confirmation *readline.Instance

func confirm(s string) bool {
	confirmation.SetPrompt(fmt.Sprintf("%s [y]es, [n]o: ", s))
	l, err := confirmation.Readline()
	if err != nil {
		return false
	}

	switch l {
	default:
		fmt.Fprintf(os.Stderr, "Aborting\n")
		return false
	case "y", "yes":
		return true
	}
}

var cmds = map[string]func(root, cwd client.Fid, cmdline string) (client.Fid, error){
	"stat":   stat,
	"ls":     ls,
	"cd":     cd,
	"cat":    cat,
	"get":    get,
	"put":    put,
	"mkdir":  mkdir,
	"rm":     rm,
	"hold":   hold,
	"unhold": unhold,
	"stuff":  stuff,
}

func stat(root, cwd client.Fid, cmdline string) (client.Fid, error) {
	f := cwd
	if cmdline != "" {
		path, absolute := parsepath(cmdline)
		if absolute {
			f = root
		}
		var err error
		f, _, err = f.Walk(path)
		if err != nil {
			return cwd, err
		}
		defer f.Clunk()
	}

	s, err := f.Stat()
	if err != nil {
		return cwd, err
	}

	fmt.Fprintf(os.Stderr, `
Stat:
	Type:        0x%X
	Dev:         0x%X
	Qid.Type:    0x%X
	Qid.Version: %d
	Qid.Path:    0x%X
	Atime:       %d
	Mtime:       %d
	Length:      %d
	Name:        %s
	UID:         %s
	GID:         %s
	MUID:        %s
`, s.Type, s.Dev, s.Qid.Type, s.Qid.Version, s.Qid.Path, s.Atime, s.Mtime, s.Length, s.Name, s.UID, s.GID, s.MUID)

	return cwd, nil
}

func ls(root, cwd client.Fid, cmdline string) (client.Fid, error) {
	path, absolute := parsepath(cmdline)
	f := cwd
	if absolute {
		f = root
	}
	var err error
	f, _, err = f.Walk(path)
	if err != nil {
		return cwd, err
	}
	defer f.Clunk()

	_, _, err = f.Open(qp.OREAD)
	if err != nil {
		return cwd, err
	}

	wf := client.WrappedFid{Fid: f}
	b, err := wf.ReadAll()
	if err != nil {
		return cwd, err
	}

	stats, err := readdir(b)
	if err != nil {
		return cwd, err
	}

	// Sort the stats. We sort alphabetically with directories first.
	var sortedstats []qp.Stat
	selectedstat := -1
	for len(stats) > 0 {
		for i := range stats {
			if selectedstat == -1 {
				// Nothing was selected, so we automatically win.
				selectedstat = i
				continue
			}

			isfile1 := stats[i].Mode&qp.DMDIR == 0
			isfile2 := stats[selectedstat].Mode&qp.DMDIR == 0

			if isfile1 && !isfile2 {
				// The previously selected file is a dir, and we got a file, so
				// we lose.
				continue
			}

			if !isfile1 && isfile2 {
				// The previously selected file is a file, and we got a dir, so
				// we win.
				selectedstat = i
				continue
			}

			if stats[i].Name < stats[selectedstat].Name {
				// We're both of the same type, but our name has lower value, so
				// we win.
				selectedstat = i
				continue
			}

			// We're not special, so we lose by default.
		}

		// Append to sorted list, cut from previous list and reset selection.
		sortedstats = append(sortedstats, stats[selectedstat])
		stats = append(stats[:selectedstat], stats[selectedstat+1:]...)
		selectedstat = -1
	}

	for _, stat := range sortedstats {
		fmt.Printf("%s  %10d  %10d  %s\n", permToString(stat.Mode), stat.Qid.Version, stat.Length, stat.Name)
	}
	return cwd, nil
}

func cd(root, cwd client.Fid, cmdline string) (client.Fid, error) {
	path, absolute := parsepath(cmdline)

	f := cwd
	if absolute {
		f = root
	}

	f, qs, err := f.Walk(path)
	if err != nil {
		return cwd, err
	}

	if qs[len(qs)-1].Type != qp.QTDIR {
		return cwd, errors.New("not a directory")
	}

	cwd.Clunk()
	return f, nil
}

func cat(root, cwd client.Fid, cmdline string) (client.Fid, error) {
	path, absolute := parsepath(cmdline)

	f := cwd
	if absolute {
		f = root
	}
	var err error
	f, _, err = f.Walk(path)
	if err != nil {
		return cwd, err
	}
	defer f.Clunk()

	_, _, err = f.Open(qp.OREAD)
	if err != nil {
		return cwd, err
	}

	var off uint64
	for {
		b, err := f.ReadOnce(off, 1024)
		if err != nil {
			return cwd, err
		}
		if len(b) == 0 {
			break
		}

		off += uint64(len(b))

		fmt.Printf("%s", b)
	}

	fmt.Fprintf(os.Stderr, "\n")
	return cwd, nil
}

var heldFid client.Fid

func hold(root, cwd client.Fid, cmdline string) (client.Fid, error) {
	if heldFid != nil {
		return cwd, errors.New("Already holding fid")
	}
	path, absolute := parsepath(cmdline)

	f := cwd
	if absolute {
		f = root
	}

	var err error
	f, _, err = f.Walk(path)
	if err != nil {
		return cwd, err
	}
	heldFid = f
	fmt.Fprintf(os.Stderr, "Holding fid\n")
	return cwd, nil
}

func unhold(root, cwd client.Fid, cmdline string) (client.Fid, error) {
	if heldFid == nil {
		return cwd, errors.New("No fid held")
	}
	heldFid.Clunk()
	heldFid = nil
	fmt.Fprintf(os.Stderr, "Releasing fid\n")
	return cwd, nil
}

func stuff(root, cwd client.Fid, cmdline string) (client.Fid, error) {
	args, err := parseCommandLine(cmdline)
	if err != nil {
		return cwd, err
	}

	cmd := kingpin.New("stuff", "")
	content := cmd.Arg("content", "the string to stuff").Required().String()
	target := cmd.Arg("target", "the file to stuff it into").Required().String()
	_, err = cmd.Parse(args)
	if err != nil {
		return cwd, err
	}

	remotepath, absolute := parsepath(*target)
	if len(remotepath) == 0 {
		return cwd, errors.New("need a destination")
	}

	f := cwd
	if absolute {
		f = root
	}
	f, _, err = f.Walk(remotepath)
	if err != nil {
		return cwd, err
	}
	defer f.Clunk()

	_, _, err = f.Open(qp.OWRITE)
	if err != nil {
		return cwd, err
	}

	wf := &client.WrappedFid{Fid: f}
	err = wf.WriteAll([]byte(*content))

	return cwd, err
}

func get(root, cwd client.Fid, cmdline string) (client.Fid, error) {
	args, err := parseCommandLine(cmdline)
	if err != nil {
		return cwd, err
	}
	cmd := kingpin.New("get", "")
	remote := cmd.Arg("remote", "remote filename").Required().String()
	local := cmd.Arg("local", "local filename").Required().String()
	_, err = cmd.Parse(args)
	if err != nil {
		return cwd, err
	}

	remotepath, absolute := parsepath(*remote)
	lf, err := os.Create(*local)
	if err != nil {
		return cwd, err
	}

	f := cwd
	if absolute {
		f = root
	}
	f, _, err = f.Walk(remotepath)
	if err != nil {
		return cwd, err
	}
	defer f.Clunk()

	stat, err := f.Stat()
	if err != nil {
		return cwd, err
	}
	if stat.Mode&qp.DMDIR != 0 {
		return cwd, errors.New("file is a directory")
	}
	_, _, err = f.Open(qp.OREAD)
	if err != nil {
		return cwd, err
	}

	fmt.Fprintf(os.Stderr, "Downloading: %s to %s [%dB]", *remote, *local, stat.Length)
	wf := client.WrappedFid{Fid: f}
	strs, err := wf.ReadAll()
	if err != nil {
		return cwd, err
	}
	fmt.Fprintf(os.Stderr, " - Downloaded %dB.\n", len(strs))
	for len(strs) > 0 {
		n, err := lf.Write(strs)
		if err != nil {
			return cwd, err
		}
		strs = strs[n:]
	}

	return cwd, nil
}

func put(root, cwd client.Fid, cmdline string) (client.Fid, error) {
	args, err := parseCommandLine(cmdline)
	if err != nil {
		return cwd, err
	}
	cmd := kingpin.New("put", "")
	local := cmd.Arg("local", "local filename").Required().String()
	remote := cmd.Arg("remote", "remote filename").Required().String()
	_, err = cmd.Parse(args)
	if err != nil {
		return cwd, err
	}

	strs, err := ioutil.ReadFile(*local)
	if err != nil {
		return cwd, err
	}

	remotepath, absolute := parsepath(*remote)
	if len(remotepath) == 0 {
		return cwd, errors.New("need a destination")
	}
	f := cwd
	if absolute {
		f = root
	}
	f, _, err = f.Walk(remotepath[:len(remotepath)-1])
	if err != nil {
		return cwd, err
	}
	defer f.Clunk()

	_, _, err = f.Create(remotepath[len(remotepath)-1], 0666, qp.OWRITE)
	if err != nil {
		f, _, err = f.Walk(remotepath[len(remotepath)-1:])
		if err != nil {
			return cwd, err
		}
		defer f.Clunk()

		_, _, err := f.Open(qp.OTRUNC | qp.OWRITE)
		if err != nil {
			return cwd, err
		}
	}

	wf := &client.WrappedFid{Fid: f}
	fmt.Fprintf(os.Stderr, "Uploading: %s to %s [%dB]", *local, *remote, len(strs))
	err = wf.WriteAll(strs)
	if err != nil {
		return cwd, err
	}

	return cwd, nil
}

func mkdir(root, cwd client.Fid, cmdline string) (client.Fid, error) {
	remotepath, absolute := parsepath(cmdline)
	if len(remotepath) == 0 {
		return cwd, errors.New("need a destination")
	}
	f := cwd
	if absolute {
		f = root
	}

	fid, _, err := f.Walk(remotepath[:len(remotepath)-1])
	if err != nil {
		return cwd, err
	}
	if fid == nil {
		return cwd, err
	}
	defer fid.Clunk()
	_, _, err = fid.Create(remotepath[len(remotepath)-1], 0666|qp.DMDIR, qp.OREAD)
	return cwd, err
}

func rm(root, cwd client.Fid, cmdline string) (client.Fid, error) {
	remotepath, absolute := parsepath(cmdline)
	if len(remotepath) == 0 {
		return cwd, errors.New("need a destination")
	}
	f := cwd
	if absolute {
		f = root
	}

	fid, _, err := f.Walk(remotepath)
	if err != nil {
		return cwd, err
	}
	if fid == nil {
		return cwd, err
	}
	return cwd, fid.Remove()
}

func main() {
	kingpin.Parse()

	conn, err := net.Dial("tcp", *address)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Connect failed: %v\n", err)
		return
	}

	c := client.New(conn)
	go func() {
		err := c.Serve()
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nConnection lost: %v\n", err)
			os.Exit(-1)
		}
	}()

	_, _, err = c.Version(1024*1024, qp.Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Version failed: %v\n", err)
		return
	}

	root, _, err := c.Attach(nil, *user, *service)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Attach failed: %v\n", err)
		return
	}

	cwd := root

	confirmation, err = readline.New("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not make confirmation readline instance: %v\n", err)
		return
	}

	if len(*command) > 0 {
		args := ""
		for i := 1; i < len(*command); i++ {
			if i != 1 {
				args += " "
			}
			args += (*command)[i]
		}

		f, ok := cmds[(*command)[0]]
		if !ok {
			fmt.Fprintf(os.Stderr, "no such command: [%s]\n", *command)
			return
		}
		cwd, err = f(root, cwd, args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\ncommand %s failed: %v\n", *command, err)
		}
		return
	}

	completer := readline.NewPrefixCompleter()
	for k := range cmds {
		completer.Children = append(completer.Children, readline.PcItem(k))
	}

	rl, err := readline.NewEx(&readline.Config{
		Prompt:       "9p> ",
		AutoComplete: completer,
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create readline: %v\n", err)
		return
	}

	defer rl.Close()

	fmt.Fprintf(os.Stderr, "Welcome to the qptools 9P cli.\nPress tab to see available commands.\n")

	for {
		line, err := rl.Readline()
		if err != nil { // io.EOF
			break
		}

		idx := strings.Index(line, " ")
		var cmd, args string
		if idx != -1 {
			cmd = line[:idx]
			args = line[idx+1:]
		} else {
			cmd = line
		}

		f, ok := cmds[cmd]
		if !ok {
			fmt.Fprintf(os.Stderr, "no such command: [%s]\n", cmd)
			continue
		}
		cwd, err = f(root, cwd, args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\ncommand %s failed: %v\n", cmd, err)
		}
	}
}
