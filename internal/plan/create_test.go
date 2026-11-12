package plan

import (
	"errors"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/engine"
)

func TestParsePort(t *testing.T) {
	for in, want := range map[string]PortSpec{
		"80":                  {ContainerPort: 80, Proto: "tcp"},
		"8080:80":             {HostPort: 8080, ContainerPort: 80, Proto: "tcp"},
		"127.0.0.1:5432:5432": {HostIP: "127.0.0.1", HostPort: 5432, ContainerPort: 5432, Proto: "tcp"},
		"53:53/udp":           {HostPort: 53, ContainerPort: 53, Proto: "udp"},
		"53:53/UDP":           {HostPort: 53, ContainerPort: 53, Proto: "udp"},
		"8443:443/tcp":        {HostPort: 8443, ContainerPort: 443, Proto: "tcp"},
		"[::1]:8080:80":       {HostIP: "::1", HostPort: 8080, ContainerPort: 80, Proto: "tcp"},
		"[::1]::80":           {HostIP: "::1", ContainerPort: 80, Proto: "tcp"},
		"0.0.0.0::80":         {HostIP: "0.0.0.0", ContainerPort: 80, Proto: "tcp"},
		" 1:65535 ":           {HostPort: 1, ContainerPort: 65535, Proto: "tcp"},
	} {
		got, err := ParsePort(in)
		if err != nil || got != want {
			t.Errorf("ParsePort(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for in, says := range map[string]string{
		"70000:80":      "between 1 and 65535",
		"8080:0":        "between 1 and 65535",
		"8080:80/sctp":  "tcp or udp",
		"::1:8080:80":   "in brackets",
		"[::1:8080:80":  "closing bracket",
		"[::1]8080:80":  "after the address",
		"300.1.1.1:1:1": "not an IP address",
		"http:80":       "not a number",
		":80":           "host port is missing",
		"":              "a port entry is",
	} {
		_, err := ParsePort(in)
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("ParsePort(%q) = %v; want an error saying %q", in, err, says)
		}
	}
}

func TestParseEnv(t *testing.T) {
	for in, want := range map[string]string{
		"LOG_LEVEL=info":       "LOG_LEVEL=info",
		"EMPTY=":               "EMPTY=",
		"URL=a=b=c":            "URL=a=b=c",
		"SPACED=  kept  as is": "SPACED=  kept  as is",
	} {
		if got, err := ParseEnv(in); err != nil || got != want {
			t.Errorf("ParseEnv(%q) = %q, %v", in, got, err)
		}
	}
	for in, says := range map[string]string{
		"=value":     "needs a name",
		"NOEQUALS":   "NAME=value",
		"BAD NAME=x": "whitespace",
	} {
		if _, err := ParseEnv(in); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("ParseEnv(%q) = %v; want an error saying %q", in, err, says)
		}
	}
	// A malformed entry is quoted only by its beginning.
	if _, err := ParseEnv("thisisaverylongsecretvalue"); err == nil || strings.Contains(err.Error(), "secretvalue") {
		t.Errorf("a malformed entry is echoed in full: %v", err)
	}
}

func TestParseMount(t *testing.T) {
	for in, want := range map[string]MountSpec{
		"/srv/data:/data:ro":  {Type: "bind", Source: "/srv/data", Destination: "/data", ReadOnly: true},
		"cache:/var/cache":    {Type: "volume", Source: "cache", Destination: "/var/cache"},
		"cache:/var/cache:rw": {Type: "volume", Source: "cache", Destination: "/var/cache"},
	} {
		if got, err := ParseMount(in); err != nil || got != want {
			t.Errorf("ParseMount(%q) = %+v, %v", in, got, err)
		}
	}
	for in, says := range map[string]string{
		"/srv/data:data": "absolute path",
		"/srv/data":      "source:destination",
		"a:/b:ro:extra":  "source:destination",
		":/data":         "source",
		"/x:/y:rx":       "ro or rw",
		"-bad:/data":     "start with a letter or digit",
	} {
		if _, err := ParseMount(in); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("ParseMount(%q) = %v; want an error saying %q", in, err, says)
		}
	}
}

