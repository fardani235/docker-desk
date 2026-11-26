package plan

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/fakeengine"
)

// Plans over the hostile fixture (openspec change escape-daemon-text, D3
// and D9): every string a plan shows is text — the daemon's part escaped
// once, by this package — and everything a plan sends is what the daemon
// gave.

// planText is every string of a plan that is drawn somewhere.
func planText(p Plan) []string {
	out := []string{p.Title, p.JobTitle, p.JobTarget, p.Target.Kind, p.Target.Name, p.Target.ID, p.Nothing, p.Scope, p.SuccessNote}
	out = append(out, p.Effects...)
	out = append(out, p.Warnings...)
	out = append(out, p.Blockers...)
	for _, sk := range p.Skipped {
		out = append(out, sk.Name, sk.Reason)
	}
	for _, st := range p.Steps {
		out = append(out, st.Name, st.OtherName, st.Detail, st.FailNote, describe(st))
	}
	return out
}

// inertText fails if s could act on a terminal, forge a line, reorder
// what is drawn or was escaped twice.
func inertText(t *testing.T, what, s string) {
	t.Helper()
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || escape.Reorders(r) {
			t.Errorf("%s: %U is drawn raw in %q", what, r, s)
			return
		}
	}
	if strings.Contains(s, `\\\\`) {
		t.Errorf("%s: escaped twice: %q", what, s)
	}
}

func checkPlan(t *testing.T, what string, p Plan, err error) {
	t.Helper()
	if err != nil {
		inertText(t, what+" (refusal)", err.Error())
		return
	}
	for _, s := range planText(p) {
		inertText(t, what, s)
	}
	// Every word a plan sets can be typed as it is shown.
	if p.Word != escape.Name(p.Word) {
		t.Errorf("%s: the word %q cannot be typed as shown", what, p.Word)
	}
}

func hostileRig(t *testing.T) (*rig, fakeengine.Hostile) {
	t.Helper()
	var h fakeengine.Hostile
	r := newRig(t, func(s *fakeengine.Server) {
		h = s.SeedHostile()
		s.AddContainer(fakeengine.ContainerSpec{Name: "plain", Image: "app:1", State: "exited"})
		s.AddVolume("idle"+fakeengine.HostileConceal+"vol", nil)
		s.AddNetwork("idle"+fakeengine.HostileConceal+"net", nil)
	})
	return r, h
}

