package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/live"
	"github.com/fardani235/docker-desk/internal/plan"
)

// onKey routes one key press. Order matters: an open dialog gets everything
// (so no key can reach an action behind it), then the filter input, then
// global keys, then the focused pane.
func (m *Model) onKey(k tea.KeyMsg) tea.Cmd {
	key := k.String()

	if key == "ctrl+c" {
		return m.requestQuit()
	}
	// A terminal too small to draw the desk cannot show a dialog either.
	// Nothing may be acted on, or confirmed, that is not on screen.
	if m.tooSmall() {
		if key == "q" {
			return m.requestQuit()
		}
		return nil
	}
	if m.dlg != nil {
		return m.onDialogKey(k)
	}
	// A key pressed while a confirmation is still being prepared means the
	// operator has moved on. Drop the pending request, so that its dialog
	// cannot appear under keys typed for something else — the `y` of a
	// filter word must never land in a "stop container?" that opened late.
	dropped := m.preparing
	if m.preparing {
		m.planSeq++
		m.preparing = false
		m.setStatus(toneDim, droppedNotice)
	}
	if m.filtering {
		return m.onFilterKey(k)
	}

	switch key {
	case "q":
		return m.requestQuit()
	case "?":
		m.dlg = &dialog{kind: dlgHelp}
		m.dlg.text.wrap = true
		return nil
	case "tab":
		m.focus = (m.focus + 1) % paneCount
		return nil
	case "shift+tab":
		m.focus = (m.focus + paneCount - 1) % paneCount
		return nil
	case "1", "2", "3", "4", "5":
		m.focus = Pane(key[0] - '1')
		return nil
	case "]":
		m.cycleTab(1)
		return nil
	case "[":
		m.cycleTab(-1)
		return nil
	case "r":
		m.pending |= live.DirtyAll
		for i := range m.lastStart {
			m.lastStart[i] = time.Time{}
		}
		m.setStatus(toneDim, "Refreshing every list…")
		return nil
	case "T":
		m.setTheme(nextTheme(m.st.theme))
		if dropped {
			// Changing the theme writes to the status line too; that an
			// action was dropped is the more important thing to have read.
			m.setStatus(toneWarn, droppedNotice+" (Theme: "+m.st.theme+".)")
		}
		return nil
	}

	switch m.focus {
	case PaneKinds:
		return m.onKindsKey(key)
	case PaneList:
		return m.onListKey(key)
	case PaneDetails:
		return m.onDetailsKey(key)
	case PaneEvents:
		return m.onEventsKey(key)
	case PaneJobs:
		return m.onJobsKey(key)
	}
	return nil
}

const droppedNotice = "Dropped the pending action: a key was pressed before its confirmation appeared."

// setTheme repaints the desk in another palette. Nothing is stored: every
// frame is drawn from the styles, so the next one is in the new colours.
func (m *Model) setTheme(t Theme) {
	m.st = newStyles(t)
	note := ""
	if !m.st.color {
		note = " This terminal shows no colour (NO_COLOR?), so themes look alike."
	}
	m.setStatus(toneAccent, "Theme: "+t.Name+" — keep it with --theme "+t.Name+" or DOCKER_DESK_THEME="+t.Name+"."+note)
}

// requestQuit quits, or asks first when jobs are still running.
func (m *Model) requestQuit() tea.Cmd {
	if m.jobs.RunningCount() > 0 && (m.dlg == nil || m.dlg.kind != dlgQuit) {
		m.dlg = &dialog{kind: dlgQuit}
		return nil
	}
	if m.dlg != nil && m.dlg.kind == dlgQuit {
		return nil // Ctrl+C again is not consent; `y` is
	}
	m.quitting = true
	return tea.Quit
}

func (m *Model) setKind(k Kind) {
	m.kind = Kind(clamp(int(k), 0, int(kindCount)-1))
	m.ensureRows(m.kind)
}

func (m *Model) onKindsKey(key string) tea.Cmd {
	switch key {
	case "down", "j":
		m.setKind(m.kind + 1)
	case "up", "k":
		m.setKind(m.kind - 1)
	case "g", "home":
		m.setKind(0)
	case "G", "end":
		m.setKind(kindCount - 1)
	case "enter", "right", "l":
		m.focus = PaneList
	}
	return nil
}

func (m *Model) onListKey(key string) tea.Cmd {
	page := max(m.listHeight-1, 1)
	switch key {
	case "down", "j":
		m.moveSelection(1)
	case "up", "k":
		m.moveSelection(-1)
	case "pgdown", "ctrl+f":
		m.moveSelection(page)
	case "pgup", "ctrl+b":
		m.moveSelection(-page)
	case "g", "home":
		m.selectIndex(0)
	case "G", "end":
		m.selectIndex(len(m.rows[m.kind]) - 1)
	case "left", "h":
		m.focus = PaneKinds
	case "right", "l":
		m.focus = PaneDetails
	case "/":
		m.filtering = true
		m.filterPrev = m.filter[m.kind]
		m.filterIn.SetValue(m.filter[m.kind])
		m.filterIn.CursorEnd()
		m.filterIn.Focus()
	case "esc":
		if m.filter[m.kind] != "" {
			m.filter[m.kind] = ""
			m.invalidateRows(m.kind)
		}
	case "enter":
		m.openMenu()
	default:
		if a, ok := m.actionFor(key); ok {
			return m.invoke(a)
		}
	}
	return nil
}