func TestParseRestart(t *testing.T) {
	for in, want := range map[string]RestartSpec{
		"":               {Name: "no"},
		"no":             {Name: "no"},
		"always":         {Name: "always"},
		"unless-stopped": {Name: "unless-stopped"},
		"on-failure":     {Name: "on-failure"},
		"on-failure:3":   {Name: "on-failure", MaximumRetryCount: 3},
	} {
		if got, err := ParseRestart(in); err != nil || got != want {
			t.Errorf("ParseRestart(%q) = %+v, %v", in, got, err)
		}
	}
	for in, says := range map[string]string{
		"sometimes":     "not a restart policy",
		"always:3":      "only on-failure",
		"on-failure:x":  "whole number",
		"on-failure:-1": "whole number",
	} {
		if _, err := ParseRestart(in); err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("ParseRestart(%q) = %v; want an error saying %q", in, err, says)
		}
	}
}

func TestParseCreateNamesTheField(t *testing.T) {
	base := CreateInput{Image: "alpine"}
	for name, tc := range map[string]struct {
		in    func(*CreateInput)
		field Field
		entry int
	}{
		"image":   {func(in *CreateInput) { in.Image = "" }, FieldImage, -1},
		"port":    {func(in *CreateInput) { in.Ports = []string{"8080:80", "", "70000:80"} }, FieldPorts, 2},
		"env":     {func(in *CreateInput) { in.Env = []string{"=value"} }, FieldEnv, 0},
		"mount":   {func(in *CreateInput) { in.Mounts = []string{"/srv/data:data"} }, FieldMounts, 0},
		"restart": {func(in *CreateInput) { in.Restart = "on-failure:x" }, FieldRestart, -1},
		"command": {func(in *CreateInput) { in.Command = "sh -c 'oops" }, FieldCommand, -1},
	} {
		in := base
		tc.in(&in)
		_, err := ParseCreate(in)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != tc.field || fe.Entry != tc.entry {
			t.Errorf("%s: %v (%+v); want field %d entry %d", name, err, fe, tc.field, tc.entry)
		}
	}
	spec, err := ParseCreate(CreateInput{Image: "alpine", Ports: []string{"", " "}, Env: []string{""}, Command: "  "})
	if err != nil || spec.Image != "alpine:latest" || spec.Ports != nil || spec.Env != nil || spec.Command != nil || spec.Restart.Name != "no" {
		t.Fatalf("blank entries are ignored and the defaults apply: %+v %v", spec, err)
	}
}

func createSnap() Snapshot {
	return Snapshot{
		Containers: []engine.Container{{ID: "c0ffee0000000000aaaa", Name: "web", State: "running"}},
		Images:     []engine.Image{{ID: "sha256:abc", RepoTags: []string{"nginx:1.27"}}},
	}
}

