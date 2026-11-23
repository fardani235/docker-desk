package plan

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
)

const (
	recID     = "3f9c1a2b4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8"
	recOldImg = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	recNewImg = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	sentinel  = "s3cret-sentinel-value"
)

// inspectDoc is a container's inspect document as the daemon writes it, with
// the daemon's own defaults filled in (runc, private namespaces, 64 MiB of
// shared memory, json-file, the default masked paths) and the old image's
// configuration merged in, so that a test sees what a real daemon answers.
type inspectDoc struct {
	top, cfg, host map[string]any
	nets           map[string]any
	mounts         []any
}

func newDoc() *inspectDoc {
	d := &inspectDoc{
		cfg: map[string]any{
			"Hostname": recID[:12], "Image": "shop/web:1", "Tty": false, "OpenStdin": false,
			"AttachStdout": true, "AttachStderr": true,
			"Env":          []string{"PATH=/usr/bin:/bin", "APP_VERSION=1.0"},
			"Cmd":          []string{"serve"},
			"Entrypoint":   []string{"/entry.sh"},
			"WorkingDir":   "/app",
			"Labels":       map[string]string{"org.opencontainers.image.version": "1.0"},
			"ExposedPorts": map[string]any{"8000/tcp": map[string]any{}},
			"StopSignal":   "SIGQUIT",
			"Volumes":      map[string]any{"/cache": map[string]any{}},
		},
		host: map[string]any{
			"NetworkMode": "bridge", "RestartPolicy": map[string]any{"Name": "no", "MaximumRetryCount": 0},
			"LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{}},
			"Runtime":   "runc", "IpcMode": "private", "CgroupnsMode": "private", "ShmSize": 67108864,
			"ConsoleSize": []int{0, 0}, "MaskedPaths": []string{"/proc/kcore"}, "ReadonlyPaths": []string{"/proc/sys"},
			"Binds": []string{}, "Privileged": false,
		},
		nets: map[string]any{"bridge": map[string]any{"IPAMConfig": nil, "Aliases": nil, "NetworkID": "n-bridge", "IPAddress": "172.17.0.2"}},
	}
	d.top = map[string]any{"Id": recID, "Name": "/web", "Image": recOldImg}
	return d
}

func (d *inspectDoc) raw() []byte {
	top := map[string]any{}
	for k, v := range d.top {
		top[k] = v
	}
	top["Config"], top["HostConfig"] = d.cfg, d.host
	top["Mounts"] = d.mounts
	if top["Mounts"] == nil {
		top["Mounts"] = []any{}
	}
	top["NetworkSettings"] = map[string]any{"Networks": d.nets}
	b, err := json.Marshal(top)
	if err != nil {
		panic(err)
	}
	return b
}

func (d *inspectDoc) detail() engine.ContainerDetail {
	var cd engine.ContainerDetail
	cd.ID, cd.Name, cd.ImageID, cd.Image, cd.State = recID, "web", recOldImg, "shop/web:1", engine.StateRunning
	cd.Raw = d.raw()
	return cd
}

// oldImage is the running image: its environment, command, entrypoint,
// labels, working directory, stop signal and exposed port are what the
// container's document repeats.
func oldImage() engine.ImageDetail {
	raw, _ := json.Marshal(map[string]any{
		"Id": recOldImg,
		"Config": map[string]any{
			"Env": []string{"PATH=/usr/bin:/bin", "APP_VERSION=1.0"}, "Cmd": []string{"serve"},
			"Entrypoint": []string{"/entry.sh"}, "WorkingDir": "/app",
			"Labels":       map[string]string{"org.opencontainers.image.version": "1.0"},
			"ExposedPorts": map[string]any{"8000/tcp": map[string]any{}}, "StopSignal": "SIGQUIT",
			"Volumes": map[string]any{"/cache": map[string]any{}},
		},
	})
	var d engine.ImageDetail
	d.ID, d.Raw = recOldImg, raw
	return d
}

func clone(t *testing.T, d *inspectDoc) (CloneSpec, []string) {
	t.Helper()
	spec, refused, err := Clone(d.detail(), oldImage(), CloneDefaults{})
	if err != nil {
		t.Fatal(err)
	}
	return spec, refused
}