func (m *Model) openMenu() {
	r, ok := m.currentRow()
	var items []action
	for _, a := range kindActions[m.kind] {
		if a.needsRow && !ok {
			continue
		}
		items = append(items, a)
	}
	if len(items) == 0 {
		return
	}
	m.dlg = &dialog{kind: dlgMenu, menu: items, menuRow: r, menuHasRow: ok, openedAt: time.Now()}
}

// onFilterKey edits the list filter. Rows are filtered as the text changes;
// Enter keeps the filter, Esc restores the one that was there before.
func (m *Model) onFilterKey(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		m.filtering = false
		m.filterIn.Blur()
		return nil
	case "esc":
		m.filtering = false
		m.filterIn.Blur()
		m.filter[m.kind] = m.filterPrev
		m.invalidateRows(m.kind)
		return nil
	}
	var cmd tea.Cmd
	m.filterIn, cmd = m.filterIn.Update(k)
	m.filter[m.kind] = strings.TrimSpace(m.filterIn.Value())
	m.invalidateRows(m.kind)
	return cmd
}

func (m *Model) onDetailsKey(key string) tea.Cmd {
	page := max(m.detailHeight-2, 1)
	scroll := func(delta int) {
		switch m.tabName() {
		case tabLogs:
			m.scrollLogs(delta)
		case tabInspect:
			m.inspect.scroll(delta)
		case tabInfo:
			m.info1.scroll(delta)
		}
	}
	switch key {
	case "down", "j":
		scroll(1)
	case "up", "k":
		scroll(-1)
	case "pgdown", "ctrl+f", " ":
		scroll(page)
	case "pgup", "ctrl+b":
		scroll(-page)
	case "g", "home":
		scroll(-1 << 30)
	case "G", "end":
		scroll(1 << 30)
		if m.tabName() == tabLogs {
			m.logFollow = true
		}
	case "f":
		if m.tabName() == tabLogs {
			if m.logFollow {
				m.scrollLogs(-1)
			} else {
				m.logFollow = true
			}
		}
	case "w":
		switch m.tabName() {
		case tabLogs:
			m.logWrap = !m.logWrap
		case tabInspect:
			m.inspect.toggleWrap()
		case tabInfo:
			m.info1.toggleWrap()
		}
	case "right", "l":
		m.cycleTab(1)
	case "left", "h":
		m.cycleTab(-1)
	case "esc":
		m.focus = PaneList
	default:
		// The Details pane describes the highlighted object, so that
		// object's actions work from here too.
		if a, ok := m.actionFor(key); ok {
			return m.invoke(a)
		}
	}
	return nil
}

func (m *Model) onEventsKey(key string) tea.Cmd {
	page := max(m.eventHeight-1, 1)
	switch key {
	case "down", "j":
		m.scrollEvents(1)
	case "up", "k":
		m.scrollEvents(-1)
	case "pgdown", "ctrl+f", " ":
		m.scrollEvents(page)
	case "pgup", "ctrl+b":
		m.scrollEvents(-page)
	case "g", "home":
		m.scrollEvents(-1 << 30)
	case "G", "end":
		m.evFollow = true
	case "f":
		if m.evFollow {
			m.scrollEvents(-1)
		} else {
			m.evFollow = true
		}
	case "esc":
		m.focus = PaneList
	}
	return nil
}

func (m *Model) onJobsKey(key string) tea.Cmd {
	switch key {
	case "down", "j":
		m.moveJob(1)
	case "up", "k":
		m.moveJob(-1)
	case "g", "home":
		m.moveJob(-1 << 20)
	case "G", "end":
		m.moveJob(1 << 20)
	case "enter":
		list := m.jobs.List()
		if i, ok := m.selectedJob(list); ok {
			m.dlg = &dialog{kind: dlgJob, jobID: list[i].ID, follow: true}
			m.dlg.text.wrap = true
		}
	case "c":
		m.cancelSelectedJob()
	case "x":
		if n := m.jobs.ClearFinished(); n > 0 {
			m.setStatus(toneDim, "Cleared finished jobs.")
		}
	case "esc":
		m.focus = PaneList
	}
	return nil
}

func (m *Model) cancelSelectedJob() {
	list := m.jobs.List()
	i, ok := m.selectedJob(list)
	if !ok {
		return
	}
	j := list[i]
	if j.State != jobs.Running {
		m.setStatus(toneDim, "That job has already finished.")
		return
	}
	// Cancelling only ever stops further requests; it changes nothing on
	// the daemon, so it needs no confirmation.
	m.jobs.Cancel(j.ID)
	m.setStatus(toneWarn, "Cancelling: "+j.Title+" "+j.Target)
}

// ---- dialogs ---------------------------------------------------------------------------

