package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/chzyer/readline"
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

	var cmds map[string]func(string)
	cmds = map[string]func(string){
		"ls": func(s string) {
			if s == "" {
				s = "/"
			}
			strs, err := c.List(s)
			if err != nil {
				fmt.Printf("ls failed: %v\n", err)
				return
			}
			fmt.Printf("%v\n", strs)
		},
		"cat": func(s string) {
			strs, err := c.Read(s)
			if err != nil {
				fmt.Printf("cat failed: %v\n", err)
				return
			}
			fmt.Printf("%s\n", strs)
		},
		"monitor": func(s string) {
			var off uint64
			for {
				strs, err := c.ReadSome(s, off)
				if err != nil {
					fmt.Printf("monitor failed: %v\n", err)
					return
				}
				off += uint64(len(strs))
				fmt.Printf("%s", strs)
			}
		},
		"get": func(string) {
			fmt.Printf("get is not yet implemented\n")
		},
		"put": func(string) {
			fmt.Printf("put is not yet implemented\n")
		},
		"mkdir": func(s string) {
			err := c.Create(s, true)
			if err != nil {
				fmt.Printf("mkdir failed: %v\n", err)
				return
			}
		},
		"rm": func(s string) {
			err := c.Remove(s)
			if err != nil {
				fmt.Printf("rm failed: %v\n", err)
				return
			}
		},
		"chmod": func(string) {
			fmt.Printf("chmod is not yet implemented\n")
		},
		"quit": func(string) {
			fmt.Printf("bye\n")
			loop = false
		},
		"help": func(string) {
			fmt.Printf("Available commands: \n")
			for k := range cmds {
				fmt.Printf("\t%s\n", k)
			}
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
		f(args)
	}
}