func TestPlansOverTheHostileFixtureAreText(t *testing.T) {
	r, h := hostileRig(t)
	snap := r.snap()
	const name = `evil\\box\x1b]0;pwned\x07`

	// Every container action, applicable or not.
	for _, op := range []Op{OpStart, OpStop, OpRestart, OpPause, OpUnpause, OpKill, OpRemoveContainer} {
		p, err := Container(op, snap, h.ContainerID)
		checkPlan(t, "container "+op.String(), p, err)
		if err != nil {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("%v: the refusal does not name the container as text: %q", op, err)
			}
			continue
		}
		if p.Target.Name != name || p.JobTarget != name || p.Steps[0].Name != name {
			t.Errorf("%v: target %q, job %q, step %q", op, p.Target.Name, p.JobTarget, p.Steps[0].Name)
		}
		// What is sent is the identifier, as the daemon gave it.
		if p.Steps[0].ID != h.ContainerID {
			t.Errorf("%v: sends %q", op, p.Steps[0].ID)
		}
	}
	// The removal is refused while it runs, and says which container.
	p, err := Container(OpRemoveContainer, snap, h.ContainerID)
	if err != nil || len(p.Blockers) == 0 || !strings.Contains(p.Blockers[0], name) {
		t.Fatalf("remove: %v %+v", err, p.Blockers)
	}

	// The image: by its reference, which is sent as it is.
	var img engine.Image
	for _, x := range snap.Images {
		for _, ref := range x.Refs() {
			if ref == h.ImageRef {
				img = x
			}
		}
	}
	p, err = ImageRemove(snap, img.ID, h.ImageRef)
	checkPlan(t, "image remove", p, err)
	if err != nil || p.Steps[0].ID != h.ImageRef || p.Steps[0].Name != escape.Name(h.ImageRef) {
		t.Fatalf("image remove: %v %+v", err, p.Steps)
	}
	if len(p.Blockers) == 0 || !strings.Contains(p.Blockers[0], name) {
		t.Fatalf("image remove names its users: %+v", p.Blockers)
	}
	checkPlan(t, "image prune", ImagePrune(snap, true), nil)
	_, err = Push(snap, img.ID, h.ImageRef)
	checkPlan(t, "push", Plan{}, err)
	_, err = ImageUntag(snap, img.ID, h.ImageRef)
	checkPlan(t, "untag", Plan{}, err)

	// The volume and the network: in use, so refused, naming the user.
	p, err = VolumeRemove(snap, h.Volume)
	checkPlan(t, "volume remove", p, err)
	if err != nil || p.Steps[0].ID != h.Volume || p.Target.Name != `vol\x1b[8mdata` {
		t.Fatalf("volume remove: %v %+v", err, p)
	}
	vp := VolumePrune(snap, true)
	checkPlan(t, "volume prune", vp, nil)
	if len(vp.Steps) != 1 || vp.Steps[0].ID != "idle"+fakeengine.HostileConceal+"vol" || vp.Steps[0].Name != `idle\x1b[8mvol` {
		t.Fatalf("volume prune: %+v", vp.Steps)
	}
	p, err = NetworkRemove(snap, h.NetworkID)
	checkPlan(t, "network remove", p, err)
	np := NetworkPrune(snap)
	checkPlan(t, "network prune", np, nil)
	if len(np.Steps) != 1 || np.Steps[0].Name != `idle\x1b[8mnet` {
		t.Fatalf("network prune: %+v", np.Steps)
	}

	// Connect and disconnect: two names in one line.
	p, err = NetworkDisconnect(snap, h.NetworkID, h.ContainerID)
	checkPlan(t, "disconnect", p, err)
	if err != nil || p.Steps[0].ID != h.NetworkID || p.Steps[0].Other != h.ContainerID {
		t.Fatalf("disconnect: %v %+v", err, p.Steps)
	}
	if got := describe(p.Steps[0]); got != "disconnect "+name+` from net\x1b[8mlan` {
		t.Fatalf("disconnect is described as %q", got)
	}
	_, err = NetworkConnect(snap, h.NetworkID, h.ContainerID)
	checkPlan(t, "connect (already connected)", Plan{}, err)
	if err == nil || !strings.Contains(err.Error(), name) {
		t.Fatalf("connect: %v", err)
	}
	var bridge string
	for _, n := range snap.Networks {
		if n.Name == "bridge" {
			bridge = n.ID
		}
	}
	p, err = NetworkConnect(snap, bridge, h.ContainerID)
	checkPlan(t, "connect", p, err)
	if err != nil || describe(p.Steps[0]) != "connect "+name+" to bridge" {
		t.Fatalf("connect: %v %+v", err, p.Steps)
	}

	// The project is a label's value: found by it, shown as text.
	p, err = ProjectStop(snap, h.Project)
	checkPlan(t, "project stop", p, err)
	if err != nil || len(p.Steps) != 1 || p.Steps[0].ID != h.ContainerID || p.JobTarget != `proj\u009b2Jx` {
		t.Fatalf("project stop: %v %+v", err, p)
	}
	p, err = ProjectRemove(snap, h.Project)
	checkPlan(t, "project remove", p, err)
	_, err = ProjectStop(snap, "gone"+fakeengine.HostileConceal)
	checkPlan(t, "project stop, gone", Plan{}, err)

	// A selection: what is left out is named as the list named it.
	members := []Member{
		{ID: h.ContainerID, Name: name},
		{ID: "gone", Name: `gone\x1b[8m`},
	}
	p, err = Set(OpRemoveContainer, snap, members)
	checkPlan(t, "set", p, err)
	if err != nil || len(p.Skipped) != 2 || p.Skipped[0].Name != name {
		t.Fatalf("set: %v %+v", err, p.Skipped)
	}
	p, err = Set(OpRemoveVolume, snap, []Member{{ID: h.Volume, Name: `vol\x1b[8mdata`}})
	checkPlan(t, "volume set", p, err)
	if err != nil || len(p.Skipped) != 1 || p.Skipped[0].Name != `vol\x1b[8mdata` {
		t.Fatalf("volume set: %v %+v", err, p.Skipped)
	}
}

