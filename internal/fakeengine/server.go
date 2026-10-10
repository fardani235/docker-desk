// Package fakeengine is an in-memory Docker Engine that serves the real
// Engine HTTP API on a unix socket.
//
// It exists so that every mutating path of the desk can be exercised through
// Docker's own client library without a real daemon ever receiving a mutating
// request. It is a test double and a sandbox, not an emulator: it models the
// behaviour the desk depends on (states, in-use refusals, events, stream
// framing, exec hijacking) and nothing else.
//
// Response bodies are built from the Moby API types, so their JSON shape is
// Docker's. Behaviour is cross-checked by running the real docker CLI against
// it; see docs/TESTING.md.
package fakeengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/events"
)

const (
	// APIVersion is the API version the fake advertises.
	APIVersion = "1.56"
	// MinAPIVersion is the oldest API version the fake accepts.
	MinAPIVersion = "1.40"
	// EngineVersion is the engine version the fake reports.
	EngineVersion = "29.0.0-fake"
)

// Request is one HTTP request the fake received, with the API version prefix
// removed from the path.
type Request struct {
	Method string
	Path   string
	Query  string
	Time   time.Time
}

// Mutating reports whether the request could change engine state.
func (r Request) Mutating() bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead
}

func (r Request) String() string {
	if r.Query != "" {
		return r.Method + " " + r.Path + "?" + r.Query
	}
	return r.Method + " " + r.Path
}

// Server is the fake engine.
type Server struct {
	mu         sync.Mutex
	containers map[string]*Container // by ID
	images     map[string]*Image     // by ID
	volumes    map[string]*Volume    // by name
	networks   map[string]*Network   // by ID
	execs      map[string]*execProc
	seq        int
	ipSeq      map[string]int // per network

	// Name is the daemon's host name in /info.
	Name string
	// StatsInterval is the period of statistics samples (default 1s).
	StatsInterval time.Duration
	// PullStepDelay is the pause between pull progress messages.
	PullStepDelay time.Duration
	// Unpullable lists repository names the fake registry refuses.
	Unpullable map[string]bool

	reqMu    sync.Mutex
	requests []Request

	evMu      sync.Mutex
	evHistory []events.Message
	evSubs    map[chan events.Message]struct{}
	evGen     int // bumped by DropEventStreams

	failMu sync.Mutex
	fails  []*injectedFailure

	listener net.Listener
	httpSrv  *http.Server
	socket   string
	closed   chan struct{}
	wg       sync.WaitGroup
}

type injectedFailure struct {
	method  string
	pattern *regexp.Regexp
	status  int
	message string
	times   int // remaining; <0 means forever
	// delay, when set, makes this an injected stall instead of a failure:
	// the request waits that long and is then handled normally.
	delay time.Duration
}

// New returns an empty fake engine with the three predefined networks.
func New() *Server {
	s := &Server{
		containers:    map[string]*Container{},
		images:        map[string]*Image{},
		volumes:       map[string]*Volume{},
		networks:      map[string]*Network{},
		execs:         map[string]*execProc{},
		ipSeq:         map[string]int{},
		evSubs:        map[chan events.Message]struct{}{},
		Name:          "fake-dockerd",
		StatsInterval: time.Second,
		PullStepDelay: 30 * time.Millisecond,
		Unpullable:    map[string]bool{},
		closed:        make(chan struct{}),
	}
	s.addNetworkLocked("bridge", "bridge", "172.17.0.0/16", nil)
	s.addNetworkLocked("host", "host", "", nil)
	s.addNetworkLocked("none", "null", "", nil)
	return s
}

// Start serves the API on a unix socket at path.
func (s *Server) Start(path string) error {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	return s.serve(l, path)
}

func (s *Server) serve(l net.Listener, path string) error {
	s.mu.Lock()
	s.listener = l
	s.socket = path
	s.httpSrv = &http.Server{Handler: s}
	srv := s.httpSrv
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		_ = srv.Serve(l)
	}()
	return nil
}

// StartTemp serves the API on a fresh socket in a new temporary directory.
// The returned function stops the fake and removes the directory.
func (s *Server) StartTemp() (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "ddfake")
	if err != nil {
		return nil, err
	}
	if err := s.Start(dir + "/d.sock"); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return func() {
		s.Close()
		_ = os.RemoveAll(dir)
	}, nil
}

// Socket returns the path of the unix socket.
func (s *Server) Socket() string { return s.socket }

// Host returns the endpoint URL to give to a Docker client.
func (s *Server) Host() string { return "unix://" + s.socket }

// Stop stops serving and severs every open connection (streams included) but
// keeps the engine state, like a daemon being stopped. Restart resumes it.
func (s *Server) Stop() {
	s.mu.Lock()
	srv := s.httpSrv
	s.httpSrv = nil
	s.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	s.DropEventStreams()
	s.dropAllStreams()
}