func TestCloneLeavesTheOldImagesDefaultsToTheNewImage(t *testing.T) {
	spec, refused := clone(t, newDoc())
	if len(refused) != 0 {
		t.Fatalf("a plain container is refused: %v", refused)
	}
	if spec.Env != nil || spec.Cmd != nil || spec.Entrypoint != nil || spec.WorkingDir != "" || spec.StopSignal != "" ||
		spec.Labels != nil || spec.ExposedOnly != nil || spec.Hostname != "" {
		t.Errorf("the old image's settings were frozen onto the new image: %+v", spec)
	}
	from := strings.Join(spec.FromImage, " | ")
	for _, w := range []string{"2 environment variables (PATH, APP_VERSION)", "command", "entrypoint", "working directory", "stop signal", "1 label"} {
		if !strings.Contains(from, w) {
			t.Errorf("FromImage %q does not say %q", from, w)
		}
	}
	if spec.Image != "shop/web:1" || spec.Name != "web" || spec.Restart.Name != "no" || spec.NetworkMode != "bridge" {
		t.Errorf("identity: %+v", spec)
	}
	if len(spec.Networks) != 1 || spec.Networks[0].Network != "bridge" {
		t.Errorf("networks: %+v", spec.Networks)
	}
}

func TestCloneCarriesTheContainersOwnSettings(t *testing.T) {
	d := newDoc()
	d.cfg["Entrypoint"] = []string{"/bin/sh", "-c"}
	d.cfg["Cmd"] = []string{"exec web --port 8000"}
	d.cfg["WorkingDir"] = "/srv"
	d.cfg["User"] = "1000:1000"
	d.cfg["Hostname"] = "web-host"
	d.cfg["Tty"], d.cfg["OpenStdin"] = true, true
	d.host["Init"] = true
	d.cfg["Env"] = []string{"LOG_LEVEL=info", "TOKEN=" + sentinel, "PATH=/usr/bin:/bin", "APP_VERSION=1.0"}
	d.cfg["Labels"] = map[string]string{
		"org.opencontainers.image.version": "1.0",
		engine.LabelComposeProject:         "shop", engine.LabelComposeService: "web",
		"com.docker.compose.config-hash": "abc",
	}
	d.cfg["Healthcheck"] = map[string]any{"Test": []string{"CMD", "curl", "-f", "http://localhost/"}, "Interval": int64(30 * time.Second), "Timeout": int64(5 * time.Second), "StartPeriod": int64(10 * time.Second), "Retries": 3}
	d.cfg["StopSignal"] = "SIGTERM"
	d.cfg["StopTimeout"] = 30
	spec, refused := clone(t, d)
	if len(refused) != 0 {
		t.Fatalf("refused: %v", refused)
	}
	init := true
	timeout := 30
	want := CloneSpec{
		Image: "shop/web:1", Name: "web",
		Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{"exec web --port 8000"},
		Env:        []string{"LOG_LEVEL=info", "TOKEN=" + sentinel},
		WorkingDir: "/srv", User: "1000:1000", Hostname: "web-host", Tty: true, OpenStdin: true, Init: &init,
		Labels:      map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: "web", "com.docker.compose.config-hash": "abc"},
		Healthcheck: &engine.HealthConfig{Test: []string{"CMD", "curl", "-f", "http://localhost/"}, Interval: 30 * time.Second, Timeout: 5 * time.Second, StartPeriod: 10 * time.Second, Retries: 3},
		StopSignal:  "SIGTERM", StopTimeout: &timeout,
	}
	check := func(name string, got, want any) {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %#v, want %#v", name, got, want)
		}
	}
	check("entrypoint", spec.Entrypoint, want.Entrypoint)
	check("command", spec.Cmd, want.Cmd)
	check("working directory", spec.WorkingDir, want.WorkingDir)
	check("user", spec.User, want.User)
	check("hostname", spec.Hostname, want.Hostname)
	check("tty", spec.Tty, want.Tty)
	check("open stdin", spec.OpenStdin, want.OpenStdin)
	check("init", spec.Init, want.Init)
	check("labels", spec.Labels, want.Labels)
	check("health check", spec.Healthcheck, want.Healthcheck)
	check("stop signal", spec.StopSignal, want.StopSignal)
	check("stop timeout", spec.StopTimeout, want.StopTimeout)
	check("environment", spec.Env, want.Env) // every NAME=value pair, values included
	if got := spec.EnvNames(); !reflect.DeepEqual(got, []string{"LOG_LEVEL", "TOKEN"}) {
		t.Errorf("names %v", got)
	}
}

