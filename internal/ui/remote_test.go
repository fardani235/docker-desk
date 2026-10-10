package ui

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/dockerctx"
)

// A daemon on another machine is the likeliest way to change the wrong
// thing, so the desk says it is remote — and how safely it is reached —
// wherever it names the daemon: the header, every confirmation, every form,
// the picker, the help, and even the "terminal too small" screen.

func remoteBench(t *testing.T, ep dockerctx.Endpoint, o benchOpts) *bench {
	t.Helper()
	o.endpoint = &ep
	return newBench(t, o)
}

func TestRemoteDaemonIsAnnouncedEverywhere(t *testing.T) {
	verified := &dockerctx.TLS{CAFile: "/c/ca.pem", CertFile: "/c/cert.pem", KeyFile: "/c/key.pem"}
	cases := []struct {
		ep    dockerctx.Endpoint
		label string
		words string
	}{
		{dockerctx.Endpoint{Host: "ssh://ops@prod-1", Context: "prod", Source: "test"}, "[REMOTE ssh]", "an SSH tunnel to /var/run/docker.sock on prod-1"},
		{dockerctx.Endpoint{Host: "tcp://10.0.0.5:2376", Context: "prod", Source: "test", TLS: verified}, "[REMOTE tls]", "TCP to 10.0.0.5:2376 with TLS: daemon verified"},
		{dockerctx.Endpoint{Host: "tcp://10.0.0.5:2376", Context: "prod", Source: "test", TLS: &dockerctx.TLS{SkipVerify: true}}, "[REMOTE tls, UNVERIFIED]", "with TLS, but the daemon's certificate"},
		{dockerctx.Endpoint{Host: "tcp://10.0.0.5:2375", Context: "prod", Source: "test"}, "[REMOTE tcp, NO TLS]", "not encrypted"},
		{dockerctx.Endpoint{Host: "tcp://127.0.0.1:2375", Context: "prod", Source: "test"}, "[tcp, NO TLS]", "not encrypted"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			b := remoteBench(t, tc.ep, benchOpts{})
			daemon := tc.ep.Host + "  (context prod)  " + tc.label

			// The header, at every layout.
			for _, sz := range [][2]int{{140, 40}, {100, 30}, {80, 24}} {
				b.step(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
				header := strings.Split(b.screen(), "\n")[0]
				if !strings.Contains(header, tc.ep.Host) || !strings.Contains(header, tc.label) || !strings.Contains(header, "connected") {
					t.Fatalf("%dx%d header must carry the endpoint, %s and the connection state:\n%s", sz[0], sz[1], tc.label, header)
				}
				b.assertFrameGeometry()
			}
			b.step(tea.WindowSizeMsg{Width: 140, Height: 40})

			// A confirmation.
			b.selectRow("shop-web-1")
			b.srv.ResetRequests()
			b.keys("t")
			b.see("Stop container", daemon)
			if n := len(b.srv.Mutations()); n != 0 {
				t.Fatalf("%d mutating request(s) before the confirmation", n)
			}
			b.keys("esc")

			// A form.
			b.kind(KindImages)
			b.keys("P")
			b.see("Pull image", daemon)
			b.keys("esc")

			// The container picker.
			b.kind(KindNetworks)
			b.selectRow("shop_default")
			b.keys("c")
			b.see("daemon     "+tc.ep.Host+"  "+tc.label, "continue to confirmation")
			b.keys("esc")

			// The help.
			b.keys("?")
			b.see("Docker Desk talks to one daemon: "+tc.ep.Host, "reached over: ", tc.words)
			b.keys("esc")

			// Even when nothing else can be drawn.
			b.step(tea.WindowSizeMsg{Width: 60, Height: 20})
			b.see("daemon: "+tc.ep.Host+"  "+tc.label, "needs at least 80×24")
		})
	}
}

