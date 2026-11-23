package plan

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// recreateRig is a daemon with one container, web, created from shop/web:1
// with a configuration of its own, and a newer build that shop/web:1 names
// now.
type recreateRig struct {
	*rig
	oldID, newID string // container
	oldImg       string
	newImg       string
}

func newRecreateRig(t *testing.T, running bool) *recreateRig {
	t.Helper()
	r := newRig(t, func(s *fakeengine.Server) {
		s.AddImage(7_000_000, 2*time.Hour, "shop/web:1")
		s.AddNetwork("shop_default", map[string]string{engine.LabelComposeProject: "shop"})
		s.AddContainer(fakeengine.ContainerSpec{Name: "db", Image: "shop/web:1", Networks: []string{"shop_default"}})
	})
	ctx := context.Background()
	id, err := r.eng.CreateContainer(ctx, engine.ContainerCreateOptions{
		Image: "shop/web:1", Name: "web",
		Env:         []string{"LOG_LEVEL=info", "TOKEN=" + sentinel},
		Labels:      map[string]string{engine.LabelComposeProject: "shop", engine.LabelComposeService: "web"},
		NetworkMode: "shop_default",
		Networks:    []engine.NetworkAttachment{{Network: "shop_default", Aliases: []string{"web"}}},
		Ports:       []engine.PortBinding{{HostPort: 8080, ContainerPort: 80, Proto: "tcp"}},
		ExposedOnly: []string{"9000/tcp"},
		Mounts:      []engine.MountOption{{Type: "volume", Source: "web-data", Target: "/data"}, {Type: "bind", Source: "/srv/conf", Target: "/etc/web", ReadOnly: true}},
		Restart:     engine.RestartPolicy{Name: "on-failure", MaximumRetryCount: 3},
		Resources:   engine.ResourceLimits{Memory: 512 << 20, CPUQuota: 50000},
		Healthcheck: &engine.HealthConfig{Test: []string{"CMD", "probe"}, Interval: 30 * time.Second},
		StopSignal:  "SIGQUIT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if running {
		if err := r.eng.StartContainer(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	rr := &recreateRig{rig: r, oldID: id}
	rr.oldImg = imageID(t, r, "shop/web:1")
	rr.newImg = "sha256:" + r.srv.MoveTag("shop/web:1", 9_000_000)
	r.srv.ResetRequests()
	return rr
}

// plan builds the re-create plan exactly as the desk does: inspect, the
// running image's inspect, the daemon's defaults, the clone, the plan.
func (rr *recreateRig) plan(id string) (Plan, CloneSpec) {
	rr.t.Helper()
	ctx := context.Background()
	d, err := rr.eng.InspectContainer(ctx, id)
	if err != nil {
		rr.t.Fatal(err)
	}
	base, err := rr.eng.InspectImage(ctx, d.ImageID)
	if err != nil {
		rr.t.Fatal(err)
	}
	info, err := rr.eng.Info(ctx)
	if err != nil {
		rr.t.Fatal(err)
	}
	spec, refused, err := Clone(d, base, DefaultsFrom(info))
	if err != nil {
		rr.t.Fatal(err)
	}
	now, err := rr.eng.InspectImage(ctx, d.Image)
	if err != nil {
		rr.t.Fatal(err)
	}
	p, err := ContainerRecreate(rr.snap(), d, spec, refused, ImagePin{Ref: d.Image, ID: now.ID})
	if err != nil {
		rr.t.Fatal(err)
	}
	return p, spec
}

func prevName(id string) string { return "web-previous-" + engine.ShortID(id) }

func ops(p Plan) []Op {
	var out []Op
	for _, s := range p.Steps {
		out = append(out, s.Op)
	}
	return out
}

func TestRecreatePlanSteps(t *testing.T) {
	for _, tc := range []struct {
		running bool
		ops     []Op
		deps    [][]int
	}{
		{true, []Op{OpStop, OpRename, OpCreateContainer, OpStart}, [][]int{nil, {0}, {1}, {2}}},
		{false, []Op{OpRename, OpCreateContainer, OpStart}, [][]int{nil, {0}, {1}}},
	} {
		rr := newRecreateRig(t, tc.running)
		p, _ := rr.plan(rr.oldID)
		if !p.Runnable() {
			t.Fatalf("not runnable: %v", p.Blockers)
		}
		if got := ops(p); !reflect.DeepEqual(got, tc.ops) {
			t.Errorf("running=%v: ops %v", tc.running, got)
		}
		for i, s := range p.Steps {
			if !reflect.DeepEqual(s.DependsOn, tc.deps[i]) {
				t.Errorf("step %d depends on %v, want %v", i, s.DependsOn, tc.deps[i])
			}
			switch s.Op {
			case OpRemoveContainer, OpRemoveImage, OpRemoveVolume, OpRemoveNetwork, OpKill:
				t.Errorf("step %d removes something: %v", i, s.Op)
			}
		}
		create := p.Steps[len(p.Steps)-2]
		start := p.Steps[len(p.Steps)-1]
		if !create.ProducesID || create.Clone == nil || create.Pin != rr.newImg || start.IDFrom != len(p.Steps)-2 {
			t.Errorf("create %+v start %+v", create, start)
		}
		rename := p.Steps[len(p.Steps)-3]
		if rename.ID != rr.oldID || rename.Rename != prevName(rr.oldID) {
			t.Errorf("rename %+v", rename)
		}
		if p.Tier != TierTyped || p.Word != "recreate" || p.PinRef != "shop/web:1" || p.PinID != rr.newImg {
			t.Errorf("tier %v word %q pin %q %q", p.Tier, p.Word, p.PinRef, p.PinID)
		}
	}
}

func TestRecreatePlanChecks(t *testing.T) {
	rr := newRecreateRig(t, true)
	ctx := context.Background()
	d, _ := rr.eng.InspectContainer(ctx, rr.oldID)
	base, _ := rr.eng.InspectImage(ctx, d.ImageID)
	spec, refused, _ := Clone(d, base, CloneDefaults{})
	pin := ImagePin{Ref: "shop/web:1", ID: rr.newImg}

	// The original name, held by another container (the snapshot is what
	// the daemon says; a name may have moved between the reads).
	snap := rr.snap()
	snap.Containers = append(snap.Containers, engine.Container{ID: strings.Repeat("e", 64), Name: "web"})
	p, err := ContainerRecreate(snap, d, spec, refused, pin)
	if err != nil || p.Runnable() || !strings.Contains(strings.Join(p.Blockers, "\n"), "web is held by another container") {
		t.Errorf("taken name: %v %v", err, p.Blockers)
	}
	// The container itself does not count.
	if p, _ := ContainerRecreate(rr.snap(), d, spec, refused, pin); !p.Runnable() {
		t.Errorf("the container's own name blocked it: %v", p.Blockers)
	}
	// A network that no longer exists.
	snap = rr.snap()
	var nets []engine.Network
	for _, n := range snap.Networks {
		if n.Name != "shop_default" {
			nets = append(nets, n)
		}
	}
	snap.Networks = nets
	p, _ = ContainerRecreate(snap, d, spec, refused, pin)
	if p.Runnable() || !strings.Contains(strings.Join(p.Blockers, "\n"), "Networks: shop_default no longer exist") {
		t.Errorf("missing network: %v", p.Blockers)
	}
	// The derived name is taken: the next free one is used and shown.
	rr.srv.AddContainer(fakeengine.ContainerSpec{Name: prevName(rr.oldID), Image: "shop/web:1", State: "exited"})
	p, _ = ContainerRecreate(rr.snap(), d, spec, refused, pin)
	want := prevName(rr.oldID) + "-2"
	if !p.Runnable() || p.Steps[1].Rename != want || !strings.Contains(strings.Join(p.Effects, "\n"), "renamed "+want) {
		t.Errorf("collision: %v %+v %v", p.Blockers, p.Steps[1], p.Effects)
	}
	// A set-aside container is not offered a re-create of its own.
	rr.srv.Rename("web", prevName(rr.oldID)+"-3")
	d2, _ := rr.eng.InspectContainer(ctx, rr.oldID)
	spec2, refused2, _ := Clone(d2, base, CloneDefaults{})
	p, _ = ContainerRecreate(rr.snap(), d2, spec2, refused2, pin)
	if p.Runnable() || !strings.Contains(strings.Join(p.Blockers, "\n"), "set aside by a re-create; its name was web") {
		t.Errorf("set aside: %v", p.Blockers)
	}
	// No longer there.
	if _, err := ContainerRecreate(Snapshot{}, d, spec, refused, pin); err == nil {
		t.Error("a vanished container is planned")
	}
}

// texts is everything a plan shows or logs about itself.
func texts(p Plan) []string {
	out := []string{p.Title, p.JobTitle, p.JobTarget, p.Target.Name, p.Target.ID, p.Word, p.SuccessNote, p.Nothing}
	out = append(out, p.Effects...)
	out = append(out, p.Warnings...)
	out = append(out, p.Blockers...)
	for _, s := range p.Steps {
		out = append(out, describe(s), s.Name, s.Detail, s.FailNote)
	}
	return out
}

func TestRecreatePlanReading(t *testing.T) {
	rr := newRecreateRig(t, true)
	p, _ := rr.plan(rr.oldID)
	all := strings.Join(texts(p), "\n")
	if strings.Contains(all, sentinel) {
		t.Fatalf("an environment value is shown:\n%s", all)
	}
	if p.Title != "Re-create container" || p.JobTitle != "re-create container" || p.JobTarget != "web" ||
		p.Target != (Target{Kind: "container", Name: "web", ID: engine.ShortID(rr.oldID)}) {
		t.Errorf("labels: %+v", p)
	}
	eff := strings.Join(p.Effects, "\n")
	for _, w := range []string{
		"web is stopped",
		"renamed " + prevName(rr.oldID) + " and kept",
		"created from shop/web:1 — the build " + engine.ShortID(rr.newImg) + ", replacing " + engine.ShortID(rr.oldImg),
		"2 environment variables (LOG_LEVEL, TOKEN)",
		"1 published port", "1 exposed port", "2 mounts", "1 network (shop_default)", "restart on-failure, at most 3 retries",
		"health check", "stop behaviour", "resource limits",
		"Nothing is deleted; the old container stays stopped under " + prevName(rr.oldID),
	} {
		if !strings.Contains(eff, w) {
			t.Errorf("effects do not say %q:\n%s", w, eff)
		}
	}
	warn := strings.Join(p.Warnings, "\n")
	for _, w := range []string{"/srv/conf", "not checked", "shell", "compose project shop", "The compose file is not read"} {
		if !strings.Contains(warn, w) {
			t.Errorf("warnings do not say %q:\n%s", w, warn)
		}
	}
	if len(p.Blockers) != 0 {
		t.Errorf("blockers %v", p.Blockers)
	}

	// One blocker per refused family; the dialog opens and explains.
	r2 := newRig(t, func(s *fakeengine.Server) {
		s.AddImage(1, time.Hour, "shop/web:1")
		s.AddContainer(fakeengine.ContainerSpec{Name: "web", Image: "shop/web:1", Privileged: true})
	})
	newImg := r2.srv.MoveTag("shop/web:1", 2)
	d, _ := r2.eng.InspectContainer(context.Background(), r2.srv.ContainerID("web"))
	base, _ := r2.eng.InspectImage(context.Background(), d.ImageID)
	spec, refused, _ := Clone(d, base, CloneDefaults{})
	p2, err := ContainerRecreate(r2.snap(), d, spec, refused, ImagePin{Ref: "shop/web:1", ID: "sha256:" + newImg})
	if err != nil || p2.Runnable() || len(p2.Blockers) != 1 || !strings.HasPrefix(p2.Blockers[0], "Privilege: ") {
		t.Errorf("refused plan: %v %v", err, p2.Blockers)
	}
}

func TestRecreateSucceeds(t *testing.T) {
	rr := newRecreateRig(t, true)
	p, spec := rr.plan(rr.oldID)
	j := rr.run(p)
	if j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	prev := prevName(rr.oldID)
	if st, _ := rr.srv.ContainerState(prev); st != "exited" || rr.srv.ContainerID(prev) != rr.oldID {
		t.Errorf("old container: %q under %q", st, prev)
	}
	newID := rr.srv.ContainerID("web")
	if newID == "" || newID == rr.oldID {
		t.Fatal("no new container under the original name")
	}
	if st, _ := rr.srv.ContainerState("web"); st != "running" {
		t.Errorf("new container %q", st)
	}
	var got []string
	for _, m := range rr.srv.Mutations() {
		got = append(got, m.Method+" "+m.Path+"?"+m.Query)
	}
	want := []string{
		"POST /containers/" + rr.oldID + "/stop?",
		"POST /containers/" + rr.oldID + "/rename?name=" + prev,
		"POST /containers/create?name=web",
		"POST /containers/" + newID + "/start?",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requests:\n got %q\nwant %q", got, want)
	}
	if !logHas(j, "rename container web to "+prev) || !logHas(j, "the old container is "+prev+", stopped. Nothing was deleted.") ||
		!logHas(j, "create container web from shop/web:1 ("+engine.ShortID(newID)+")") {
		t.Errorf("log: %q", j.Log)
	}
	for _, l := range append(j.Log, j.Err, j.Title, j.Target, j.Status) {
		if strings.Contains(l, sentinel) {
			t.Errorf("job shows the value: %q", l)
		}
	}

	// The new container is the old one's configuration on the new build,
	// and the derivation applied to it gives the same configuration back.
	d, _ := rr.eng.InspectContainer(context.Background(), newID)
	if d.ImageID != rr.newImg {
		t.Errorf("new container runs %s", d.ImageID)
	}
	base, _ := rr.eng.InspectImage(context.Background(), d.ImageID)
	again, refused, err := Clone(d, base, CloneDefaults{})
	if err != nil || len(refused) != 0 {
		t.Fatal(err, refused)
	}
	spec.FromImage, again.FromImage = nil, nil
	if !reflect.DeepEqual(again, spec) {
		t.Errorf("derived again:\n got %+v\nwant %+v", again, spec)
	}
	if !reflect.DeepEqual(spec.Env, []string{"LOG_LEVEL=info", "TOKEN=" + sentinel}) {
		t.Errorf("env %q", spec.Env)
	}

	// And it can be re-created in turn.
	newer := "sha256:" + rr.srv.MoveTag("shop/web:1", 11_000_000)
	p2, _ := rr.plan(newID)
	if j := rr.run(p2); j.State != jobs.Succeeded {
		t.Fatalf("second re-create: %+v", j)
	}
	d3, _ := rr.eng.InspectContainer(context.Background(), "web")
	if d3.ImageID != newer || rr.srv.ContainerID(prevName(newID)) != newID {
		t.Errorf("second re-create: web runs %s; %s", d3.ImageID, rr.srv.ContainerNames())
	}
}

func TestRecreateOfAStoppedContainerSendsNoStop(t *testing.T) {
	rr := newRecreateRig(t, false)
	p, _ := rr.plan(rr.oldID)
	if j := rr.run(p); j.State != jobs.Succeeded {
		t.Fatalf("job: %+v", j)
	}
	for _, m := range rr.srv.Mutations() {
		if strings.HasSuffix(m.Path, "/stop") {
			t.Errorf("a stop was sent: %s", m)
		}
	}
}

func TestRecreatePinnedTagMovedSendsNothing(t *testing.T) {
	rr := newRecreateRig(t, true)
	p, _ := rr.plan(rr.oldID)
	moved := rr.srv.MoveTag("shop/web:1", 12_000_000)
	rr.srv.ResetRequests()
	j := rr.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "the tag moved") || !strings.Contains(j.Err, "nothing was stopped, renamed or created") ||
		!logHas(j, engine.ShortID(moved)) || !logHas(j, engine.ShortID(rr.newImg)) {
		t.Fatalf("job: %+v", j)
	}
	if n := len(rr.srv.Mutations()); n != 0 {
		t.Fatalf("%d mutating requests sent after the tag moved", n)
	}
	if st, _ := rr.srv.ContainerState("web"); st != "running" || rr.srv.ContainerID("web") != rr.oldID {
		t.Error("the container was touched")
	}

	// The reference no longer resolves at all.
	rr2 := newRecreateRig(t, true)
	p2, _ := rr2.plan(rr2.oldID)
	rr2.srv.FailNext("GET", `^/images/shop/web:1/json$`, 404, "No such image: shop/web:1", 1)
	j = rr2.run(p2)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "no longer names an image") || len(rr2.srv.Mutations()) != 0 {
		t.Fatalf("gone: %+v %v", j, rr2.srv.Mutations())
	}
}

func TestRecreateTagMovedBeforeTheCreateCreatesNothing(t *testing.T) {
	rr := newRecreateRig(t, true)
	p, _ := rr.plan(rr.oldID)
	hook := &renameHook{Engine: rr.eng, after: func() { rr.srv.MoveTag("shop/web:1", 13_000_000) }}
	before := len(rr.srv.CreateRequests())
	j := rr.runOn(hook, p)
	if j.State != jobs.Failed || !logHas(j, "now names") || !logHas(j, "nothing was created") || !logHas(j, "stays stopped as "+prevName(rr.oldID)) {
		t.Fatalf("job: %+v", j)
	}
	if len(rr.srv.CreateRequests()) != before {
		t.Error("a create was sent")
	}
}

// renameHook acts right after the rename succeeds.
type renameHook struct {
	engine.Engine
	after func()
}

func (h *renameHook) RenameContainer(ctx context.Context, id, name string) error {
	err := h.Engine.RenameContainer(ctx, id, name)
	if err == nil && h.after != nil {
		h.after()
	}
	return err
}

func TestRecreateFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		method    string
		path      string
		message   string
		says      []string
		skipped   int
		oldName   string // where the old container is afterwards; "prev" for the derived name
		oldState  string
		newExists bool
	}{
		{"stop", "POST", `/stop$`, "cannot stop", []string{"✗ stop container web — cannot stop", "web is left as it was", "↷ skipped rename container web", "↷ skipped create container web", "↷ skipped start container web"}, 3, "web", "running", false},
		{"rename", "POST", `/rename$`, "name in use", []string{"✗ rename container web to", "web is stopped and keeps its name; nothing was created", "↷ skipped create container web"}, 2, "web", "exited", false},
		{"create", "POST", `^/containers/create$`, "invalid TOKEN=" + sentinel + " in request", []string{"✗ create container web from shop/web:1", "‹value hidden›", "stays stopped as web-previous-", "`docker rename", "↷ skipped start container web"}, 1, "prev", "exited", false},
		{"start", "POST", `^/containers/[0-9a-f]+/start$`, "port is already allocated", []string{"✗ start container web", "port is already allocated", "was created but not started", "The old container stays stopped as web-previous-"}, 0, "prev", "exited", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := newRecreateRig(t, true)
			p, _ := rr.plan(rr.oldID)
			rr.srv.FailNext(tc.method, tc.path, 500, tc.message, 1)
			j := rr.run(p)
			if j.State != jobs.Failed {
				t.Fatalf("job %+v", j)
			}
			for _, s := range tc.says {
				if !logHas(j, s) {
					t.Errorf("log does not say %q:\n%s", s, strings.Join(j.Log, "\n"))
				}
			}
			if !logHas(j, "skipped") && tc.skipped > 0 {
				t.Errorf("nothing skipped")
			}
			old := tc.oldName
			if old == "prev" {
				old = prevName(rr.oldID)
			}
			if rr.srv.ContainerID(old) != rr.oldID {
				t.Errorf("old container is not %q: %v", old, rr.srv.ContainerNames())
			}
			if st, _ := rr.srv.ContainerState(rr.oldID); st != tc.oldState {
				t.Errorf("old container %q, want %q", st, tc.oldState)
			}
			newID := rr.srv.ContainerID("web")
			if tc.newExists != (newID != "" && newID != rr.oldID) {
				t.Errorf("new container exists: %v (%v)", !tc.newExists, rr.srv.ContainerNames())
			}
			for _, l := range append(j.Log, j.Err, j.Status) {
				if strings.Contains(l, sentinel) {
					t.Errorf("value shown: %q", l)
				}
			}
			for _, m := range rr.srv.Mutations() {
				if m.Method == "DELETE" {
					t.Errorf("something was deleted: %s", m)
				}
			}
		})
	}
}

func TestRecreateCancelledAfterTheRename(t *testing.T) {
	rr := newRecreateRig(t, true)
	p, _ := rr.plan(rr.oldID)
	before := len(rr.srv.CreateRequests())
	id := rr.jm.Start(p.JobTitle, p.JobTarget, func(jctx context.Context, run *jobs.Run) error {
		ctx, cancel := context.WithCancel(jctx)
		defer cancel()
		return Execute(ctx, &renameHook{Engine: rr.eng, after: cancel}, p, run)
	})
	rr.jm.Wait()
	j, _ := rr.jm.Get(id)
	if j.State != jobs.Cancelled && j.State != jobs.Failed {
		t.Fatalf("job %+v", j)
	}
	if !logHas(j, "already done: rename container web to "+prevName(rr.oldID)) || !logHas(j, "already done: stop container web") {
		t.Errorf("log: %q", j.Log)
	}
	if len(rr.srv.CreateRequests()) != before {
		t.Error("a create was sent after the cancel")
	}
	for _, m := range rr.srv.Mutations() {
		if strings.HasSuffix(m.Path, "/start") || m.Path == "/containers/create" {
			t.Errorf("sent after the cancel: %s", m)
		}
	}
}
