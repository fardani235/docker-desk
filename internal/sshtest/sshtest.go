// Package sshtest runs a throwaway OpenSSH server for tests: the real sshd,
// unprivileged, on a loopback port, with its own host key and one throwaway
// client key. It lets the desk's SSH transport be proven with the real ssh
// client against a real SSH server — without the operator's ~/.ssh, an
// account's password, or another machine.
//
// What the server permits is as little as the tunnel needs: public-key
// logins with the throwaway key only, for the account running the test, no
// terminal, no agent or X11 forwarding, and outgoing connections only.
// (Outgoing TCP has to stay allowed: OpenSSH refuses to connect to unix
// sockets as well when AllowTcpForwarding is "no" or PermitOpen is "none" —
// found while writing this, and the reason the desk's own error message
// names those settings.) Tests that need a server that refuses socket
// forwarding, or runs no commands, ask for it.
//
// Tests that use it skip themselves on machines without sshd.
package sshtest

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Options changes what the server permits.
type Options struct {
	// NoSocketForwarding makes the server refuse to connect to unix
	// sockets, as a hardened sshd_config does.
	NoSocketForwarding bool
	// ForceCommand, when set, replaces every command a client asks for.
	ForceCommand string
	// KeyPassphrase protects the client's key, so that ssh has a question
	// to ask before it can log in.
	KeyPassphrase string
	// ViaJumpHost makes the client reach the host alias "target" through
	// the alias "jump" (ProxyJump); both are this server. The ssh that the
	// client starts for the jump is a separate process with its own login
	// — and its own questions.
	ViaJumpHost bool
}

// Server is a running sshd and what a client needs to reach it.
type Server struct {
	t    testing.TB
	dir  string
	sshd string
	Port string
	// User is the account the server accepts: the one running the test.
	User string

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{} // closed when cmd has exited
}

// Start runs sshd and returns once it accepts connections. The test is
// skipped when OpenSSH (sshd, ssh, ssh-keygen) is not installed or sshd
// cannot run unprivileged here.
func Start(t testing.TB, o Options) *Server {
	t.Helper()
	sshd := lookup("sshd", "/usr/sbin/sshd", "/usr/local/sbin/sshd", "/sbin/sshd")
	keygen := lookup("ssh-keygen")
	if sshd == "" || keygen == "" || lookup("ssh") == "" {
		skip(t, "OpenSSH (sshd, ssh, ssh-keygen) is not installed; the real-SSH tests need it")
	}
	me, err := user.Current()
	if err != nil {
		skip(t, "cannot tell who is running the test: %v", err)
	}
	// A short path: it ends up inside ssh's forwarding specification.
	dir, err := os.MkdirTemp("", "ddsshd")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{t: t, dir: dir, sshd: sshd, User: me.Username}
	t.Cleanup(func() {
		s.Stop()
		_ = os.RemoveAll(dir)
	})
	for name, passphrase := range map[string]string{"hostkey": "", "id": o.KeyPassphrase} {
		if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", passphrase, "-C", "docker-desk-test", "-f", filepath.Join(dir, name)).CombinedOutput(); err != nil {
			skip(t, "ssh-keygen failed: %v\n%s", err, out)
		}
	}
	pub, err := os.ReadFile(filepath.Join(dir, "id.pub"))
	if err != nil {
		t.Fatal(err)
	}
	s.write("authorized_keys", string(pub))

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, s.Port, _ = net.SplitHostPort(l.Addr().String())
	_ = l.Close()

	sockets := "local"
	if o.NoSocketForwarding {
		sockets = "no"
	}
	conf := fmt.Sprintf(`ListenAddress 127.0.0.1
Port %s
HostKey %s
PidFile none
AuthorizedKeysFile %s
AuthenticationMethods publickey
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
StrictModes no
AllowUsers %s
AllowStreamLocalForwarding %s
AllowTcpForwarding local
AllowAgentForwarding no
X11Forwarding no
PermitTTY no
PermitTunnel no
LogLevel VERBOSE
`, s.Port, filepath.Join(dir, "hostkey"), filepath.Join(dir, "authorized_keys"), s.User, sockets)
	if o.ForceCommand != "" {
		conf += "ForceCommand " + o.ForceCommand + "\n"
	}
	s.write("sshd_config", conf)

	hostPub, err := os.ReadFile(filepath.Join(dir, "hostkey.pub"))
	if err != nil {
		t.Fatal(err)
	}
	s.write("known_hosts", fmt.Sprintf("[127.0.0.1]:%s %s", s.Port, hostPub))
	// The client's whole configuration: with -F, neither ~/.ssh/config nor
	// the system's is read, and no agent or other key is ever offered.
	s.write("ssh_config", fmt.Sprintf(`Host *
  IdentityFile %s
  IdentitiesOnly yes
  IdentityAgent none
  UserKnownHostsFile %s
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  PreferredAuthentications publickey
`, filepath.Join(dir, "id"), filepath.Join(dir, "known_hosts"))+jumpConfig(o, s.Port))

	if err := s.Restart(); err != nil {
		skip(t, "sshd cannot run here: %v", err)
	}
	return s
}

