// Package keypath names where a line of indented JSON sits in its document:
// `Config.Health.Status`, `Mounts[0].Source`. It is pure and knows nothing
// of terminals.
//
// The path is derived from indentation alone, as a reader would: the depth
// of a line is its leading spaces over the indent width, its parent is the
// last line opened one level up, and its own segment is the `"key":` it
// starts with, or its position `[n]` when the parent is an array. The input
// is what encoding/json's Indent writes (one value or key per line, empty
// containers as `{}` and `[]`). Anything else gives a best-effort path and
// never a panic.
package keypath

import (
	"strconv"
	"strings"
)

// frame is a container opened by a line: an object or an array, its own
// path, and how many elements an array has had so far.
type frame struct {
	path  string
	array bool
	n     int
}

// Paths returns the key path of every line. The opening line of the
// document has the empty path; a closing line has the path of what it
// closes. indent is the width of one level (2 for json.Indent(…, "  ")).
func Paths(lines []string, indent int) []string {
	if indent < 1 {
		indent = 1
	}
	out := make([]string, len(lines))
	var stack []frame // stack[d] was opened by a line at depth d
	for i, raw := range lines {
		body := strings.TrimLeft(raw, " ")
		depth := (len(raw) - len(body)) / indent
		body = strings.TrimRight(body, " \t\r")
		if body == "" {
			continue
		}
		if body[0] == '}' || body[0] == ']' {
			if depth < len(stack) {
				out[i] = stack[depth].path
				stack = stack[:depth]
			}
			continue
		}
		if depth > len(stack) {
			depth = len(stack) // deeper than anything open: take the innermost
		}
		stack = stack[:depth]

		path := ""
		rest := body
		if depth > 0 {
			parent := &stack[depth-1]
			if parent.array {
				path = parent.path + "[" + strconv.Itoa(parent.n) + "]"
				parent.n++
			} else if key, after, ok := leadingKey(body); ok {
				path = join(parent.path, key)
				rest = after
			} else {
				path = parent.path
			}
		} else if key, after, ok := leadingKey(body); ok {
			// A fragment that starts inside an object.
			path, rest = join("", key), after
		}
		out[i] = path
		if opens(rest) {
			stack = append(stack, frame{path: path, array: strings.HasSuffix(rest, "[")})
		}
	}
	return out
}

// At is the key path of line i alone.
func At(lines []string, i, indent int) string {
	if i < 0 || i >= len(lines) {
		return ""
	}
	return Paths(lines[:i+1], indent)[i]
}

// opens reports whether what follows a key (or the whole line) opens a
// container that continues on the next lines.
func opens(rest string) bool {
	return strings.HasSuffix(rest, "{") || strings.HasSuffix(rest, "[")
}

// leadingKey reads `"key": ` at the start of a line.
func leadingKey(body string) (key, rest string, ok bool) {
	if len(body) < 2 || body[0] != '"' {
		return "", "", false
	}
	end := -1
	for j := 1; j < len(body); j++ {
		if body[j] == '\\' {
			j++
			continue
		}
		if body[j] == '"' {
			end = j
			break
		}
	}
	if end < 0 {
		return "", "", false
	}
	after := strings.TrimLeft(body[end+1:], " ")
	if !strings.HasPrefix(after, ":") {
		return "", "", false // a string value in an array, not a key
	}
	quoted := body[:end+1]
	if k, err := strconv.Unquote(quoted); err == nil {
		key = k
	} else {
		key = body[1:end] // JSON escapes Go does not know (\/): keep the text
	}
	return key, strings.TrimLeft(after[1:], " "), true
}

// join appends a key to a path. A key that is not a plain name — labels
// such as com.docker.compose.project, ports such as 80/tcp — is written in
// brackets and quotes, so that the dots of the path stay unambiguous.
func join(path, key string) string {
	if plain(key) {
		if path == "" {
			return key
		}
		return path + "." + key
	}
	return path + "[" + strconv.Quote(key) + "]"
}

func plain(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}
