package ui

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/authconfig"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
)

const (
	uiPushRef      = "host.test:5000/acme/api:3.28.3"
	uiPushPassword = "pa55-only-in-config"
	helperSecret   = "helper-secret-value-xyz"
)

func pushSeed(s *fakeengine.Server) {
	s.PushStepDelay = time.Millisecond
	s.AddImage(30_000_000, 0, uiPushRef, "host.test:5000/acme/api:latest")
	s.AddImage(5_000_000, time.Hour) // untagged
	s.AddImage(1_000_000, 0, "other/thing:1")
}

// pushConfig writes a Docker configuration directory holding body.
func pushConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func authsConfig(t *testing.T) string {
	t.Helper()
	enc := base64.StdEncoding.EncodeToString([]byte("ci:" + uiPushPassword))
	return pushConfig(t, `{"auths":{"host.test:5000":{"auth":"`+enc+`"}}}`)
}

// fakeHelper installs docker-credential-<name> on PATH: script is its body.
func fakeHelper(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "docker-credential-"+name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// countingHelper installs docker-credential-deskcount, which records that
// it ran and answers with a credential; ran reports whether it was run.
func countingHelper(t *testing.T) (ran func() bool) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "ran")
	fakeHelper(t, "deskcount", `cat >/dev/null; : > "`+marker+`"; printf '{"Username":"ci","Secret":"`+helperSecret+`"}'`)
	return func() bool { _, err := os.Stat(marker); return err == nil }
}

func helperConfig(t *testing.T) string {
	t.Helper()
	return pushConfig(t, `{"credHelpers":{"host.test:5000":"deskcount"}}`)
}

func newPushBench(t *testing.T, o benchOpts) *bench {
	t.Helper()
	if o.seed == nil {
		o.seed = pushSeed
	}
	b := newBench(t, o)
	b.kind(KindImages)
	b.m.focus = PaneList
	return b
}

// pushDialog highlights row and presses p; it fails unless the typed push
// confirmation opened.
func (b *bench) pushDialog(row string) *dialog {
	b.t.Helper()
	b.selectRow(row)
	b.keys("p")
	d := b.m.dlg
	if d == nil || d.kind != dlgPlan || len(d.plan.Steps) != 1 || d.plan.Steps[0].Op != plan.OpPushImage {
		b.t.Fatalf("p on %s opened no push confirmation:\n%s", row, b.screen())
	}
	return d
}

// noSecret fails if any credential value is in s.
func noSecret(t *testing.T, where, s string) {
	t.Helper()
	for _, v := range []string{uiPushPassword, helperSecret, base64.StdEncoding.EncodeToString([]byte("ci:" + uiPushPassword))} {
		if strings.Contains(s, v) {
			t.Fatalf("a credential value appears in %s:\n%s", where, s)
		}
	}
}

func pushJobLog(b *bench, id int) string {
	j, _ := b.m.jobs.Get(id)
	return strings.Join(j.Log, "\n") + "\n" + j.Status + "\n" + j.Err
}