// Restart serves again on the same socket after Stop.
func (s *Server) Restart() error {
	return s.Start(s.socket)
}

// Close stops the fake for good.
func (s *Server) Close() {
	select {
	case <-s.closed:
		return
	default:
		close(s.closed)
	}
	s.Stop()
	s.mu.Lock()
	for _, c := range s.containers {
		c.stopGenerators()
	}
	s.mu.Unlock()
	s.wg.Wait()
	if s.socket != "" {
		_ = os.Remove(s.socket)
	}
}

// ---- request log ------------------------------------------------------------

// Requests returns every request received so far.
func (s *Server) Requests() []Request {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Mutations returns the requests that could have changed state.
func (s *Server) Mutations() []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Mutating() {
			out = append(out, r)
		}
	}
	return out
}

// ResetRequests clears the request log.
func (s *Server) ResetRequests() {
	s.reqMu.Lock()
	s.requests = nil
	s.reqMu.Unlock()
}

// CountRequests counts logged requests with the given method whose path
// matches the regular expression.
func (s *Server) CountRequests(method, pathRegexp string) int {
	re := regexp.MustCompile(pathRegexp)
	n := 0
	for _, r := range s.Requests() {
		if r.Method == method && re.MatchString(r.Path) {
			n++
		}
	}
	return n
}

// FailNext makes the next `times` requests matching method and path regexp
// fail with the given status and daemon message. times < 0 fails forever.
func (s *Server) FailNext(method, pathRegexp string, status int, message string, times int) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	s.fails = append(s.fails, &injectedFailure{
		method: method, pattern: regexp.MustCompile(pathRegexp), status: status, message: message, times: times,
	})
}

// Stall makes every request matching method and path regexp wait for d
// before it is handled, like a daemon that has stopped answering.
func (s *Server) Stall(method, pathRegexp string, d time.Duration) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	s.fails = append(s.fails, &injectedFailure{method: method, pattern: regexp.MustCompile(pathRegexp), times: -1, delay: d})
}

// ClearFailures removes every injected failure and stall.
func (s *Server) ClearFailures() {
	s.failMu.Lock()
	s.fails = nil
	s.failMu.Unlock()
}

func (s *Server) injected(method, path string) *injectedFailure {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	for i, f := range s.fails {
		if f.method == method && f.pattern.MatchString(path) {
			if f.times > 0 {
				f.times--
				if f.times == 0 {
					s.fails = append(s.fails[:i], s.fails[i+1:]...)
				}
			}
			return f
		}
	}
	return nil
}

// ---- HTTP plumbing ----------------------------------------------------------

var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

// ServeHTTP routes one API request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	s.reqMu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: path, Query: r.URL.RawQuery, Time: time.Now()})
	s.reqMu.Unlock()

	w.Header().Set("Api-Version", APIVersion)
	w.Header().Set("Ostype", "linux")
	w.Header().Set("Server", "Docker/"+EngineVersion+" (linux)")
	w.Header().Set("Docker-Experimental", "false")

	if f := s.injected(r.Method, path); f != nil {
		if f.delay == 0 {
			httpError(w, f.status, "%s", f.message)
			return
		}
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return
		case <-s.closed:
			return
		}
	}
	s.route(w, r, path)
}