func TestCloneEntrypointAndCommandRules(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ep, cmd []string
		wantEP  []string
		wantCmd []string
		fromCmd bool
		fromEP  bool
	}{
		{"both the image's", []string{"/entry.sh"}, []string{"serve"}, nil, nil, true, true},
		{"command overridden", []string{"/entry.sh"}, []string{"migrate"}, nil, []string{"migrate"}, false, true},
		{"entrypoint given equal, no command", []string{"/entry.sh"}, nil, []string{"/entry.sh"}, nil, false, false},
		{"entrypoint overridden", []string{"/bin/other"}, []string{"serve"}, []string{"/bin/other"}, []string{"serve"}, false, false},
		{"entrypoint cleared", nil, []string{"sleep", "1"}, []string{""}, []string{"sleep", "1"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDoc()
			d.cfg["Entrypoint"], d.cfg["Cmd"] = tc.ep, tc.cmd
			spec, _ := clone(t, d)
			if !reflect.DeepEqual(spec.Entrypoint, tc.wantEP) || !reflect.DeepEqual(spec.Cmd, tc.wantCmd) {
				t.Errorf("got entrypoint %q command %q", spec.Entrypoint, spec.Cmd)
			}
			from := strings.Join(spec.FromImage, ",")
			if strings.Contains(from, "command") != tc.fromCmd || strings.Contains(from, "entrypoint") != tc.fromEP {
				t.Errorf("FromImage %q", from)
			}
		})
	}
}

func TestCloneHealthCheckKeepsOnlyWhatDiffers(t *testing.T) {
	d := newDoc()
	img := oldImage()
	var ic map[string]map[string]any
	_ = json.Unmarshal(img.Raw, &ic)
	ic["Config"]["Healthcheck"] = map[string]any{"Test": []string{"CMD", "probe"}, "Interval": int64(time.Minute)}
	img.Raw, _ = json.Marshal(ic)
	// The daemon stores the image's test merged with the container's interval.
	d.cfg["Healthcheck"] = map[string]any{"Test": []string{"CMD", "probe"}, "Interval": int64(10 * time.Second)}
	spec, _, err := Clone(d.detail(), img, CloneDefaults{})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Healthcheck == nil || spec.Healthcheck.Test != nil || spec.Healthcheck.Interval != 10*time.Second {
		t.Errorf("health check %+v: the test is the image's, the interval the container's", spec.Healthcheck)
	}
	d.cfg["Healthcheck"] = map[string]any{"Test": []string{"CMD", "probe"}, "Interval": int64(time.Minute)}
	spec, _, _ = Clone(d.detail(), img, CloneDefaults{})
	if spec.Healthcheck != nil || !strings.Contains(strings.Join(spec.FromImage, ","), "health check") {
		t.Errorf("the image's own health check is carried: %+v %v", spec.Healthcheck, spec.FromImage)
	}
}

func TestClonePortsAndStorage(t *testing.T) {
	d := newDoc()
	d.cfg["ExposedPorts"] = map[string]any{"8000/tcp": map[string]any{}, "80/tcp": map[string]any{}, "9000/tcp": map[string]any{}, "53/udp": map[string]any{}, "9100/tcp": map[string]any{}}
	d.host["PortBindings"] = map[string]any{
		"80/tcp":   []any{map[string]any{"HostIp": "", "HostPort": "8080"}},
		"53/udp":   []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": "5353"}},
		"9100/tcp": []any{map[string]any{"HostIp": "", "HostPort": ""}},
	}
	d.mounts = []any{
		map[string]any{"Type": "bind", "Source": "/srv/conf", "Destination": "/etc/web", "Mode": "ro", "RW": false, "Propagation": "rprivate"},
		map[string]any{"Type": "volume", "Name": "web-data", "Source": "/var/lib/docker/volumes/web-data/_data", "Destination": "/data", "Driver": "local", "Mode": "z", "RW": true},
		map[string]any{"Type": "volume", "Name": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "Source": "/var/lib/docker/volumes/0123/_data", "Destination": "/cache", "Driver": "local", "Mode": "", "RW": true},
		map[string]any{"Type": "volume", "Name": "cache-data", "Source": "/var/lib/docker/volumes/cache-data/_data", "Destination": "/cache-ro", "Driver": "local", "Mode": "ro,z", "RW": false},
	}
	spec, refused := clone(t, d)
	if len(refused) != 0 {
		t.Fatalf("refused: %v", refused)
	}
	wantPorts := []PortSpec{
		{HostIP: "127.0.0.1", HostPort: 5353, ContainerPort: 53, Proto: "udp"},
		{HostPort: 8080, ContainerPort: 80, Proto: "tcp"},
		{ContainerPort: 9100, Proto: "tcp"}, // published to a port the daemon picks
	}
	if !reflect.DeepEqual(spec.Ports, wantPorts) {
		t.Errorf("ports %+v", spec.Ports)
	}
	// 9000 is exposed without a binding: exposed only, not published. 8000
	// is the image's own.
	if !reflect.DeepEqual(spec.ExposedOnly, []string{"9000/tcp"}) {
		t.Errorf("exposed only %v", spec.ExposedOnly)
	}
	for _, p := range spec.Ports {
		if p.ContainerPort == 9000 {
			t.Error("an exposed-only port became published")
		}
	}
	wantMounts := []MountSpec{
		{Type: "bind", Source: "/srv/conf", Destination: "/etc/web", ReadOnly: true},
		{Type: "volume", Source: "web-data", Destination: "/data"},
		// The anonymous volume keeps the daemon's name: its data stays.
		{Type: "volume", Source: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Destination: "/cache"},
		{Type: "volume", Source: "cache-data", Destination: "/cache-ro", ReadOnly: true},
	}
	if !reflect.DeepEqual(spec.Mounts, wantMounts) {
		t.Errorf("mounts %+v", spec.Mounts)
	}
}

