package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// ---- Pointer input -----------------------------------------------------------------
//
// A click acts on what the latest frame drew at its cell, never on what the
// state would draw now: every draw site records the regions it draws (a
// pane, a row, a tab, a dialog entry) into the frame's hit map while it
// draws them (docs/DECISIONS.md, N1). The map is made current only once
// the whole frame is drawn, it is dropped on a resize and on quit, and every
// hit is checked against the state before it is applied.
//
// The pointer navigates; it never consents. No path leads from a mouse
// event to confirmed or submitForm: a confirmation records no interactive
// region at all, and the wheel only moves cursors.

// hitKind is what a region of the frame means.
type hitKind int

const (
	hitPane       hitKind = iota // a pane's rectangle
	hitKindRow                   // index: the Kind
	hitListRow                   // index: row, key: row key, sub: the Kind
	hitDetailsTab                // index: tab, key: tab name, sub: the Kind
	hitDaemon                    // index: member ID
	hitJobRow                    // index: job ID
	hitEventRow                  // index: absolute ring position
	hitText                      // index: a textRow
	hitFilesRow                  // key: path
	hitSpaceRow                  // key: row key
	hitRepoRow                   // key: tag
	hitOverlay                   // the Files, Space, Repository or Packages view
	hitDialog                    // the open dialog's box: inert unless a row says otherwise
	hitMenuRow                   // index: menu entry
	hitPickerRow                 // index: item, key: item id
	hitDaemonsRow                // key: picker entry key
	hitFormRow                   // index: field, sub: row of the field
)

// textRow names an editable line.
type textRow int

const (
	textListFilter textRow = iota + 1
	textSearch
	textRepoName
	textRepoFilter
	textFilesFilter
	textExportPath
)

// paneNone is the pane of a region that is not in a pane (the daemon strip).
const paneNone Pane = -1

type hit struct {
	r     rect
	kind  hitKind
	pane  Pane
	index int
	sub   int
	key   string
}

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// dlgHit is a dialog row, by its line in the dialog's body; recordDialog
// places it once the dialog's position is known.
type dlgHit struct {
	line  int
	kind  hitKind
	index int
	sub   int
	key   string
}

// mouseWheelRows is how far one wheel notch travels in a list or text.
const mouseWheelRows = 3

// ---- recording -------------------------------------------------------------------------

func (m *Model) record(h hit) { m.building = append(m.building, h) }

// recordRow records line of the content being drawn now (at m.org). A line
// outside the content's height is not on screen and is not recorded.
func (m *Model) recordRow(line int, kind hitKind, pane Pane, index, sub int, key string) {
	if line < 0 || line >= m.org.h {
		return
	}
	m.record(hit{r: rect{m.org.x, m.org.y + line, m.org.w, 1}, kind: kind, pane: pane, index: index, sub: sub, key: key})
}

func (m *Model) recordDialogRow(line int, kind hitKind, index, sub int, key string) {
	m.dlgHits = append(m.dlgHits, dlgHit{line: line, kind: kind, index: index, sub: sub, key: key})
}

// recordDialog places the dialog drawn at (x, y), w×h: its box, then its
// rows. Body line i is the box's row i+2 (the border, then a blank line).
func (m *Model) recordDialog(x, y, w, h int) {
	m.record(hit{r: rect{x, y, w, h}, kind: hitDialog, pane: paneNone})
	for _, d := range m.dlgHits {
		if d.line+2 > h-2 { // below the box's last body row
			continue
		}
		m.record(hit{r: rect{x + 1, y + 2 + d.line, w - 2, 1}, kind: d.kind, pane: paneNone, index: d.index, sub: d.sub, key: d.key})
	}
}

// recordTitle records what a pane's top border holds that can be clicked:
// the List's filter and the Details pane's tabs.
func (m *Model) recordTitle(p Pane, r rect, title, right string) {
	focused := m.focus == p
	switch p {
	case PaneList:
		if !m.filtering && m.filter[m.kind] == "" {
			return
		}
		if col, ok := boxRightAt(title, right, r.w, focused); ok {
			m.record(hit{r: rect{r.x + col, r.y, ansi.StringWidth(right), 1}, kind: hitText, pane: PaneList, index: int(textListFilter)})
		}
	case PaneDetails:
		if _, ok := m.currentRow(); !ok {
			return
		}
		tabs, full := m.detailsTabParts()
		if right != full {
			return // the short form: one tab and its place, nothing to pick
		}
		col, ok := boxRightAt(title, right, r.w, focused)
		if !ok {
			return
		}
		x := r.x + col
		for i, part := range tabs {
			w := ansi.StringWidth(part)
			m.record(hit{r: rect{x, r.y, w, 1}, kind: hitDetailsTab, pane: PaneDetails, index: i, sub: int(m.kind), key: kindTabs[m.kind][i]})
			x += w + 1
		}
	}
}