func httpError(w http.ResponseWriter, status int, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": fmt.Sprintf(format, args...)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

func (s *Server) route(w http.ResponseWriter, r *http.Request, path string) {
	seg := strings.Split(strings.Trim(path, "/"), "/")
	m := r.Method
	get := m == http.MethodGet
	post := m == http.MethodPost
	del := m == http.MethodDelete

	switch {
	case path == "/_ping" && (get || m == http.MethodHead):
		w.Header().Set("Builder-Version", "2")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		if get {
			_, _ = w.Write([]byte("OK"))
		}
	case path == "/version" && get:
		s.handleVersion(w)
	case path == "/info" && get:
		s.handleInfo(w)
	case path == "/events" && get:
		s.handleEvents(w, r)

	case path == "/containers/json" && get:
		s.handleContainerList(w, r)
	case len(seg) == 3 && seg[0] == "containers" && seg[2] == "json" && get:
		s.handleContainerInspect(w, seg[1])
	case len(seg) == 3 && seg[0] == "containers" && seg[2] == "logs" && get:
		s.handleLogs(w, r, seg[1])
	case len(seg) == 3 && seg[0] == "containers" && seg[2] == "stats" && get:
		s.handleStats(w, r, seg[1])
	case len(seg) == 3 && seg[0] == "containers" && seg[2] == "exec" && post:
		s.handleExecCreate(w, r, seg[1])
	case len(seg) == 3 && seg[0] == "containers" && post:
		s.handleContainerAction(w, r, seg[1], seg[2])
	case len(seg) == 2 && seg[0] == "containers" && del:
		s.handleContainerRemove(w, r, seg[1])

	case len(seg) == 3 && seg[0] == "exec" && seg[2] == "start" && post:
		s.handleExecStart(w, r, seg[1])
	case len(seg) == 3 && seg[0] == "exec" && seg[2] == "resize" && post:
		s.handleExecResize(w, r, seg[1])
	case len(seg) == 3 && seg[0] == "exec" && seg[2] == "json" && get:
		s.handleExecInspect(w, seg[1])

	case path == "/images/json" && get:
		s.handleImageList(w, r)
	case path == "/images/create" && post:
		s.handleImagePull(w, r)
	case len(seg) >= 3 && seg[0] == "images" && seg[len(seg)-1] == "json" && get:
		s.handleImageInspect(w, strings.Join(seg[1:len(seg)-1], "/"))
	case len(seg) >= 2 && seg[0] == "images" && del:
		s.handleImageRemove(w, r, strings.Join(seg[1:], "/"))

	case path == "/volumes" && get:
		s.handleVolumeList(w, r)
	case path == "/volumes/create" && post:
		s.handleVolumeCreate(w, r)
	case len(seg) == 2 && seg[0] == "volumes" && get:
		s.handleVolumeInspect(w, seg[1])
	case len(seg) == 2 && seg[0] == "volumes" && del:
		s.handleVolumeRemove(w, r, seg[1])

	case path == "/networks" && get:
		s.handleNetworkList(w, r)
	case path == "/networks/create" && post:
		s.handleNetworkCreate(w, r)
	case len(seg) == 2 && seg[0] == "networks" && get:
		s.handleNetworkInspect(w, seg[1])
	case len(seg) == 2 && seg[0] == "networks" && del:
		s.handleNetworkRemove(w, seg[1])
	case len(seg) == 3 && seg[0] == "networks" && seg[2] == "connect" && post:
		s.handleNetworkConnect(w, r, seg[1])
	case len(seg) == 3 && seg[0] == "networks" && seg[2] == "disconnect" && post:
		s.handleNetworkDisconnect(w, r, seg[1])

	default:
		httpError(w, http.StatusNotFound, "page not found")
	}
}

func (s *Server) handleVersion(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"Platform":      map[string]string{"Name": "Docker Desk fake engine"},
		"Version":       EngineVersion,
		"ApiVersion":    APIVersion,
		"MinAPIVersion": MinAPIVersion,
		"Os":            "linux",
		"Arch":          "amd64",
		"KernelVersion": "0.0.0-fake",
		"GoVersion":     "go",
		"GitCommit":     "fake",
		"Components": []map[string]any{{
			"Name": "Engine", "Version": EngineVersion,
			"Details": map[string]string{"ApiVersion": APIVersion, "MinAPIVersion": MinAPIVersion, "Os": "linux", "Arch": "amd64"},
		}},
	})
}

func (s *Server) handleInfo(w http.ResponseWriter) {
	s.mu.Lock()
	running, paused, stopped := 0, 0, 0
	for _, c := range s.containers {
		switch c.State {
		case "running", "restarting":
			running++
		case "paused":
			paused++
		default:
			stopped++
		}
	}
	info := map[string]any{
		"ID":                 "FAKE:FAKE:FAKE",
		"Containers":         len(s.containers),
		"ContainersRunning":  running,
		"ContainersPaused":   paused,
		"ContainersStopped":  stopped,
		"Images":             len(s.images),
		"Driver":             "overlayfs",
		"DriverStatus":       [][2]string{},
		"Plugins":            map[string]any{"Volume": []string{"local"}, "Network": []string{"bridge", "host", "null"}, "Log": []string{"json-file"}},
		"MemoryLimit":        true,
		"SwapLimit":          true,
		"KernelVersion":      "0.0.0-fake",
		"OperatingSystem":    "Docker Desk fake engine",
		"OSVersion":          "0",
		"OSType":             "linux",
		"Architecture":       "x86_64",
		"NCPU":               fakeCPUs,
		"MemTotal":           int64(fakeMemTotal),
		"DockerRootDir":      "/var/lib/fake-docker",
		"Name":               s.Name,
		"ServerVersion":      EngineVersion,
		"CgroupDriver":       "systemd",
		"CgroupVersion":      "2",
		"LoggingDriver":      "json-file",
		"SystemTime":         time.Now().Format(time.RFC3339Nano),
		"IndexServerAddress": "https://index.docker.io/v1/",
		"Labels":             []string{},
		"SecurityOptions":    []string{},
		"Warnings":           []string{},
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, info)
}