func (m *Model) onDialogKey(k tea.KeyMsg) tea.Cmd {
	d := m.dlg
	key := k.String()
	switch d.kind {
	case dlgPlan:
		return m.onPlanKey(k)

	case dlgForm:
		switch key {
		case "esc":
			m.closeDialog()
			return nil
		case "enter":
			if d.form == formShell && !m.armed() {
				return nil // the command is pre-filled: Enter typed ahead must not run it
			}
			return m.submitForm()
		}
		if d.formBusy {
			return nil
		}
		d.formErr = ""
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(k)
		return cmd

	case dlgPicker:
		switch key {
		case "esc", "q":
			m.dlg = nil
		case "down", "j":
			d.pickSel = clamp(d.pickSel+1, 0, len(d.items)-1)
		case "up", "k":
			d.pickSel = clamp(d.pickSel-1, 0, len(d.items)-1)
		case "enter":
			if d.pickSel < len(d.items) && m.armed() {
				id, pick := d.items[d.pickSel].id, d.onPick
				m.dlg = nil
				return pick(m, id)
			}
		}
		return nil

	case dlgMenu:
		switch key {
		case "esc", "q":
			m.dlg = nil
		case "down", "j":
			d.menuSel = clamp(d.menuSel+1, 0, len(d.menu)-1)
		case "up", "k":
			d.menuSel = clamp(d.menuSel-1, 0, len(d.menu)-1)
		case "enter":
			a, r, had := d.menu[d.menuSel], d.menuRow, d.menuHasRow
			m.dlg = nil
			return m.invokeOn(a, r, had)
		default:
			for _, a := range d.menu {
				if a.key == key {
					r, had := d.menuRow, d.menuHasRow
					m.dlg = nil
					return m.invokeOn(a, r, had)
				}
			}
		}
		return nil

	case dlgHelp, dlgJob:
		page := max(m.height-10, 1)
		switch key {
		case "esc", "q", "?", "enter":
			m.dlg = nil
		case "down", "j":
			d.text.scroll(1)
		case "up", "k":
			d.text.scroll(-1)
			d.follow = false
		case "pgdown", " ", "ctrl+f":
			d.text.scroll(page)
		case "pgup", "ctrl+b":
			d.text.scroll(-page)
			d.follow = false
		case "g", "home":
			d.text.off = 0
			d.follow = false
		case "G", "end":
			d.text.off = 1 << 30
			d.follow = true
		case "c":
			if d.kind == dlgJob {
				if j, ok := m.jobs.Get(d.jobID); ok && j.State == jobs.Running {
					m.jobs.Cancel(d.jobID)
				}
			}
		}
		return nil

	case dlgQuit:
		switch key {
		case "y":
			m.quitting = true
			return tea.Quit
		case "n", "esc", "q":
			m.dlg = nil
		}
		return nil
	}
	return nil
}

// closeDialog dismisses the open dialog and abandons any plan still being
// prepared for it, so that its answer cannot open or submit anything later.
func (m *Model) closeDialog() {
	if m.dlg != nil && (m.dlg.recomputing || m.dlg.formBusy) {
		m.planSeq++
		m.preparing = false
	}
	m.dlg = nil
}

// onPlanKey handles a confirmation. This function and submitForm are the
// only places from which a mutation can start.
func (m *Model) onPlanKey(k tea.KeyMsg) tea.Cmd {
	d := m.dlg
	p := d.plan
	key := k.String()

	// Keys that never confirm anything.
	switch key {
	case "esc":
		m.closeDialog()
		return nil
	case "up":
		d.listOff--
		return nil
	case "down":
		d.listOff++
		return nil
	case "pgup":
		d.listOff -= 8
		return nil
	case "pgdown":
		d.listOff += 8
		return nil
	case "tab":
		if d.scope != nil && !m.preparing {
			// Recompute the preview for the other scope from a fresh
			// snapshot. Until it arrives this dialog confirms nothing.
			d.recomputing = true
			return m.preparePrune(d.scope, !d.scopeAll)
		}
		return nil
	}
	if d.recomputing {
		return nil
	}

	if !p.Runnable() {
		// Refused or nothing to do: any of the usual "go away" keys closes.
		switch key {
		case "enter", "n", "q":
			m.dlg = nil
		}
		return nil
	}
	if !m.armed() {
		// Too soon to have been read. Declining is always allowed; anything
		// that would proceed — including text for the typed word — is not.
		if key == "n" && p.Tier != plan.TierTyped {
			m.dlg = nil
		}
		return nil
	}

	switch p.Tier {
	case plan.TierTyped:
		if key == "enter" {
			if d.input.Value() == p.Word {
				m.dlg = nil
				m.runPlan(p)
				return nil
			}
			d.mismatch = true
			return nil
		}
		d.mismatch = false
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(k)
		return cmd
	default:
		switch key {
		case "y":
			m.dlg = nil
			m.runPlan(p)
		case "n", "q":
			m.dlg = nil
		}
		// Enter, space and everything else do nothing: consent is `y`.
		return nil
	}
}