func TestCloneNetworks(t *testing.T) {
	d := newDoc()
	d.host["NetworkMode"] = "shop_default"
	d.nets = map[string]any{
		"shop_default": map[string]any{"Aliases": []string{"web", "shop-web-1", recID[:12]}, "DNSNames": []string{"web", "shop-web-1", recID[:12]}},
		"shop_backend": map[string]any{"Aliases": []string{"api"}, "IPAMConfig": map[string]any{}},
	}
	spec, refused := clone(t, d)
	if len(refused) != 0 {
		t.Fatalf("refused: %v", refused)
	}
	want := []engine.NetworkAttachment{
		{Network: "shop_backend", Aliases: []string{"api"}},
		{Network: "shop_default", Aliases: []string{"web", "shop-web-1"}}, // not the old short ID
	}
	if spec.NetworkMode != "shop_default" || !reflect.DeepEqual(spec.Networks, want) {
		t.Errorf("mode %q networks %+v", spec.NetworkMode, spec.Networks)
	}
	for _, mode := range []string{"host", "none"} {
		d := newDoc()
		d.host["NetworkMode"] = mode
		d.nets = map[string]any{mode: map[string]any{}}
		spec, refused := clone(t, d)
		if len(refused) != 0 || spec.NetworkMode != mode || len(spec.Networks) != 0 || spec.Hostname != "" {
			t.Errorf("%s: %+v %v", mode, spec, refused)
		}
	}
	d = newDoc()
	d.host["NetworkMode"] = "container:" + strings.Repeat("a", 64)
	_, refused = clone(t, d)
	if len(refused) != 1 || !strings.HasPrefix(refused[0], FamNamespaces+": network namespace of another container") {
		t.Errorf("borrowed namespace: %v", refused)
	}
	d = newDoc()
	d.host["NetworkMode"] = "shop_default"
	d.nets = map[string]any{"shop_default": map[string]any{"IPAMConfig": map[string]any{"IPv4Address": "10.0.0.5"}}}
	_, refused = clone(t, d)
	if len(refused) != 1 || refused[0] != FamNetwork+": a fixed address on shop_default" {
		t.Errorf("fixed address: %v", refused)
	}
}

