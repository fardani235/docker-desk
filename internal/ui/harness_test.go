package ui

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
)

func TestMain(m *testing.M) {
	engine.InstallTestGuard()
	os.Exit(m.Run())
}

// bench is a miniature Bubble Tea runtime for tests. Commands run on their
// own goroutines exactly as in the real runtime, but every Update and View
// happens on the test's goroutine, so the model can be inspected between
// steps without a data race and without a terminal.
type bench struct {
	t    testing.TB
	srv  *fakeengine.Server
	eng  engine.Engine
	m    *Model
	msgs chan tea.Msg
	quit bool
	// execs counts terminal hand-offs (tea.Exec) the model asked for.
	execs int
	// steps counts messages delivered to Update: everything that travelled
	// through the message queue.
	steps int
}

type benchOpts struct {
	seed     func(*fakeengine.Server)
	readOnly bool
	width    int
	height   int
	// frame overrides the 5 ms test frame interval (0 keeps it).
	frame time.Duration
	// stats overrides the fake's 20 ms statistics interval (0 keeps it).
	stats time.Duration
	// arm is the confirmation arming delay; the default (0) disables it so
	// that tests do not have to wait. TestConfirmationsMustBeReadFirst sets it.
	arm time.Duration
	// endpoint, when set, is what the desk is told it talks to, while the
	// engine underneath stays the fake: how a remote daemon is presented can
	// be tested without one.
	endpoint *dockerctx.Endpoint
	// theme names the palette ("" leaves the default).
	theme string
	// linkProblem stands in for the SSH tunnel's report on itself.
	linkProblem func() string
}

func newBench(t testing.TB, o benchOpts) *bench {
	t.Helper()
	srv := fakeengine.New()
	srv.StatsInterval = 20 * time.Millisecond
	srv.PullStepDelay = 2 * time.Millisecond
	if o.stats > 0 {
		srv.StatsInterval = o.stats
	}
	if o.seed != nil {
		o.seed(srv)
	} else {
		srv.SeedDemo()
	}
	if o.frame == 0 {
		o.frame = 5 * time.Millisecond
	}
	if o.arm == 0 {
		o.arm = -1
	}
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	eng, err := engine.New(engine.Options{Host: srv.Host(), ReadOnly: o.readOnly})
	if err != nil {
		t.Fatal(err)
	}
	ep := dockerctx.Endpoint{Host: srv.Host(), Context: "default", Source: "test"}
	if o.endpoint != nil {
		ep = *o.endpoint
	}
	var theme Theme
	if o.theme != "" {
		if theme, err = ThemeByName(o.theme); err != nil {
			t.Fatal(err)
		}
	}
	m := New(Options{
		Engine:        eng,
		Endpoint:      ep,
		Info:          engine.Info{Name: "sandbox", ServerVersion: fakeengine.EngineVersion, APIVersion: "1.56"},
		ReadOnly:      o.readOnly,
		Theme:         theme,
		LinkProblem:   o.linkProblem,
		FrameInterval: o.frame,
		Debounce:      20 * time.Millisecond,
		PollInterval:  time.Hour, // tests refresh through events, or ask
		SelectDelay:   10 * time.Millisecond,
		ArmDelay:      o.arm,
	})
	m.hub.MinBackoff, m.hub.MaxBackoff = 10*time.Millisecond, 40*time.Millisecond
	b := &bench{t: t, srv: srv, eng: eng, m: m, msgs: make(chan tea.Msg, 4096)}
	t.Cleanup(func() { m.Close(); eng.Close() })
	if o.width == 0 {
		o.width, o.height = 140, 40
	}
	b.exec(m.Init())
	b.step(tea.WindowSizeMsg{Width: o.width, Height: o.height})
	b.until("first data", func() bool {
		return m.loaded[0] && m.loaded[1] && m.loaded[2] && m.loaded[3] && srv.EventSubscribers() == 1
	})
	b.settle()
	return b
}

func (b *bench) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if msg != nil {
			b.msgs <- msg
		}
	}()
}

