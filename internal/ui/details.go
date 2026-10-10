package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/live"
	"github.com/fardani235/docker-desk/internal/plan"
)

// Details tabs.
const (
	tabInfo    = "Info"
	tabLogs    = "Logs"
	tabStats   = "Stats"
	tabInspect = "Inspect"
)

var kindTabs = [kindCount][]string{
	KindContainers: {tabInfo, tabLogs, tabStats, tabInspect},
	KindImages:     {tabInfo, tabInspect},
	KindVolumes:    {tabInfo, tabInspect},
	KindNetworks:   {tabInfo, tabInspect},
	KindCompose:    {tabInfo},
}

func (m *Model) tabName() string {
	tabs := kindTabs[m.kind]
	return tabs[clamp(m.tab[m.kind], 0, len(tabs)-1)]
}

func (m *Model) cycleTab(delta int) {
	n := len(kindTabs[m.kind])
	m.tab[m.kind] = ((m.tab[m.kind]+delta)%n + n) % n
}

// textView is a scrollable block of static text (info, inspect, job output,
// help). Lines are wrapped lazily for the width it is drawn at.
type textView struct {
	lines   []string
	wrapped []string
	wrapW   int
	wrap    bool
	off     int
}

func (v *textView) set(lines []string) {
	v.lines = lines
	v.wrapW = -1
}

func (v *textView) layout(w int) {
	if v.wrapW == w {
		return
	}
	v.wrapW = w
	v.wrapped = v.wrapped[:0]
	for _, l := range v.lines {
		if v.wrap {
			v.wrapped = append(v.wrapped, wrapLine(l, w)...)
		} else {
			v.wrapped = append(v.wrapped, l)
		}
	}
}

func (v *textView) toggleWrap() {
	v.wrap = !v.wrap
	v.wrapW = -1
}

func (v *textView) render(w, h int) []string {
	v.layout(w)
	v.off = clamp(v.off, 0, max(len(v.wrapped)-h, 0))
	end := min(v.off+h, len(v.wrapped))
	return v.wrapped[v.off:end]
}

func (v *textView) scroll(delta int) { v.off = max(v.off+delta, 0) }

// position describes where the view is, for a pane title.
func (v *textView) position(h int) string {
	if len(v.wrapped) <= h {
		return ""
	}
	return fmt.Sprintf("%d–%d of %d", v.off+1, min(v.off+h, len(v.wrapped)), len(v.wrapped))
}

// detailState is the inspect result for the highlighted object.
type detailState struct {
	key     string // kind:identity the data belongs to
	loading bool
	err     string
	seq     uint64

	container *engine.ContainerDetail
	image     *engine.ImageDetail
	volume    *engine.VolumeDetail
	network   *engine.NetworkDetail
}

type detailMsg struct {
	key string
	seq uint64
	err error

	container *engine.ContainerDetail
	image     *engine.ImageDetail
	volume    *engine.VolumeDetail
	network   *engine.NetworkDetail
}

// currentDetailKey identifies what the Details pane should describe.
func (m *Model) currentDetailKey() string {
	r, ok := m.currentRow()
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%s", m.kind, r.key)
}

// loadDetail fetches the inspect document for the highlighted object.
func (m *Model) loadDetail() tea.Cmd {
	key := m.wantDetail
	if key != m.detail.key {
		m.detail = detailState{key: key, seq: m.detail.seq}
		m.inspect.set(nil)
	}
	r, ok := m.currentRow()
	if !ok || key == "" || m.kind == KindCompose {
		m.detail.loading = false
		return nil
	}
	m.detail.loading = true
	m.detail.seq++
	seq := m.detail.seq
	eng, kind := m.eng, m.kind
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		msg := detailMsg{key: key, seq: seq}
		switch kind {
		case KindContainers:
			d, err := eng.InspectContainer(ctx, r.containerID)
			msg.container, msg.err = &d, err
		case KindImages:
			d, err := eng.InspectImage(ctx, r.imageID)
			msg.image, msg.err = &d, err
		case KindVolumes:
			d, err := eng.InspectVolume(ctx, r.volume)
			msg.volume, msg.err = &d, err
		case KindNetworks:
			d, err := eng.InspectNetwork(ctx, r.networkID)
			msg.network, msg.err = &d, err
		}
		return msg
	}
}