func TestPushKeyConfirmAndPush(t *testing.T) {
	cfg := authsConfig(t)
	b := newPushBench(t, benchOpts{dockerConfig: cfg})
	d := b.pushDialog(uiPushRef)
	if len(b.srv.PushRequests()) != 0 {
		t.Fatal("a push was sent when the dialog opened")
	}
	p := d.plan
	if p.Tier != plan.TierTyped || p.Word != "3.28.3" || p.CredSource != "auths" || p.Registry != "host.test:5000" || !p.Runnable() {
		t.Fatalf("plan: tier %v word %q source %q registry %q", p.Tier, p.Word, p.CredSource, p.Registry)
	}
	text := b.dialogText()
	for _, s := range []string{
		"Push image", uiPushRef, "(" + p.Target.ID + ")", "registry host.test:5000", "login auths",
		"remote tag 3.28.3 is created, or moved", "cannot be undone from this desk",
		"local image and its tags are not changed", "Nothing is built",
		"Type 3.28.3 and press Enter",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("dialog lacks %q:\n%s", s, text)
		}
	}
	noSecret(t, "the dialog", text)
	noSecret(t, "the screen", b.screen())

	// y is not consent; Enter without the tag is not either.
	b.keys("y")
	b.keys("backspace")
	b.keys("enter")
	if b.m.dlg == nil || len(b.srv.PushRequests()) != 0 {
		t.Fatal("y or a bare Enter sent the push or closed the dialog")
	}
	// The wrong word.
	b.typeText("3.28.2")
	b.keys("enter")
	if b.m.dlg == nil || !b.m.dlg.mismatch || len(b.srv.PushRequests()) != 0 {
		t.Fatal("the wrong word sent the push")
	}
	if !strings.Contains(b.dialogText(), `That is not "3.28.3". Nothing was sent.`) {
		t.Fatalf("no mismatch line:\n%s", b.dialogText())
	}
	for range "3.28.2" {
		b.keys("backspace")
	}
	b.typeText("3.28.3")
	b.keys("enter")
	if b.m.dlg != nil {
		t.Fatalf("the dialog stayed open:\n%s", b.screen())
	}
	j := b.waitJob(b.lastJob().ID)
	if j.State != jobs.Succeeded {
		t.Fatalf("job: %+v\n%s", j, pushJobLog(b, j.ID))
	}
	reqs := b.srv.PushRequests()
	if len(reqs) != 1 || reqs[0].Ref != uiPushRef || reqs[0].Tag != "3.28.3" {
		t.Fatalf("push requests: %+v", reqs)
	}
	ac, err := authconfig.Decode(reqs[0].Auth)
	if err != nil || ac.Username != "ci" || ac.Password != uiPushPassword || ac.ServerAddress != "host.test:5000" {
		t.Fatalf("X-Registry-Auth: %+v %v", ac, err)
	}
	log := pushJobLog(b, j.ID)
	digest := b.srv.PushedManifest(uiPushRef)
	for _, s := range []string{"digest " + digest, plan.PushUnchangedNote, "credential: auths"} {
		if !strings.Contains(log, s) {
			t.Errorf("job lacks %q:\n%s", s, log)
		}
	}
	noSecret(t, "the job", log)
	// Only that reference: the image's other tag was not pushed, and the
	// local tags are as they were.
	if refs := strings.Join(b.srv.ImageRefs(), " "); !strings.Contains(refs, uiPushRef) || !strings.Contains(refs, "host.test:5000/acme/api:latest") {
		t.Fatalf("local tags: %s", refs)
	}
	if b.srv.PushedManifest("host.test:5000/acme/api:latest") != "" {
		t.Fatal("another tag was pushed")
	}
}

func TestPushFromTheMenu(t *testing.T) {
	b := newPushBench(t, benchOpts{dockerConfig: authsConfig(t)})
	b.selectRow(uiPushRef)
	b.keys("enter")
	if b.m.dlg == nil || b.m.dlg.kind != dlgMenu {
		t.Fatal("no menu")
	}
	idx := -1
	for i, a := range b.m.dlg.menu {
		if a.key == "p" && a.label == "push…" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("the menu has no push:\n%s", b.dialogText())
	}
	for i := 0; i < idx; i++ {
		b.keys("down")
	}
	b.keys("enter")
	if d := b.m.dlg; d == nil || d.kind != dlgPlan || d.plan.Word != "3.28.3" {
		t.Fatalf("the menu's push opened no confirmation:\n%s", b.screen())
	}
	if len(b.srv.PushRequests()) != 0 {
		t.Fatal("a push was sent")
	}
}

