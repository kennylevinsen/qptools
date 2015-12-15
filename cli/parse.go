package main

import "errors"

var conv = map[rune]rune{
	'n': '\n',
	'r': '\r',
	't': '\t',
	'b': '\b',
	'f': '\f',
	'v': '\v',
	'0': 0,
}

func ParseCommandLine(str string) ([]string, error) {
	var s []string
	inDoubleQuote := false
	inSingleQuote := false
	inEscape := false
	var buf string
	for i, c := range str {

		switch c {
		case '"':
			if !inSingleQuote && !inEscape {
				inDoubleQuote = !inDoubleQuote
				continue
			}
		case '\'':
			if !inDoubleQuote && !inEscape {
				inSingleQuote = !inSingleQuote
				continue
			}
		case '\\':
			inEscape = true
			continue
		case ' ':
			if !inSingleQuote && !inDoubleQuote {
				s = append(s, buf)
				buf = ""
				continue
			}
		}

		if inEscape {
			x, exists := conv[c]
			if exists {
				c = x
			}
			inEscape = false
		}

		buf += string(c)

		if i == len(str)-1 {
			if inEscape || inDoubleQuote || inSingleQuote {
				return nil, errors.New("syntax error")
			}
			s = append(s, buf)
			buf = ""
		}

	}

	return s, nil
}
