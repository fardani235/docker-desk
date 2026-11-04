package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/forensics"
)

// The Packages view: an image's ingredient list, what its SBOM declares
// against what its own package databases say is installed (docs/DECISIONS.md,
// K1–K15). It is opened with i from Images › Findings once the analysis has
// finished, and reads only the report that analysis made: opening it sends
// nothing, and the archive is not read again. The two sides are joined here,
// when the rows are needed, within one ecosystem at a time.

type pkgView struct {
	open bool
	rep  *forensics.Report
	ref  string // the image, as the Findings tab named it

	order  forensics.PkgOrder
	filter forensics.PkgFilter
	query  string
	typing bool
	before string // the query when typing began; Esc puts it back

	cursorKey string
	cursor    int
	off       int
	listH     int
	// top holds the list at its first line, so a header taller than the
	// list can be read: the view opens so, and k or g at the first row
	// return to it. curShown is whether the highlighted row was drawn.
	top      bool
	curShown bool

	// The join, sorted, and the rows the filter and search let through:
	// recomputed only when one of them changes.
	memo struct {
		rep    *forensics.Report
		order  forensics.PkgOrder
		filter forensics.PkgFilter
		query  string
		all    []forensics.PkgRow
		rows   []forensics.PkgRow
		keys   map[string]int // index in rows, by package identity
		counts [5]int
	}
}

// ---- opening and closing ------------------------------------------------------

// openPackages opens the view on the image the Findings tab shows. The desk
// underneath is not touched, so closing puts back exactly what was there.
func (m *Model) openPackages() {
	t, ok := m.findingsTarget()
	if !ok {
		return
	}
	rep := m.fx.cache[t.key]
	if rep == nil || rep.Packages == nil {
		m.setStatus(toneDim, "The package list opens once the analysis has finished.")
		return
	}
	if m.pk.rep != rep {
		// Another image: from the top, keeping the order and the state
		// shown, which are the operator's choices rather than the image's.
		m.pk = pkgView{rep: rep, order: m.pk.order, filter: m.pk.filter}
	}
	m.pk.open, m.pk.ref, m.pk.typing = true, t.ref, false
	if m.pk.cursor == 0 {
		m.pk.top = true
	}
}

func (m *Model) closePackages() {
	m.pk.open, m.pk.typing = false, false
}

// pkgRows returns every joined row and the rows the view shows.
func (m *Model) pkgRows() (all, shown []forensics.PkgRow) {
	p := &m.pk
	if p.memo.rep != p.rep || p.memo.order != p.order {
		p.memo.all = forensics.JoinPackages(p.rep.Packages)
		forensics.SortPackages(p.memo.all, p.order)
		p.memo.counts = forensics.PkgCounts(p.memo.all)
		p.memo.rep, p.memo.order = p.rep, p.order
		p.memo.rows = nil
		p.memo.query = "\x00" // force the filter below
	}
	if p.memo.rows == nil || p.memo.filter != p.filter || p.memo.query != p.query {
		p.memo.rows = make([]forensics.PkgRow, 0, len(p.memo.all))
		p.memo.keys = map[string]int{}
		for _, r := range p.memo.all {
			if p.filter.Shows(r.State) && r.Matches(p.query) {
				p.memo.keys[r.Key()] = len(p.memo.rows)
				p.memo.rows = append(p.memo.rows, r)
			}
		}
		p.memo.filter, p.memo.query = p.filter, p.query
	}
	return p.memo.all, p.memo.rows
}

// pkgCursor returns the index of the highlighted package: the one last
// chosen if it is shown, else the same position. The package chosen is
// kept while a filter hides it, so it is highlighted again when the filter
// shows it; only moving chooses another.
func (m *Model) pkgCursor(rows []forensics.PkgRow) int {
	if len(rows) == 0 {
		return -1
	}
	if i, ok := m.pk.memo.keys[m.pk.cursorKey]; ok && m.pk.cursorKey != "" && i < len(rows) {
		m.pk.cursor = i
		return i
	}
	m.pk.cursor = clamp(m.pk.cursor, 0, len(rows)-1)
	if m.pk.cursorKey == "" {
		m.pk.cursorKey = rows[m.pk.cursor].Key()
	}
	return m.pk.cursor
}