func TestPushRefusals(t *testing.T) {
	t.Run("untagged", func(t *testing.T) {
		b := newPushBench(t, benchOpts{dockerConfig: authsConfig(t)})
		var untagged string
		for _, n := range b.rowNames() {
			if strings.HasPrefix(n, "<untagged>") {
				untagged = n
			}
		}
		b.selectRow(untagged)
		b.keys("p")
		if b.m.dlg != nil || !strings.Contains(b.m.status.text, "no tag to push") {
			t.Fatalf("untagged: dialog %v status %q", b.m.dlg != nil, b.m.status.text)
		}
		if len(b.srv.PushRequests()) != 0 || len(b.srv.Mutations()) != 0 {
			t.Fatal("something was sent")
		}
	})
	t.Run("selection", func(t *testing.T) {
		b := newPushBench(t, benchOpts{dockerConfig: authsConfig(t)})
		b.selectRow(uiPushRef)
		b.keys(" ")
		b.selectRow("other/thing:1")
		b.keys(" ")
		b.keys("p")
		if b.m.dlg != nil || !strings.Contains(b.m.status.text, "one object at a time") {
			t.Fatalf("selection: dialog %v status %q", b.m.dlg != nil, b.m.status.text)
		}
		if len(b.srv.PushRequests()) != 0 {
			t.Fatal("pushed")
		}
	})
	t.Run("read-only", func(t *testing.T) {
		ran := countingHelper(t)
		b := newPushBench(t, benchOpts{readOnly: true, dockerConfig: helperConfig(t)})
		b.selectRow(uiPushRef)
		b.keys("p")
		if b.m.dlg != nil || !strings.Contains(b.m.status.text, "Read-only mode") {
			t.Fatalf("read-only: dialog %v status %q", b.m.dlg != nil, b.m.status.text)
		}
		if ran() {
			t.Fatal("read-only ran the credential helper")
		}
		if len(b.srv.PushRequests()) != 0 || len(b.srv.Mutations()) != 0 {
			t.Fatal("something was sent")
		}
	})
	t.Run("no-registry", func(t *testing.T) {
		ran := countingHelper(t)
		b := newPushBench(t, benchOpts{noRegistry: true, dockerConfig: helperConfig(t)})
		b.selectRow(uiPushRef)
		b.keys("p")
		if b.m.dlg != nil || b.m.status.text != pushRefusedNoRegistry {
			t.Fatalf("--no-registry: dialog %v status %q", b.m.dlg != nil, b.m.status.text)
		}
		if ran() {
			t.Fatal("--no-registry ran the credential helper")
		}
		if len(b.srv.PushRequests()) != 0 || len(b.srv.Mutations()) != 0 {
			t.Fatal("something was sent")
		}
	})
}

