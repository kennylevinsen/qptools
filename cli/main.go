package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
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

func main() {
	kingpin.Parse()

	c := &client.SimpleClient{}
	err := c.Dial("tcp", *address, *user, *service)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Connect failed: %v\n", err)
		return
	}

	confirmation, err := readline.New("")
	confirm := func(s string) bool {
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

	cwd := "/"
	loop := true
	cmds := map[string]func(string) error{
		"ls": func(s string) error {
			if !(len(s) > 0 && s[0] == '/') {
				s = path.Join(cwd, s)
			}
			stats, err := c.List(s)
			if err != nil {
				return err
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
						// The previously selected file is a dir, and we got a file, so we lose.
						continue
					}

					if !isfile1 && isfile2 {
						// The previously selected file is a file, and we got a dir, so we win.
						selectedstat = i
						continue
					}

					if stats[i].Name < stats[selectedstat].Name {
						// We're both of the same type, but our name as lower value, so we win.
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
			return nil
		},
		"cd": func(s string) error {
			if !(len(s) > 0 && s[0] == '/') {
				s = path.Join(cwd, s)
			}
			stat, err := c.Stat(s)
			if err != nil {
				return err
			}
			if stat.Mode&qp.DMDIR == 0 {
				return errors.New("file is not a directory")
			}
			cwd = s
			return nil
		},
		"pwd": func(string) error {
			fmt.Printf("%s\n", cwd)
			return nil
		},
		"cat": func(s string) error {
			if !(len(s) > 0 && s[0] == '/') {
				s = path.Join(cwd, s)
			}
			strs, err := c.Read(s)
			if err != nil {
				return err
			}
			fmt.Printf("%s", strs)
			fmt.Fprintf(os.Stderr, "\n")
			return nil
		},
		"monitor": func(s string) error {
			if !(len(s) > 0 && s[0] == '/') {
				s = path.Join(cwd, s)
			}
			fmt.Fprintf(os.Stderr, "Monitoring %s\n", s)
			var off uint64
			for {
				strs, err := c.ReadSome(s, off)
				if err != nil {
					return err
				}
				off += uint64(len(strs))
				fmt.Printf("%s", strs)
			}
		},
		"get": func(s string) error {
			args, err := parseCommandLine(s)
			if err != nil {
				return err
			}
			cmd := kingpin.New("put", "")
			remote := cmd.Arg("remote", "remote filename").Required().String()
			local := cmd.Arg("local", "local filename").Required().String()
			_, err = cmd.Parse(args)
			if err != nil {
				return err
			}

			if !(len(s) > 0 && s[0] == '/') {
				*remote = path.Join(cwd, *remote)
			}
			f, err := os.Create(*local)
			if err != nil {
				return err
			}

			stat, err := c.Stat(*remote)
			if err != nil {
				return err
			}
			if stat.Mode&qp.DMDIR != 0 {
				return errors.New("file is a directory")
			}

			fmt.Fprintf(os.Stderr, "Downloading: %s to %s [%dB]", remote, local, stat.Length)
			strs, err := c.Read(*remote)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, " - Downloaded %dB.\n", len(strs))
			for len(strs) > 0 {
				n, err := f.Write(strs)
				if err != nil {
					return err
				}
				strs = strs[n:]
			}

			return nil
		},
		"put": func(s string) error {
			args, err := parseCommandLine(s)
			if err != nil {
				return err
			}
			cmd := kingpin.New("put", "")
			local := cmd.Arg("local", "local filename").Required().String()
			remote := cmd.Arg("remote", "remote filename").Required().String()
			_, err = cmd.Parse(args)
			if err != nil {
				return err
			}

			target := path.Join(cwd, path.Base(*remote))

			strs, err := ioutil.ReadFile(*local)
			if err != nil {
				return err
			}
			stat, err := c.Stat(target)
			if err != nil {
				fmt.Fprintf(os.Stderr, "File does not exist. Creating file: %s", target)
				err := c.Create(target, false)
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, " - Done.\n")
			} else {
				if !confirm("File exists. Do you want to overwrite it?") {
					return nil
				}
			}
			if stat.Mode&qp.DMDIR != 0 {
				return errors.New("file is a directory")
			}

			fmt.Fprintf(os.Stderr, "Uploading: %s to %s [%dB]", *local, target, len(strs))
			err = c.Write(strs, target)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, " - Done.\n")
			return nil
		},
		"mv": func(s string) error {
			args, err := parseCommandLine(s)
			if err != nil {
				return err
			}
			cmd := kingpin.New("mv", "")
			source := cmd.Arg("source", "source filename").Required().String()
			destination := cmd.Arg("destination", "destination filename").Required().String()
			_, err = cmd.Parse(args)
			if err != nil {
				return err
			}

			return c.Rename(*source, *destination)
		},
		"mkdir": func(s string) error {
			if !(len(s) > 0 && s[0] == '/') {
				s = path.Join(cwd, s)
			}
			return c.Create(s, true)
		},
		"rm": func(s string) error {
			if !(len(s) > 0 && s[0] == '/') {
				s = path.Join(cwd, s)
			}

			if !confirm(fmt.Sprintf("Are you sure you want to delete %s?", s)) {
				return nil
			}

			fmt.Fprintf(os.Stderr, "Deleting %s\n", s)
			return c.Remove(s)
		},
		"quit": func(string) error {
			fmt.Fprintf(os.Stderr, "bye\n")
			loop = false
			return nil
		},
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
			fmt.Fprintf(os.Stderr, "no such command: [%s]\n", command)
			return
		}
		err = f(args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\ncommand %s failed: %v\n", command, err)
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

	for loop {
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
		err = f(args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\ncommand %s failed: %v\n", cmd, err)
		}
	}
}