// TestLocalSocketLooksAsItAlwaysDid: none of the above appears for a daemon
// on this machine's own socket.
func TestLocalSocketLooksAsItAlwaysDid(t *testing.T) {
	b := newBench(t, benchOpts{})
	header := strings.Split(b.screen(), "\n")[0]
	if strings.Contains(header, "[") || strings.Contains(b.screen(), "REMOTE") {
		t.Fatalf("a local socket needs no tag:\n%s", header)
	}
	b.selectRow("shop-web-1")
	b.keys("t")
	b.see("daemon     " + b.srv.Host() + "  (context default)")
	if strings.Contains(b.screen(), "REMOTE") || strings.Contains(b.screen(), "(context default)  [") {
		t.Fatalf("a local socket needs no tag in a confirmation:\n%s", b.screen())
	}
	b.keys("esc", "?")
	b.see("reached over: a unix socket on this machine")
}

// TestRemoteTagIsNeverDropped: in the narrowest layout, with the longest
// header, the engine's name and version give way, then the title, then
// "— retrying", and at last the middle of the endpoint itself — never the
// connection state, the remote tag or the read-only flag, and never the
// endpoint's beginning (which machine) or its end (which socket).
func TestRemoteTagIsNeverDropped(t *testing.T) {
	verified := &dockerctx.TLS{CAFile: "/c/ca.pem"}
	cases := []struct {
		ep         dockerctx.Endpoint
		head, tail string
		tag        string
	}{
		{dockerctx.Endpoint{Host: "tcp://10.20.30.40:2375", Context: "production-eu-west-1", Source: "test"}, "tcp://10.20.30.40:2375", "", "[REMOTE tcp, NO TLS]"},
		// The review's examples: these lost the end of the address, or the
		// socket that tells two rootless daemons on one host apart.
		{dockerctx.Endpoint{Host: "tcp://192.168.100.200:2375", Context: "c", Source: "test"}, "tcp://192.168.100.200:2375", "", "[REMOTE tcp, NO TLS]"},
		{dockerctx.Endpoint{Host: "tcp://192.168.100.200:2376", Context: "c", Source: "test", TLS: &dockerctx.TLS{SkipVerify: true}}, "tcp://192.16", ":2376", "[REMOTE tls, UNVERIFIED]"},
		{dockerctx.Endpoint{Host: "ssh://deploy@docker-01.prod.example.com/run/user/1000/docker.sock", Context: "c", Source: "test"}, "ssh://deploy@doc", "/1000/docker.sock", "[REMOTE ssh]"},
		{dockerctx.Endpoint{Host: "ssh://deploy@docker-01.prod.example.com/run/user/1001/docker.sock", Context: "c", Source: "test"}, "ssh://deploy@doc", "/1001/docker.sock", "[REMOTE ssh]"},
		{dockerctx.Endpoint{Host: "tcp://docker.internal.example.com:2376", Context: "c", Source: "test", TLS: verified}, "tcp://docker.inter", "example.com:2376", "[REMOTE tls]"},
	}
	for _, tc := range cases {
		b := remoteBench(t, tc.ep, benchOpts{readOnly: true, width: 80, height: 24})
		check := func(state string) {
			t.Helper()
			header := strings.Split(b.screen(), "\n")[0]
			for _, want := range []string{tc.head, tc.tail, tc.tag, "[READ-ONLY]", state} {
				if !strings.Contains(header, want) {
					t.Errorf("%s, %s: %q is missing from the 80-column header:\n%s", tc.ep.Host, state, want, header)
				}
			}
			b.assertFrameGeometry()
		}
		check("● connected")
		b.srv.Stop()
		b.see("DISCONNECTED")
		check("✗ DISCONNECTED")
	}
}

