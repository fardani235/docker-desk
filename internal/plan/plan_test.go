package plan

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
)

// world is a small engine with one of everything the rules care about.
func world() Snapshot {
	proj := func(p, svc string) map[string]string {
		return map[string]string{
			engine.LabelComposeProject: p, engine.LabelComposeService: svc,
			engine.LabelComposeWorkingDir: "/src/" + p, engine.LabelComposeConfig: "/src/" + p + "/compose.yaml",
		}
	}
	vol := func(name string) []engine.Mount {
		return []engine.Mount{{Type: "volume", Name: name, Destination: "/data"}}
	}
	net := func(name, id string) []engine.Attachment {
		return []engine.Attachment{{Network: name, NetworkID: id}}
	}
	return Snapshot{
		Containers: []engine.Container{
			{ID: "c-web", Name: "web", State: engine.StateRunning, ImageID: "sha256:img-nginx", Labels: proj("shop", "web"), Networks: net("shop_default", "n-shop")},
			{ID: "c-db", Name: "db", State: engine.StateRunning, ImageID: "sha256:img-pg", Labels: proj("shop", "db"), Mounts: vol("shop_pgdata"), Networks: net("shop_default", "n-shop")},
			{ID: "c-job", Name: "job", State: engine.StateExited, ImageID: "sha256:img-nginx", Labels: proj("shop", "job"), Networks: net("shop_default", "n-shop")},
			{ID: "c-blog", Name: "blog", State: engine.StateExited, ImageID: "sha256:img-ghost", Labels: proj("blog", "app"), Mounts: vol("blog_data"), Networks: net("blog_default", "n-blog")},
			{ID: "c-paused", Name: "paused", State: engine.StatePaused, ImageID: "sha256:img-alpine", Networks: net("bridge", "n-bridge")},
			{ID: "c-new", Name: "fresh", State: engine.StateCreated, ImageID: "sha256:img-alpine", Networks: net("bridge", "n-bridge"),
				Mounts: []engine.Mount{{Type: "volume", Name: "anon-used"}, {Type: "bind", Source: "/etc"}}},
			{ID: "c-loop", Name: "looping", State: engine.StateRestarting, ImageID: "sha256:img-alpine", Networks: net("bridge", "n-bridge")},
		},
		Images: []engine.Image{
			{ID: "sha256:img-nginx", RepoTags: []string{"nginx:1.27"}, Size: 100, Created: time.Unix(500, 0)},
			{ID: "sha256:img-pg", RepoTags: []string{"postgres:16", "postgres:latest"}, Size: 200, Created: time.Unix(400, 0)},
			{ID: "sha256:img-ghost", RepoTags: []string{"ghost:5"}, Size: 300, Created: time.Unix(300, 0)},
			{ID: "sha256:img-alpine", RepoTags: []string{"alpine:3.20"}, Size: 10, Created: time.Unix(200, 0)},
			{ID: "sha256:img-tool", RepoTags: []string{"tool:1", "tool:latest"}, Size: 50, Created: time.Unix(150, 0)},
			{ID: "sha256:img-dangling1", Dangling: true, Size: 7, Created: time.Unix(100, 0)},
			{ID: "sha256:img-dangling2", Dangling: true, Size: 9, Created: time.Unix(50, 0)},
			{ID: "sha256:img-digest", RepoTags: []string{"kic/stable@sha256:abc"}, Size: 70, Created: time.Unix(20, 0)},
		},
		Volumes: []engine.Volume{
			{Name: "shop_pgdata", Labels: map[string]string{engine.LabelComposeProject: "shop"}},
			{Name: "blog_data", Labels: map[string]string{engine.LabelComposeProject: "blog"}},
			{Name: "old-backups"},
			{Name: "anon-used", Labels: map[string]string{engine.LabelAnonymousVolume: ""}},
			{Name: strings.Repeat("ab", 32), Labels: map[string]string{engine.LabelAnonymousVolume: ""}},
			{Name: strings.Repeat("cd", 32)}, // 64 hex but not labelled anonymous by the daemon
		},
		Networks: []engine.Network{
			{ID: "n-bridge", Name: "bridge", Driver: "bridge"},
			{ID: "n-host", Name: "host", Driver: "host"},
			{ID: "n-none", Name: "none", Driver: "null"},
			{ID: "n-shop", Name: "shop_default", Driver: "bridge", Labels: map[string]string{engine.LabelComposeProject: "shop"}},
			{ID: "n-blog", Name: "blog_default", Driver: "bridge", Labels: map[string]string{engine.LabelComposeProject: "blog"}},
			{ID: "n-orphan", Name: "orphan", Driver: "bridge"},
		},
	}
}