// detailsTabParts is the tab strip as detailsTitle draws it in full.
func (m *Model) detailsTabParts() (parts []string, joined string) {
	tabs := kindTabs[m.kind]
	cur := clamp(m.tab[m.kind], 0, len(tabs)-1)
	for i, t := range tabs {
		if i == cur {
			parts = append(parts, "["+t+"]")
		} else {
			parts = append(parts, t)
		}
	}
	return parts, strings.Join(parts, " ")
}

// hitAt is the most specific region of the last frame at (x, y).
func (m *Model) hitAt(x, y int) (hit, bool) {
	for i := len(m.hits) - 1; i >= 0; i-- {
		if m.hits[i].r.contains(x, y) {
			return m.hits[i], true
		}
	}
	return hit{}, false
}

// ---- dispatch --------------------------------------------------------------------------

// mouseApplies is the gate every pointer event passes first.
func (m *Model) mouseApplies(msg tea.MouseMsg) bool {
	switch {
	case m.opts.NoMouse, m.quitting, m.tooSmall(), m.cur == nil, m.frames == 0, len(m.hits) == 0:
		return false
	case msg.Action != tea.MouseActionPress:
		return false // motion and release
	}
	switch msg.Button {
	case tea.MouseButtonLeft, tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		return true
	}
	return false
}

// onMouse is the one entry point for pointer input. It routes in the order
// onKey does: an open dialog, then the views that cover the panes, then the
// desk.
func (m *Model) onMouse(msg tea.MouseMsg) tea.Cmd {
	if !m.mouseApplies(msg) {
		return nil
	}
	h, ok := m.hitAt(msg.X, msg.Y)
	if !ok {
		return nil
	}
	delta := 0
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		delta = -1
	case tea.MouseButtonWheelDown:
		delta = 1
	}
	m.dirty = true
	switch {
	case m.dlg != nil:
		return m.mouseDialog(h, delta)
	case m.pk.open:
		return nil // the Packages view takes keys only
	case m.fs.open && m.fs.rd != nil:
		m.mouseReader(delta) // nothing reaches the tree behind it
		return nil
	case m.fs.open:
		m.mouseFiles(h, delta)
		return nil
	case m.sp.open:
		m.mouseSpace(h, delta)
		return nil
	case m.rb.open:
		m.mouseRepo(h, delta)
		return nil
	}
	return m.mouseDesk(h, delta)
}

// ---- the desk --------------------------------------------------------------------------

func (m *Model) mouseDesk(h hit, delta int) tea.Cmd {
	if delta != 0 {
		m.wheelPane(h.pane, delta)
		return nil
	}
	if h.kind != hitText || !m.editingRow(textRow(h.index)) {
		m.endDeskEdits()
	}
	switch h.kind {
	case hitPane:
		m.focus = h.pane
	case hitKindRow:
		m.focus = PaneKinds
		m.setKind(Kind(h.index))
	case hitListRow:
		m.focus = PaneList
		m.evJump = nil // the operator has taken over, as with a key
		if Kind(h.sub) == m.kind {
			m.selectKey(h.index, h.key)
		}
	case hitDetailsTab:
		m.focus = PaneDetails
		if tabs := kindTabs[m.kind]; Kind(h.sub) == m.kind && h.index < len(tabs) && tabs[h.index] == h.key {
			m.tab[m.kind] = h.index
		}
	case hitDaemon:
		return m.focusMember(h.index)
	case hitJobRow:
		m.focus = PaneJobs
		if _, ok := m.jobs.Get(h.index); ok {
			m.jobSel = h.index
		}
	case hitEventRow:
		m.focus = PaneEvents
		m.holdAt(uint64(h.index))
	case hitText:
		switch textRow(h.index) {
		case textListFilter:
			m.focus = PaneList
			if !m.filtering {
				m.filtering = true
				m.filterPrev = m.filter[m.kind]
				m.filterIn.SetValue(m.filter[m.kind])
				m.filterIn.Focus()
			}
			m.filterIn.CursorEnd()
		case textSearch:
			m.focus = PaneDetails
			if !m.ds.typing {
				m.openSearch()
			}
			m.ds.in.CursorEnd()
		}
	}
	return nil
}