// ---- keys ----------------------------------------------------------------------------

func (m *Model) onPackagesKey(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	if m.pk.typing {
		m.onPackagesSearchKey(k)
		return nil
	}
	_, rows := m.pkgRows()
	cur := m.pkgCursor(rows)
	page := max(m.pk.listH-1, 1)
	move := func(i int) {
		if len(rows) == 0 {
			return
		}
		if m.pk.top && !m.pk.curShown && i > cur {
			m.pk.top = false // first bring the highlighted row into view
			return
		}
		i = clamp(i, 0, len(rows)-1)
		m.pk.top = i == 0 && cur == 0 && i <= cur
		m.pk.cursor, m.pk.cursorKey = i, rows[i].Key()
	}
	switch key {
	case "esc", "i":
		if m.pk.query != "" && key == "esc" {
			m.pk.query = "" // Esc clears the search first, then closes
			return nil
		}
		m.closePackages()
	case "q":
		return m.requestQuit()
	case "?":
		m.dlg = &dialog{kind: dlgHelp}
		m.dlg.text.wrap = true
	case "T":
		m.setTheme(nextTheme(m.st.theme))
	case "/":
		m.pk.typing, m.pk.before = true, m.pk.query
	// The line above the list says the order and the state shown; the
	// desk's status line is left as it was, for when the view closes.
	case "o":
		m.pk.order = m.pk.order.Next()
	case "s":
		m.pk.filter = m.pk.filter.Next()
	case "S":
		m.pk.filter = forensics.ShowAll
	case "down", "j":
		move(cur + 1)
	case "up", "k":
		move(cur - 1)
	case "pgdown", "ctrl+f", " ":
		move(cur + page)
	case "pgup", "ctrl+b":
		move(cur - page)
	case "g", "home":
		move(0)
		m.pk.top = true
	case "G", "end":
		move(len(rows) - 1)
	}
	return nil
}

