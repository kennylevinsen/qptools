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
)

func usage() {
	fmt.Printf(`qptools 9P cli

Usage: %s address user [service]

    address     The address to connect to.
    user        The user to connect as.
    service     The service to request (defaults to "").

Example: %s localhost:9999 glenda

`, os.Args[0], os.Args[0])
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
	loop := true
	if len(os.Args) < 3 {
		fmt.Printf("Too few arguments\n")
		usage()
		return
	}

	addr := os.Args[1]
	user := os.Args[2]
	service := ""
	if len(os.Args) > 3 {
		service = os.Args[3]
	}

	c := &client.SimpleClient{}
	err := c.Dial("tcp", addr, user, service)
	if err != nil {
		fmt.Printf("Connect failed: %v\n", err)
		return
	}

	cwd := "/"

	confirmation, err := readline.New("")
	confirm := func(s string) bool {
		confirmation.SetPrompt(fmt.Sprintf("%s [y]es, [n]o: ", s))
		l, err := confirmation.Readline()
		if err != nil {
			return false
		}

		switch l {
		default:
			fmt.Printf("Aborting\n")
			return false
		case "y", "yes":
			return true
		}
	}

	var cmds map[string]func(string) error
	cmds = map[string]func(string) error{
		"ls": func(s string) error {
			if !(len(s) > 0 && s[0] == '/') {
				s = path.Join(cwd, s)
			}
			stats, err := c.List(s)
			if err != nil {
				return err
			}

			// Sort the stats
			var sortedstats []qp.Stat
			selectedstat := -1
			for len(stats) > 0 {
				for i := range stats {
					if selectedstat == -1 {
						selectedstat = i
						continue
					}
					isfile1 := stats[i].Mode&qp.DMDIR == 0
					isfile2 := stats[selectedstat].Mode&qp.DMDIR == 0
					if isfile1 && !isfile2 {
						continue
					}

					if !isfile1 && isfile2 {
						selectedstat = i
						continue
					}

					if stats[i].Name < stats[selectedstat].Name {
						selectedstat = i
						continue
					}
				}
				sortedstats = append(sortedstats, stats[selectedstat])
				stats = append(stats[:selectedstat], stats[selectedstat+1:]...)
				selectedstat = -1
			}

			for _, stat := range sortedstats {
				fmt.Printf("%s\t%8d\t%s\n", permToString(stat.Mode), stat.Length, stat.Name)
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
			fmt.Printf("Showing content of %s\n%s\n", s, strs)
			return nil
		},
		"monitor": func(s string) error {
			if !(len(s) > 0 && s[0] == '/') {
				s = path.Join(cwd, s)
			}
			var off uint64
			for {
				strs, err := c.ReadSome(s, off)
				if err != nil {
					return err
				}
				off += uint64(len(strs))
				fmt.Printf("%s", strs)
			}
			return nil
		},
		"get": func(s string) error {
			if !(len(s) > 0 && s[0] == '/') {
				s = path.Join(cwd, s)
			}
			target := path.Base(s)
			f, err := os.Create(target)
			if err != nil {
				return err
			}

			fmt.Printf("Checking: %s", s)
			stat, err := c.Stat(s)
			if err != nil {
				return err
			}
			if stat.Mode&qp.DMDIR != 0 {
				return errors.New("file is a directory")
			}
			fmt.Printf(" - Done.\n")

			fmt.Printf("Downloading: %s to %s [%dB]", s, target, stat.Length)
			strs, err := c.Read(s)
			if err != nil {
				return err
			}
			fmt.Printf(" - Downloaded %dB.\n", len(strs))
			fmt.Printf("Writing data to %s", s)
			for len(strs) > 0 {
				n, err := f.Write(strs)
				if err != nil {
					return err
				}
				strs = strs[n:]
			}
			fmt.Printf(" - Done.\n")

			return nil
		},
		"put": func(s string) error {
			target := path.Join(cwd, path.Base(s))

			strs, err := ioutil.ReadFile(s)
			if err != nil {
				return err
			}
			fmt.Printf("Checking: %s", target)
			stat, err := c.Stat(target)
			fmt.Printf(" - Done.\n")
			if err != nil {
				fmt.Printf("File does not exist. Creating file: %s", target)
				err := c.Create(target, false)
				if err != nil {
					return err
				}
				fmt.Printf(" - Done.\n")
			} else {
				if !confirm("File exists. Do you want to overwrite it?") {
					return nil
				}
			}
			if stat.Mode&qp.DMDIR != 0 {
				return errors.New("file is a directory")
			}

			fmt.Printf("Uploading: %s to %s [%dB]", s, target, len(strs))
			err = c.Write(strs, target)
			if err != nil {
				return err
			}
			fmt.Printf(" - Done.\n")
			return nil
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

			fmt.Printf("Deleting %s\n", s)
			return c.Remove(s)
		},
		"quit": func(string) error {
			fmt.Printf("bye\n")
			loop = false
			return nil
		},
		"help": func(string) error {
			fmt.Printf("Available commands: \n")
			for k := range cmds {
				fmt.Printf("\t%s\n", k)
			}
			return nil
		},
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
		fmt.Printf("failed to create readline: %v\n", err)
		return
	}

	defer rl.Close()

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
			fmt.Printf("no such command: [%s]\n", cmd)
			continue
		}
		err = f(args)
		if err != nil {
			fmt.Printf("\ncommand %s failed: %v\n", cmd, err)
		}
	}
}