func (m *Model) onDetail(msg detailMsg) {
	if msg.key != m.detail.key || msg.seq != m.detail.seq {
		return // for an object that is no longer highlighted
	}
	m.detail.loading = false
	if msg.err != nil {
		if engine.IsNotFound(msg.err) {
			// It was removed while shown. The list refresh that follows the
			// engine's event moves the highlight; no error is raised.
			m.detail.err = "This object no longer exists."
			m.pending |= live.DirtyAll
		} else {
			m.detail.err = msg.err.Error()
		}
		return
	}
	m.detail.err = ""
	var raw []byte
	switch {
	case msg.container != nil:
		m.detail.container, raw = msg.container, msg.container.Raw
	case msg.image != nil:
		m.detail.image, raw = msg.image, msg.image.Raw
	case msg.volume != nil:
		m.detail.volume, raw = msg.volume, msg.volume.Raw
	case msg.network != nil:
		m.detail.network, raw = msg.network, msg.network.Raw
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, raw, "", "  ") == nil {
		off := m.inspect.off
		m.inspect.set(strings.Split(pretty.String(), "\n"))
		m.inspect.off = off
	}
}

// ---- Info ----------------------------------------------------------------------------

type kv struct{ k, v string }

func kvLines(pairs []kv, st styles) []string {
	w := 0
	for _, p := range pairs {
		if p.k != "" && len(p.k) > w {
			w = len(p.k)
		}
	}
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		switch {
		case p.k == "" && p.v == "":
			out = append(out, "")
		case p.k == "":
			out = append(out, p.v)
		default:
			out = append(out, st.tone(toneDim, fit(p.k, w))+"  "+p.v)
		}
	}
	return out
}

func labelsSorted(m map[string]string, skipPrefix string) []string {
	var out []string
	for k, v := range m {
		if skipPrefix != "" && strings.HasPrefix(k, skipPrefix) {
			continue
		}
		if v == "" {
			out = append(out, k)
		} else {
			out = append(out, k+"="+v)
		}
	}
	sort.Strings(out)
	return out
}

func section(st styles, title string) kv {
	return kv{v: st.tone(toneAccent, title)}
}