// jumpConfig is the client configuration for Options.ViaJumpHost.
func jumpConfig(o Options, port string) string {
	if !o.ViaJumpHost {
		return ""
	}
	return fmt.Sprintf(`Host jump
  HostName 127.0.0.1
  Port %s
Host target
  HostName 127.0.0.1
  Port %s
  ProxyJump jump
`, port, port)
}

// skip ends a test that cannot run here. With DOCKER_DESK_REQUIRE_SSHD set
// it fails the test instead: on a machine that is known to have OpenSSH, a
// helper that quietly stopped working would otherwise turn every real-SSH
// test into a skip, and the suite would still be green.
func skip(t testing.TB, format string, args ...any) {
	t.Helper()
	if os.Getenv("DOCKER_DESK_REQUIRE_SSHD") != "" {
		t.Fatalf("DOCKER_DESK_REQUIRE_SSHD is set, but: "+format, args...)
	}
	t.Skipf(format, args...)
}

func lookup(name string, also ...string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, p := range also {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func (s *Server) write(name, content string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, name), []byte(content), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// Command is the ssh client invocation that reaches this server and nothing
// else, in the form DOCKER_DESK_SSH takes.
func (s *Server) Command() []string {
	return []string{"ssh", "-F", filepath.Join(s.dir, "ssh_config")}
}

// CommandString is Command as one string.
func (s *Server) CommandString() string { return strings.Join(s.Command(), " ") }

// URL is the ssh:// endpoint of a daemon socket behind this server. It names
// no user: ssh then logs in as the one running the test, which is the only
// account the server accepts.
func (s *Server) URL(socket string) string {
	return "ssh://127.0.0.1:" + s.Port + socket
}

// JumpURL is the same daemon socket reached through the jump host
// (Options.ViaJumpHost).
func (s *Server) JumpURL(socket string) string {
	return "ssh://target" + socket
}

// ForgetHostKey empties the client's known_hosts, so that the server's key
// is unknown to it.
func (s *Server) ForgetHostKey() { s.write("known_hosts", "") }

// UseWrongKey replaces the client's key with one the server does not accept.
func (s *Server) UseWrongKey() {
	s.t.Helper()
	_ = os.Remove(filepath.Join(s.dir, "id"))
	_ = os.Remove(filepath.Join(s.dir, "id.pub"))
	if out, err := exec.Command(lookup("ssh-keygen"), "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(s.dir, "id")).CombinedOutput(); err != nil {
		s.t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
}

// Stop ends the server and every connection it holds, as a machine going
// away does.
func (s *Server) Stop() {
	s.mu.Lock()
	cmd, done := s.cmd, s.done
	s.cmd, s.done = nil, nil
	s.mu.Unlock()
	if cmd == nil {
		return
	}
	// sshd serves each connection from a child that starts a session of
	// its own, so neither the parent's death nor its process group reaches
	// it: the children are found and ended one by one, deepest first.
	for _, pid := range descendants(cmd.Process.Pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
	<-done
}

// descendants lists the processes below pid, children before their parents.
func descendants(pid int) []int {
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	var all []int
	for _, f := range strings.Fields(string(out)) {
		if child, err := strconv.Atoi(f); err == nil {
			all = append(all, descendants(child)...)
			all = append(all, child)
		}
	}
	return all
}

// Restart starts the server (again) on the same port with the same keys.
func (s *Server) Restart() error {
	s.Stop()
	// A file, not a pipe: a pipe would keep Wait from returning for as
	// long as any connection's process still held its end.
	logPath := filepath.Join(s.dir, "sshd.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	readLog := func() string {
		data, _ := os.ReadFile(logPath)
		return strings.TrimSpace(string(data))
	}
	cmd := exec.Command(s.sshd, "-D", "-e", "-f", filepath.Join(s.dir, "sshd_config"))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	s.mu.Lock()
	s.cmd, s.done = cmd, done
	s.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", "127.0.0.1:"+s.Port, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return nil
		}
		select {
		case <-done:
			s.mu.Lock()
			s.cmd, s.done = nil, nil
			s.mu.Unlock()
			return fmt.Errorf("sshd exited at once: %s", readLog())
		case <-time.After(30 * time.Millisecond):
		}
	}
	s.Stop()
	return fmt.Errorf("sshd did not start listening: %s", readLog())
}

// Log is what sshd has written so far.
func (s *Server) Log() string {
	data, _ := os.ReadFile(filepath.Join(s.dir, "sshd.log"))
	return string(data)
}
