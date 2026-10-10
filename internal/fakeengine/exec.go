package fakeengine

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// execProc is a process started with exec in a fake container.
type execProc struct {
	id          string
	containerID string
	hostname    string
	cmd         []string
	tty         bool

	mu       sync.Mutex
	rows     uint
	cols     uint
	started  bool
	running  bool
	exitCode int
	conn     net.Conn
}

// kill ends the process by severing its connection.
func (e *execProc) kill(code int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		e.running = false
		e.exitCode = code
	}
	if e.conn != nil {
		_ = e.conn.Close()
	}
}

func (e *execProc) size() (rows, cols uint) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rows, e.cols
}

func (s *Server) handleExecCreate(w http.ResponseWriter, r *http.Request, ref string) {
	var body struct {
		Cmd         []string
		Tty         bool
		ConsoleSize *[2]uint
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Cmd) == 0 {
		httpError(w, http.StatusBadRequest, "No exec command specified")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.findContainerLocked(ref)
	if c == nil {
		httpError(w, http.StatusNotFound, "No such container: %s", ref)
		return
	}
	switch c.State {
	case "running":
	case "paused":
		httpError(w, http.StatusConflict, "Container %s is paused, unpause the container before exec", c.ID)
		return
	default:
		httpError(w, http.StatusConflict, "container %s is not running", c.ID)
		return
	}
	e := &execProc{
		id:          s.newIDLocked("exec", c.Name),
		containerID: c.ID,
		hostname:    short(c.ID),
		cmd:         body.Cmd,
		tty:         body.Tty,
		rows:        24,
		cols:        80,
	}
	if body.ConsoleSize != nil && body.ConsoleSize[0] > 0 {
		e.rows, e.cols = body.ConsoleSize[0], body.ConsoleSize[1]
	}
	s.execs[e.id] = e
	s.emit("container", "exec_create: "+strings.Join(body.Cmd, " "), c.ID, map[string]string{"name": c.Name, "image": c.Image, "execID": e.id})
	writeJSON(w, http.StatusCreated, map[string]string{"Id": e.id})
}

func (s *Server) handleExecInspect(w http.ResponseWriter, id string) {
	s.mu.Lock()
	e := s.execs[id]
	s.mu.Unlock()
	if e == nil {
		httpError(w, http.StatusNotFound, "No such exec instance: %s", id)
		return
	}
	e.mu.Lock()
	resp := map[string]any{
		"ID":          e.id,
		"Running":     e.running,
		"ExitCode":    e.exitCode,
		"ContainerID": e.containerID,
		"Pid":         4343,
		"OpenStdin":   true,
		"OpenStdout":  true,
		"OpenStderr":  true,
		"ProcessConfig": map[string]any{
			"tty": e.tty, "entrypoint": e.cmd[0], "arguments": e.cmd[1:], "privileged": false,
		},
	}
	e.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleExecResize(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	e := s.execs[id]
	s.mu.Unlock()
	if e == nil {
		httpError(w, http.StatusNotFound, "No such exec instance: %s", id)
		return
	}
	h, _ := strconv.ParseUint(r.URL.Query().Get("h"), 10, 32)
	wd, _ := strconv.ParseUint(r.URL.Query().Get("w"), 10, 32)
	e.mu.Lock()
	e.rows, e.cols = uint(h), uint(wd)
	e.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// shellLauncher recognises the commands that start an interactive shell.
func shellLauncher(cmd []string) bool {
	switch base(cmd[0]) {
	case "sh", "bash", "ash", "dash", "zsh":
		if len(cmd) == 1 {
			return true
		}
		// sh -c '… exec bash || exec sh'
		return len(cmd) == 3 && cmd[1] == "-c" && strings.Contains(cmd[2], "exec sh")
	}
	return false
}

func base(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

var knownExecutables = map[string]bool{
	"sh": true, "bash": true, "ash": true, "dash": true, "echo": true, "hostname": true, "true": true,
	"false": true, "whoami": true, "pwd": true, "ls": true, "cat": true, "env": true, "uname": true, "stty": true, "ps": true,
}

func (s *Server) handleExecStart(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		Detach      bool
		Tty         bool
		ConsoleSize *[2]uint
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	s.mu.Lock()
	e := s.execs[id]
	var c *Container
	if e != nil {
		c = s.containers[e.containerID]
	}
	if e == nil {
		s.mu.Unlock()
		httpError(w, http.StatusNotFound, "No such exec instance: %s", id)
		return
	}
	if c == nil || c.State != "running" {
		s.mu.Unlock()
		httpError(w, http.StatusConflict, "container %s is not running", e.containerID)
		return
	}
	e.mu.Lock()
	already := e.started
	e.started = true
	if body.ConsoleSize != nil && body.ConsoleSize[0] > 0 {
		e.rows, e.cols = body.ConsoleSize[0], body.ConsoleSize[1]
	}
	e.mu.Unlock()
	noShell := c.NoShell
	name, image := c.Name, c.Image
	s.mu.Unlock()

	if already {
		httpError(w, http.StatusConflict, "exec command %s is already running", id)
		return
	}
	exe := base(e.cmd[0])
	missing := !knownExecutables[exe] || (noShell && (exe == "sh" || exe == "bash" || exe == "ash" || exe == "dash"))
	startErr := fmt.Sprintf(
		"OCI runtime exec failed: exec failed: unable to start container process: exec: %q: executable file not found in $PATH: unknown", e.cmd[0])
	if missing && body.Detach {
		httpError(w, http.StatusBadRequest, "%s", startErr)
		return
	}
	if body.Detach {
		w.WriteHeader(http.StatusOK)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		httpError(w, http.StatusInternalServerError, "connection cannot be hijacked")
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	mediaType := "application/vnd.docker.raw-stream"
	if !e.tty {
		mediaType = "application/vnd.docker.multiplexed-stream"
	}
	if r.Header.Get("Upgrade") != "" {
		fmt.Fprintf(conn, "HTTP/1.1 101 UPGRADED\r\nContent-Type: %s\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n", mediaType)
	} else {
		fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: %s\r\n\r\n", mediaType)
	}

	attrs := map[string]string{"name": name, "image": image, "execID": e.id}
	if missing {
		// Like the real daemon: the connection is upgraded first, so a
		// process that cannot start is reported *inside the stream* and
		// through the exec's exit code (127), not as an HTTP error.
		var out io.Writer = conn
		if !e.tty {
			out = &muxWriter{w: conn, stream: 1}
		}
		_, _ = io.WriteString(out, startErr+"\r\n")
		e.mu.Lock()
		e.exitCode = 127
		e.mu.Unlock()
		_ = conn.Close()
		return
	}

	e.mu.Lock()
	e.conn = conn
	e.running = true
	e.mu.Unlock()
	s.emit("container", "exec_start: "+strings.Join(e.cmd, " "), e.containerID, attrs)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		code := s.runExec(e, rw.Reader, conn)
		e.mu.Lock()
		if e.running {
			e.running = false
			e.exitCode = code
		}
		final := e.exitCode
		e.mu.Unlock()
		_ = conn.Close()
		a := map[string]string{"name": name, "image": image, "execID": e.id, "exitCode": strconv.Itoa(final)}
		s.emit("container", "exec_die", e.containerID, a)
	}()
}

// runExec runs the fake process and returns its exit code.
func (s *Server) runExec(e *execProc, in *bufio.Reader, conn net.Conn) int {
	var out io.Writer = conn
	if !e.tty {
		out = &muxWriter{w: conn, stream: 1}
	}
	sh := &fakeShell{e: e, out: out, tty: e.tty}
	if shellLauncher(e.cmd) {
		return sh.interactive(in)
	}
	if base(e.cmd[0]) == "sh" || base(e.cmd[0]) == "bash" {
		// sh -c "script"
		if len(e.cmd) >= 3 && e.cmd[1] == "-c" {
			code := 0
			for _, part := range strings.Split(e.cmd[2], ";") {
				var exit bool
				code, exit = sh.run(strings.TrimSpace(part))
				if exit {
					break
				}
			}
			return code
		}
	}
	code, _ := sh.run(strings.Join(e.cmd, " "))
	return code
}

// muxWriter frames output as Docker's multiplexed stream.
type muxWriter struct {
	w      io.Writer
	stream byte
}

func (m *muxWriter) Write(p []byte) (int, error) {
	var hdr [8]byte
	hdr[0] = m.stream
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(p)))
	if _, err := m.w.Write(append(hdr[:], p...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// fakeShell is a tiny line-oriented shell: enough to prove that bytes travel
// both ways, that the terminal size is propagated, and that exit codes work.
type fakeShell struct {
	e   *execProc
	out io.Writer
	tty bool
}

func (f *fakeShell) nl() string {
	if f.tty {
		return "\r\n"
	}
	return "\n"
}

func (f *fakeShell) println(s string) { _, _ = io.WriteString(f.out, s+f.nl()) }

func (f *fakeShell) prompt() {
	_, _ = io.WriteString(f.out, "root@"+f.e.hostname+":/# ")
}

func (f *fakeShell) interactive(in *bufio.Reader) int {
	f.prompt()
	var line []byte
	last := 0
	for {
		b, err := in.ReadByte()
		if err != nil {
			return last
		}
		switch {
		case b == '\r' || b == '\n':
			_, _ = io.WriteString(f.out, f.nl())
			cmd := strings.TrimSpace(string(line))
			line = line[:0]
			if cmd != "" {
				code, exit := f.run(cmd)
				last = code
				if exit {
					return code
				}
			}
			f.prompt()
		case b == 0x04: // Ctrl-D
			if len(line) == 0 {
				f.println("exit")
				return last
			}
		case b == 0x03: // Ctrl-C
			_, _ = io.WriteString(f.out, "^C"+f.nl())
			line = line[:0]
			f.prompt()
		case b == 0x7f || b == 0x08:
			if len(line) > 0 {
				line = line[:len(line)-1]
				_, _ = io.WriteString(f.out, "\b \b")
			}
		case b == 0x1b: // swallow an escape sequence (arrow keys etc.)
			if nb, err := in.Peek(1); err == nil && nb[0] == '[' {
				_, _ = in.ReadByte()
				for {
					c, err := in.ReadByte()
					if err != nil || (c >= 0x40 && c <= 0x7e) {
						break
					}
				}
			}
		case b >= 0x20:
			line = append(line, b)
			if f.tty {
				_, _ = f.out.Write([]byte{b}) // the remote pty echoes
			}
		}
	}
}

// run executes one command line; exit reports whether the shell should end.
func (f *fakeShell) run(cmdline string) (code int, exit bool) {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return 0, false
	}
	switch base(fields[0]) {
	case "exit":
		code := 0
		if len(fields) > 1 {
			code, _ = strconv.Atoi(fields[1])
		}
		return code, true
	case "echo":
		f.println(strings.Trim(strings.TrimSpace(strings.TrimPrefix(cmdline, fields[0])), `"'`))
	case "hostname":
		f.println(f.e.hostname)
	case "whoami":
		f.println("root")
	case "pwd":
		f.println("/")
	case "true":
	case "false":
		return 1, false
	case "ls":
		f.println("bin  dev  etc  home  lib  proc  root  sys  tmp  usr  var")
	case "uname":
		f.println("Linux")
	case "env":
		f.println("PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
		f.println("HOSTNAME=" + f.e.hostname)
		if f.tty {
			f.println("TERM=xterm")
		}
	case "cat":
		if len(fields) > 1 && fields[1] == "/etc/os-release" {
			f.println(`NAME="Docker Desk fake Linux"`)
		} else {
			f.println("cat: " + strings.Join(fields[1:], " ") + ": No such file or directory")
			return 1, false
		}
	case "stty", "size":
		rows, cols := f.e.size()
		f.println(fmt.Sprintf("%d %d", rows, cols))
	case "ps":
		f.println("PID   USER     TIME  COMMAND")
		f.println("    1 root      0:00 /bin/app")
	default:
		f.println("sh: 1: " + fields[0] + ": not found")
		return 127, false
	}
	return 0, false
}