func TestContainerCreatePullsOnlyWhenMissing(t *testing.T) {
	// Present: create, then start the created ID.
	p, err := ContainerCreate(createSnap(), ContainerCreateSpec{Image: "nginx:1.27", Name: "web2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 2 || p.Steps[0].Op != OpCreateContainer || p.Steps[1].Op != OpStart {
		t.Fatalf("steps: %+v", p.Steps)
	}
	if !p.Steps[0].ProducesID || len(p.Steps[0].DependsOn) != 0 || p.Steps[0].IDFrom != -1 {
		t.Errorf("create step: %+v", p.Steps[0])
	}
	if p.Steps[1].IDFrom != 0 || len(p.Steps[1].DependsOn) != 1 || p.Steps[1].DependsOn[0] != 0 || p.Steps[1].ID != "" {
		t.Errorf("start step must take the create's ID and depend on it: %+v", p.Steps[1])
	}
	if !p.Runnable() || p.Tier != TierForm {
		t.Errorf("a form plan that runs: %+v", p)
	}

	// Missing, without a tag: pull exactly alpine:latest first.
	p, err = ContainerCreate(createSnap(), ContainerCreateSpec{Image: "alpine"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 3 || p.Steps[0].Op != OpPullImage || p.Steps[0].ID != "alpine:latest" {
		t.Fatalf("steps: %+v", p.Steps)
	}
	if p.Steps[1].Create.Image != "alpine:latest" || p.Steps[1].DependsOn[0] != 0 {
		t.Errorf("create: %+v", p.Steps[1])
	}
	if p.Steps[2].IDFrom != 1 || p.Steps[2].DependsOn[0] != 1 {
		t.Errorf("start: %+v", p.Steps[2])
	}
	if p.JobTarget != "alpine:latest" || !strings.Contains(p.Steps[1].Name, "daemon") {
		t.Errorf("no name: the job is labelled by the image and the step says the daemon names it: %q %q", p.JobTarget, p.Steps[1].Name)
	}
}

func TestContainerCreateRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		spec  ContainerCreateSpec
		field Field
		says  string
	}{
		"bad image":            {ContainerCreateSpec{Image: "Not Valid"}, FieldImage, "spaces"},
		"name taken":           {ContainerCreateSpec{Image: "alpine", Name: "web"}, FieldName, "taken"},
		"name invalid":         {ContainerCreateSpec{Image: "alpine", Name: "x"}, FieldName, "at least 2"},
		"name chars":           {ContainerCreateSpec{Image: "alpine", Name: "a/b"}, FieldName, "may only contain"},
		"repeated port":        {ContainerCreateSpec{Image: "alpine", Ports: []PortSpec{{HostPort: 80, ContainerPort: 80, Proto: "tcp"}, {HostPort: 80, ContainerPort: 81, Proto: "tcp"}}}, FieldPorts, "published twice"},
		"bad env":              {ContainerCreateSpec{Image: "alpine", Env: []string{"=x"}}, FieldEnv, "needs a name"},
		"relative dest":        {ContainerCreateSpec{Image: "alpine", Mounts: []MountSpec{{Type: "bind", Source: "/a", Destination: "a"}}}, FieldMounts, "absolute"},
		"retry not on-failure": {ContainerCreateSpec{Image: "alpine", Restart: RestartSpec{Name: "always", MaximumRetryCount: 2}}, FieldRestart, "only on-failure"},
	} {
		_, err := ContainerCreate(createSnap(), tc.spec)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != tc.field || !strings.Contains(fe.Msg, tc.says) {
			t.Errorf("%s: %v; want field %d saying %q", name, err, tc.field, tc.says)
		}
	}
	// The same host port on different protocols or addresses is fine.
	_, err := ContainerCreate(createSnap(), ContainerCreateSpec{Image: "alpine", Ports: []PortSpec{
		{HostPort: 53, ContainerPort: 53, Proto: "tcp"},
		{HostPort: 53, ContainerPort: 53, Proto: "udp"},
		{HostIP: "127.0.0.1", HostPort: 53, ContainerPort: 54, Proto: "tcp"},
	}})
	if err != nil {
		t.Errorf("distinct bindings refused: %v", err)
	}
	// 0.0.0.0 and "every address" are the same address.
	_, err = ContainerCreate(createSnap(), ContainerCreateSpec{Image: "alpine", Ports: []PortSpec{
		{HostPort: 80, ContainerPort: 80, Proto: "tcp"},
		{HostIP: "0.0.0.0", HostPort: 80, ContainerPort: 81, Proto: "tcp"},
	}})
	if err == nil {
		t.Error("0.0.0.0:80 repeats :80")
	}
}

func TestCreateNeverDescribesTheEnvironment(t *testing.T) {
	spec := ContainerCreateSpec{Image: "alpine", Name: "app", Env: []string{"TOKEN=s3cret"}, Command: []string{"echo", "s3cret-arg"}}
	p, err := ContainerCreate(createSnap(), spec)
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	all = append(all, p.Title, p.JobTitle, p.JobTarget, p.Target.Name)
	all = append(all, p.Effects...)
	for _, st := range p.Steps {
		all = append(all, describe(st), st.Name, st.Detail)
	}
	for _, s := range all {
		if strings.Contains(s, "s3cret") || strings.Contains(s, "TOKEN") {
			t.Errorf("an environment value or name leaked into %q", s)
		}
	}
	if got := describe(p.Steps[1]); got != "create container app from alpine:latest" {
		t.Errorf("describe = %q", got)
	}
}

func TestCreateOptionsMapping(t *testing.T) {
	o := CreateOptions(ContainerCreateSpec{
		Image: "alpine:latest", Name: "n",
		Ports:   []PortSpec{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Proto: "tcp"}},
		Env:     []string{"A=1"},
		Mounts:  []MountSpec{{Type: "volume", Source: "v1", Destination: "/v", ReadOnly: true}},
		Restart: RestartSpec{Name: "on-failure", MaximumRetryCount: 3},
		Command: []string{"sh", "-c", "true"},
	})
	if o.Image != "alpine:latest" || o.Name != "n" || len(o.Ports) != 1 || o.Ports[0].HostPort != 8080 ||
		o.Mounts[0].Target != "/v" || !o.Mounts[0].ReadOnly || o.Restart.MaximumRetryCount != 3 || len(o.Cmd) != 3 || o.Env[0] != "A=1" {
		t.Fatalf("%+v", o)
	}
	if o := CreateOptions(ContainerCreateSpec{Image: "alpine:latest"}); o.Cmd != nil || o.Env != nil {
		t.Fatalf("an empty command keeps the image's: %+v", o)
	}
}