// ---- identifiers -------------------------------------------------------------

// newIDLocked returns a deterministic 64-hex identifier.
func (s *Server) newIDLocked(kind, name string) string {
	s.seq++
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%d", kind, name, s.seq)))
	return hex.EncodeToString(sum[:])
}

func stableID(kind, name string) string {
	sum := sha256.Sum256([]byte(kind + "/" + name))
	return hex.EncodeToString(sum[:])
}

func short(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// ---- events -------------------------------------------------------------------

const eventHistory = 256

// emit publishes an engine event. It may be called with s.mu held.
func (s *Server) emit(typ, action, id string, attrs map[string]string) {
	now := time.Now()
	if attrs == nil {
		attrs = map[string]string{}
	}
	msg := events.Message{
		Type:     events.Type(typ),
		Action:   events.Action(action),
		Actor:    events.Actor{ID: id, Attributes: attrs},
		Scope:    "local",
		Time:     now.Unix(),
		TimeNano: now.UnixNano(),
	}
	s.evMu.Lock()
	s.evHistory = append(s.evHistory, msg)
	if len(s.evHistory) > eventHistory {
		s.evHistory = s.evHistory[len(s.evHistory)-eventHistory:]
	}
	for ch := range s.evSubs {
		select {
		case ch <- msg:
		default: // a subscriber that cannot keep up loses the event
		}
	}
	s.evMu.Unlock()
}

// Emit publishes an arbitrary event, for tests that need a specific burst.
func (s *Server) Emit(typ, action, id string, attrs map[string]string) {
	s.emit(typ, action, id, attrs)
}

// DropEventStreams closes every open /events connection, as a daemon restart
// would.
func (s *Server) DropEventStreams() {
	s.evMu.Lock()
	for ch := range s.evSubs {
		close(ch)
		delete(s.evSubs, ch)
	}
	s.evGen++
	s.evMu.Unlock()
}

// EventSubscribers returns the number of open /events connections.
func (s *Server) EventSubscribers() int {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	return len(s.evSubs)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var since time.Time
	if v := q.Get("since"); v != "" {
		since = parseUnix(v)
	}
	until := time.Time{}
	if v := q.Get("until"); v != "" {
		until = parseUnix(v)
	}

	ch := make(chan events.Message, 4096)
	s.evMu.Lock()
	var replay []events.Message
	if !since.IsZero() {
		for _, e := range s.evHistory {
			if !time.Unix(0, e.TimeNano).Before(since) {
				replay = append(replay, e)
			}
		}
	}
	s.evSubs[ch] = struct{}{}
	s.evMu.Unlock()
	defer func() {
		s.evMu.Lock()
		if _, ok := s.evSubs[ch]; ok {
			delete(s.evSubs, ch)
		}
		s.evMu.Unlock()
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	enc := json.NewEncoder(w)
	for _, e := range replay {
		if enc.Encode(e) != nil {
			return
		}
	}
	if fl != nil {
		fl.Flush()
	}

	var deadline <-chan time.Time
	if !until.IsZero() {
		d := time.Until(until)
		if d < 0 {
			d = 0
		}
		t := time.NewTimer(d)
		defer t.Stop()
		deadline = t.C
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closed:
			return
		case <-deadline:
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if enc.Encode(e) != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
	}
}

func parseUnix(v string) time.Time {
	var sec, nsec int64
	if i := strings.IndexByte(v, '.'); i >= 0 {
		fmt.Sscanf(v[:i], "%d", &sec)
		frac := (v[i+1:] + "000000000")[:9]
		fmt.Sscanf(frac, "%d", &nsec)
	} else {
		fmt.Sscanf(v, "%d", &sec)
	}
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, nsec)
}

// ---- filters ------------------------------------------------------------------

// parseFilters decodes the `filters` query parameter: {"term":{"value":true}}.
func parseFilters(r *http.Request) map[string][]string {
	out := map[string][]string{}
	raw := r.URL.Query().Get("filters")
	if raw == "" {
		return out
	}
	var m map[string]map[string]bool
	if err := json.Unmarshal([]byte(raw), &m); err == nil {
		for k, vs := range m {
			for v, on := range vs {
				if on {
					out[k] = append(out[k], v)
				}
			}
			sort.Strings(out[k])
		}
		return out
	}
	// Legacy form: {"term":["value"]}.
	var legacy map[string][]string
	if err := json.Unmarshal([]byte(raw), &legacy); err == nil {
		return legacy
	}
	return out
}

func boolParam(r *http.Request, name string) bool {
	v := strings.ToLower(r.URL.Query().Get(name))
	return v == "1" || v == "true"
}
