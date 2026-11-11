package ui

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The keys the README and the in-app help give for the daemon session are
// the ones the key switch answers to (keys.go: keyDaemons, keyNextFocus,
// keyPrevFocus; daemons.go: onDaemonsKey).
func TestDocumentedDaemonKeysAreTheKeySwitchs(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	for _, want := range []string{
		"| `" + keyDaemons + "` | the daemon picker",
		"| `" + keyNextFocus + "` `" + keyPrevFocus + "` | focus the next / previous connected daemon",
		"**In the daemon picker** (`" + keyDaemons + "`)",
		"`Enter` focus the daemon, or add the context",
		"`d` remove the daemon (then `y` to confirm, `n` to keep it)",
		"`r` read the contexts again",
		"`Esc`, `q` or `" + keyDaemons + "` close",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md does not say %q", want)
		}
	}

	b := newMultiBench(t, benchOpts{})
	help := strings.Join(b.m.helpLines(100), "\n")
	help = ansi.Strip(help)
	for _, want := range []string{keyDaemons + " ", keyNextFocus + "  " + keyPrevFocus, "⏎ focus a daemon or add a context · d remove · r re-read · Esc close"} {
		if !strings.Contains(help, want) {
			t.Errorf("the help does not say %q:\n%s", want, help)
		}
	}

	// And each picker key does what both say.
	beta := b.daemon("beta", nil)
	for _, closeKey := range []string{"esc", "q", keyDaemons} {
		b.keys(keyDaemons)
		b.keys(closeKey)
		if b.m.dlg != nil {
			t.Fatalf("%q does not close the picker", closeKey)
		}
	}
	b.keys(keyDaemons, "r")
	b.see("Contexts read again.")
	mem := b.add("beta", beta) // Enter on a context adds it
	b.pickerTo("m:" + itoa(mem.ID()))
	b.keys("enter") // Enter on a daemon focuses it
	if b.m.cur != mem {
		t.Fatal("Enter on a daemon did not focus it")
	}
	b.pickerTo("m:" + itoa(mem.ID()))
	b.keys("d", "n")
	if b.m.sess.Len() != 2 {
		t.Fatal("n removed the daemon")
	}
	b.keys("d", "y")
	b.until("removed", func() bool { return b.m.sess.Len() == 1 })
}

func itoa(n int) string { return strconv.Itoa(n) }
