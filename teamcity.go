package main

import (
	"strings"
)

// message is one "##kind[name key='value' ...]" line, as PHPUnit writes with --log-teamcity.
type message struct {
	Kind  string
	Name  string
	Attrs map[string]string
}

func parseMessage(line string) (message, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "##") || !strings.HasSuffix(line, "]") {
		return message{}, false
	}

	kind, rest, ok := strings.Cut(line[2:len(line)-1], "[")
	if !ok || kind == "" {
		return message{}, false
	}

	name, rest, _ := strings.Cut(rest, " ")
	msg := message{Kind: kind, Name: name, Attrs: map[string]string{}}

	for {
		rest = strings.TrimLeft(rest, " ")
		key, after, ok := strings.Cut(rest, "='")
		if !ok {
			return msg, true
		}

		value, remaining := readValue(after)
		msg.Attrs[key] = value
		rest = remaining
	}
}

// readValue reads an escaped value up to its closing quote and returns the rest of the line.
func readValue(s string) (string, string) {
	var b strings.Builder

	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			return b.String(), s[i+1:]
		case '|':
			if i+1 == len(s) {
				return b.String(), ""
			}
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			default:
				// |' |[ |] ||
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(s[i])
		}
	}

	return b.String(), ""
}

// testName turns "php_qn:///path/FooTest.php::\App\FooTest::testBar" into "App\FooTest::testBar".
func testName(msg message) string {
	if _, name, ok := strings.Cut(msg.Attrs["locationHint"], `::\`); ok {
		return name
	}

	return msg.Attrs["name"]
}

// testPath turns "php_qn:///path/FooTest.php::\App\FooTest::testBar" into "/path/FooTest.php".
func testPath(msg message) string {
	location := strings.TrimPrefix(msg.Attrs["locationHint"], "php_qn://")
	path, _, _ := strings.Cut(location, `::\`)

	return path
}