// editingRow reports whether t is the line being typed in now.
func (m *Model) editingRow(t textRow) bool {
	switch t {
	case textListFilter:
		return m.filtering
	case textSearch:
		return m.ds.typing
	case textRepoName:
		return m.rb.editing
	case textRepoFilter:
		return m.rb.filtering
	case textFilesFilter:
		return m.fs.typing
	}
	return false
}

// endDeskEdits ends typing in the List filter or the Details search the way
// Enter does: the text is kept.
func (m *Model) endDeskEdits() {
	if m.filtering {
		m.filtering = false
		m.filterIn.Blur()
	}
	if m.ds.typing {
		m.ds.typing = false
		m.ds.in.Blur()
		if v := m.tabView(); v != nil && v.find.query != "" {
			v.find.seek = true
		}
	}
}

// selectKey highlights the List row drawn as row i with key, wherever it is
// now; a row that has gone is not replaced by another.
func (m *Model) selectKey(i int, key string) {
	rows := m.rows[m.kind]
	if i < 0 || i >= len(rows) || rows[i].key != key {
		i = -1
		for j, r := range rows {
			if r.key == key {
				i = j
				break
			}
		}
	}
	if i >= 0 {
		m.selectIndex(i)
	}
}

// holdAt holds the Events pane and puts its cursor on the entry at ring
// position pos, if the held copy still has it.
func (m *Model) holdAt(pos uint64) {
	if !m.evHeld && !m.holdEvents() {
		return
	}
	base := m.evSnapEnd - uint64(len(m.evSnap))
	if pos < base || pos >= m.evSnapEnd {
		return
	}
	m.evCur = int(pos - base)
}

// wheelPane moves the pane under the pointer; focus stays where it is.
func (m *Model) wheelPane(p Pane, delta int) {
	switch p {
	case PaneKinds:
		m.setKind(m.kind + Kind(delta))
	case PaneList:
		m.moveSelection(mouseWheelRows * delta)
	case PaneDetails:
		m.scrollDetails(mouseWheelRows * delta)
	case PaneEvents:
		m.wheelEvents(delta)
	case PaneJobs:
		m.moveJob(mouseWheelRows * delta)
	}
}

// wheelEvents reads back on an upward notch; a downward one moves the held
// cursor and, from the newest entry, resumes following.
func (m *Model) wheelEvents(delta int) {
	if delta < 0 {
		m.readBack(-mouseWheelRows)
		return
	}
	if !m.evHeld {
		return
	}
	if m.evCur >= len(m.evSnap)-1 {
		m.resumeEvents()
		return
	}
	m.moveEventCursor(mouseWheelRows)
}

// ---- dialogs ---------------------------------------------------------------------------