// TestFallenTunnelExplainsItself: "disconnected" cannot tell a daemon that
// stopped from an SSH tunnel that fell; the tunnel's own reason is shown,
// once per reason, and not while the desk is connected.
func TestFallenTunnelExplainsItself(t *testing.T) {
	var problem atomic.Value
	problem.Store("")
	asked := atomic.Int64{}
	ep := dockerctx.Endpoint{Host: "ssh://ops@prod-1", Context: "prod", Source: "test"}
	b := remoteBench(t, ep, benchOpts{linkProblem: func() string { asked.Add(1); return problem.Load().(string) }})

	// Connected: whatever the tunnel says is not shown (and need not be asked).
	problem.Store("stale words from an earlier outage")
	b.run(60 * 1e6)
	if strings.Contains(b.screen(), "stale words") {
		t.Fatal("a tunnel problem was shown while the desk is connected")
	}

	problem.Store("Connection to prod-1 closed by remote host.")
	b.srv.Stop()
	b.see("DISCONNECTED", "The ssh link to ssh://ops@prod-1 is down — retrying: Connection to prod-1 closed by remote host.")

	// The same reason is not repeated on every frame: once the message has
	// been replaced by something else it stays replaced.
	b.m.setStatus(toneDim, "something else happened")
	b.run(80 * 1e6)
	b.see("something else happened")

	// A new reason is news.
	problem.Store("ssh: connect to host prod-1 port 22: Connection refused")
	b.see("is down — retrying: ssh: connect to host prod-1 port 22: Connection refused")

	if err := b.srv.Restart(); err != nil {
		t.Fatal(err)
	}
	problem.Store("")
	b.see("● connected", "Reconnected to the Docker daemon.")
	if asked.Load() == 0 {
		t.Fatal("the tunnel was never asked")
	}
}

