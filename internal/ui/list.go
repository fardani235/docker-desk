package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/plan"
)

// row is one line of the List pane. Its key is the object's identity, so the
// highlight follows the object — not the position — across refreshes.
type row struct {
	key    string
	cells  []string
	tone   tone
	name   string // for messages
	search string // lower-cased text the filter matches

	containerID string
	state       string
	imageID     string
	imageRef    string // empty for an untagged image
	volume      string
	networkID   string
	project     string
}

type column struct {
	title string
	min   int  // narrowest useful width
	grow  int  // share of spare width; 0 = fixed
	right bool // right-aligned
	prio  int  // columns with the highest prio are dropped first when narrow
}

var columns = [kindCount][]column{
	KindContainers: {
		{title: "NAME", min: 12, grow: 3},
		{title: "IMAGE", min: 10, grow: 3, prio: 2},
		{title: "STATE", min: 10},
		{title: "CPU", min: 6, right: true, prio: 1},
		{title: "MEM", min: 8, right: true, prio: 1},
		{title: "STATUS", min: 12, grow: 2, prio: 3},
		{title: "PORTS", min: 10, grow: 2, prio: 4},
	},
	KindImages: {
		{title: "REPOSITORY", min: 14, grow: 4},
		{title: "TAG", min: 8, grow: 2},
		{title: "IMAGE ID", min: 12, prio: 2},
		{title: "SIZE", min: 8, right: true},
		{title: "AGE", min: 5, right: true, prio: 1},
		{title: "USED BY", min: 8, right: true, prio: 3},
	},
	KindVolumes: {
		{title: "NAME", min: 16, grow: 4},
		{title: "DRIVER", min: 7, prio: 2},
		{title: "IN USE", min: 14, grow: 1},
		{title: "CREATED", min: 8, right: true, prio: 1},
	},
	KindNetworks: {
		{title: "NAME", min: 14, grow: 3},
		{title: "DRIVER", min: 7},
		{title: "SCOPE", min: 6, prio: 3},
		{title: "CONTAINERS", min: 10, right: true},
		{title: "SUBNET", min: 15, grow: 1, prio: 1},
		{title: "ID", min: 12, prio: 2},
	},
	KindCompose: {
		{title: "PROJECT", min: 14, grow: 3},
		{title: "CONTAINERS", min: 10, right: true},
		{title: "RUNNING", min: 8, right: true},
		{title: "WORKING DIR", min: 12, grow: 3, prio: 1},
	},
}

// Indices of the live cells of a container row, filled at draw time.
const (
	colContainerCPU = 3
	colContainerMem = 4
)

func stateTone(state string) tone {
	switch state {
	case engine.StateRunning:
		return toneGood
	case engine.StatePaused, engine.StateRestarting, engine.StateRemoving:
		return toneWarn
	case engine.StateDead:
		return toneBad
	case engine.StateCreated, engine.StateExited:
		return toneDim
	}
	return toneNormal
}

func stateGlyph(state string) string {
	switch state {
	case engine.StateRunning:
		return "●"
	case engine.StatePaused:
		return "‖"
	case engine.StateRestarting:
		return "↻"
	case engine.StateExited, engine.StateCreated:
		return "○"
	case engine.StateDead:
		return "✗"
	}
	return "·"
}