func (m *Model) onPackagesSearchKey(k tea.KeyMsg) {
	switch k.Type {
	case tea.KeyEnter:
		m.pk.typing = false
	case tea.KeyEsc:
		m.pk.typing, m.pk.query = false, m.pk.before
	case tea.KeyBackspace:
		if r := []rune(m.pk.query); len(r) > 0 {
			m.pk.query = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		m.pk.query = ""
	case tea.KeyRunes, tea.KeySpace:
		if len(m.pk.query) < 80 {
			m.pk.query += string(k.Runes)
		}
	}
}

func (m *Model) packagesHints() []string {
	if m.pk.typing {
		return []string{"type", "to search", "Enter", "keep", "Esc", "undo"}
	}
	pairs := []string{"↑↓", "move", "/", "search", "o", "order", "s", "state"}
	if m.pk.query != "" {
		pairs = append(pairs, "Esc", "clear search")
	}
	return pairs
}

// ---- drawing ---------------------------------------------------------------------

var pkgStateLabel = [...]string{"both", "differs", "declared only", "undeclared", "not checkable"}

var pkgStateWords = [...]string{
	"declared, and installed at that version",
	"declared, and installed at another version",
	"declared, and the package database does not list it",
	"installed, and the SBOM does not list it",
	"not checkable",
}

func pkgStateTone(s forensics.PkgState) tone {
	switch s {
	case forensics.StateBoth:
		return toneNormal
	case forensics.StateDiffers, forensics.StateDeclaredOnly:
		return toneWarn
	case forensics.StateUndeclared:
		return toneBad
	}
	return toneDim
}

// pkgLine is a line of the scrolling part; a package row carries its index.
type pkgLine struct {
	text string
	row  int // -1 for a line of the header
}

// packagesBox draws the view over the desk's body: w×h cells.
func (m *Model) packagesBox(w, h int) []string {
	st := m.st
	inner := w - 4
	bodyH := h - 2
	all, rows := m.pkgRows()
	cur := m.pkgCursor(rows)

	// The scrolling part is the header, the column titles, then the rows;
	// only the rows in view are drawn, so a list of thousands costs a
	// frame no more than a list of ten.
	head := m.pkgHeaderLines(inner, all)
	header := len(head)
	cols := pkgColumns(inner - 2)
	empty := ""
	if len(rows) == 0 {
		empty = "No package is listed."
		if len(all) > 0 {
			empty = fmt.Sprintf("No package matches (%s%s). s shows another state, Esc clears the search.", m.pk.filter, m.searchWords())
		}
	}
	total := header + 1 + max(len(rows), 1)
	lineAt := func(i int) pkgLine {
		switch {
		case i < header:
			return head[i]
		case i == header:
			return pkgLine{text: st.tone(toneDim, pkgColumnTitles(cols)), row: -1}
		case len(rows) == 0:
			return pkgLine{text: st.tone(toneDim, empty), row: -1}
		}
		r := i - header - 1
		return pkgLine{text: m.pkgRowText(rows[r], cols), row: r}
	}

	var detail []string
	if cur >= 0 {
		detail = m.pkgDetail(rows[cur], inner)
	}
	detailH := clamp(bodyH/3, 3, 8)
	if len(detail) > detailH {
		detail = detail[:detailH]
	}
	for len(detail) < detailH {
		detail = append(detail, "")
	}
	// The line that says what is shown stays put above the list.
	listH := max(bodyH-len(detail)-2, 3)
	m.pk.listH = listH

	curLine := -1
	if cur >= 0 {
		curLine = header + 1 + cur
	}
	maxOff := max(total-listH, 0)
	if curLine >= 0 {
		m.pk.off = clamp(m.pk.off, curLine-listH+1, curLine)
		if cur == 0 && curLine < listH {
			m.pk.off = 0 // the header above the first row stays in view
		}
	}
	m.pk.off = clamp(m.pk.off, 0, maxOff)
	if m.pk.top {
		m.pk.off = 0
	}
	m.pk.curShown = curLine >= m.pk.off && curLine < m.pk.off+listH

	body := []string{m.pkgShowingLine(inner, len(rows), len(all))}
	for i := m.pk.off; i < total && len(body) < listH+1; i++ {
		l := lineAt(i)
		if l.row >= 0 && l.row == cur {
			body = append(body, st.selected.Render("▸ "+fit(ansi.Strip(l.text), inner-2)))
		} else {
			body = append(body, "  "+l.text)
		}
	}
	for len(body) < listH+1 {
		body = append(body, "")
	}
	pos := " no rows "
	if cur >= 0 {
		pos = fmt.Sprintf(" %d of %d ", cur+1, len(rows))
	}
	body = append(body, st.tone(toneDim, strings.Repeat("─", max(inner-len([]rune(pos))-1, 0))+pos+"─"))
	body = append(body, detail...)

	padded := make([]string, 0, len(body))
	for _, l := range body {
		padded = append(padded, " "+fit(l, inner)+" ")
	}
	title := "Packages — what the SBOM declares, against what the image's databases say is installed"
	if w < wideMinWidth {
		title = "Packages — declared against installed"
	}
	return st.box(title, m.pk.ref, padded, w, h, true)
}

func (m *Model) searchWords() string {
	if m.pk.query == "" {
		return ""
	}
	return fmt.Sprintf(", search %q", m.pk.query)
}

// pkgShowingLine says what the list is: its order, the state shown, the
// search, and how many rows that leaves.
func (m *Model) pkgShowingLine(w, shown, all int) string {
	st := m.st
	parts := []string{
		"by " + st.tone(toneAccent, m.pk.order.String()),
		"showing " + st.tone(toneAccent, m.pk.filter.String()),
	}
	switch {
	case m.pk.typing:
		parts = append(parts, "search: "+st.tone(toneAccent, m.pk.query+"▏"))
	case m.pk.query != "":
		parts = append(parts, "search: "+st.tone(toneAccent, m.pk.query))
	}
	parts = append(parts, fmt.Sprintf("%d of %d rows", shown, all))
	return fit(strings.Join(parts, st.tone(toneDim, " · ")), w)
}

// pkgHeaderLines is what the view opens on: both sides, how many each
// lists, what is not known and why, and how the rows agree.
func (m *Model) pkgHeaderLines(w int, all []forensics.PkgRow) []pkgLine {
	st := m.st
	p := m.pk.rep.Packages
	var out []pkgLine
	add := func(label, text string, tn tone) {
		for i, part := range wrapWords(text, max(w-13, 20)) {
			l := "           "
			if i == 0 {
				l = st.tone(toneDim, fit(label, 10)) + " "
			}
			out = append(out, pkgLine{text: l + st.tone(tn, part), row: -1})
		}
	}
	sb := p.SBOM
	switch {
	case !sb.Listed:
		add("SBOM", "none: the image carries no SBOM attestation", toneWarn)
	case sb.Unknown != "":
		add("SBOM", "unknown: "+sb.Unknown, toneWarn)
	default:
		s := fmt.Sprintf("%s · %d packages · attestation %s", sb.Format, sb.Total, engineShort(sb.Digest))
		if sb.Others > 0 {
			s += fmt.Sprintf(" · %d more SBOM(s) not compared", sb.Others)
		}
		add("SBOM", s, toneNormal)
		if sb.Capped() {
			add("bound", fmt.Sprintf("%d of the SBOM's %d packages are kept (the desk keeps %d): rows beyond them are not shown, and an installed package cannot be called undeclared", len(sb.Packages), sb.Total, len(sb.Packages)), toneWarn)
		}
	}
	var absent []string
	// Databases unknown for one reason are said once.
	var reasons []string
	unknownFor := map[string][]string{}
	for _, d := range p.DBs {
		if d.State == forensics.DBUnknown {
			if unknownFor[d.Unknown] == nil {
				reasons = append(reasons, d.Unknown)
			}
			unknownFor[d.Unknown] = append(unknownFor[d.Unknown], d.Manager)
		}
	}
	for _, why := range reasons {
		add("installed", joinAnd(unknownFor[why])+" unknown: "+why, toneWarn)
	}
	for _, d := range p.DBs {
		switch d.State {
		case forensics.DBAbsent:
			absent = append(absent, d.Manager)
		case forensics.DBRead:
			add("installed", fmt.Sprintf("%s · %s (layer %d) · %d packages", d.Manager, d.Path, d.Layer, d.Total), toneNormal)
			if d.Capped() {
				add("bound", fmt.Sprintf("%d of %s's %d packages are kept: a declared package not among them is not called declared only", len(d.Packages), d.Manager, d.Total), toneWarn)
			}
		}
		for _, n := range d.Notes {
			add("note", n, toneDim)
		}
	}
	if len(absent) == 3 {
		add("installed", "no dpkg, apk or rpm database in any layer", toneDim)
	} else if len(absent) > 0 {
		add("", "no "+strings.Join(absent, " or ")+" database in any layer", toneDim)
	}
	n := m.pk.memo.counts
	var parts []string
	for s, c := range n {
		if c > 0 {
			parts = append(parts, st.tone(pkgStateTone(forensics.PkgState(s)), fmt.Sprintf("%d %s", c, forensics.PkgState(s))))
		}
	}
	// Every count is shown: the line wraps between counts, never cuts one.
	label, line, lw := st.tone(toneDim, fit("rows", 10))+" ", "", 0
	for _, part := range parts {
		pw := ansi.StringWidth(part)
		if lw > 0 && 11+lw+3+pw > w {
			out = append(out, pkgLine{text: label + line, row: -1})
			label, line, lw = "           ", "", 0
		}
		if lw > 0 {
			line += st.tone(toneDim, " · ")
			lw += 3
		}
		line += part
		lw += pw
	}
	if lw > 0 {
		out = append(out, pkgLine{text: label + line, row: -1})
	}
	out = append(out, pkgLine{row: -1})
	return out
}

func joinAnd(xs []string) string {
	if len(xs) < 2 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func engineShort(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

type pkgCols struct{ name, version, license, typ, layer, state int }

const pkgGap = 2

func pkgColumns(w int) pkgCols {
	c := pkgCols{typ: 6, layer: 5, state: 13}
	rest := max(w-c.typ-c.layer-c.state-5*pkgGap, 24)
	c.name = rest * 4 / 10
	c.version = rest * 3 / 10
	c.license = rest - c.name - c.version
	return c
}

func (c pkgCols) join(name, version, license, typ, layer, state string) string {
	gap := strings.Repeat(" ", pkgGap)
	return fit(name, c.name) + gap + fit(version, c.version) + gap + fit(license, c.license) + gap +
		fit(typ, c.typ) + gap + fit(layer, c.layer) + gap + fit(state, c.state)
}

func pkgColumnTitles(c pkgCols) string {
	return c.join("NAME", "VERSION", "LICENCE", "TYPE", "LAYER", "STATE")
}

func pkgLayer(r forensics.PkgRow) string {
	ip := r.Installed
	switch {
	case ip == nil || ip.Layer == 0:
		return "--"
	case ip.LayerUnsure != "":
		return fmt.Sprintf("L%d?", ip.Layer)
	}
	return fmt.Sprintf("L%d", ip.Layer)
}

func (m *Model) pkgRowText(r forensics.PkgRow, c pkgCols) string {
	typ := r.Type
	if typ == "" {
		typ = "?"
	}
	lic := r.License()
	if lic == "" {
		lic = "—"
	}
	text := c.join(r.Name(), r.Version(), lic, typ, pkgLayer(r), pkgStateLabel[r.State])
	return m.st.tone(pkgStateTone(r.State), text)
}

func (m *Model) pkgDetail(r forensics.PkgRow, w int) []string {
	st := m.st
	var out []string
	kv := func(k, v string, tn tone) {
		for i, part := range wrapWords(v, max(w-11, 10)) {
			label := "           "
			if i == 0 {
				label = st.tone(toneDim, fit(k, 10)) + " "
			}
			out = append(out, label+st.tone(tn, part))
		}
	}
	state := pkgStateWords[r.State]
	if r.State == forensics.StateUncheckable {
		state += ": " + r.Why
	}
	kv("state", state, pkgStateTone(r.State))
	if d := r.Declared; d != nil {
		s := "version " + orDash(d.Version) + " · licence " + orDash(d.License)
		if d.PURL != "" {
			s += " · " + d.PURL
		}
		kv("declared", s, toneNormal)
	}
	if ip := r.Installed; ip != nil {
		s := "version " + orDash(ip.Version)
		if ip.Arch != "" {
			s += " · " + ip.Arch
		}
		if ip.License != "" {
			s += " · licence " + ip.License
		}
		kv("installed", s+" · listed by "+ip.Source, toneNormal)
		if ip.Layer > 0 {
			by := fmt.Sprintf("layer %d", ip.Layer)
			if l := ip.Layer - 1; l < len(m.pk.rep.Layers) && m.pk.rep.Layers[l].Instruction != "" {
				by += " · " + m.pk.rep.Layers[l].Instruction
			}
			kv("added by", by, toneNormal)
		}
		if ip.LayerUnsure != "" {
			kv("unsure", ip.LayerUnsure, toneDim)
		}
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