func (b *bench) step(msg tea.Msg) {
	switch v := msg.(type) {
	case tea.BatchMsg:
		for _, c := range v {
			b.exec(c)
		}
		return
	case tea.QuitMsg:
		b.quit = true
		return
	}
	// tea.Exec produces an unexported message the real runtime turns into a
	// terminal hand-off; count it instead.
	if reflect.TypeOf(msg).String() == "tea.execMsg" {
		b.execs++
		return
	}
	b.steps++
	_, cmd := b.m.Update(msg)
	b.exec(cmd)
}

// until processes messages until cond holds.
func (b *bench) until(what string, cond func() bool) {
	b.t.Helper()
	deadline := time.After(8 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case msg := <-b.msgs:
			b.step(msg)
		case <-deadline:
			b.t.Fatalf("timed out waiting for %s\n%s", what, b.screen())
		}
	}
}

// settle processes messages for a short while, letting frames tick.
func (b *bench) settle() { b.run(60 * time.Millisecond) }

func (b *bench) run(d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case msg := <-b.msgs:
			b.step(msg)
		case <-deadline:
			return
		}
	}
}

func keyMsg(s string) tea.KeyMsg {
	special := map[string]tea.KeyType{
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "home": tea.KeyHome, "end": tea.KeyEnd,
		"ctrl+c": tea.KeyCtrlC, "backspace": tea.KeyBackspace, " ": tea.KeySpace,
	}
	if t, ok := special[s]; ok {
		if t == tea.KeySpace {
			return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		}
		return tea.KeyMsg{Type: t}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// keys presses keys one after another, letting the model react to each.
func (b *bench) keys(keys ...string) {
	b.t.Helper()
	for _, k := range keys {
		b.step(keyMsg(k))
		// Anything a key triggers (a plan being prepared) is allowed to finish.
		b.until("idle after key "+k, func() bool { return !b.m.preparing })
	}
}

// typeText types a string one rune at a time.
func (b *bench) typeText(s string) {
	for _, r := range s {
		b.keys(string(r))
	}
}

func (b *bench) screen() string { return ansi.Strip(b.m.View()) }

func (b *bench) see(subs ...string) {
	b.t.Helper()
	b.until("screen to show "+strings.Join(subs, " + "), func() bool {
		s := b.screen()
		for _, sub := range subs {
			if !strings.Contains(s, sub) {
				return false
			}
		}
		return true
	})
}

func (b *bench) notSee(sub string) {
	b.t.Helper()
	b.until("screen to no longer show "+sub, func() bool { return !strings.Contains(b.screen(), sub) })
}

// selectRow moves the highlight to the row with the given name.
func (b *bench) selectRow(name string) {
	b.t.Helper()
	b.m.ensureRows(b.m.kind)
	for i, r := range b.m.rows[b.m.kind] {
		if r.name == name {
			b.m.selectIndex(i)
			b.m.dirty = true
			b.m.afterChange()
			b.settle()
			return
		}
	}
	b.t.Fatalf("no row named %q in %s; rows: %v", name, kindNames[b.m.kind], b.rowNames())
}

func (b *bench) rowNames() []string {
	var out []string
	for _, r := range b.m.rows[b.m.kind] {
		out = append(out, r.name)
	}
	return out
}

func (b *bench) kind(k Kind) {
	b.t.Helper()
	b.m.setKind(k)
	b.m.dirty = true
	b.m.afterChange()
	b.settle()
}

// assertFrameGeometry checks the invariant every frame must satisfy: exactly
// height lines, each exactly width cells — otherwise panes tear.
func (b *bench) assertFrameGeometry() {
	b.t.Helper()
	b.m.dirty = true
	lines := strings.Split(b.m.View(), "\n")
	if len(lines) != b.m.height {
		b.t.Fatalf("frame has %d lines, terminal has %d", len(lines), b.m.height)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != b.m.width {
			b.t.Fatalf("line %d is %d cells wide, terminal is %d:\n%q\n\n%s", i, w, b.m.width, ansi.Strip(l), b.screen())
		}
	}
}

func (b *bench) dump(name string) {
	if os.Getenv("DD_SHOW") != "" {
		fmt.Printf("\n===== %s =====\n%s\n", name, b.screen())
	}
}
