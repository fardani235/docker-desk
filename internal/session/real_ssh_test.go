package session

import (
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/sshtest"
)

// The same as TestPickedSSHNeverAsks with the real ssh client and a real
// sshd: a key that has a passphrase, and a host key that is not known. In
// both cases nothing is asked, the member is shown disconnected with what
// ssh said, and once the reason is gone it connects. Skipped without
// OpenSSH.
func TestPickedSSHNeverAsksRealSSH(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{KeyPassphrase: "correct horse"})
	a, b := startFake(t, "a"), startFake(t, "far-away")
	s := newSession(t, Config{DialOptions: DialOptions{SSHCommand: sshd.Command(), SSHTimeout: 10 * time.Second}})
	ma := launch(t, s, a, false)

	m := add(t, s, endpoint(sshd.URL(b.Socket()), "far"))
	waitFor(t, "disconnected", func() bool { return m.State() == Disconnected })
	if r := m.Reason(); !strings.Contains(strings.ToLower(r), "permission denied") {
		t.Fatalf("reason %q, want ssh's refusal", r)
	}
	if s.Focused() != ma || ma.State() != Connected {
		t.Fatal("the session stopped working")
	}
	if _, err := s.Remove(m.ID()); err != nil {
		t.Fatal(err)
	}
	<-m.Closed()
}

func TestPickedSSHUnknownHostKeyRealSSH(t *testing.T) {
	sshd := sshtest.Start(t, sshtest.Options{})
	sshd.ForgetHostKey()
	b := startFake(t, "far-away")
	s := newSession(t, Config{DialOptions: DialOptions{SSHCommand: sshd.Command(), SSHTimeout: 10 * time.Second}})
	s.Start()
	m := add(t, s, endpoint(sshd.URL(b.Socket()), "far"))
	waitFor(t, "disconnected", func() bool { return m.State() == Disconnected })
	if r := m.Reason(); !strings.Contains(strings.ToLower(r), "host key") {
		t.Fatalf("reason %q, want the host key", r)
	}
}