func TestPushCredentialSources(t *testing.T) {
	t.Run("no credential", func(t *testing.T) {
		b := newPushBench(t, benchOpts{dockerConfig: pushConfig(t, "")})
		d := b.pushDialog(uiPushRef)
		text := b.dialogText()
		if d.plan.CredSource != "" || !strings.Contains(text, "login none found") || !strings.Contains(text, "No credential for host.test:5000 was found") {
			t.Fatalf("dialog:\n%s", text)
		}
		if !d.plan.Runnable() {
			t.Fatal("a push without a credential may still be attempted")
		}
		b.typeText("3.28.3")
		b.keys("enter")
		j := b.waitJob(b.lastJob().ID)
		if j.State != jobs.Succeeded {
			t.Fatalf("job %+v", j)
		}
		if r := b.srv.PushRequests(); len(r) != 1 || r[0].Auth != "" {
			t.Fatalf("requests %+v", r)
		}
	})
	t.Run("registry wants one", func(t *testing.T) {
		b := newPushBench(t, benchOpts{dockerConfig: pushConfig(t, ""), seed: func(s *fakeengine.Server) {
			pushSeed(s)
			s.RequirePushAuth("host.test:5000/acme/api")
		}})
		b.pushDialog(uiPushRef)
		b.typeText("3.28.3")
		b.keys("enter")
		j := b.waitJob(b.lastJob().ID)
		log := pushJobLog(b, j.ID)
		if j.State != jobs.Failed || !strings.Contains(log, "unauthorized: authentication required") || !strings.Contains(log, plan.PushUnchangedNote) {
			t.Fatalf("job %+v\n%s", j, log)
		}
	})
	t.Run("credential helper", func(t *testing.T) {
		fakeHelper(t, "desktest", `cat >/dev/null; printf '{"Username":"ci","Secret":"`+helperSecret+`"}'`)
		b := newPushBench(t, benchOpts{dockerConfig: pushConfig(t, `{"credHelpers":{"host.test:5000":"desktest"}}`)})
		d := b.pushDialog(uiPushRef)
		if d.plan.CredSource != "credHelpers desktest" || !strings.Contains(b.dialogText(), "login credHelpers desktest") {
			t.Fatalf("source %q\n%s", d.plan.CredSource, b.dialogText())
		}
		noSecret(t, "the dialog", b.dialogText())
		b.typeText("3.28.3")
		b.keys("enter")
		j := b.waitJob(b.lastJob().ID)
		if j.State != jobs.Succeeded {
			t.Fatalf("job %+v\n%s", j, pushJobLog(b, j.ID))
		}
		ac, err := authconfig.Decode(b.srv.PushRequests()[0].Auth)
		if err != nil || ac.Password != helperSecret {
			t.Fatalf("the helper's credential was not sent: %+v %v", ac, err)
		}
		noSecret(t, "the job", pushJobLog(b, j.ID))
		noSecret(t, "the screen", b.screen())
	})
	t.Run("helper fails", func(t *testing.T) {
		fakeHelper(t, "deskfail", `cat >/dev/null; echo "leaked `+helperSecret+`"; echo "stderr `+helperSecret+`" >&2; exit 3`)
		b := newPushBench(t, benchOpts{dockerConfig: pushConfig(t, `{"credHelpers":{"host.test:5000":"deskfail"}}`)})
		d := b.pushDialog(uiPushRef)
		text := b.dialogText()
		if d.plan.Runnable() || !strings.Contains(text, "REFUSED") || !strings.Contains(text, "credential helper deskfail exited with status 3") {
			t.Fatalf("dialog:\n%s", text)
		}
		noSecret(t, "the dialog", text)
		b.typeText("3.28.3")
		b.keys("enter")
		if len(b.srv.PushRequests()) != 0 || len(b.m.jobs.List()) != 0 {
			t.Fatal("a refused push was sent")
		}
	})
	t.Run("helper fails when the push starts", func(t *testing.T) {
		// It answers for the dialog, then fails — printing a secret — when
		// the job reads the credential again.
		marker := filepath.Join(t.TempDir(), "once")
		fakeHelper(t, "deskflaky", `cat >/dev/null; if [ -e "`+marker+`" ]; then echo "`+helperSecret+`"; exit 4; fi; : > "`+marker+`"; printf '{"Username":"ci","Secret":"`+helperSecret+`"}'`)
		b := newPushBench(t, benchOpts{dockerConfig: pushConfig(t, `{"credHelpers":{"host.test:5000":"deskflaky"}}`)})
		b.pushDialog(uiPushRef)
		b.typeText("3.28.3")
		b.keys("enter")
		j := b.waitJob(b.lastJob().ID)
		log := pushJobLog(b, j.ID)
		if j.State != jobs.Failed || !strings.Contains(log, "exited with status 4") || !strings.Contains(log, "nothing was sent") {
			t.Fatalf("job %+v\n%s", j, log)
		}
		if len(b.srv.PushRequests()) != 0 {
			t.Fatal("pushed after the helper failed")
		}
		noSecret(t, "the job", log)
		noSecret(t, "the screen", b.screen())
	})
	t.Run("helper missing", func(t *testing.T) {
		b := newPushBench(t, benchOpts{dockerConfig: pushConfig(t, `{"credHelpers":{"host.test:5000":"desk-test-not-installed"}}`)})
		d := b.pushDialog(uiPushRef)
		if d.plan.Runnable() || !strings.Contains(b.dialogText(), "is not on PATH") {
			t.Fatalf("dialog:\n%s", b.dialogText())
		}
	})
	t.Run("source changed before the job", func(t *testing.T) {
		cfg := pushConfig(t, "")
		b := newPushBench(t, benchOpts{dockerConfig: cfg})
		b.pushDialog(uiPushRef)
		// A credential appears after the dialog said there was none.
		enc := base64.StdEncoding.EncodeToString([]byte("ci:" + uiPushPassword))
		if err := os.WriteFile(filepath.Join(cfg, "config.json"), []byte(`{"auths":{"host.test:5000":{"auth":"`+enc+`"}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		b.typeText("3.28.3")
		b.keys("enter")
		j := b.waitJob(b.lastJob().ID)
		log := pushJobLog(b, j.ID)
		if j.State != jobs.Failed || !strings.Contains(log, "credential source changed") || !strings.Contains(log, "nothing was sent") {
			t.Fatalf("job %+v\n%s", j, log)
		}
		if len(b.srv.PushRequests()) != 0 {
			t.Fatal("a credential nobody confirmed was sent")
		}
		noSecret(t, "the job", log)
	})
	t.Run("a new login from the same source is honoured", func(t *testing.T) {
		cfg := authsConfig(t)
		b := newPushBench(t, benchOpts{dockerConfig: cfg})
		b.pushDialog(uiPushRef)
		enc := base64.StdEncoding.EncodeToString([]byte("ci:rotated-" + uiPushPassword))
		if err := os.WriteFile(filepath.Join(cfg, "config.json"), []byte(`{"auths":{"host.test:5000":{"auth":"`+enc+`"}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		b.typeText("3.28.3")
		b.keys("enter")
		j := b.waitJob(b.lastJob().ID)
		if j.State != jobs.Succeeded {
			t.Fatalf("job %+v\n%s", j, pushJobLog(b, j.ID))
		}
		ac, _ := authconfig.Decode(b.srv.PushRequests()[0].Auth)
		if ac == nil || ac.Password != "rotated-"+uiPushPassword {
			t.Fatalf("the credential was not read again when the push started: %+v", ac)
		}
	})
}

func TestPushLinkWarning(t *testing.T) {
	cases := []struct {
		name string
		ep   *dockerctx.Endpoint
		want string
	}{
		{"tcp without TLS", &dockerctx.Endpoint{Host: "tcp://10.0.0.5:2375", Context: "prod", Source: "test"},
			"unencrypted tcp:// (tcp://10.0.0.5:2375): the credential (X-Registry-Auth) is sent to it over that unencrypted connection"},
		{"tls unverified", &dockerctx.Endpoint{Host: "tcp://10.0.0.5:2376", Context: "prod", Source: "test", TLS: &dockerctx.TLS{SkipVerify: true}},
			"TLS certificate is not verified"},
		{"tls verified", &dockerctx.Endpoint{Host: "tcp://10.0.0.5:2376", Context: "prod", Source: "test", TLS: &dockerctx.TLS{CAFile: "/c/ca.pem"}}, ""},
		{"unix socket", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newPushBench(t, benchOpts{dockerConfig: authsConfig(t), endpoint: c.ep})
			d := b.pushDialog(uiPushRef)
			warn := strings.Join(d.plan.Warnings, "\n")
			if c.want == "" {
				if strings.Contains(warn, "X-Registry-Auth") {
					t.Fatalf("unexpected link warning: %s", warn)
				}
				return
			}
			if !strings.Contains(warn, c.want) || !strings.Contains(b.dialogText(), "X-Registry-Auth") {
				t.Fatalf("warnings: %s\n%s", warn, b.dialogText())
			}
			if !d.plan.Runnable() {
				t.Fatal("a warning must not block")
			}
		})
	}
}

func TestPushConfirmationMustBeArmed(t *testing.T) {
	b := newPushBench(t, benchOpts{dockerConfig: authsConfig(t), arm: 400 * time.Millisecond})
	b.pushDialog(uiPushRef)
	// Typed ahead of the dialog being readable: ignored, nothing sent.
	b.typeText("3.28.3")
	b.keys("enter")
	if b.m.dlg == nil || len(b.srv.PushRequests()) != 0 {
		t.Fatal("an unarmed confirmation sent the push")
	}
	if b.m.dlg.input.Value() != "" {
		t.Fatalf("text was accepted before the dialog armed: %q", b.m.dlg.input.Value())
	}
	b.run(450 * time.Millisecond)
	b.typeText("3.28.3")
	b.keys("enter")
	j := b.waitJob(b.lastJob().ID)
	if j.State != jobs.Succeeded || len(b.srv.PushRequests()) != 1 {
		t.Fatalf("job %+v", j)
	}
}

func TestPushJob(t *testing.T) {
	t.Run("progress and success", func(t *testing.T) {
		b := newPushBench(t, benchOpts{dockerConfig: authsConfig(t), seed: func(s *fakeengine.Server) {
			pushSeed(s)
			s.PushStepDelay = 15 * time.Millisecond
		}})
		b.pushDialog(uiPushRef)
		b.typeText("3.28.3")
		b.keys("enter")
		id := b.lastJob().ID
		sawProgress := false
		b.until("layer progress", func() bool {
			j, _ := b.m.jobs.Get(id)
			if strings.Contains(j.Status, "layers ·") && j.Fraction > 0 && j.Fraction < 1 {
				sawProgress = true
			}
			return sawProgress || j.State != jobs.Running
		})
		if !sawProgress {
			t.Fatal("no layer progress was reported")
		}
		j := b.waitJob(id)
		if j.State != jobs.Succeeded || !strings.HasPrefix(j.Status, "pushed ") {
			t.Fatalf("job %+v", j)
		}
		b.see("push image", uiPushRef)
	})
	t.Run("cancel", func(t *testing.T) {
		b := newPushBench(t, benchOpts{dockerConfig: authsConfig(t), seed: func(s *fakeengine.Server) {
			pushSeed(s)
			s.PushStepDelay = 40 * time.Millisecond
		}})
		b.pushDialog(uiPushRef)
		b.typeText("3.28.3")
		b.keys("enter")
		id := b.lastJob().ID
		b.until("the push to start", func() bool { return len(b.srv.PushRequests()) == 1 })
		b.run(80 * time.Millisecond)
		b.m.jobs.Cancel(id)
		j := b.waitJob(id)
		log := pushJobLog(b, id)
		if j.State != jobs.Cancelled || !strings.Contains(log, plan.PushCancelledNote) || strings.Contains(log, "✓") {
			t.Fatalf("job %+v\n%s", j, log)
		}
		if b.srv.PushedManifest(uiPushRef) != "" {
			t.Fatal("the cancelled push completed")
		}
	})
	t.Run("registry refuses", func(t *testing.T) {
		b := newPushBench(t, benchOpts{dockerConfig: authsConfig(t), seed: func(s *fakeengine.Server) {
			pushSeed(s)
			s.RefusePush("host.test:5000/acme/api", "denied: requested access to the resource is denied")
		}})
		b.pushDialog(uiPushRef)
		b.typeText("3.28.3")
		b.keys("enter")
		j := b.waitJob(b.lastJob().ID)
		log := pushJobLog(b, j.ID)
		if j.State != jobs.Failed || !strings.Contains(j.Err, "denied: requested access to the resource is denied") ||
			!strings.Contains(log, plan.PushUnchangedNote) || strings.Contains(log, "digest") {
			t.Fatalf("job %+v\n%s", j, log)
		}
		if refs := strings.Join(b.srv.ImageRefs(), " "); !strings.Contains(refs, uiPushRef) {
			t.Fatalf("local tags changed: %s", refs)
		}
	})
}
