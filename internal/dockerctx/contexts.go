package dockerctx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Context is one entry of the Docker context store, as the daemon picker
// lists it: what `docker context ls` shows, and whether the desk can use it.
type Context struct {
	// Name is the context's name; "default" is the implicit one.
	Name string
	// Description is the context's stored description.
	Description string
	// Endpoint is where it points. Host is set whenever it could be read,
	// even when the context is unusable, so that the picker can show it.
	Endpoint Endpoint
	// Current marks the context the Docker CLI would use right now — the
	// one `docker context ls` stars.
	Current bool
	// Problem says why the desk cannot use the context; "" when it can.
	Problem string
}

// Usable reports whether the context can be added to a session.
func (c Context) Usable() bool { return c.Problem == "" }

// defaultDescription is what the Docker CLI says of the implicit context.
const defaultDescription = "Current DOCKER_HOST based configuration"

// ListContexts returns every context in the Docker configuration store
// ($DOCKER_CONFIG, or ~/.docker) and the implicit default context, sorted by
// name as `docker context ls` sorts them. A context whose metadata cannot be
// read, or whose endpoint the desk cannot use, is listed with the reason
// rather than left out: the operator sees that it exists and why it cannot
// be added. (The CLI refuses to list anything when one entry is malformed.)
//
// Each endpoint is resolved the way Resolve resolves a context named with
// --context: the same metadata, the same TLS material.
func ListContexts(env Env, homeDir string) []Context {
	cfgDir := ConfigDir(env, homeDir)
	envHost := strings.TrimSpace(env("DOCKER_HOST"))

	// Which one would the CLI use? Resolve answers that with the CLI's own
	// precedence; a failure only means that no entry is starred.
	current := ""
	if ep, err := resolve(Selection{}, env, homeDir); err == nil {
		current = ep.Context
	}

	def := Context{Name: DefaultContext, Description: defaultDescription}
	if ep, err := defaultEndpoint("", envHost, env, cfgDir); err != nil {
		def.Problem = err.Error()
	} else {
		ep.Source = "the daemon picker (the default context)"
		def.Endpoint = ep
	}
	out := []Context{def}

	if cfgDir != "" {
		dir := filepath.Join(cfgDir, "contexts", "meta")
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			out = append(out, Context{Name: "(" + dir + ")", Problem: "the context store cannot be read: " + err.Error()})
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			out = append(out, storedContext(cfgDir, filepath.Join(dir, e.Name(), "meta.json")))
		}
	}

	for i := range out {
		c := &out[i]
		c.Current = c.Name == current
		c.Endpoint.Context = c.Name
		if c.Problem != "" {
			continue
		}
		if safe(c.Endpoint.Host) != c.Endpoint.Host || safe(c.Name) != c.Name {
			c.Problem = "its name or endpoint contains control characters"
			c.Endpoint.Host = safe(c.Endpoint.Host)
			continue
		}
		if _, err := c.Endpoint.Target(); err != nil {
			c.Problem = err.Error()
		}
	}
	for i := range out {
		// Nothing printed may carry a control sequence (see Resolve).
		out[i].Name = safe(out[i].Name)
		out[i].Description = safe(out[i].Description)
		out[i].Problem = safe(out[i].Problem)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// storedContext reads one entry of the store.
func storedContext(cfgDir, metaPath string) Context {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return Context{Name: "(" + filepath.Base(filepath.Dir(metaPath)) + ")", Problem: "its metadata cannot be read: " + err.Error()}
	}
	var meta struct {
		Name     string `json:"Name"`
		Metadata struct {
			Description string `json:"Description"`
		} `json:"Metadata"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return Context{Name: "(" + filepath.Base(filepath.Dir(metaPath)) + ")", Problem: fmt.Sprintf("its metadata %s cannot be parsed: %v", metaPath, err)}
	}
	c := Context{Name: meta.Name, Description: meta.Metadata.Description}
	if c.Name == "" {
		c.Name = "(" + filepath.Base(filepath.Dir(metaPath)) + ")"
		c.Problem = "its metadata names no context"
		return c
	}
	// The store files a context under the hash of its name; one filed
	// elsewhere is not what the CLI would find under that name.
	ep, err := contextEndpoint(cfgDir, c.Name)
	if err != nil {
		c.Problem = err.Error()
		return c
	}
	ep.Source = "the daemon picker (context " + c.Name + ")"
	c.Endpoint = ep
	return c
}