func isNA(err error) bool {
	var n *NotApplicable
	return errors.As(err, &n)
}

func stepIDs(p Plan) string {
	var ids []string
	for _, s := range p.Steps {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, ",")
}

func joined(ss []string) string { return strings.Join(ss, " | ") }

func TestContainerActionsApplicability(t *testing.T) {
	w := world()
	cases := []struct {
		op      Op
		id      string
		wantNA  string // substring of the reason when not applicable
		tier    Tier
		blocked bool
	}{
		{OpStart, "c-job", "", TierConfirm, false},
		{OpStart, "c-new", "", TierConfirm, false},
		{OpStart, "c-web", "already running", 0, false},
		{OpStart, "c-paused", "unpause it instead", 0, false},
		{OpStart, "c-loop", "restarting", 0, false},
		{OpStop, "c-web", "", TierConfirm, false},
		{OpStop, "c-paused", "", TierConfirm, false},
		{OpStop, "c-job", "is not running", 0, false},
		{OpRestart, "c-web", "", TierConfirm, false},
		{OpRestart, "c-job", "", TierConfirm, false},
		{OpPause, "c-web", "", TierConfirm, false},
		{OpPause, "c-paused", "already paused", 0, false},
		{OpPause, "c-job", "is not running", 0, false},
		{OpUnpause, "c-paused", "", TierConfirm, false},
		{OpUnpause, "c-web", "is not paused", 0, false},
		{OpKill, "c-web", "", TierConfirm, false},
		{OpKill, "c-job", "is not running", 0, false},
		{OpRemoveContainer, "c-job", "", TierTyped, false},
		{OpRemoveContainer, "c-web", "", TierTyped, true},
		{OpRemoveContainer, "c-paused", "", TierTyped, true},
		{OpRemoveContainer, "c-loop", "", TierTyped, true},
		{OpStop, "c-gone", "no longer exists", 0, false},
	}
	for _, tc := range cases {
		p, err := Container(tc.op, w, tc.id)
		name := tc.op.String() + " " + tc.id
		if tc.wantNA != "" {
			if !isNA(err) || !strings.Contains(err.Error(), tc.wantNA) {
				t.Errorf("%s: want not-applicable containing %q, got %v", name, tc.wantNA, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if p.Tier != tc.tier || (len(p.Blockers) > 0) != tc.blocked || p.Runnable() == tc.blocked {
			t.Errorf("%s: tier=%v blockers=%v runnable=%v", name, p.Tier, p.Blockers, p.Runnable())
		}
		// The plan names the exact object and acts on exactly it.
		if len(p.Steps) != 1 || p.Steps[0].ID != tc.id || p.Steps[0].Op != tc.op {
			t.Errorf("%s: steps %+v", name, p.Steps)
		}
		if p.Target.Kind != "container" || p.Target.Name == "" || p.Target.ID == "" || p.Title == "" || len(p.Effects) == 0 {
			t.Errorf("%s: plan does not name its object and effect: %+v", name, p)
		}
		if tc.tier == TierTyped && p.Word != "remove" {
			t.Errorf("%s: word %q", name, p.Word)
		}
	}
}

func TestContainerPlanWording(t *testing.T) {
	w := world()
	stop, _ := Container(OpStop, w, "c-web")
	if !strings.Contains(joined(stop.Effects), "stop signal") || !strings.Contains(joined(stop.Effects), "kills it") {
		t.Errorf("stop effects: %v", stop.Effects)
	}
	kill, _ := Container(OpKill, w, "c-web")
	if !strings.Contains(joined(kill.Effects), "SIGKILL") || len(kill.Warnings) == 0 {
		t.Errorf("kill plan: %+v", kill)
	}
	rm, _ := Container(OpRemoveContainer, w, "c-web")
	if !strings.Contains(joined(rm.Blockers), "web is running") || !strings.Contains(joined(rm.Blockers), "never force-removes") {
		t.Errorf("remove blockers: %v", rm.Blockers)
	}
	rmNew, _ := Container(OpRemoveContainer, w, "c-new")
	if !strings.Contains(joined(rmNew.Effects), "volumes are kept (1 volume)") {
		t.Errorf("remove must say volumes are kept (bind mounts do not count): %v", rmNew.Effects)
	}
}

func TestImageRemove(t *testing.T) {
	w := world()

	// The only reference of an image a container was created from: refused,
	// naming the containers — stopped ones included.
	p, err := ImageRemove(w, "sha256:img-nginx", "nginx:1.27")
	if err != nil {
		t.Fatal(err)
	}
	if p.Runnable() || !strings.Contains(joined(p.Blockers), "2 containers") ||
		!strings.Contains(joined(p.Blockers), "job (exited)") || !strings.Contains(joined(p.Blockers), "web (running)") {
		t.Errorf("in-use image: %+v", p.Blockers)
	}

	// One of several references: only an untag, allowed even though in use,
	// and the dialog says the data stays.
	p, err = ImageRemove(w, "sha256:img-pg", "postgres:latest")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Runnable() || p.Title != "Untag image" || stepIDs(p) != "postgres:latest" ||
		!strings.Contains(joined(p.Effects), "Only the reference postgres:latest") ||
		!strings.Contains(joined(p.Effects), "still referenced as postgres:16") {
		t.Errorf("untag plan: %+v", p)
	}
	if p.Tier != TierTyped || p.Word != "remove" {
		t.Errorf("untag tier: %v %q", p.Tier, p.Word)
	}

	// Unused, last reference: deleted by reference.
	w2 := world()
	w2.Images[4].RepoTags = []string{"tool:1"}
	p, _ = ImageRemove(w2, "sha256:img-tool", "tool:1")
	if !p.Runnable() || p.Title != "Remove image" || stepIDs(p) != "tool:1" || p.TotalSize != 50 {
		t.Errorf("delete plan: %+v", p)
	}

	// Untagged: removed by ID.
	p, _ = ImageRemove(w, "sha256:img-dangling1", "")
	if !p.Runnable() || stepIDs(p) != "sha256:img-dangling1" || !strings.Contains(p.Target.Name, "<untagged>") {
		t.Errorf("untagged plan: %+v", p)
	}

	// A digest-only reference is a reference.
	p, _ = ImageRemove(w, "sha256:img-digest", "kic/stable@sha256:abc")
	if !p.Runnable() || stepIDs(p) != "kic/stable@sha256:abc" {
		t.Errorf("digest reference plan: %+v", p)
	}

	// Stale rows.
	if _, err := ImageRemove(w, "sha256:gone", "x:1"); !isNA(err) {
		t.Errorf("missing image: %v", err)
	}
	if _, err := ImageRemove(w, "sha256:img-nginx", "nginx:old"); !isNA(err) {
		t.Errorf("reference that is gone: %v", err)
	}
	if _, err := ImageRemove(w, "sha256:img-nginx", ""); !isNA(err) {
		t.Errorf("an image that gained a tag must not be removed by ID: %v", err)
	}
}

func TestImagePrune(t *testing.T) {
	w := world()

	p := ImagePrune(w, false)
	if !p.Runnable() || !p.Listed || p.Tier != TierTyped || p.Word != "prune" {
		t.Fatalf("dangling prune: %+v", p)
	}
	// Oldest first, dangling only, by ID.
	if stepIDs(p) != "sha256:img-dangling2,sha256:img-dangling1" || p.TotalSize != 16 || p.Target.Name != "2 images" {
		t.Errorf("dangling prune steps %q size %d target %q", stepIDs(p), p.TotalSize, p.Target.Name)
	}

	// All unused: adds unused tagged images, one step per reference; images
	// any container (running or stopped) was created from are never listed.
	p = ImagePrune(w, true)
	want := "kic/stable@sha256:abc,sha256:img-dangling2,sha256:img-dangling1,tool:1,tool:latest"
	if stepIDs(p) != want {
		t.Errorf("all-unused steps:\n got %s\nwant %s", stepIDs(p), want)
	}
	if p.Target.Name != "4 images" || p.TotalSize != 70+9+7+50 || len(p.Warnings) == 0 {
		t.Errorf("all-unused: target %q size %d warnings %v", p.Target.Name, p.TotalSize, p.Warnings)
	}
	for _, s := range p.Steps {
		for _, used := range []string{"nginx", "postgres", "ghost", "alpine"} {
			if strings.Contains(s.ID, used) {
				t.Errorf("in-use image offered for pruning: %s", s.ID)
			}
		}
	}
	// The size of a multi-tag image is counted once, on its last reference.
	if p.Steps[3].Size != 0 || p.Steps[4].Size != 50 {
		t.Errorf("multi-tag sizes: %d, %d", p.Steps[3].Size, p.Steps[4].Size)
	}

	// A dangling image a stopped container was created from is in use.
	w.Containers = append(w.Containers, engine.Container{ID: "c-old", Name: "old", State: engine.StateExited, ImageID: "sha256:img-dangling1"})
	if got := stepIDs(ImagePrune(w, false)); got != "sha256:img-dangling2" {
		t.Errorf("dangling image used by a stopped container was offered: %s", got)
	}

	empty := ImagePrune(Snapshot{}, false)
	if empty.Runnable() || !strings.Contains(empty.Nothing, "Nothing to prune") {
		t.Errorf("empty prune: %+v", empty)
	}
}

func TestVolumeRemoveAndPrune(t *testing.T) {
	w := world()
	anonUnused, hexNamed := strings.Repeat("ab", 32), strings.Repeat("cd", 32)

	p, err := VolumeRemove(w, "shop_pgdata")
	if err != nil || p.Runnable() || !strings.Contains(joined(p.Blockers), "db (running)") {
		t.Errorf("in-use volume: %+v %v", p, err)
	}
	// Referenced only by a stopped container: still in use.
	p, _ = VolumeRemove(w, "blog_data")
	if p.Runnable() || !strings.Contains(joined(p.Blockers), "blog (exited)") {
		t.Errorf("volume of a stopped container: %+v", p)
	}
	p, _ = VolumeRemove(w, "old-backups")
	if !p.Runnable() || p.Tier != TierTyped || stepIDs(p) != "old-backups" || !strings.Contains(joined(p.Effects), "all data in it") {
		t.Errorf("unused volume: %+v", p)
	}
	if _, err := VolumeRemove(w, "ghost"); !isNA(err) {
		t.Errorf("missing volume: %v", err)
	}

	// Default scope: only volumes the daemon labelled anonymous, and unused.
	p = VolumePrune(w, false)
	if stepIDs(p) != anonUnused {
		t.Errorf("anonymous prune: %s", stepIDs(p))
	}
	if !strings.Contains(p.Steps[0].Name, "(anonymous)") || len(p.Steps[0].Name) > 40 {
		t.Errorf("anonymous volume display name: %q", p.Steps[0].Name)
	}
	// All: named unused ones too, with a warning; in-use ones never.
	p = VolumePrune(w, true)
	if stepIDs(p) != anonUnused+","+hexNamed+",old-backups" || len(p.Warnings) == 0 {
		t.Errorf("all-unused prune: %s / %v", stepIDs(p), p.Warnings)
	}
	for _, s := range p.Steps {
		if s.ID == "shop_pgdata" || s.ID == "blog_data" || s.ID == "anon-used" {
			t.Errorf("in-use volume offered for pruning: %s", s.ID)
		}
	}
	if e := VolumePrune(Snapshot{}, false); e.Runnable() || e.Nothing == "" {
		t.Errorf("empty prune: %+v", e)
	}
}

func TestNetworkRemoveAndPrune(t *testing.T) {
	w := world()

	for _, id := range []string{"n-bridge", "n-host", "n-none"} {
		p, err := NetworkRemove(w, id)
		if err != nil || p.Runnable() || !strings.Contains(joined(p.Blockers), "predefined") {
			t.Errorf("%s: %+v %v", id, p.Blockers, err)
		}
	}
	p, _ := NetworkRemove(w, "n-shop")
	if p.Runnable() || !strings.Contains(joined(p.Blockers), "3 containers") {
		t.Errorf("network with containers: %v", p.Blockers)
	}
	// Only a stopped container references it: Docker would delete it, the
	// desk refuses.
	p, _ = NetworkRemove(w, "n-blog")
	if p.Runnable() || !strings.Contains(joined(p.Blockers), "blog (exited)") || !strings.Contains(joined(p.Blockers), "Stopped containers count") {
		t.Errorf("network of a stopped container: %v", p.Blockers)
	}
	p, _ = NetworkRemove(w, "n-orphan")
	if !p.Runnable() || p.Tier != TierTyped || stepIDs(p) != "n-orphan" {
		t.Errorf("unused network: %+v", p)
	}

	p = NetworkPrune(w)
	if stepIDs(p) != "n-orphan" || p.Target.Name != "1 network" {
		t.Errorf("network prune: %s", stepIDs(p))
	}
	if !strings.Contains(joined(p.Effects), "stopped containers are kept") {
		t.Errorf("prune must state the conservative rule: %v", p.Effects)
	}
	// A container list that carries only the network name still counts.
	w.Networks = append(w.Networks, engine.Network{ID: "n-legacy", Name: "legacy", Driver: "bridge"})
	w.Containers = append(w.Containers, engine.Container{ID: "c-l", Name: "l", State: engine.StateExited, Networks: []engine.Attachment{{Network: "legacy"}}})
	if got := stepIDs(NetworkPrune(w)); got != "n-orphan" {
		t.Errorf("network referenced by name only was offered: %s", got)
	}
}

func TestNetworkConnectDisconnect(t *testing.T) {
	w := world()

	cands, err := ConnectCandidates(w, "n-orphan")
	if err != nil || len(cands) != len(w.Containers) {
		t.Fatalf("connect candidates: %d %v", len(cands), err)
	}
	cands, _ = ConnectCandidates(w, "n-shop")
	for _, c := range cands {
		if c.Name == "web" || c.Name == "db" || c.Name == "job" {
			t.Errorf("already-attached container offered: %s", c.Name)
		}
	}
	if len(cands) != 4 {
		t.Errorf("want 4 candidates, got %d", len(cands))
	}
	for _, id := range []string{"n-host", "n-none"} {
		if _, err := ConnectCandidates(w, id); !isNA(err) || !strings.Contains(err.Error(), "cannot be connected") {
			t.Errorf("%s connect: %v", id, err)
		}
		if _, err := DisconnectCandidates(w, id); !isNA(err) {
			t.Errorf("%s disconnect: %v", id, err)
		}
	}
	if _, err := DisconnectCandidates(w, "n-orphan"); !isNA(err) || !strings.Contains(err.Error(), "nothing to disconnect") {
		t.Errorf("disconnect on an empty network: %v", err)
	}
	dc, _ := DisconnectCandidates(w, "n-shop")
	if len(dc) != 3 || dc[0].Name != "db" {
		t.Errorf("disconnect candidates: %+v", dc)
	}

	p, err := NetworkConnect(w, "n-orphan", "c-web")
	if err != nil || !p.Runnable() || p.Tier != TierConfirm {
		t.Fatalf("connect plan: %+v %v", p, err)
	}
	if s := p.Steps[0]; s.Op != OpConnect || s.ID != "n-orphan" || s.Other != "c-web" {
		t.Errorf("connect step: %+v", s)
	}
	if !strings.Contains(joined(p.Effects), "web") || !strings.Contains(joined(p.Effects), "orphan") {
		t.Errorf("connect must name both objects: %v", p.Effects)
	}
	p, _ = NetworkConnect(w, "n-orphan", "c-job")
	if !strings.Contains(joined(p.Effects), "takes effect when it starts") {
		t.Errorf("connect of a stopped container: %v", p.Effects)
	}
	if _, err := NetworkConnect(w, "n-shop", "c-web"); !isNA(err) {
		t.Errorf("connect when already attached: %v", err)
	}

	p, err = NetworkDisconnect(w, "n-shop", "c-web")
	if err != nil || !p.Runnable() || p.Steps[0].Op != OpDisconnect || len(p.Warnings) == 0 {
		t.Errorf("disconnect of the only network must warn: %+v %v", p, err)
	}
	if _, err := NetworkDisconnect(w, "n-orphan", "c-web"); !isNA(err) {
		t.Errorf("disconnect when not attached: %v", err)
	}
}

func TestComposeProjects(t *testing.T) {
	w := world()
	ps := Projects(w)
	if len(ps) != 2 || ps[0].Name != "blog" || ps[1].Name != "shop" {
		t.Fatalf("projects: %+v", ps)
	}
	shop := ps[1]
	if len(shop.Containers) != 3 || shop.Running != 2 || shop.WorkingDir != "/src/shop" || shop.ConfigFiles != "/src/shop/compose.yaml" {
		t.Errorf("shop: %+v", shop)
	}
	if ps[0].Running != 0 || len(ps[0].Containers) != 1 {
		t.Errorf("blog: %+v", ps[0])
	}
	if got := Projects(Snapshot{Containers: []engine.Container{{ID: "x", Name: "x"}}}); len(got) != 0 {
		t.Errorf("unlabelled containers must not form a project: %+v", got)
	}

	// Stop: only the running containers, each listed, `y` tier.
	p, err := ProjectStop(w, "shop")
	if err != nil || !p.Runnable() || p.Tier != TierConfirm || !p.Listed || stepIDs(p) != "c-db,c-web" {
		t.Errorf("project stop: %+v %v", p, err)
	}
	for _, s := range p.Steps {
		if s.Op != OpStop {
			t.Errorf("stop step: %+v", s)
		}
	}
	if _, err := ProjectStop(w, "blog"); !isNA(err) || !strings.Contains(err.Error(), "nothing to stop") {
		t.Errorf("stop of a stopped project: %v", err)
	}
	if _, err := ProjectStop(w, "nope"); !isNA(err) {
		t.Errorf("stop of a missing project: %v", err)
	}

	// Remove while running: refused, naming the running ones.
	p, _ = ProjectRemove(w, "shop")
	if p.Runnable() || !strings.Contains(joined(p.Blockers), "db (running), web (running)") || !strings.Contains(joined(p.Blockers), "stop the project first") {
		t.Errorf("remove of a running project: %v", p.Blockers)
	}
	// Remove when stopped: every container, typed tier, leftovers named.
	p, _ = ProjectRemove(w, "blog")
	if !p.Runnable() || p.Tier != TierTyped || p.Word != "remove" || stepIDs(p) != "c-blog" {
		t.Errorf("project remove: %+v", p)
	}
	eff := joined(p.Effects)
	if !strings.Contains(eff, "networks blog_default") || !strings.Contains(eff, "volumes blog_data") || !strings.Contains(eff, "not `compose down`") {
		t.Errorf("project remove must name what stays: %v", p.Effects)
	}
	// Only container removals — never a network or volume step.
	for _, s := range p.Steps {
		if s.Op != OpRemoveContainer || s.Kind != "container" {
			t.Errorf("project remove step touches something else: %+v", s)
		}
	}
}

func TestFormPlans(t *testing.T) {
	w := world()

	for in, want := range map[string]string{
		"alpine":                                    "alpine:latest",
		"  alpine:3.20 ":                            "alpine:3.20",
		"docker.io/library/nginx":                   "nginx:latest",
		"ghcr.io/org/app:v1":                        "ghcr.io/org/app:v1",
		"registry.local:5000/team/app":              "registry.local:5000/team/app:latest",
		"busybox@sha256:" + strings.Repeat("a", 64): "busybox@sha256:" + strings.Repeat("a", 64),
	} {
		got, err := NormalizeRef(in)
		if err != nil || got != want {
			t.Errorf("NormalizeRef(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "Alpine", "two words", "bad:tag:extra", "http://x/y"} {
		if _, err := NormalizeRef(bad); err == nil {
			t.Errorf("NormalizeRef(%q) must fail", bad)
		}
	}

	p, err := Pull(w, "alpine")
	if err != nil || stepIDs(p) != "alpine:latest" || p.Steps[0].Op != OpPullImage || p.Tier != TierForm {
		t.Errorf("pull plan: %+v %v", p, err)
	}
	p, _ = Pull(w, "nginx:1.27")
	if !strings.Contains(joined(p.Effects), "already present") {
		t.Errorf("pull of a present image: %v", p.Effects)
	}

	if p, err := VolumeCreate(w, " scratch "); err != nil || stepIDs(p) != "scratch" || p.Steps[0].Op != OpCreateVolume {
		t.Errorf("volume create: %+v %v", p, err)
	}
	for in, want := range map[string]string{
		"":            "enter a name",
		"x":           "at least 2",
		"-bad":        "must start with",
		"has space":   "may only contain",
		"a/b":         "may only contain",
		"old-backups": "already exists",
	} {
		if _, err := VolumeCreate(w, in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("VolumeCreate(%q): want %q, got %v", in, want, err)
		}
	}
	if p, err := NetworkCreate(w, "frontend"); err != nil || stepIDs(p) != "frontend" || p.Steps[0].Op != OpCreateNetwork {
		t.Errorf("network create: %+v %v", p, err)
	}
	if _, err := NetworkCreate(w, "bridge"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate network: %v", err)
	}
	if _, err := NetworkCreate(w, ""); err == nil {
		t.Error("empty network name must fail")
	}
}

// TestNoPlanCanForce is a structural check: a Step has no way to express
// force, and the vocabulary of operations contains nothing that deletes more
// than the one object it names.
func TestNoPlanCanForce(t *testing.T) {
	w := world()
	var all []Plan
	for _, c := range w.Containers {
		for _, op := range []Op{OpStart, OpStop, OpRestart, OpPause, OpUnpause, OpKill, OpRemoveContainer} {
			if p, err := Container(op, w, c.ID); err == nil {
				all = append(all, p)
			}
		}
	}
	all = append(all, ImagePrune(w, true), VolumePrune(w, true), NetworkPrune(w))
	for _, name := range []string{"shop", "blog"} {
		if p, err := ProjectRemove(w, name); err == nil {
			all = append(all, p)
		}
	}
	active := map[string]bool{}
	for _, c := range w.Containers {
		active[c.ID] = c.IsActive()
	}
	for _, p := range all {
		if !p.Runnable() {
			continue
		}
		for _, s := range p.Steps {
			if s.Op == OpRemoveContainer && active[s.ID] {
				t.Errorf("%s: a runnable plan removes active container %s", p.Title, s.ID)
			}
			if s.ID == "" || s.Name == "" {
				t.Errorf("%s: step without identity: %+v", p.Title, s)
			}
		}
	}
}