func (m *Model) mouseDialog(h hit, delta int) tea.Cmd {
	d := m.dlg
	if h.kind < hitDialog {
		return nil // outside the dialog: nothing behind it can be reached
	}
	if delta != 0 {
		switch d.kind {
		case dlgPlan:
			d.listOff += mouseWheelRows * delta // the body clamps it when drawn
		case dlgHelp, dlgJob:
			d.text.scroll(mouseWheelRows * delta)
			if delta < 0 {
				d.follow = false
			}
		case dlgMenu:
			if len(d.menu) > 0 {
				d.menuSel = clamp(d.menuSel+delta, 0, len(d.menu)-1)
			}
		case dlgPicker:
			if len(d.items) > 0 {
				d.pickSel = clamp(d.pickSel+delta, 0, len(d.items)-1)
			}
		case dlgDaemons:
			if p := d.dm; p.confirm == 0 {
				sel, entries := m.pickerSelected(p)
				if i := clamp(sel+delta, 0, len(entries)-1); i >= 0 && i < len(entries) {
					p.selKey = entries[i].key
				}
			}
		}
		return nil
	}
	switch h.kind {
	case hitMenuRow:
		if d.kind != dlgMenu || h.index >= len(d.menu) {
			return nil
		}
		// Exactly what Enter on the entry does: at most a plan or a form.
		d.menuSel = h.index
		a, r, had := d.menu[h.index], d.menuRow, d.menuHasRow
		m.dlg = nil
		return m.invokeOn(a, r, had, d.menuSet)
	case hitPickerRow:
		if d.kind != dlgPicker || h.index >= len(d.items) || d.items[h.index].id != h.key {
			return nil
		}
		if d.pickSel != h.index {
			d.pickSel = h.index
			return nil
		}
		// A second click on the selected row chooses it, as Enter does —
		// which leads to a confirmation, never past one.
		if !m.armed() || d.items[h.index].disabled {
			return nil
		}
		id, pick := d.items[h.index].id, d.onPick
		m.dlg = nil
		return pick(m, id)
	case hitDaemonsRow:
		p := d.dm
		if d.kind != dlgDaemons || p.confirm != 0 {
			return nil // a removal waits for y on the keyboard
		}
		if p.selKey == h.key {
			return m.onDaemonsKey("enter")
		}
		for _, e := range m.pickerEntries(p) {
			if e.key == h.key {
				p.selKey = h.key
			}
		}
	case hitFormRow:
		if d.kind != dlgForm || d.formBusy || h.index >= len(d.fields) {
			return nil
		}
		f := &d.fields[h.index]
		if h.sub >= f.rowCount() {
			return nil
		}
		d.focus, f.row = h.index, h.sub
		d.refocus()
		if in := d.formInput(); in != nil {
			in.CursorEnd()
		}
	case hitText:
		if textRow(h.index) == textExportPath && d.kind == dlgExport && !d.exp.confirm {
			d.exp.input.Focus()
			d.exp.input.CursorEnd()
		}
	}
	return nil
}

// ---- the views over the panes ----------------------------------------------------------

func (m *Model) mouseFiles(h hit, delta int) {
	fs := &m.fs
	v, ix := m.filesView()
	if delta != 0 {
		if len(v.Rows) > 0 {
			m.filesCursor(v)
			fs.cursor = v.Step(fs.cursor, mouseWheelRows*delta)
			fs.top = false
		}
		return
	}
	if fs.typing && !(h.kind == hitText && textRow(h.index) == textFilesFilter) {
		fs.typing = false // kept, as Enter keeps it
	}
	switch h.kind {
	case hitFilesRow:
		if v.Find(h.key) >= 0 {
			fs.cursor = h.key
			fs.top = false
		}
	case hitText:
		if textRow(h.index) == textFilesFilter && ix != nil && !fs.typing {
			fs.typing, fs.before = true, fs.filter
		}
	}
}

func (m *Model) mouseSpace(h hit, delta int) {
	rows := m.spaceObjectRows()
	if delta != 0 {
		if cur := m.spaceCursor(rows); cur >= 0 {
			m.sp.cursorKey = rows[clamp(cur+mouseWheelRows*delta, 0, len(rows)-1)].key
		}
		return
	}
	if h.kind != hitSpaceRow {
		return
	}
	for _, r := range rows {
		if r.key == h.key {
			m.sp.cursorKey = h.key
		}
	}
}

func (m *Model) mouseRepo(h hit, delta int) {
	rb := &m.rb
	rows := m.repoRows()
	if delta != 0 {
		if !rb.editing && !rb.filtering {
			m.repoPointerMove(rows, m.repoCursor(rows)+mouseWheelRows*delta)
		}
		return
	}
	onText := func(t textRow) bool { return h.kind == hitText && textRow(h.index) == t }
	if rb.editing && !onText(textRepoName) {
		// Out of the line with its text, and nothing asked: Enter asks.
		rb.editing, rb.nameErr = false, ""
		rb.name.Blur()
	}
	if rb.filtering && !onText(textRepoFilter) {
		rb.filtering = false
	}
	switch h.kind {
	case hitRepoRow:
		for i, t := range rows {
			if t == h.key {
				m.repoPointerMove(rows, i)
			}
		}
	case hitText:
		switch textRow(h.index) {
		case textRepoName:
			if !rb.editing {
				m.startEditing()
			}
			rb.name.CursorEnd()
		case textRepoFilter:
			if !rb.filtering && rb.listing != nil {
				rb.filtering, rb.queryBefore = true, rb.query
			}
		}
	}
}

// repoPointerMove moves the highlight for the pointer. A highlight placed by
// the pointer is not described: the manifest request waits for a key.
func (m *Model) repoPointerMove(rows []string, i int) {
	before := m.rb.cursorKey
	m.repoMove(rows, i)
	if m.rb.cursorKey != before {
		m.rb.pointerQuiet = true
	}
}