func formatPorts(ports []engine.Port) string {
	var parts []string
	seen := map[string]bool{}
	for _, p := range ports {
		var s string
		if p.HostPort != 0 {
			s = fmt.Sprintf("%d→%d/%s", p.HostPort, p.PrivatePort, p.Proto)
		} else {
			s = fmt.Sprintf("%d/%s", p.PrivatePort, p.Proto)
		}
		if !seen[s] {
			seen[s] = true
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// shortImage trims what makes image names long without adding information.
func shortImage(ref string) string {
	if at := strings.Index(ref, "@sha256:"); at >= 0 {
		ref = ref[:at]
	}
	ref = strings.TrimPrefix(ref, "docker.io/library/")
	ref = strings.TrimPrefix(ref, "docker.io/")
	if strings.HasPrefix(ref, "sha256:") {
		return engine.ShortID(ref)
	}
	return ref
}

// ensureRows rebuilds the rows of a kind when the data or the filter changed.
func (m *Model) ensureRows(k Kind) {
	if m.rowsFor[k] == m.dataVersion+1 {
		return
	}
	m.rowsFor[k] = m.dataVersion + 1
	var rows []row
	now := time.Now()
	switch k {
	case KindContainers:
		cs := append([]engine.Container(nil), m.snap.Containers...)
		sort.SliceStable(cs, func(i, j int) bool {
			if cs[i].IsActive() != cs[j].IsActive() {
				return cs[i].IsActive()
			}
			return cs[i].Name < cs[j].Name
		})
		for _, c := range cs {
			state := stateGlyph(c.State) + " " + c.State
			rows = append(rows, row{
				key: c.ID, name: c.Name, containerID: c.ID, state: c.State,
				tone:  stateTone(c.State),
				cells: []string{c.Name, shortImage(c.Image), state, "", "", c.Status, formatPorts(c.Ports)},
			})
		}
	case KindImages:
		type irow struct {
			img       engine.Image
			ref       string
			repo, tag string
		}
		var irs []irow
		for _, img := range m.snap.Images {
			refs := img.Refs()
			if len(refs) == 0 {
				irs = append(irs, irow{img: img, repo: "<none>", tag: "<none>"})
				continue
			}
			for _, ref := range refs {
				repo, tag := engine.SplitRef(ref)
				irs = append(irs, irow{img: img, ref: ref, repo: repo, tag: tag})
			}
		}
		sort.SliceStable(irs, func(i, j int) bool {
			if (irs[i].ref == "") != (irs[j].ref == "") {
				return irs[j].ref == ""
			}
			if irs[i].repo != irs[j].repo {
				return irs[i].repo < irs[j].repo
			}
			if irs[i].tag != irs[j].tag {
				return irs[i].tag < irs[j].tag
			}
			return irs[i].img.ID < irs[j].img.ID
		})
		for _, ir := range irs {
			users := len(m.snap.ImageUsers(ir.img))
			used := "-"
			t := toneNormal
			if users > 0 {
				used = fmt.Sprintf("%d ctr", users)
			} else {
				t = toneDim
			}
			if ir.img.Dangling {
				used = "dangling"
			}
			name := ir.ref
			if name == "" {
				name = "<untagged> " + engine.ShortID(ir.img.ID)
			}
			rows = append(rows, row{
				key: ir.img.ID + "|" + ir.ref, name: name, imageID: ir.img.ID, imageRef: ir.ref, tone: t,
				cells: []string{ir.repo, ir.tag, engine.ShortID(ir.img.ID), humanBytes(ir.img.Size), humanAge(ir.img.Created, now), used},
			})
		}
	case KindVolumes:
		vs := append([]engine.Volume(nil), m.snap.Volumes...)
		sort.SliceStable(vs, func(i, j int) bool {
			if vs[i].Anonymous() != vs[j].Anonymous() {
				return vs[j].Anonymous()
			}
			return vs[i].Name < vs[j].Name
		})
		for _, v := range vs {
			users := m.snap.VolumeUsers(v.Name)
			use, t := "unused", toneDim
			if len(users) > 0 {
				use, t = fmt.Sprintf("%d ctr", len(users)), toneNormal
				if len(users) == 1 {
					use = users[0].Name
				}
			}
			name := v.Name
			if len(name) == 64 {
				name = name[:12] + "…"
			}
			if v.Anonymous() {
				name += " (anonymous)"
			}
			created := "-"
			if ts, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
				created = humanAge(ts, now)
			}
			rows = append(rows, row{
				key: v.Name, name: v.Name, volume: v.Name, tone: t,
				cells: []string{name, v.Driver, use, created},
			})
		}
	case KindNetworks:
		ns := append([]engine.Network(nil), m.snap.Networks...)
		sort.SliceStable(ns, func(i, j int) bool { return ns[i].Name < ns[j].Name })
		for _, n := range ns {
			users := len(m.snap.NetworkUsers(n))
			t := toneNormal
			if users == 0 && !n.Predefined() {
				t = toneDim
			}
			rows = append(rows, row{
				key: n.ID, name: n.Name, networkID: n.ID, tone: t,
				cells: []string{n.Name, n.Driver, n.Scope, fmt.Sprintf("%d", users), strings.Join(n.Subnets, ","), engine.ShortID(n.ID)},
			})
		}
	case KindCompose:
		for _, p := range m.projects {
			t := toneGood
			if p.Running == 0 {
				t = toneDim
			} else if p.Running < len(p.Containers) {
				t = toneWarn
			}
			rows = append(rows, row{
				key: p.Name, name: p.Name, project: p.Name, tone: t,
				cells: []string{p.Name, fmt.Sprintf("%d", len(p.Containers)), fmt.Sprintf("%d", p.Running), p.WorkingDir},
			})
		}
	}

	needle := strings.ToLower(strings.TrimSpace(m.filter[k]))
	out := rows[:0]
	for _, r := range rows {
		r.search = strings.ToLower(strings.Join(r.cells, " "))
		if needle == "" || strings.Contains(r.search, needle) {
			out = append(out, r)
		}
	}
	m.rows[k] = out

	// Keep the highlight on the same object; if it is gone, on the nearest row.
	s := &m.sel[k]
	found := false
	if s.key != "" {
		for i, r := range out {
			if r.key == s.key {
				s.idx, found = i, true
				break
			}
		}
	}
	if !found {
		s.idx = clamp(s.idx, 0, len(out)-1)
		if len(out) > 0 {
			s.key = out[s.idx].key
		} else {
			s.key = ""
		}
	}
}

// invalidateRows forces a rebuild (after a filter change).
func (m *Model) invalidateRows(k Kind) { m.rowsFor[k] = 0 }

func (m *Model) currentRow() (row, bool) {
	rows := m.rows[m.kind]
	s := m.sel[m.kind]
	if s.idx < 0 || s.idx >= len(rows) {
		return row{}, false
	}
	return rows[s.idx], true
}

func (m *Model) moveSelection(delta int) {
	rows := m.rows[m.kind]
	if len(rows) == 0 {
		return
	}
	s := &m.sel[m.kind]
	s.idx = clamp(s.idx+delta, 0, len(rows)-1)
	s.key = rows[s.idx].key
}

func (m *Model) selectIndex(i int) {
	rows := m.rows[m.kind]
	if len(rows) == 0 {
		return
	}
	s := &m.sel[m.kind]
	s.idx = clamp(i, 0, len(rows)-1)
	s.key = rows[s.idx].key
}

// kindCount returns how many objects of a kind exist (unfiltered).
func (m *Model) countOf(k Kind) (n int, known bool) {
	switch k {
	case KindContainers:
		return len(m.snap.Containers), m.loaded[listContainers]
	case KindImages:
		return len(m.snap.Images), m.loaded[listImages]
	case KindVolumes:
		return len(m.snap.Volumes), m.loaded[listVolumes]
	case KindNetworks:
		return len(m.snap.Networks), m.loaded[listNetworks]
	case KindCompose:
		return len(m.projects), m.loaded[listContainers]
	}
	return 0, false
}

func listOf(k Kind) int {
	switch k {
	case KindImages:
		return listImages
	case KindVolumes:
		return listVolumes
	case KindNetworks:
		return listNetworks
	}
	return listContainers
}

var emptyText = [kindCount]string{
	KindContainers: "No containers on this daemon.",
	KindImages:     "No images on this daemon.",
	KindVolumes:    "No volumes on this daemon.",
	KindNetworks:   "No networks on this daemon.",
	KindCompose:    "No compose projects found: no container carries the com.docker.compose.project label.",
}

// layoutColumns decides which columns fit in w cells and how wide each is.
func layoutColumns(cols []column, w int) (idx []int, widths []int) {
	const gap = 2
	keep := make([]bool, len(cols))
	for i := range keep {
		keep[i] = true
	}
	need := func() int {
		n, total := 0, 0
		for i, c := range cols {
			if keep[i] {
				total += c.min
				n++
			}
		}
		return total + gap*(n-1)
	}
	for need() > w {
		drop, worst := -1, 0
		for i, c := range cols {
			if keep[i] && c.prio > worst {
				drop, worst = i, c.prio
			}
		}
		if drop < 0 {
			break
		}
		keep[drop] = false
	}
	spare := w - need()
	grow := 0
	for i, c := range cols {
		if keep[i] {
			grow += c.grow
		}
	}
	for i, c := range cols {
		if !keep[i] {
			continue
		}
		cw := c.min
		if spare > 0 && grow > 0 && c.grow > 0 {
			cw += spare * c.grow / grow
		}
		idx = append(idx, i)
		widths = append(widths, cw)
	}
	return idx, widths
}

// renderList draws the List pane's content: w×h cells.
func (m *Model) renderList(w, h int, focused bool) []string {
	k := m.kind
	li := listOf(k)
	lines := make([]string, 0, h)

	if !m.loaded[li] {
		if m.listErr[li] != "" {
			lines = append(lines, m.st.tone(toneBad, "Could not load "+strings.ToLower(kindNames[k])+":"))
			lines = append(lines, wrapLine(m.st.tone(toneBad, m.listErr[li]), w)...)
			lines = append(lines, m.st.tone(toneDim, "Retrying automatically. Press r to retry now."))
			return lines
		}
		return append(lines, m.st.tone(toneDim, "Loading…"))
	}

	rows := m.rows[k]
	if m.listErr[li] != "" {
		lines = append(lines, m.st.tone(toneBad, fit("▲ stale list — refresh failed: "+m.listErr[li], w)))
		h--
	}
	if len(rows) == 0 {
		if m.filter[k] != "" {
			return append(lines, m.st.tone(toneDim, fmt.Sprintf("Nothing matches the filter %q. Esc clears it.", m.filter[k])))
		}
		return append(lines, wrapLine(m.st.tone(toneDim, emptyText[k]), w)...)
	}

	const cursorW = 2
	idx, widths := layoutColumns(columns[k], w-cursorW)
	cell := func(text string, ci int) string {
		if columns[k][idx[ci]].right {
			return fitRight(text, widths[ci])
		}
		return fit(text, widths[ci])
	}
	var hdr strings.Builder
	hdr.WriteString(strings.Repeat(" ", cursorW))
	for ci := range idx {
		if ci > 0 {
			hdr.WriteString("  ")
		}
		hdr.WriteString(cell(columns[k][idx[ci]].title, ci))
	}
	lines = append(lines, m.st.colHeader.Render(hdr.String()))

	visible := h - 1
	if visible < 1 {
		visible = 1
	}
	m.listHeight = visible
	s := &m.sel[k]
	if s.idx < s.off {
		s.off = s.idx
	}
	if s.idx >= s.off+visible {
		s.off = s.idx - visible + 1
	}
	s.off = clamp(s.off, 0, max(len(rows)-visible, 0))

	for i := s.off; i < len(rows) && i < s.off+visible; i++ {
		r := rows[i]
		cells := r.cells
		if k == KindContainers {
			cells = append([]string(nil), r.cells...)
			cells[colContainerCPU], cells[colContainerMem] = m.liveCells(r)
		}
		var sb strings.Builder
		for ci := range idx {
			if ci > 0 {
				sb.WriteString("  ")
			}
			sb.WriteString(cell(cells[idx[ci]], ci))
		}
		text := sb.String()
		switch {
		case i == s.idx && focused:
			lines = append(lines, m.st.selected.Render("▸ "+text))
		case i == s.idx:
			lines = append(lines, m.st.selectedDim.Render("▸ "+text))
		default:
			lines = append(lines, "  "+m.st.tone(r.tone, text))
		}
	}
	return lines
}

// liveCells returns the CPU and memory cells of a container row.
func (m *Model) liveCells(r row) (cpu, mem string) {
	if r.state != engine.StateRunning {
		return "", ""
	}
	st := m.hub.Stats(r.containerID)
	switch {
	case !st.Collected:
		return "n/c", "n/c" // beyond the statistics stream cap
	case !st.HasSample:
		return "…", "…"
	case st.Stale || st.Ended:
		return "?", "?"
	}
	return fmt.Sprintf("%.1f%%", st.Latest.CPUPercent), humanIEC(st.Latest.MemUsed)
}

func (m *Model) listTitle() (title, right string) {
	k := m.kind
	n, known := m.countOf(k)
	title = fmt.Sprintf("2 %s", kindNames[k])
	if known {
		shown := len(m.rows[k])
		if m.filter[k] != "" {
			title += fmt.Sprintf(" (%d of %d)", shown, n)
		} else if k == KindImages && shown != n {
			title += fmt.Sprintf(" (%d, %d refs)", n, shown)
		} else {
			title += fmt.Sprintf(" (%d)", n)
		}
	}
	switch {
	case m.filtering:
		right = m.filterIn.View()
	case m.filter[k] != "":
		right = "filter: " + m.filter[k]
	}
	return title, right
}

// renderKinds draws the Kinds pane.
func (m *Model) renderKinds(w, h int, focused bool) []string {
	lines := make([]string, 0, h)
	for k := Kind(0); k < kindCount; k++ {
		count := "…"
		if n, known := m.countOf(k); known {
			count = fmt.Sprintf("%d", n)
		}
		nameW := w - 2 - len(count) - 1
		text := fit(kindNames[k], nameW) + " " + count
		switch {
		case k == m.kind && focused:
			lines = append(lines, m.st.selected.Render("▸ "+text))
		case k == m.kind:
			lines = append(lines, m.st.selectedDim.Render("▸ "+text))
		default:
			lines = append(lines, "  "+text)
		}
	}
	if h >= int(kindCount)+5 && m.loaded[listContainers] {
		var running, paused, stopped int
		for _, c := range m.snap.Containers {
			switch c.State {
			case engine.StateRunning, engine.StateRestarting:
				running++
			case engine.StatePaused:
				paused++
			default:
				stopped++
			}
		}
		lines = append(lines, "")
		lines = append(lines, m.st.tone(toneGood, fit(fmt.Sprintf("  ● %d running", running), w)))
		if paused > 0 {
			lines = append(lines, m.st.tone(toneWarn, fit(fmt.Sprintf("  ‖ %d paused", paused), w)))
		}
		lines = append(lines, m.st.tone(toneDim, fit(fmt.Sprintf("  ○ %d stopped", stopped), w)))
	}
	return lines
}

// projectByName returns a compose project from the current data.
func (m *Model) projectByName(name string) (plan.Project, bool) {
	for _, p := range m.projects {
		if p.Name == name {
			return p, true
		}
	}
	return plan.Project{}, false
}