// One layer escapes a value, once: a name with one backslash is described
// with exactly two. The dialog draws these strings as they are, so escaping
// here a second time — or there — would show as four.
func TestStepDescriptionsAreEscapedOnce(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) {
		s.AddContainer(fakeengine.ContainerSpec{Name: `back\slash`, Image: "app:1"})
		s.AddNetwork(`net\work`, nil)
	})
	snap := r.snap()
	var id, net string
	for _, c := range snap.Containers {
		id = c.ID
	}
	for _, n := range snap.Networks {
		if n.Name == `net\work` {
			net = n.ID
		}
	}
	two := func(what, s string, want int) {
		t.Helper()
		if n := strings.Count(s, `\`); n != want {
			t.Errorf("%s: %d backslashes, want %d: %q", what, n, want, s)
		}
	}
	p, err := Container(OpStop, snap, id)
	if err != nil {
		t.Fatal(err)
	}
	two("the target", p.Target.Name, 2)
	two("the job's target", p.JobTarget, 2)
	two("the step", describe(p.Steps[0]), 2)
	// Escaping what the plan already escaped is what the test is for.
	two("a second escape", escape.Name(describe(p.Steps[0])), 4)

	p, err = NetworkConnect(snap, net, id)
	if err != nil {
		t.Fatal(err)
	}
	two("connect", describe(p.Steps[0]), 4) // two names, two backslashes each
	two("connect's effect", p.Effects[0], 4)
	two("rename", describe(Step{Op: OpRename, Name: escape.Name(`back\slash`), Rename: `new\name`}), 4)
	two("create", describe(Step{Op: OpCreateContainer, Name: escape.Name(`back\slash`), Create: &ContainerCreateSpec{Image: "app:1"}}), 2)
	two("the fallback", describe(Step{Op: OpRemoveVolume, Kind: "volume", Name: escape.Name(`vol\ume`)}), 2)

	// And the job says it once, too.
	p, _ = Container(OpStop, snap, id)
	j := r.run(p)
	if j.Err != "" {
		t.Fatal(j.Err)
	}
	two("the job's log", strings.Join(j.Log, "\n"), 2)
}

// Escaping is display-only: the requests carry the daemon's identifiers and
// the operator's names, byte for byte.
func TestRequestsCarryRawBytes(t *testing.T) {
	r, h := hostileRig(t)
	ctx := context.Background()

	// A rename sends exactly the name it was given.
	p, err := Container(OpStop, r.snap(), h.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	if j := r.run(p); j.Err != "" {
		t.Fatal(j.Err)
	}
	if n := r.srv.CountRequests("POST", "^/containers/"+h.ContainerID+"/stop$"); n != 1 {
		t.Fatalf("the stop was sent %d times for the container's ID", n)
	}
	rename := Plan{Title: "rename", JobTitle: "rename", Tier: TierConfirm,
		Steps: []Step{{Op: OpRename, Kind: "container", ID: h.ContainerID, Name: escape.Name(h.Name), Rename: "typed-name_1.2", IDFrom: -1}}}
	if j := r.run(rename); j.Err != "" {
		t.Fatal(j.Err)
	}
	cs, err := r.eng.Containers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cs {
		names = append(names, c.Name)
	}
	if !contains(names, "typed-name_1.2") {
		t.Fatalf("the rename did not carry the typed name: %q", names)
	}

	// A volume is removed by its name as the daemon gave it.
	vp := VolumePrune(r.snap(), true)
	if j := r.run(vp); j.Err != "" {
		t.Fatal(j.Err)
	}
	for _, v := range r.srv.VolumeNames() {
		if strings.HasPrefix(v, "idle") {
			t.Fatalf("the volume %q was not removed by its real name", v)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// A daemon's refusal is the daemon's text: escaped once in the job's log
// and in its error, whatever it carries.
func TestDaemonRefusalIsEscapedInTheJob(t *testing.T) {
	r, h := hostileRig(t)
	p, err := Container(OpStop, r.snap(), h.ContainerID)
	if err != nil {
		t.Fatal(err)
	}
	r.srv.FailNext("POST", "/stop$", 500, "no"+fakeengine.HostileConceal+` C:\x`+fakeengine.HostileOSC, 1)
	j := r.run(p)
	const want = `no\x1b[8m C:\\x\x1b]0;pwned\x07`
	if j.Err != want {
		t.Fatalf("the job's error is %q, want %q", j.Err, want)
	}
	log := strings.Join(j.Log, "\n")
	inertText(t, "the job's log", log)
	if !strings.Contains(log, want) || !strings.Contains(log, `evil\\box`) {
		t.Fatalf("the job's log:\n%s", log)
	}
}

// An error the engine layer wrote about a value before sending it is not
// the daemon's message and not the plan's text: it is made text where the
// request was sent (review finding 10). A daemon's refusal is left for the
// log to escape, once.
func TestSentErrMakesAForeignErrorText(t *testing.T) {
	raw := errors.New("not a port: 80/tcp\x1b[8m")
	if got := shownMessage(sentErr(raw)); got != `not a port: 80/tcp\x1b[8m` {
		t.Fatalf("a foreign error is logged as %q", got)
	}
	daemon := &engine.Error{Op: "create container", Msg: "no\x1b[8m"}
	if got := shownMessage(sentErr(daemon)); got != `no\x1b[8m` {
		t.Fatalf("a daemon's refusal is logged as %q", got)
	}
	if sentErr(context.Canceled) != context.Canceled || sentErr(nil) != nil {
		t.Fatal("cancellation or success was rewritten")
	}
}
