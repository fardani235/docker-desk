package dockerctx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// listFixture is a configuration directory with one good context, one that
// cannot be used and one that reaches another machine over ssh.
func listFixture(t *testing.T) string {
	t.Helper()
	cfg := t.TempDir()
	writeContext(t, cfg, "good", "unix:///run/user/1000/docker.sock")
	writeContext(t, cfg, "pipe", "npipe:////./pipe/docker_engine")
	writeContext(t, cfg, "prod", "ssh://ops@prod-1:2222")
	writeConfig(t, cfg, `{"currentContext":"good"}`)
	return cfg
}

func byName(cs []Context) map[string]Context {
	out := map[string]Context{}
	for _, c := range cs {
		out[c.Name] = c
	}
	return out
}

func TestListContexts(t *testing.T) {
	cfg := listFixture(t)
	got := ListContexts(envOf(map[string]string{"DOCKER_CONFIG": cfg}), "/nonexistent")

	var names []string
	for _, c := range got {
		names = append(names, c.Name)
	}
	if strings.Join(names, " ") != "default good pipe prod" {
		t.Fatalf("names %v, want the default context and the three stored ones, by name", names)
	}
	m := byName(got)

	if c := m["good"]; !c.Usable() || c.Endpoint.Host != "unix:///run/user/1000/docker.sock" || c.Endpoint.Context != "good" || !c.Current {
		t.Errorf("good: %+v", c)
	}
	if c := m["prod"]; !c.Usable() || c.Endpoint.Host != "ssh://ops@prod-1:2222" || c.Current {
		t.Errorf("prod: %+v", c)
	} else if l := c.Endpoint.Link(); l.Transport != TransportSSH || !l.Remote {
		t.Errorf("prod is not described as remote over ssh: %+v", l)
	}
	if c := m["pipe"]; c.Usable() || !strings.Contains(c.Problem, "unsupported endpoint scheme") || c.Endpoint.Host != "npipe:////./pipe/docker_engine" {
		t.Errorf("pipe should be unusable, with the reason and the endpoint shown: %+v", c)
	}
	if c := m["default"]; !c.Usable() || c.Endpoint.Host != DefaultHost || c.Current {
		t.Errorf("default: %+v", c)
	}
}

func TestListContextsUnreadableEntries(t *testing.T) {
	cfg := listFixture(t)
	// Malformed metadata, and metadata that names no context: listed, with
	// the reason, instead of hiding the rest as the CLI does.
	for name, body := range map[string]string{"broken": `{"Name":"broken","Endpoints":`, "anon": `{"Endpoints":{}}`} {
		dir := filepath.Join(cfg, "contexts", "meta", contextID(name))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A context file is not always the operator's own writing.
	writeContext(t, cfg, "evil", `unix:///x\u001b[2J.sock`)

	got := ListContexts(envOf(map[string]string{"DOCKER_CONFIG": cfg}), "")
	unusable := 0
	for _, c := range got {
		if !c.Usable() {
			unusable++
		}
		for _, s := range []string{c.Name, c.Endpoint.Host, c.Problem, c.Description} {
			if safe(s) != s {
				t.Errorf("a control character survives in %q", s)
			}
		}
	}
	if unusable != 4 { // pipe, broken, anon, evil
		t.Fatalf("%d unusable entries, want 4: %+v", unusable, got)
	}
	if c := byName(got)["good"]; !c.Usable() {
		t.Fatalf("one bad entry spoiled a good one: %+v", c)
	}
}

func TestListContextsDefaultFollowsDockerHost(t *testing.T) {
	cfg := listFixture(t)
	got := byName(ListContexts(envOf(map[string]string{"DOCKER_CONFIG": cfg, "DOCKER_HOST": "tcp://10.0.0.5:2375"}), ""))
	if c := got["default"]; c.Endpoint.Host != "tcp://10.0.0.5:2375" || !c.Current {
		t.Fatalf("with DOCKER_HOST set the default context is that host, and current: %+v", c)
	}
	if got["good"].Current {
		t.Fatal("DOCKER_HOST overrides the configured context")
	}
}

// TestListContextsMatchesDockerCLI compares the lister with `docker context
// ls` on the same configuration directory: the same names, the same
// endpoints, the same current one. The CLI reads the store only; no daemon is
// contacted. Skipped where the Docker CLI is not installed.
func TestListContextsMatchesDockerCLI(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("the Docker CLI is not installed")
	}
	cfg := listFixture(t)
	writeContextMeta(t, cfg, "tls", "tcp://10.0.0.5:2376", true)
	home := t.TempDir()

	for _, env := range []map[string]string{
		{"DOCKER_CONFIG": cfg, "HOME": home},
		{"DOCKER_CONFIG": cfg, "HOME": home, "DOCKER_HOST": "tcp://10.9.9.9:2375"},
		{"DOCKER_CONFIG": cfg, "HOME": home, "DOCKER_CONTEXT": "prod"},
	} {
		cmd := exec.Command(docker, "context", "ls", "--format", "json")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("docker context ls: %v\n%s", err, stderr.String())
		}
		type row struct {
			Name           string
			DockerEndpoint string
			Current        bool
			Description    string
		}
		var cli []row
		sc := bufio.NewScanner(bytes.NewReader(out))
		for sc.Scan() {
			var r row
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				t.Fatalf("parsing %q: %v", sc.Text(), err)
			}
			cli = append(cli, r)
		}

		got := ListContexts(envOf(env), home)
		if len(got) != len(cli) {
			t.Fatalf("env %v: %d contexts, the CLI lists %d:\n%s", env, len(got), len(cli), out)
		}
		for i, r := range cli {
			c := got[i]
			if c.Name != r.Name || c.Endpoint.Host != r.DockerEndpoint || c.Current != r.Current || c.Description != r.Description {
				t.Errorf("env %v, entry %d:\n  desk %q %q current=%v %q\n  cli  %q %q current=%v %q",
					env, i, c.Name, c.Endpoint.Host, c.Current, c.Description, r.Name, r.DockerEndpoint, r.Current, r.Description)
			}
		}
	}
}
