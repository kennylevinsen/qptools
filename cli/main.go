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

func main() {
	loop := true
	if len(os.Args) < 4 {
		fmt.Printf("Too few arguments\n")
		return
	}

	addr := os.Args[1]
	user := os.Args[2]
	service := os.Args[3]

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
			strs, err := c.List(s)
			if err != nil {
				return err
			}
			for _, str := range strs {
				fmt.Printf("%s ", str)
			}
			fmt.Printf("\n")
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