func TestCloneRestartAndLimits(t *testing.T) {
	pids := int64(200)
	full := map[string]any{
		"RestartPolicy": map[string]any{"Name": "on-failure", "MaximumRetryCount": 3},
		"Memory":        int64(512 << 20), "MemorySwap": int64(1 << 30), "MemoryReservation": int64(256 << 20),
		"NanoCpus": int64(1_500_000_000), "CpuShares": int64(512), "CpuQuota": int64(50000), "CpuPeriod": int64(100000),
		"CpusetCpus": "0-1", "PidsLimit": pids, "ShmSize": int64(128 << 20),
	}
	d := newDoc()
	for k, v := range full {
		d.host[k] = v
	}
	spec, refused := clone(t, d)
	if len(refused) != 0 {
		t.Fatalf("refused: %v", refused)
	}
	for _, tc := range []struct {
		name      string
		got, want any
	}{
		{"restart", spec.Restart, RestartSpec{Name: "on-failure", MaximumRetryCount: 3}},
		{"memory", spec.Resources.Memory, int64(512 << 20)},
		{"swap", spec.Resources.MemorySwap, int64(1 << 30)},
		{"reservation", spec.Resources.MemoryReservation, int64(256 << 20)},
		{"nano CPUs", spec.Resources.NanoCPUs, int64(1_500_000_000)},
		{"CPU shares", spec.Resources.CPUShares, int64(512)},
		{"CPU quota", spec.Resources.CPUQuota, int64(50000)},
		{"CPU period", spec.Resources.CPUPeriod, int64(100000)},
		{"cpuset", spec.Resources.CpusetCpus, "0-1"},
		{"pids", *spec.Resources.PidsLimit, pids},
		{"shm", spec.ShmSize, int64(128 << 20)},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	// Unset stays unset.
	spec, _ = clone(t, newDoc())
	if spec.Resources != (engine.ResourceLimits{}) || spec.Restart != (RestartSpec{Name: "no"}) {
		t.Errorf("unset limits were invented: %+v %+v", spec.Resources, spec.Restart)
	}
	d = newDoc()
	d.host["PidsLimit"] = nil
	d.host["RestartPolicy"] = map[string]any{"Name": ""}
	spec, _ = clone(t, d)
	if spec.Resources.PidsLimit != nil || spec.Restart.Name != "no" {
		t.Errorf("null pids limit %v, empty restart %q", spec.Resources.PidsLimit, spec.Restart.Name)
	}
}

func TestCloneDaemonDefaultsAreNotRefused(t *testing.T) {
	// What a real daemon reports for every container: these are not the
	// container's settings.
	d := newDoc()
	d.host["MemorySwappiness"] = nil
	d.host["OomKillDisable"] = false
	d.host["Ulimits"] = []any{}
	if _, refused := clone(t, d); len(refused) != 0 {
		t.Errorf("daemon defaults refused: %v", refused)
	}
	// A daemon whose default log driver is journald and runtime crun.
	d = newDoc()
	d.host["LogConfig"] = map[string]any{"Type": "journald", "Config": map[string]string{"tag": "{{.Name}}"}}
	d.host["Runtime"] = "crun"
	d.host["CgroupnsMode"] = "host"
	spec, refused, err := Clone(d.detail(), oldImage(), CloneDefaults{LoggingDriver: "journald", Runtime: "crun", CgroupVersion: "1"})
	if err != nil || len(refused) != 0 {
		t.Fatalf("%v %v", err, refused)
	}
	if spec.LogDriver != "journald" || spec.LogOptions["tag"] != "{{.Name}}" {
		t.Errorf("the default driver's options are carried: %q %v", spec.LogDriver, spec.LogOptions)
	}
}

// container returns a document with one setting outside the model.
func refusedBy(set func(d *inspectDoc)) *inspectDoc {
	d := newDoc()
	set(d)
	return d
}

func TestCloneRefusesEachFamily(t *testing.T) {
	for _, tc := range []struct {
		family, says string
		set          func(d *inspectDoc)
	}{
		{FamPrivilege, "runs privileged", func(d *inspectDoc) { d.host["Privileged"] = true }},
		{FamCapabilities, "adds NET_ADMIN", func(d *inspectDoc) { d.host["CapAdd"] = []string{"NET_ADMIN"} }},
		{FamCapabilities, "drops ALL", func(d *inspectDoc) { d.host["CapDrop"] = []string{"ALL"} }},
		{FamDevices, "/dev/fuse", func(d *inspectDoc) {
			d.host["Devices"] = []any{map[string]any{"PathOnHost": "/dev/fuse", "PathInContainer": "/dev/fuse", "CgroupPermissions": "rwm"}}
		}},
		{FamDevices, "device requests (nvidia)", func(d *inspectDoc) {
			d.host["DeviceRequests"] = []any{map[string]any{"Driver": "nvidia", "Count": -1}}
		}},
		{FamSecurity, "seccomp", func(d *inspectDoc) { d.host["SecurityOpt"] = []string{"seccomp=unconfined"} }},
		{FamRuntime, "runtime nvidia", func(d *inspectDoc) { d.host["Runtime"] = "nvidia" }},
		{FamKernel, "sysctls net.core.somaxconn", func(d *inspectDoc) { d.host["Sysctls"] = map[string]string{"net.core.somaxconn": "1024"} }},
		{FamKernel, "ulimits nofile", func(d *inspectDoc) { d.host["Ulimits"] = []any{map[string]any{"Name": "nofile", "Soft": 1, "Hard": 2}} }},
		{FamKernel, "OOM killer disabled", func(d *inspectDoc) { d.host["OomKillDisable"] = true }},
		{FamKernel, "memory swappiness 10", func(d *inspectDoc) { d.host["MemorySwappiness"] = 10 }},
		{FamTmpfs, "/run", func(d *inspectDoc) { d.host["Tmpfs"] = map[string]string{"/run": "rw"} }},
		{FamHost, "1 extra host entry", func(d *inspectDoc) { d.host["ExtraHosts"] = []string{"db:10.0.0.9"} }},
		{FamHost, "DNS servers 1.1.1.1", func(d *inspectDoc) { d.host["Dns"] = []string{"1.1.1.1"} }},
		{FamHost, "DNS search domains", func(d *inspectDoc) { d.host["DnsSearch"] = []string{"corp"} }},
		{FamHost, "DNS options", func(d *inspectDoc) { d.host["DnsOptions"] = []string{"ndots:1"} }},
		{FamHost, "additional groups audio", func(d *inspectDoc) { d.host["GroupAdd"] = []string{"audio"} }},
		{FamHost, "1 legacy link", func(d *inspectDoc) { d.host["Links"] = []string{"/db:/web/db"} }},
		{FamLogging, "log driver syslog", func(d *inspectDoc) { d.host["LogConfig"] = map[string]any{"Type": "syslog"} }},
		{FamRootfs, "read-only root filesystem", func(d *inspectDoc) { d.host["ReadonlyRootfs"] = true }},
		{FamRootfs, "removed automatically", func(d *inspectDoc) { d.host["AutoRemove"] = true }},
		{FamNamespaces, "PID namespace host", func(d *inspectDoc) { d.host["PidMode"] = "host" }},
		{FamNamespaces, "IPC namespace host", func(d *inspectDoc) { d.host["IpcMode"] = "host" }},
		{FamNamespaces, "UTS namespace host", func(d *inspectDoc) { d.host["UTSMode"] = "host" }},
		{FamNamespaces, "user namespace host", func(d *inspectDoc) { d.host["UsernsMode"] = "host" }},
		{FamNamespaces, "cgroup namespace host", func(d *inspectDoc) { d.host["CgroupnsMode"] = "host" }},
		{FamNamespaces, "cgroup parent /custom", func(d *inspectDoc) { d.host["CgroupParent"] = "/custom" }},
		{FamNetwork, "a fixed address on bridge", func(d *inspectDoc) {
			d.nets = map[string]any{"bridge": map[string]any{"IPAMConfig": map[string]any{"IPv6Address": "fd00::5"}}}
		}},
		{FamStorage, "a tmpfs mount at /scratch", func(d *inspectDoc) {
			d.mounts = []any{map[string]any{"Type": "tmpfs", "Destination": "/scratch"}}
		}},
	} {
		t.Run(tc.family+"/"+tc.says, func(t *testing.T) {
			_, refused := clone(t, refusedBy(tc.set))
			if len(refused) != 1 || !strings.HasPrefix(refused[0], tc.family+": ") || !strings.Contains(refused[0], tc.says) {
				t.Errorf("got %q; want one %s line saying %q", refused, tc.family, tc.says)
			}
		})
	}
}

// The families the review of the pinned HostConfig and Config added
// (docs/DECISIONS.md, round nineteen).
func TestCloneRefusesTheReviewedFamilies(t *testing.T) {
	for _, tc := range []struct {
		family, says string
		set          func(d *inspectDoc)
	}{
		{FamPorts, "random host port (-P)", func(d *inspectDoc) { d.host["PublishAllPorts"] = true }},
		{FamPorts, "host port range 8000-8010", func(d *inspectDoc) {
			d.host["PortBindings"] = map[string]any{"80/tcp": []any{map[string]any{"HostPort": "8000-8010"}}}
		}},
		{FamProcess, "umask 0027", func(d *inspectDoc) { d.host["Umask"] = 0o27 }},
		{FamProcess, "StdinOnce", func(d *inspectDoc) { d.cfg["StdinOnce"] = true }},
		{FamHost, "domain name corp.example", func(d *inspectDoc) { d.cfg["Domainname"] = "corp.example" }},
		{FamKernel, "OOM score adjustment 500", func(d *inspectDoc) { d.host["OomScoreAdj"] = 500 }},
		{FamLimits, "block I/O limits", func(d *inspectDoc) { d.host["BlkioWeight"] = 300 }},
		{FamLimits, "real-time CPU limits", func(d *inspectDoc) { d.host["CpuRealtimeRuntime"] = 950000 }},
		{FamLimits, "memory nodes 0", func(d *inspectDoc) { d.host["CpusetMems"] = "0" }},
		{FamDevices, "device cgroup rules", func(d *inspectDoc) { d.host["DeviceCgroupRules"] = []string{"c 1:3 mr"} }},
		{FamRuntime, "annotations io.example", func(d *inspectDoc) { d.host["Annotations"] = map[string]string{"io.example": "x"} }},
		{FamRuntime, "storage driver options", func(d *inspectDoc) { d.host["StorageOpt"] = map[string]string{"size": "10G"} }},
		{FamNetwork, "networking disabled", func(d *inspectDoc) { d.cfg["NetworkDisabled"] = true }},
		{FamNetwork, "driver options on bridge", func(d *inspectDoc) {
			d.nets = map[string]any{"bridge": map[string]any{"DriverOpts": map[string]string{"x": "y"}}}
		}},
		{FamNetwork, "gateway priority on bridge", func(d *inspectDoc) { d.nets = map[string]any{"bridge": map[string]any{"GwPriority": 5}} }},
		{FamStorage, "volumes from db", func(d *inspectDoc) { d.host["VolumesFrom"] = []string{"db"} }},
		{FamStorage, "volume driver nfs", func(d *inspectDoc) { d.host["VolumeDriver"] = "nfs" }},
		{FamStorage, "mount option Z at /data", func(d *inspectDoc) {
			d.mounts = []any{map[string]any{"Type": "bind", "Source": "/srv", "Destination": "/data", "Mode": "Z", "RW": true}}
		}},
		{FamStorage, "mount option z at /data", func(d *inspectDoc) {
			d.mounts = []any{map[string]any{"Type": "bind", "Source": "/srv", "Destination": "/data", "Mode": "z", "RW": true}}
		}},
		{FamStorage, "mount option Z at /var/data", func(d *inspectDoc) {
			d.mounts = []any{map[string]any{"Type": "volume", "Name": "v", "Source": "/var/lib/docker/volumes/v/_data", "Destination": "/var/data", "Mode": "Z", "RW": true}}
		}},
		{FamStorage, "rshared propagation at /data", func(d *inspectDoc) {
			d.mounts = []any{map[string]any{"Type": "bind", "Source": "/srv", "Destination": "/data", "RW": true, "Propagation": "rshared"}}
		}},
		{FamStorage, "mount option subpath at /data", func(d *inspectDoc) {
			d.host["Mounts"] = []any{map[string]any{"Type": "volume", "Source": "v", "Target": "/data", "VolumeOptions": map[string]any{"Subpath": "sub"}}}
		}},
	} {
		t.Run(tc.family+"/"+tc.says, func(t *testing.T) {
			_, refused := clone(t, refusedBy(tc.set))
			if len(refused) != 1 || !strings.HasPrefix(refused[0], tc.family+": ") || !strings.Contains(refused[0], tc.says) {
				t.Errorf("got %q; want one %s line saying %q", refused, tc.family, tc.says)
			}
		})
	}
}

func TestCloneNamesEverySetting(t *testing.T) {
	d := newDoc()
	d.host["CapAdd"] = []string{"SYS_ADMIN"}
	d.host["Devices"] = []any{map[string]any{"PathOnHost": "/dev/kvm"}}
	d.host["LogConfig"] = map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "10m"}}
	d.host["LogConfig"] = map[string]any{"Type": "splunk", "Config": map[string]string{"splunk-token": sentinel}}
	_, refused := clone(t, d)
	if len(refused) != 3 {
		t.Fatalf("want three families named, got %q", refused)
	}
	for i, f := range []string{FamCapabilities, FamDevices, FamLogging} {
		if !strings.HasPrefix(refused[i], f+": ") {
			t.Errorf("line %d %q is not %s", i, refused[i], f)
		}
	}
	if strings.Contains(strings.Join(refused, "\n"), sentinel) {
		t.Error("a log option's value appears in the refusal")
	}
}