func (m *Model) infoLines() []string {
	r, ok := m.currentRow()
	if !ok {
		return []string{m.st.tone(toneDim, "Nothing highlighted.")}
	}
	st := m.st
	var p []kv
	now := time.Now()
	switch m.kind {
	case KindContainers:
		var c engine.Container
		for _, x := range m.snap.Containers {
			if x.ID == r.containerID {
				c = x
			}
		}
		d := m.detail.container
		p = append(p,
			kv{"Name", c.Name},
			kv{"ID", engine.ShortID(c.ID)},
			kv{"Image", c.Image},
			kv{"State", st.tone(stateTone(c.State), stateGlyph(c.State)+" "+c.State) + "   " + c.Status},
		)
		if c.Health != "" {
			t := toneGood
			if c.Health != "healthy" {
				t = toneWarn
			}
			p = append(p, kv{"Health", st.tone(t, c.Health)})
		}
		if proj := c.Project(); proj != "" {
			p = append(p, kv{"Compose", proj + " / " + c.Service()})
		}
		if d != nil && d.ID == c.ID {
			p = append(p, kv{"Started", timeOrDash(d.StartedAt)})
			if !d.FinishedAt.IsZero() {
				exit := fmt.Sprintf("%s   exit code %d", timeOrDash(d.FinishedAt), d.ExitCode)
				if d.ExitCode != 0 {
					exit = st.tone(toneBad, exit)
				}
				p = append(p, kv{"Finished", exit})
			}
			if d.OOMKilled {
				p = append(p, kv{"", st.tone(toneBad, "Killed by the kernel: out of memory.")})
			}
			if d.Error != "" {
				p = append(p, kv{"Error", st.tone(toneBad, d.Error)})
			}
			cmd := strings.Join(append(append([]string{}, d.Entrypoint...), d.Cmd...), " ")
			if cmd == "" {
				cmd = strings.TrimSpace(d.Path + " " + strings.Join(d.Args, " "))
			}
			p = append(p,
				kv{"Command", cmd},
				kv{"Restart", fmt.Sprintf("%s (restarted %d×)", d.RestartPolicy, d.RestartCount)},
			)
			if d.WorkingDir != "" {
				p = append(p, kv{"Workdir", d.WorkingDir})
			}
			if d.User != "" {
				p = append(p, kv{"User", d.User})
			}
			extra := fmt.Sprintf("tty=%v  privileged=%v  env vars=%d", d.TTY, d.Privileged, d.EnvCount)
			p = append(p, kv{"Runtime", extra}, kv{"Created", timeOrDash(d.Created)})
		} else {
			p = append(p, kv{"Command", c.Command}, kv{"Created", timeOrDash(c.Created)})
		}
		ports := c.Ports
		if d != nil && d.ID == c.ID && len(d.Ports) > 0 {
			ports = d.Ports
		}
		p = append(p, kv{}, section(st, "Ports"))
		if len(ports) == 0 {
			p = append(p, kv{v: st.tone(toneDim, "  none")})
		}
		for _, port := range ports {
			if port.HostPort != 0 {
				ip := port.HostIP
				if ip == "" {
					ip = "*"
				}
				p = append(p, kv{v: fmt.Sprintf("  %s:%d → %d/%s", ip, port.HostPort, port.PrivatePort, port.Proto)})
			} else {
				p = append(p, kv{v: fmt.Sprintf("  %d/%s  (not published)", port.PrivatePort, port.Proto)})
			}
		}
		nets := c.Networks
		if d != nil && d.ID == c.ID && len(d.Networks) > 0 {
			nets = d.Networks
		}
		p = append(p, kv{}, section(st, "Networks"))
		if len(nets) == 0 {
			p = append(p, kv{v: st.tone(toneDim, "  none")})
		}
		for _, n := range nets {
			line := "  " + n.Network
			if n.IP != "" {
				line += "  " + n.IP
				if n.Gateway != "" {
					line += "  gw " + n.Gateway
				}
			} else {
				line += st.tone(toneDim, "  (no address: not running)")
			}
			p = append(p, kv{v: line})
		}
		mounts := c.Mounts
		p = append(p, kv{}, section(st, "Mounts"))
		if len(mounts) == 0 {
			p = append(p, kv{v: st.tone(toneDim, "  none")})
		}
		for _, mt := range mounts {
			src := mt.Source
			if mt.Type == "volume" {
				src = mt.Name
				if len(src) == 64 {
					src = src[:12] + "…"
				}
			}
			mode := "rw"
			if !mt.RW {
				mode = "ro"
			}
			p = append(p, kv{v: fmt.Sprintf("  %-6s %s → %s (%s)", mt.Type, src, mt.Destination, mode)})
		}
		if ls := labelsSorted(c.Labels, "com.docker.compose."); len(ls) > 0 {
			p = append(p, kv{}, section(st, "Labels"))
			for _, l := range ls {
				p = append(p, kv{v: "  " + l})
			}
		}

	case KindImages:
		var img engine.Image
		for _, x := range m.snap.Images {
			if x.ID == r.imageID {
				img = x
			}
		}
		p = append(p,
			kv{"Reference", r.name},
			kv{"ID", engine.ShortID(img.ID)},
			kv{"Size", humanBytes(img.Size)},
			kv{"Created", timeOrDash(img.Created) + "  (" + humanAge(img.Created, now) + " ago)"},
		)
		if img.Dangling {
			p = append(p, kv{"", st.tone(toneWarn, "Dangling: untagged and not referenced by another image.")})
		}
		if d := m.detail.image; d != nil && d.ID == img.ID {
			p = append(p, kv{"Platform", d.OS + "/" + d.Architecture}, kv{"Layers", fmt.Sprintf("%d", d.Layers)})
			if cmd := strings.Join(append(append([]string{}, d.Entrypoint...), d.Cmd...), " "); cmd != "" {
				p = append(p, kv{"Command", cmd})
			}
			if d.WorkingDir != "" {
				p = append(p, kv{"Workdir", d.WorkingDir})
			}
			if d.User != "" {
				p = append(p, kv{"User", d.User})
			}
			if len(d.ExposedPorts) > 0 {
				p = append(p, kv{"Exposes", strings.Join(d.ExposedPorts, " ")})
			}
		}
		p = append(p, kv{}, section(st, "References"))
		refs := img.Refs()
		if len(refs) == 0 {
			p = append(p, kv{v: st.tone(toneDim, "  none (untagged)")})
		}
		for _, ref := range refs {
			p = append(p, kv{v: "  " + ref})
		}
		p = append(p, kv{}, section(st, "Used by"))
		users := m.snap.ImageUsers(img)
		if len(users) == 0 {
			p = append(p, kv{v: st.tone(toneDim, "  no container")})
		}
		for _, u := range users {
			p = append(p, kv{v: "  " + st.tone(stateTone(u.State), stateGlyph(u.State)) + " " + u.Name + st.tone(toneDim, "  "+u.State)})
		}

	case KindVolumes:
		var v engine.Volume
		for _, x := range m.snap.Volumes {
			if x.Name == r.volume {
				v = x
			}
		}
		kind := "named"
		if v.Anonymous() {
			kind = "anonymous (created without a name)"
		}
		p = append(p,
			kv{"Name", v.Name},
			kv{"Kind", kind},
			kv{"Driver", v.Driver},
			kv{"Scope", v.Scope},
			kv{"Mountpoint", v.Mountpoint},
		)
		if ts, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
			p = append(p, kv{"Created", timeOrDash(ts) + "  (" + humanAge(ts, now) + " ago)"})
		} else if v.CreatedAt != "" {
			p = append(p, kv{"Created", v.CreatedAt})
		}
		p = append(p, kv{}, section(st, "Referenced by"))
		users := m.snap.VolumeUsers(v.Name)
		if len(users) == 0 {
			p = append(p, kv{v: st.tone(toneDim, "  no container — unused")})
		}
		for _, u := range users {
			dest := ""
			for _, mt := range u.Mounts {
				if mt.Name == v.Name {
					dest = " at " + mt.Destination
				}
			}
			p = append(p, kv{v: "  " + st.tone(stateTone(u.State), stateGlyph(u.State)) + " " + u.Name + st.tone(toneDim, "  "+u.State+dest)})
		}
		if ls := labelsSorted(v.Labels, ""); len(ls) > 0 {
			p = append(p, kv{}, section(st, "Labels"))
			for _, l := range ls {
				p = append(p, kv{v: "  " + l})
			}
		}

	case KindNetworks:
		var n engine.Network
		for _, x := range m.snap.Networks {
			if x.ID == r.networkID {
				n = x
			}
		}
		p = append(p,
			kv{"Name", n.Name},
			kv{"ID", engine.ShortID(n.ID)},
			kv{"Driver", n.Driver},
			kv{"Scope", n.Scope},
		)
		if n.Predefined() {
			p = append(p, kv{"", st.tone(toneDim, "Predefined by Docker; cannot be removed.")})
		}
		if len(n.Subnets) > 0 {
			p = append(p, kv{"Subnets", strings.Join(n.Subnets, ", ")})
		}
		if len(n.Gateways) > 0 {
			p = append(p, kv{"Gateways", strings.Join(n.Gateways, ", ")})
		}
		p = append(p, kv{"Internal", fmt.Sprintf("%v", n.Internal)}, kv{"IPv6", fmt.Sprintf("%v", n.IPv6)}, kv{"Created", timeOrDash(n.Created)})

		addr := map[string]string{}
		if d := m.detail.network; d != nil && d.ID == n.ID {
			for _, ep := range d.Endpoints {
				addr[ep.ContainerID] = ep.IPv4
			}
		}
		p = append(p, kv{}, section(st, "Containers"))
		users := m.snap.NetworkUsers(n)
		if len(users) == 0 {
			p = append(p, kv{v: st.tone(toneDim, "  none — unused")})
		}
		sort.SliceStable(users, func(i, j int) bool { return users[i].Name < users[j].Name })
		for _, u := range users {
			line := "  " + st.tone(stateTone(u.State), stateGlyph(u.State)) + " " + u.Name
			if ip := addr[u.ID]; ip != "" {
				line += "  " + ip
			} else if !u.IsActive() {
				line += st.tone(toneDim, "  configured, not running")
			}
			p = append(p, kv{v: line})
		}
		if ls := labelsSorted(n.Labels, ""); len(ls) > 0 {
			p = append(p, kv{}, section(st, "Labels"))
			for _, l := range ls {
				p = append(p, kv{v: "  " + l})
			}
		}

	case KindCompose:
		proj, ok := m.projectByName(r.project)
		if !ok {
			return []string{st.tone(toneDim, "This project no longer has any containers.")}
		}
		p = append(p,
			kv{"Project", proj.Name},
			kv{"Containers", fmt.Sprintf("%d, %d running", len(proj.Containers), proj.Running)},
			kv{"Working dir", proj.WorkingDir},
			kv{"Config files", proj.ConfigFiles},
			kv{"", st.tone(toneDim, "Grouped by the com.docker.compose.project label. The paths above are")},
			kv{"", st.tone(toneDim, "what the labels record; Docker Desk never reads or runs compose files.")},
		)
		p = append(p, kv{}, section(st, "Services"))
		for _, c := range proj.Containers {
			p = append(p, kv{v: fmt.Sprintf("  %s %s  %s", st.tone(stateTone(c.State), stateGlyph(c.State)),
				fit(c.Service(), 14), c.Name) + st.tone(toneDim, "  "+c.Status)})
		}
		nets, vols := plan.ProjectLeftovers(m.snap, proj.Name)
		p = append(p, kv{}, section(st, "Networks and volumes carrying this project's label"))
		if len(nets)+len(vols) == 0 {
			p = append(p, kv{v: st.tone(toneDim, "  none")})
		}
		for _, n := range nets {
			p = append(p, kv{v: "  network  " + n})
		}
		for _, v := range vols {
			p = append(p, kv{v: "  volume   " + v})
		}
		p = append(p, kv{v: st.tone(toneDim, "  (never removed with the project; see their own kinds)")})
	}

	lines := kvLines(p, st)
	if m.detail.loading && m.detail.key == m.wantDetail && m.kind != KindCompose && !m.hasDetail() {
		lines = append(lines, "", st.tone(toneDim, "Loading details…"))
	}
	if m.detail.err != "" && m.detail.key == m.wantDetail {
		lines = append(lines, "", st.tone(toneBad, "Inspect failed: "+m.detail.err))
	}
	return lines
}

