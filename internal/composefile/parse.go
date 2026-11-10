// Package composefile reads what a compose file declares and compares it with
// the containers a project runs (docs/DECISIONS.md, D15 and its successor).
//
// It never executes, interpolates, writes or renders a compose file. Parse
// keeps only the project name and, per service, its image and profiles;
// everything else in the document is discarded. Read is the one bounded way
// a file is opened. Compare works on plain structs, so neither the daemon nor
// the user interface can reach into the comparison, nor it into them.
package composefile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"

	yaml "go.yaml.in/yaml/v3"
)

// File is what a compose file declares, as far as the desk cares.
type File struct {
	// Name is the top-level name: field, "" when absent.
	Name string
	// Services is keyed by service name.
	Services map[string]Service
	// Includes is set when the file has a top-level include: the services
	// those files declare are not read, so a container the desk cannot place
	// is not called undeclared.
	Includes bool
}

// Service is one declared service.
type Service struct {
	// Image is the image: value exactly as written, "" when absent.
	Image string
	// Profiles lists the service's profiles.
	Profiles []string
	// Build is set when the service declares build: and may build its image.
	Build bool
	// Extends is set when the service inherits from another with extends:,
	// which can supply the image from a file the desk does not read.
	Extends bool
}

// ErrUnparseable is wrapped by every error Parse returns. The message never
// carries a byte of the document: at most a line number.
var ErrUnparseable = errors.New("not a compose file the desk can read")

// raw is the only shape the document is decoded into. Every field is a plain
// string or list of strings; a value of any other shape is a decode error.
type raw struct {
	Name     string                `yaml:"name"`
	Include  yaml.Node             `yaml:"include"`
	Services map[string]rawService `yaml:"services"`
}

type rawService struct {
	Image    string    `yaml:"image"`
	Profiles []string  `yaml:"profiles"`
	Build    yaml.Node `yaml:"build"`
	Extends  yaml.Node `yaml:"extends"`
}

// Parse decodes a compose document. It refuses, rather than returns part of,
// a document that does not decode, holds more than one document, or carries
// an explicit tag (!reset, !override, !!str …): what such a tag means is
// compose's merge logic, which the desk does not reproduce.
//
// A panic inside the decoder is turned into an error by parseNet. Tests call
// parse directly, so a fault the checks miss fails them rather than being
// caught.
func Parse(b []byte) (f File, err error) {
	defer func() {
		if r := recover(); r != nil {
			f, err = File{}, fmt.Errorf("%w: the YAML decoder failed on it", ErrUnparseable)
		}
	}()
	return parse(b)
}

// parseHook, when set, runs inside parse: tests use it to plant a fault
// and show what the net in Parse catches and what parse alone does not.
var parseHook func()

func parse(b []byte) (File, error) {
	if parseHook != nil {
		parseHook()
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return File{}, fmt.Errorf("%w: the file is empty", ErrUnparseable)
		}
		return File{}, refuse(err)
	}
	var more yaml.Node
	switch err := dec.Decode(&more); {
	case err == nil:
		return File{}, fmt.Errorf("%w: it holds more than one YAML document", ErrUnparseable)
	case !errors.Is(err, io.EOF):
		return File{}, refuse(err)
	}
	if line, ok := tagged(&doc); ok {
		return File{}, fmt.Errorf("%w: it carries an explicit YAML tag, whose meaning the desk does not reproduce (line %d)", ErrUnparseable, line)
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return File{}, fmt.Errorf("%w: its top level is not a mapping", ErrUnparseable)
	}
	var r raw
	if err := root.Decode(&r); err != nil {
		return File{}, refuse(err)
	}
	f := File{Name: r.Name, Includes: present(r.Include), Services: make(map[string]Service, len(r.Services))}
	for name, s := range r.Services {
		f.Services[name] = Service{Image: s.Image, Profiles: s.Profiles, Build: present(s.Build), Extends: present(s.Extends)}
	}
	return f, nil
}

// present reports whether a field was given a non-null value.
func present(n yaml.Node) bool {
	return n.Kind != 0 && !(n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

// tagged finds the first node carrying an explicit tag.
func tagged(n *yaml.Node) (int, bool) {
	if n.Style&yaml.TaggedStyle != 0 {
		return n.Line, true
	}
	for _, c := range n.Content {
		if line, ok := tagged(c); ok {
			return line, true
		}
	}
	return 0, false
}

var lineNo = regexp.MustCompile(`line (\d+)`)

// refuse turns a decoder error into one that names at most a line. The
// decoder quotes values in its messages ("cannot unmarshal !!str `…`"), and
// the file's bytes are never shown.
func refuse(err error) error {
	what := "it is not valid YAML"
	var te *yaml.TypeError
	if errors.As(err, &te) {
		what = "a field the desk reads has an unexpected shape"
	}
	if m := lineNo.FindStringSubmatch(err.Error()); m != nil {
		return fmt.Errorf("%w: %s (line %s)", ErrUnparseable, what, m[1])
	}
	return fmt.Errorf("%w: %s", ErrUnparseable, what)
}