// 1.7: an environment value never appears in what the derivation returns
// to be shown, nor in its error.
func TestCloneNeverQuotesAnEnvironmentValue(t *testing.T) {
	d := newDoc()
	d.cfg["Env"] = []string{"TOKEN=" + sentinel, "PATH=/usr/bin:/bin"}
	d.host["Privileged"] = true
	d.host["Tmpfs"] = map[string]string{"/run": sentinel}
	d.host["Sysctls"] = map[string]string{"net.x": sentinel}
	spec, refused := clone(t, d)
	if len(refused) == 0 {
		t.Fatal("expected a refusal")
	}
	for _, s := range append(refused, spec.FromImage...) {
		if strings.Contains(s, sentinel) {
			t.Errorf("%q contains the value", s)
		}
	}
	if spec.Env[0] != "TOKEN="+sentinel {
		t.Error("the value must still be carried")
	}
	// A broken document: the decoder's message would quote the bytes.
	bad := d.detail()
	bad.Raw = []byte(`{"Config":{"Env":["TOKEN=` + sentinel + `"` + "\x00" + `]}}`)
	if _, _, err := Clone(bad, oldImage(), CloneDefaults{}); err == nil || strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), "s3") {
		t.Errorf("error %v", err)
	}
	bad.Raw = []byte(`{"Config":{"Env":"TOKEN=` + sentinel + `"}}`)
	if _, _, err := Clone(bad, oldImage(), CloneDefaults{}); err == nil || strings.Contains(err.Error(), sentinel) {
		t.Errorf("error %v", err)
	}
}