func (m *Model) hasDetail() bool {
	d := m.detail
	return d.container != nil || d.image != nil || d.volume != nil || d.network != nil
}

// ---- Logs ----------------------------------------------------------------------------

// renderLogs draws the newest lines that fit, anchored at the bottom. Only
// the lines that will be visible are copied out of the buffer.
func (m *Model) renderLogs(w, h int) []string {
	ring, st := m.hub.Log()
	bodyH := h - 1
	if bodyH < 1 {
		bodyH = 1
	}
	if st.Target == "" || st.Target != m.currentContainerID() {
		// The stream on hand belongs to the previous highlight; never show
		// one container's lines under another's name.
		out := make([]string, bodyH)
		return append(out, m.st.tone(toneDim, fit("opening log stream…", w)))
	}
	first, end := ring.Bounds()
	viewEnd := end
	if !m.logFollow {
		viewEnd = m.logEnd
		if viewEnd > end {
			viewEnd = end
		}
		if viewEnd < first+1 && end > first {
			viewEnd = first + 1 // the lines we were looking at scrolled out
		}
		m.logEnd = viewEnd
	}
	from := first
	if viewEnd > uint64(bodyH) && viewEnd-uint64(bodyH) > first {
		from = viewEnd - uint64(bodyH)
	}
	src, _ := ring.Read(from, viewEnd)

	// Wrap from the bottom up until the pane is full.
	rows := make([]string, 0, bodyH)
	for i := len(src) - 1; i >= 0 && len(rows) < bodyH; i-- {
		line := src[i]
		if !m.st.color {
			line = ansi.Strip(line)
		}
		var parts []string
		if m.logWrap {
			parts = wrapLine(line, w)
		} else {
			parts = []string{line}
		}
		for j := len(parts) - 1; j >= 0 && len(rows) < bodyH; j-- {
			rows = append(rows, parts[j])
		}
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	out := make([]string, 0, h)
	out = append(out, rows...)
	for len(out) < bodyH {
		out = append(out, "")
	}

	// Status row.
	var status string
	t := toneDim
	held := end - first
	switch {
	case st.Err != "":
		status, t = "■ log stream failed: "+st.Err, toneBad
	case !m.logFollow:
		status, t = fmt.Sprintf("‖ scrolled back — %d newer lines · G or f follows again", end-viewEnd), toneWarn
	case st.Ended:
		status = fmt.Sprintf("■ end of output: the container is not running · %d lines", held)
	case st.Streaming:
		status = fmt.Sprintf("● following · %d lines", held)
		if first > 0 {
			status += fmt.Sprintf(" · %d older lines scrolled out", first)
		}
		t = toneGood
	default:
		status = "log stream closed"
	}
	if !m.logWrap {
		status += " · wrap off"
	}
	return append(out, m.st.tone(t, fit(status, w)))
}

func (m *Model) currentContainerID() string {
	if m.kind != KindContainers {
		return ""
	}
	r, ok := m.currentRow()
	if !ok {
		return ""
	}
	return r.containerID
}

func (m *Model) scrollLogs(delta int) {
	ring, _ := m.hub.Log()
	first, end := ring.Bounds()
	cur := end
	if !m.logFollow {
		cur = m.logEnd
	}
	next := int64(cur) + int64(delta)
	if next < int64(first)+1 {
		next = int64(first) + 1
	}
	if next >= int64(end) {
		m.logFollow = true
		return
	}
	m.logFollow = false
	m.logEnd = uint64(next)
}

// ---- Stats ---------------------------------------------------------------------------

func (m *Model) renderStats(w, h int) []string {
	r, ok := m.currentRow()
	if !ok {
		return nil
	}
	st := m.st
	if r.state != engine.StateRunning {
		return []string{
			st.tone(toneDim, fmt.Sprintf("%s is not running (%s).", r.name, r.state)),
			st.tone(toneDim, "Statistics are only collected for running containers."),
		}
	}
	state := m.hub.Stats(r.containerID)
	switch {
	case !state.Collected:
		return []string{st.tone(toneDim, "Opening statistics stream…")}
	case !state.HasSample && state.Err != "":
		return []string{st.tone(toneBad, "Statistics stream failed: "+state.Err)}
	case !state.HasSample:
		return []string{st.tone(toneDim, "Waiting for the first samples…")}
	}
	s := state.Latest
	hist := m.hub.StatsHistory(r.containerID, live.StatsHistory)
	graphW := clamp(w-2, 10, live.StatsHistory)
	series := func(f func(live.Sample) float64) []float64 {
		out := make([]float64, len(hist))
		for i, x := range hist {
			out[i] = f(x)
		}
		return out
	}
	peak := func(vs []float64) float64 {
		p := 0.0
		for _, v := range vs {
			if v > p {
				p = v
			}
		}
		return p
	}
	cpu := series(func(x live.Sample) float64 { return x.CPUPercent })
	mem := series(func(x live.Sample) float64 { return float64(x.MemUsed) })
	rx := series(func(x live.Sample) float64 { return x.NetRxRate })
	tx := series(func(x live.Sample) float64 { return x.NetTxRate })
	rd := series(func(x live.Sample) float64 { return x.BlkRdRate })
	wr := series(func(x live.Sample) float64 { return x.BlkWrRate })

	label := func(name string) string { return st.tone(toneAccent, fit(name, 6)) }
	lines := []string{}
	if state.Stale || state.Ended {
		msg := "No sample for a while: the statistics stream has stalled."
		if state.Ended {
			msg = "The statistics stream ended; the container is no longer running."
		}
		lines = append(lines, st.tone(toneWarn, msg), "")
	}
	lines = append(lines,
		label("CPU")+fmt.Sprintf("%6.1f%%", s.CPUPercent)+st.tone(toneDim, fmt.Sprintf("   peak %.1f%%   (100%% = one core)", peak(cpu))),
		"  "+st.tone(toneGood, sparkline(cpu, graphW, 0)),
		label("MEM")+fmt.Sprintf("%8s / %s", humanIEC(s.MemUsed), humanIEC(s.MemLimit))+st.tone(toneDim, fmt.Sprintf("   %.1f%% of limit", s.MemPercent)),
		"  "+st.tone(toneAccent, sparkline(mem, graphW, 0)),
		label("NET")+fmt.Sprintf("↓ %-10s ↑ %-10s", humanRate(s.NetRxRate), humanRate(s.NetTxRate))+st.tone(toneDim, fmt.Sprintf("  total ↓ %s ↑ %s", humanBytes(int64(s.NetRx)), humanBytes(int64(s.NetTx)))),
	)
	if h >= 12 {
		lines = append(lines, "  "+st.tone(toneDim, "↓ ")+sparkline(rx, graphW-2, 0), "  "+st.tone(toneDim, "↑ ")+sparkline(tx, graphW-2, 0))
	}
	lines = append(lines,
		label("DISK")+fmt.Sprintf("r %-10s w %-10s", humanRate(s.BlkRdRate), humanRate(s.BlkWrRate))+st.tone(toneDim, fmt.Sprintf("  total r %s w %s", humanBytes(int64(s.BlkRead)), humanBytes(int64(s.BlkWrite)))),
	)
	if h >= 15 {
		lines = append(lines, "  "+st.tone(toneDim, "r ")+sparkline(rd, graphW-2, 0), "  "+st.tone(toneDim, "w ")+sparkline(wr, graphW-2, 0))
	}
	lines = append(lines,
		label("PIDS")+fmt.Sprintf("%d", s.Pids),
		"",
		st.tone(toneDim, fmt.Sprintf("%d samples, about one per second; newest on the right.", len(hist))),
	)
	return lines
}

// ---- Details pane ----------------------------------------------------------------------

func (m *Model) detailsTitle() (title, right string) {
	tabs := kindTabs[m.kind]
	var parts []string
	for i, t := range tabs {
		if i == clamp(m.tab[m.kind], 0, len(tabs)-1) {
			parts = append(parts, "["+t+"]")
		} else {
			parts = append(parts, t)
		}
	}
	title = "3 Details"
	if r, ok := m.currentRow(); ok {
		title += ": " + r.name
	}
	return title, strings.Join(parts, " ")
}

func (m *Model) renderDetails(w, h int) []string {
	m.detailHeight = h
	if _, ok := m.currentRow(); !ok {
		return []string{m.st.tone(toneDim, "Highlight an object in the list to see its details here.")}
	}
	switch m.tabName() {
	case tabLogs:
		return m.renderLogs(w, h)
	case tabStats:
		return m.renderStats(w, h)
	case tabInspect:
		if len(m.inspect.lines) == 0 {
			if m.detail.err != "" {
				return wrapLine(m.st.tone(toneBad, "Inspect failed: "+m.detail.err), w)
			}
			return []string{m.st.tone(toneDim, "Loading inspect data…")}
		}
		return m.inspect.render(w, h)
	default:
		m.info1.set(m.infoLines())
		return m.info1.render(w, h)
	}
}
