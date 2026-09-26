package service

import (
	"fmt"
	"strings"
	"unicode"
)

// SplitCommand splits a command line into arguments.
//
// The rules are deliberately conservative, because a configuration file is not
// a shell and guessing wrong about a path separator is expensive:
//
//   - white space separates arguments
//   - single quotes are literal, with no escapes inside
//   - double quotes allow \" and \\
//   - outside quotes a backslash escapes only white space, a quote or another
//     backslash, so that a Windows path such as C:\tools\app.exe survives
//     intact
//   - an unterminated quote or a trailing escape is an error rather than a
//     silent guess
//
// There is no variable expansion, no globbing and no command substitution and
// there never will be: a service that needs a shell asks for one explicitly.
func SplitCommand(input string) ([]string, error) {
	var (
		args     []string
		current  strings.Builder
		inSingle bool
		inDouble bool
		hasToken bool
	)

	flush := func() {
		if hasToken {
			args = append(args, current.String())
			current.Reset()
			hasToken = false
		}
	}

	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		char := runes[i]
		switch {
		// A quote only opens or closes when it is not already inside the
		// other kind of quote.
		case char == '\'' && !inDouble:
			inSingle = !inSingle
			hasToken = true
		case char == '"' && !inSingle:
			inDouble = !inDouble
			hasToken = true
		// Inside single quotes every character is literal.
		case inSingle:
			current.WriteRune(char)
			hasToken = true
		// Inside double quotes only \" and \\ are escapes; white space is
		// literal, which is what makes quoting a path with spaces work.
		case inDouble:
			if char == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
				i++
				current.WriteRune(runes[i])
			} else {
				current.WriteRune(char)
			}
			hasToken = true
		case char == '\\':
			if i+1 >= len(runes) {
				return nil, fmt.Errorf("command ends with an incomplete escape")
			}
			if escapable(runes[i+1]) {
				i++
				current.WriteRune(runes[i])
			} else {
				current.WriteRune(char)
			}
			hasToken = true
		case unicode.IsSpace(char):
			flush()
		default:
			current.WriteRune(char)
			hasToken = true
		}
	}

	if inSingle || inDouble {
		return nil, fmt.Errorf("command has an unterminated quote")
	}
	flush()
	return args, nil
}

func escapable(char rune) bool {
	return unicode.IsSpace(char) || char == '\'' || char == '"' || char == '\\'
}