func TestPreviousName(t *testing.T) {
	snap := Snapshot{Containers: []engine.Container{{ID: recID, Name: "web"}}}
	got, err := PreviousName("web", recID, snap)
	if err != nil || got != "web-previous-3f9c1a2b4d5e" {
		t.Fatalf("free: %q %v", got, err)
	}
	snap.Containers = append(snap.Containers, engine.Container{ID: "x", Name: "web-previous-3f9c1a2b4d5e"})
	if got, _ := PreviousName("/web", recID, snap); got != "web-previous-3f9c1a2b4d5e-2" {
		t.Errorf("taken: %q", got)
	}
	snap.Containers = append(snap.Containers, engine.Container{ID: "y", Name: "web-previous-3f9c1a2b4d5e-2"})
	if got, _ := PreviousName("web", recID, snap); got != "web-previous-3f9c1a2b4d5e-3" {
		t.Errorf("third choice: %q", got)
	}
	long := strings.Repeat("a", 200)
	got, err = PreviousName(long, recID, Snapshot{})
	if err != nil || len(got) != previousNameBound || !strings.HasSuffix(got, "-previous-3f9c1a2b4d5e") || validName("container", got, 2) != nil {
		t.Errorf("long: %d %q %v", len(got), got, err)
	}
	full := Snapshot{}
	for i := 1; i <= previousNameTries; i++ {
		n, _ := PreviousName("web", recID, full)
		full.Containers = append(full.Containers, engine.Container{Name: n})
	}
	if _, err := PreviousName("web", recID, full); err == nil {
		t.Error("no free name is an error")
	}
	if orig, ok := SetAsideName("web-previous-3f9c1a2b4d5e-2", recID); !ok || orig != "web" {
		t.Errorf("set aside: %q %v", orig, ok)
	}
	for _, n := range []string{"web", "web-previous-000000000000", "web-previous-3f9c1a2b4d5e-x", "-previous-3f9c1a2b4d5e"} {
		if _, ok := SetAsideName(n, recID); ok {
			t.Errorf("%q is not a set-aside name", n)
		}
	}
}

func TestCloneOptionsMapsEveryField(t *testing.T) {
	d := newDoc()
	d.cfg["Env"] = []string{"A=1"}
	d.cfg["Entrypoint"] = []string{"/x"}
	d.cfg["Labels"] = map[string]string{"k": "v"}
	d.host["NetworkMode"] = "shop_default"
	d.nets = map[string]any{"shop_default": map[string]any{"Aliases": []string{"web"}}}
	d.cfg["ExposedPorts"] = map[string]any{"9000/tcp": map[string]any{}}
	d.host["Memory"] = 1 << 20
	spec, _ := clone(t, d)
	o := CloneOptions(spec)
	if o.Image != "shop/web:1" || o.Name != "web" || o.Env[0] != "A=1" || o.Entrypoint[0] != "/x" || o.Labels["k"] != "v" ||
		o.NetworkMode != "shop_default" || o.Networks[0].Aliases[0] != "web" || o.ExposedOnly[0] != "9000/tcp" ||
		o.Resources.Memory != 1<<20 || o.ShmSize != 64<<20 || o.Restart.Name != "no" {
		t.Errorf("%+v", o)
	}
}