// TestLongEndpointNeverHidesTheTag: an SSH endpoint that names its socket
// can be longer than a dialog is wide. The endpoint is then continued on the
// next line and the context and the tag move below it — in the header it is
// the endpoint's own tail that gives way. Found by the first end-to-end run
// over SSH, where "[ssh]" had been cut off the confirmation.
func TestLongEndpointNeverHidesTheTag(t *testing.T) {
	host := "ssh://deploy@build-runner-17.eu-west-1.internal.example.com:2222/run/user/1000/docker.sock"
	ep := dockerctx.Endpoint{Host: host, Context: "runner-17", Source: "test"}
	b := remoteBench(t, ep, benchOpts{readOnly: true})

	for _, sz := range [][2]int{{140, 40}, {100, 30}, {80, 24}} {
		b.step(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		header := strings.Split(b.screen(), "\n")[0]
		for _, want := range []string{"ssh://deploy@build", "/1000/docker.sock", "[REMOTE ssh]", "[READ-ONLY]", "connected"} {
			if !strings.Contains(header, want) {
				t.Errorf("%d columns: %q is missing from the header:\n%s", sz[0], want, header)
			}
		}
		b.assertFrameGeometry()
	}

	// A dialog (read-only mode still opens the help and the action menu;
	// for a confirmation, use a desk that may act).
	b2 := remoteBench(t, ep, benchOpts{width: 100, height: 30})
	b2.selectRow("shop-web-1")
	b2.keys("t")
	b2.see("Stop container", "(context runner-17)  [REMOTE ssh]")
	// The whole endpoint is there, across two lines of the dialog.
	flat := strings.Join(strings.Fields(strings.NewReplacer("┃", " ", "│", " ").Replace(b2.screen())), "")
	if !strings.Contains(flat, strings.ReplaceAll(host, " ", "")) {
		t.Fatalf("the endpoint is not shown in full in the confirmation:\n%s", b2.screen())
	}
	b2.assertFrameGeometry()
	b2.keys("esc")
	b2.kind(KindImages)
	b2.keys("P")
	b2.see("Pull image", "(context runner-17)  [REMOTE ssh]", "Image reference:")
	b2.assertFrameGeometry()
}

// TestLongContextNeverHidesTheTag: found by the review. With a long context
// name the context and the tag did not fit on one line of the dialog, and the
// line was cut — "[REMOTE tls, U…". Each now gets a line when it needs one.
func TestLongContextNeverHidesTheTag(t *testing.T) {
	ep := dockerctx.Endpoint{Host: "tcp://10.0.0.5:2376", Context: "production-eu-west-1-cluster-admin", Source: "test", TLS: &dockerctx.TLS{SkipVerify: true}}
	b := remoteBench(t, ep, benchOpts{width: 80, height: 24})
	b.selectRow("shop-web-1")
	b.keys("t")
	b.see("Stop container", "tcp://10.0.0.5:2376", "(context production-eu-west-1-cluster-admin)", "[REMOTE tls, UNVERIFIED]", "y proceed")
	b.assertFrameGeometry()
	b.keys("esc")
	b.kind(KindImages)
	b.keys("P")
	b.see("Pull image", "(context production-eu-west-1-cluster-admin)", "[REMOTE tls, UNVERIFIED]", "Enter pull")
	b.keys("esc")

	// A context name longer than the dialog is wide is continued, not cut.
	long := dockerctx.Endpoint{Host: "ssh://ops@prod-1", Context: strings.Repeat("very-long-context-name-", 4) + "end", Source: "test"}
	b2 := remoteBench(t, long, benchOpts{width: 80, height: 24})
	b2.selectRow("shop-web-1")
	b2.keys("t")
	b2.see("Stop container", "[REMOTE ssh]", "y proceed")
	flat := strings.Join(strings.Fields(strings.NewReplacer("┃", " ", "│", " ").Replace(b2.screen())), "")
	if !strings.Contains(flat, long.Context+")") {
		t.Fatalf("the context name is not shown in full:\n%s", b2.screen())
	}
	b2.assertFrameGeometry()
}

// TestConfirmationsFitTheSmallestTerminal: found by the review. At 80×24 a
// confirmation with much to say was taller than the terminal: the key hints,
// and for the largest the prompt and the input line themselves, were drawn
// off screen — and the extra "daemon" lines of a remote endpoint pushed two
// more dialogs over. (One overflowed before this change; the review found
// that too.) What identifies the object and the daemon, and the prompt, the
// input and the keys, now always stay on screen; what lies between scrolls.
func TestConfirmationsFitTheSmallestTerminal(t *testing.T) {
	endpoints := map[string]dockerctx.Endpoint{
		"local": {},
		"remote, long": {
			Host:    "ssh://deploy@build-runner-17.eu-west-1.internal.example.com:2222/run/user/1000/docker.sock",
			Context: "production-eu-west-1-cluster-admin", Source: "test",
		},
	}
	dialogs := []struct {
		name  string
		kind  Kind
		row   string
		keys  []string
		typed string // the word to type, for a typed confirmation
		want  []string
	}{
		{"stop", KindContainers, "shop-web-1", []string{"t"}, "", []string{"Stop container", "y proceed"}},
		{"remove container, refused", KindContainers, "shop-web-1", []string{"d"}, "", []string{"REFUSED", "Esc close"}},
		{"remove container", KindContainers, "old-migration", []string{"d"}, "remove", nil},
		{"prune images", KindImages, "", []string{"X"}, "prune", nil},
		{"prune images, all unused", KindImages, "", []string{"X", "tab"}, "prune", nil},
		{"prune volumes, all unused", KindVolumes, "", []string{"X", "tab"}, "prune", nil},
		{"prune networks", KindNetworks, "", []string{"X"}, "prune", nil},
		{"remove project", KindCompose, "blog", []string{"d"}, "remove", nil},
		{"stop project", KindCompose, "shop", []string{"t"}, "", []string{"y proceed"}},
	}
	for epName, ep := range endpoints {
		for _, dlg := range dialogs {
			t.Run(epName+"/"+dlg.name, func(t *testing.T) {
				o := benchOpts{width: 80, height: 24}
				b := newBench(t, o)
				if ep.Host != "" {
					b = remoteBench(t, ep, o)
				}
				b.kind(dlg.kind)
				if dlg.row != "" {
					b.selectRow(dlg.row)
				}
				b.keys(dlg.keys...)
				b.until("a dialog", func() bool { return b.m.dlg != nil && !b.m.dlg.recomputing })
				want := dlg.want
				if dlg.typed != "" {
					want = []string{"and press Enter to proceed:", "Enter proceed", "Esc cancel"}
				}
				screen := b.screen()
				for _, w := range want {
					if !strings.Contains(screen, w) {
						t.Fatalf("%q is not on screen at 80×24:\n%s", w, screen)
					}
				}
				// The whole box is on screen, below the header.
				if _, _, h := b.m.renderDialog(); h > b.m.height-1 {
					t.Fatalf("the dialog is %d rows high on a %d-row terminal:\n%s", h, b.m.height, screen)
				}
				// The daemon, in full, whatever else had to scroll: every
				// line that names it is on screen as it was laid out, and
				// together those lines hold the endpoint, the context and
				// the tag.
				inner := clamp(b.m.width-4, 40, dialogMaxWidth) - 4
				whole := ""
				for _, line := range b.m.daemonLines(inner, true) {
					text := strings.TrimSpace(ansi.Strip(line))
					if !strings.Contains(screen, text) {
						t.Fatalf("%q is not on screen:\n%s", text, screen)
					}
					whole += strings.TrimSpace(strings.TrimPrefix(text, "daemon"))
				}
				for _, part := range []string{b.m.endpoint.Host, "(context " + b.m.endpoint.Context + ")", b.m.linkSuffix()} {
					if !strings.Contains(strings.ReplaceAll(whole, " ", ""), strings.ReplaceAll(part, " ", "")) {
						t.Fatalf("%q is not in the dialog's daemon lines: %q", part, whole)
					}
				}
				b.assertFrameGeometry()

				if dlg.typed == "" {
					return
				}
				// What is typed is visible where it is typed...
				b.typeText(dlg.typed)
				if !strings.Contains(b.screen(), "> "+dlg.typed) {
					t.Fatalf("the typed word is not visible:\n%s", b.screen())
				}
				// ...and everything the dialog has to say can be reached.
				seen := map[string]bool{}
				for i := 0; i < 60; i++ {
					for _, line := range strings.Split(b.screen(), "\n") {
						seen[strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "┃│"))] = true
					}
					b.keys("down")
				}
				p := b.m.dlg.plan
				for _, s := range p.Steps {
					found := false
					for line := range seen {
						if strings.Contains(line, s.Name) {
							found = true
						}
					}
					if p.Listed && !found {
						t.Fatalf("%q is in the set but cannot be scrolled to", s.Name)
					}
				}
			})
		}
	}
}

// TestThemeKeyDoesNotHideADroppedAction: a key pressed while a confirmation
// is being prepared drops it, and says so. T is such a key; its own message
// must not replace that one.
func TestThemeKeyDoesNotHideADroppedAction(t *testing.T) {
	b := newBench(t, benchOpts{})
	b.selectRow("shop-web-1")
	b.srv.Stall("GET", "/containers/json", 300*time.Millisecond)
	b.step(keyMsg("t"))
	if !b.m.preparing {
		t.Fatal("expected a confirmation to be in preparation")
	}
	b.step(keyMsg("T"))
	if b.m.preparing {
		t.Fatal("the pending action survived a key")
	}
	b.see("Dropped the pending action", "Theme: light")
	b.run(500 * time.Millisecond)
	if b.m.dlg != nil {
		t.Fatal("the confirmation opened after its action was dropped")
	}
	if n := len(b.srv.Mutations()); n != 0 {
		t.Fatalf("%d mutating request(s)", n)
	}
}
