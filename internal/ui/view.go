package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// rect is a pane's position and outer size in terminal cells.
type rect struct{ x, y, w, h int }

// layout places the five panes for a terminal of w×h cells.
//
// Wide (≥ 120 columns): Kinds on the left, List over Details in the centre,
// Events over Jobs on the right — acting on one side, the engine's answer on
// the other, slow work in the bottom corner.
//
// Compact (80–119 columns): the right column becomes a strip under the
// centre so that all five panes stay on screen.
type layout struct {
	kinds, list, details, events, jobs rect
	wide                               bool
}

const wideMinWidth = 120

func computeLayout(w, h int) layout {
	const top = 1        // header
	bodyH := h - top - 2 // status line and key hints
	var l layout
	if w >= wideMinWidth {
		l.wide = true
		kindsW := 20
		rightW := clamp(w*32/100, 38, 64)
		centerW := w - kindsW - rightW
		listH := clamp(bodyH*40/100, 6, bodyH-8)
		jobsH := clamp(bodyH*30/100, 6, 14)
		l.kinds = rect{0, top, kindsW, bodyH}
		l.list = rect{kindsW, top, centerW, listH}
		l.details = rect{kindsW, top + listH, centerW, bodyH - listH}
		l.events = rect{kindsW + centerW, top, rightW, bodyH - jobsH}
		l.jobs = rect{kindsW + centerW, top + bodyH - jobsH, rightW, jobsH}
		return l
	}
	stripH := clamp(bodyH*30/100, 6, 10)
	topH := bodyH - stripH
	kindsW := 18
	centerW := w - kindsW
	listH := clamp(topH*42/100, 5, topH-6)
	eventsW := w * 58 / 100
	l.kinds = rect{0, top, kindsW, topH}
	l.list = rect{kindsW, top, centerW, listH}
	l.details = rect{kindsW, top + listH, centerW, topH - listH}
	l.events = rect{0, top + topH, eventsW, stripH}
	l.jobs = rect{eventsW, top + topH, w - eventsW, stripH}
	return l
}

// View returns the current frame. When nothing visible changed since the
// last call the cached frame is returned, and Bubble Tea writes nothing.
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	if !m.dirty && m.frame != "" {
		return m.frame
	}
	m.dirty = false
	m.frames++
	m.frame = strings.Join(m.render(), "\n")
	return m.frame
}

// Frames returns how many frames have been rendered (not merely requested).
func (m *Model) Frames() uint64 { return m.frames }

func (m *Model) render() []string {
	if m.width == 0 || m.height == 0 {
		return []string{"Docker Desk — " + m.endpoint.Host}
	}
	if m.tooSmall() {
		return m.renderTooSmall()
	}
	l := computeLayout(m.width, m.height)

	pane := func(p Pane, r rect, title, right string, content func(w, h int) []string) []string {
		return m.st.box(title, right, content(r.w-2, r.h-2), r.w, r.h, m.focus == p)
	}
	kinds := pane(PaneKinds, l.kinds, "1 Kinds", "", func(w, h int) []string { return m.renderKinds(w, h, m.focus == PaneKinds) })
	lt, lr := m.listTitle()
	list := pane(PaneList, l.list, lt, lr, func(w, h int) []string { return m.renderList(w, h, m.focus == PaneList) })
	dt, dr := m.detailsTitle(l.details.w)
	details := pane(PaneDetails, l.details, dt, dr, m.renderDetails)
	et, er := m.eventsTitle()
	events := pane(PaneEvents, l.events, et, er, func(w, h int) []string { return m.renderEvents(w, h, m.focus == PaneEvents) })
	jt, jr := m.jobsTitle()
	jobsPane := pane(PaneJobs, l.jobs, jt, jr, func(w, h int) []string { return m.renderJobs(w, h, m.focus == PaneJobs) })

	var body []string
	center := append(append([]string{}, list...), details...)
	if l.wide {
		right := append(append([]string{}, events...), jobsPane...)
		body = joinH(kinds, center, right)
	} else {
		body = append(joinH(kinds, center), joinH(events, jobsPane)...)
	}

	switch {
	case m.pk.open:
		body = m.packagesBox(m.width, len(body))
	case m.sp.open:
		// Full screen, under the header that names the daemon; a dialog
		// opened from it is drawn on top, as anywhere else.
		body = m.spaceBox(m.width, len(body))
	case m.rb.open:
		body = m.repoBox(m.width, len(body))
	}
	out := make([]string, 0, m.height)
	out = append(out, m.renderHeader())
	out = append(out, body...)
	out = append(out, m.renderStatus(), m.renderHints())

	if m.dlg != nil {
		box, w, h := m.renderDialog()
		x := (m.width - w) / 2
		y := (m.height - h) / 2
		if y < 1 {
			y = 1 // never cover the header: the daemon stays visible
		}
		out = m.st.overlay(out, box, x, y, w)
	}
	return out
}

// tooSmall reports whether the terminal cannot hold the desk (or its size is
// not known yet). In that state nothing but quitting is accepted.
func (m *Model) tooSmall() bool {
	return m.width < MinWidth || m.height < MinHeight
}

func (m *Model) renderTooSmall() []string {
	msg := []string{
		"Docker Desk",
		"",
		fmt.Sprintf("This terminal is %d×%d.", m.width, m.height),
		fmt.Sprintf("The desk needs at least %d×%d", MinWidth, MinHeight),
		"to show all its panes.",
		"",
		"daemon: " + m.endpoint.Host + m.linkSuffix(),
		"",
		"Enlarge the window, or q to quit.",
		"(No other key does anything while",
		"the desk cannot be drawn.)",
	}
	out := make([]string, 0, m.height)
	for i := 0; i < m.height; i++ {
		line := ""
		if i < len(msg) {
			line = msg[i]
		}
		out = append(out, fit(line, m.width))
	}
	return out
}

// renderHeader shows which daemon the desk is pointed at. The endpoint and
// the connection state are never dropped; less important parts give way
// first when the terminal is narrow.
func (m *Model) renderHeader() string {
	st := m.st
	sep := st.headerKey.Render("  ")
	title := st.header.Render(" Docker Desk")
	endpoint := st.tone(toneAccent, m.endpoint.Host)
	ctx := st.headerKey.Render("ctx ") + m.endpoint.Context
	eng := st.headerKey.Render("Engine ") + m.info.ServerVersion
	host := st.headerKey.Render("on ") + m.info.Name
	conn := st.tone(toneGood, "● connected")
	connShort := conn
	if !m.hub.Connected() {
		conn = st.headerAlert.Render("✗ DISCONNECTED — retrying")
		connShort = st.headerAlert.Render("✗ DISCONNECTED")
	}
	var flags string
	if m.link.Label != "" {
		// Never dropped, like the endpoint: on a daemon that is not this
		// machine's own socket, that is the first thing to know.
		flags = sep + st.tone(m.linkTone(), "["+m.link.Label+"]")
	}
	if m.readOnly {
		flags += sep + st.tone(toneWarn, "[READ-ONLY]")
	}
	// Try progressively shorter headers.
	variants := [][]string{
		{title, endpoint, ctx, eng, host, conn},
		{title, endpoint, ctx, eng, conn},
		{title, endpoint, ctx, conn},
		{title, endpoint, conn},
		{endpoint, conn},
		{endpoint, connShort},
	}
	for _, v := range variants {
		line := strings.Join(v, sep) + flags
		if ansi.StringWidth(line) <= m.width {
			return fit(line, m.width)
		}
	}
	// Still too long: the endpoint itself gives way — in its middle. Its
	// beginning says which machine and its end which socket, and what
	// follows it (connected or not, remote, read-only) is never what gets
	// cut off. The whole of it is in the help and in every dialog.
	tail := sep + connShort + flags
	room := max(m.width-ansi.StringWidth(tail), 12)
	return fit(st.tone(toneAccent, middleTruncate(m.endpoint.Host, room))+tail, m.width)
}

// middleTruncate shortens s to w cells by replacing its middle with an
// ellipsis, sharing the room between the beginning and the end.
func middleTruncate(s string, w int) string {
	r := []rune(s)
	if len(r) <= w || w < 5 {
		return s
	}
	tail := (w - 1) / 2
	head := w - 1 - tail
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// linkTone is the colour of the "how is this daemon reached" tag: caution for
// a daemon on another machine, alarm when the link itself is not safe.
func (m *Model) linkTone() tone {
	switch {
	case m.link.Insecure:
		return toneBad
	case m.link.Remote:
		return toneWarn
	}
	return toneDim
}

// linkSuffix is the same tag as plain text, for places without styling.
func (m *Model) linkSuffix() string {
	if m.link.Label == "" {
		return ""
	}
	return "  [" + m.link.Label + "]"
}

// daemonLines names the daemon inside a dialog of inner width w: the
// endpoint, the context and — when it is not a local socket — how it is
// reached, so that a confirmation says "this is another machine" in its own
// text. Nothing of it is ever cut off: each of the three goes beside what is
// already there when it fits and onto a line of its own when it does not,
// and one that is longer than a line is continued on the next.
func (m *Model) daemonLines(w int, withContext bool) []string {
	st := m.st
	const label = 11 // "daemon" padded, and a space
	room := max(w-label, 8)
	indent := strings.Repeat(" ", label)

	var lines []string
	for i, part := range wrapLine(m.endpoint.Host, room) {
		if i == 0 {
			lines = append(lines, st.tone(toneDim, fit("daemon", label-1))+" "+part)
		} else {
			lines = append(lines, indent+part)
		}
	}
	add := func(text string, t tone) {
		last := len(lines) - 1
		if ansi.StringWidth(lines[last])+2+ansi.StringWidth(text) <= w {
			lines[last] += "  " + st.tone(t, text)
			return
		}
		for _, part := range wrapLine(text, room) {
			lines = append(lines, indent+st.tone(t, part))
		}
	}
	if withContext {
		add("(context "+m.endpoint.Context+")", toneDim)
	}
	if m.link.Label != "" {
		add("["+m.link.Label+"]", m.linkTone())
	}
	return lines
}

func (m *Model) renderStatus() string {
	switch {
	case m.preparing:
		// Takes precedence over any message: a dialog is about to appear.
		return m.st.tone(toneAccent, fit(" "+spinnerFrames[m.spinner]+" reading the daemon's current state — a confirmation will open; any key cancels it", m.width))
	case m.status.text != "":
		return m.st.tone(m.status.tone, fit(" "+m.status.text, m.width))
	}
	return strings.Repeat(" ", m.width)
}

func (m *Model) renderHints() string {
	var pairs []string
	add := func(k, v string) { pairs = append(pairs, k, v) }
	switch {
	case m.pk.open:
		pairs = m.packagesHints()
	case m.sp.open:
		pairs = m.spaceHints()
	case m.rb.open:
		pairs = m.repoHints()
	case m.filtering:
		add("type", "to filter")
		add("Enter", "keep")
		add("Esc", "undo")
	case m.ds.typing:
		m.searchHints(add) // every key is text: no action can be offered
	case m.focus == PaneKinds:
		add("↑↓", "pick a kind")
		add("Enter", "go to list")
	case m.focus == PaneList || m.focus == PaneDetails:
		if m.focus == PaneList {
			add("↑↓", "move")
			if n := len(m.marks[m.kind]); n > 0 {
				add("Esc", fmt.Sprintf("clear %d selected", n))
			} else if selectable(m.kind) {
				add("Space", "select")
			}
			add("/", "filter")
			add("⏎", "actions")
		} else if m.tabName() == tabUpstream {
			add("r", "ask again")
			if _, ok := m.upstreamPinned(); ok && !m.readOnly {
				add("P", "pull by digest")
			}
			if m.up.run != nil || m.up.batch != nil {
				add("c", "cancel")
			}
			add("[ ]", "tab")
		} else if m.tabName() == tabFindings {
			m.searchHints(add)
			add("↑↓", "finding")
			add("o", "order")
			add("v", "reveal")
			add("i", "packages")
			add("w", "export")
			add("[ ]", "tab")
		} else {
			m.searchHints(add)
			add("↑↓", "scroll")
			add("[ ]", "tab")
			if m.tabName() == tabLogs {
				add("f", "follow")
			}
			switch m.tabName() {
			case tabProcesses:
				add("r", "new snapshot")
			case tabChanges:
				if m.chg.run != nil {
					add("c", "cancel")
				} else if t, ok := m.changesTarget(); ok && t.refusal.None() && t.old != nil && t.new != nil {
					if _, _, done := m.contentFor(t); !done {
						add("⏎", "compare contents")
					}
				}
			default:
				add("w", "wrap")
			}
		}
		for _, a := range kindActions[m.kind] {
			if m.shadowed(a.key) {
				continue // n steps through matches while a search is open
			}
			add(a.key, strings.TrimSuffix(a.label, "…"))
		}
		if m.kind == KindImages {
			add("U", "upstream")
		}
	case m.focus == PaneEvents && m.evHeld:
		add("↑↓", "move")
		add("⏎", "go to object")
		add("Space", "resume")
	case m.focus == PaneEvents:
		add("Space", "hold")
		add("↑", "read back")
	case m.focus == PaneJobs:
		add("↑↓", "move")
		add("Enter", "output")
		add("c", "cancel")
		add("x", "clear finished")
	}
	left := " " + m.hints(pairs...)
	right := m.hints("Tab", "pane", "?", "keys", "q", "quit") + " "
	switch {
	case m.pk.open:
		right = m.hints("Esc", "close", "?", "keys") + " "
	case m.sp.open:
		right = m.hints("S", "close", "?", "keys") + " " // no pane takes keys behind the view
	case m.rb.open && (m.rb.editing || m.rb.filtering):
		right = "" // every key is text
	case m.rb.open:
		right = m.hints("B Esc", "close", "?", "keys") + " "
	}
	lw, rw := ansi.StringWidth(left), ansi.StringWidth(right)
	if lw+rw+1 > m.width {
		return fit(left, m.width-rw-1) + " " + right
	}
	return left + strings.Repeat(" ", m.width-lw-rw) + right
}

// helpLines is the content of the help overlay: every key, by pane and kind.
func (m *Model) helpLines(w int) []string {
	st := m.st
	var out []string
	sect := func(title string) { out = append(out, "", st.tone(toneAccent, title)) }
	row := func(k, v string) { out = append(out, "  "+st.hintKey.Render(fit(k, 16))+" "+v) }

	out = append(out, wrapWords("Docker Desk talks to one daemon: "+m.endpoint.Host, w)...)
	for _, line := range []string{"chosen by: " + m.endpoint.Source, "reached over: " + m.link.Words} {
		for _, part := range wrapWords(line, w) {
			out = append(out, st.tone(toneDim, part))
		}
	}

	sect("Everywhere")
	row("Tab / Shift+Tab", "next / previous pane")
	row("1 2 3 4 5", "focus Kinds / List / Details / Events / Jobs")
	row("[  ]", "previous / next Details tab")
	row("r", "refresh every list now")
	row("S", "Space: where the bytes went, and what removing something frees")
	row("B", "Repository: a registry's tags for the highlighted image's repository")
	row("T", "next colour theme ("+m.st.theme+" now)")
	row("?", "this help")
	row("q, Ctrl+C", "quit (asks first if jobs are running)")

	sect("Moving (lists and text)")
	row("↑ ↓  or  k j", "one row")
	row("PgUp PgDn", "one page")
	row("g  G", "first / last (G also resumes following)")

	sect("List pane")
	row("/", "filter rows as you type (Enter keeps, Esc undoes)")
	row("Space", "select / unselect the highlighted row (not Compose)")
	row("a", "select every row the filter shows")
	row("Esc", "clear the selection; then the filter; then back to Kinds")
	row("Enter", "menu of the actions available on the highlighted object or selection")
	out = append(out,
		"  With a selection, an action key applies to every selected object of that",
		"  kind: start, stop, restart, pause, unpause, kill, remove. shell, connect and",
		"  disconnect work on one object only and refuse; prune refuses too.",
	)

	sect("Details pane")
	row("← →  or  h l", "switch tab")
	row("f", "Logs: pause / resume following")
	row("w", "toggle line wrapping (not Processes: one row per process)")

	sect("Details pane: search (/)")
	row("/", "Info, Inspect, Processes, Changes: find as you type; Logs: filter")
	row("Enter", "keep the query; jump to the current match")
	row("n  N", "next / previous match, wrapping at either end")
	row("Esc", "clear the search; again, back to the List")
	out = append(out,
		"  Find hides nothing: every match is marked (bold, underlined; the current",
		"  one also reversed; without colour › ‹ and » «), the bar counts them —",
		"  find: q  3 of 9 — and on Inspect names the key: at State.Health.Status.",
		"  Logs filter instead: lines that do not match are hidden, following shows the",
		"  newest that does, scrolling back moves through those only. The count is of",
		"  the lines held here (5000 at most, the last 1000 when the stream opened);",
		"  older output on the daemon is not searched. Case is ignored, colour codes",
		"  are never matched, and a query is plain text, not a pattern. Each tab keeps",
		"  its own query until another object is highlighted. While a search is open",
		"  n and N step through matches, so Volumes' and Networks' n (new) waits.",
		"  Stats and Findings are not text: the bar says there is nothing to search.",
		"  Searching sends nothing to the daemon.",
	)

	sect("Containers › Processes tab")
	row("r", "take a new snapshot (GET /containers/{id}/top, the daemon's default ps)")
	out = append(out,
		"  A snapshot, not a live view: taken when the tab is opened, when the",
		"  highlight moves to another container, and on r — never on a timer.",
		"  Its time is shown; it is marked stale while the daemon is unreachable.",
		"  Only running containers have processes; a stopped one says so.",
		"  Command lines are shown exactly as the daemon reports them, arguments",
		"  included — nothing is masked, and nothing is written to jobs, logs or files.",
	)

	sect("Containers › Changes tab")
	row("Enter", "compare contents: packages and findings of both images (a job)")
	row("c", "cancel the comparison in progress")
	out = append(out,
		"  What differs between the image the container runs and the image its",
		"  reference names now. Identities, layers and config are read at once",
		"  (inspect and history of each image); no archive is read before Enter.",
		"  Environment variables are compared by name; values are never read.",
		"  Restarting keeps the image a container runs; only re-creating it takes",
		"  the new one, which this desk does not do. Nothing to compare is said, why.",
	)

	sect("Images › Findings tab (Details pane focused)")
	row("↑ ↓  PgUp PgDn", "move between findings")
	row("o", "order by layer, then group, then certainty")
	row("v", "reveal the highlighted secret; it hides when the cursor moves")
	row("i", "the image's packages: what its SBOM declares against what is installed")
	row("w", "export the analysis to a file: json, spdx, cyclonedx or text (asks first)")
	row("Enter", "analyse (when asked first, or after a failure)")
	row("c", "cancel the analysis in progress")
	out = append(out,
		"  Opening the tab reads the image's own archive (GET /images/get) once and",
		"  keeps the report for its ID and platform. Findings name a rule, a source",
		"  (daemon, image, desk), a certainty, the layer and the build instruction.",
	)

	sect("Images › Upstream tab, and U")
	row("r", "ask the registry again (only while the tab is shown); the answer is replaced")
	row("c", "cancel the check, or the check of every image, in progress")
	row("U", "Images: check every tagged image, one at a time, as one job")
	if !m.readOnly {
		row("P", "on a moved verdict: pull the build the tag names now, by its digest")
	}
	out = append(out,
		"  Does the tag this image carries name a different build upstream today?",
		"  Opening the tab asks the tag's registry once, as a job, after naming the host",
		"  and whether a credential from the Docker configuration is involved. The answer",
		"  is kept with its age; nothing polls, and a list refresh asks nothing.",
		"  current, moved, gone, unknown or not applicable: never newer, never stale.",
		"  moved shows both build times as their producers wrote them, which is later",
		"  and by how much, and the layers the two share. When the desk cannot reach",
		"  the registry the daemon is asked instead; that answer says via the daemon",
		"  and has no build time. U stops at a quota refusal and says when it resets.",
		"  The list's UPSTREAM column is filled only by a check. --no-registry forbids",
		"  every registry request; read-only mode does not, since the check only reads.",
	)

	sect("Repository browser (B, from any pane)")
	row("↑ ↓ PgUp PgDn", "move between tags; the row the highlight rests on is described")
	row("Tab", "ask for the next page of tags (100 a page, 1000 held at most)")
	row("/", "filter the tags held (Enter keeps, Esc undoes)")
	row("e", "edit the repository name: every key is text until Enter asks or Esc")
	row("d", "read the build times of the rows on screen, as one job (c cancels)")
	if !m.readOnly {
		row("Enter", "pull the highlighted build by its digest: type the tag to confirm")
	}
	row("r", "forget what was read about this repository and ask again")
	row("B, Esc", "close: the desk is as it was (Esc clears a filter first)")
	out = append(out,
		"  B on an image lists its repository's tags from its registry: one request",
		"  for the first page, nothing else. When the highlight rests on a tag, one",
		"  manifest request says what it names — digest, platforms — kept with its age.",
		"  HERE joins that digest with the RepoDigests of the images on this daemon:",
		"  here, here, the tag moved upstream, same build as <tag>, or -. Tags are in",
		"  text order (numbers by value), never by date. The pull confirmation names",
		"  name:tag@sha256:…; the daemon fetches the digest, not the tag. A 401 is said",
		"  as the registry's 401: no repository, or a credential not allowed to see it.",
		"  --no-registry: the view does not open. Read-only: it browses; it pulls nothing.",
	)

	sect("Packages view (i, from Images › Findings)")
	row("↑ ↓ PgUp PgDn", "move between packages; the cursor stays on its package")
	row("/", "search name, version, licence and purl (Enter keeps, Esc undoes)")
	row("o", "order by name, state, layer or licence")
	row("s", "show all, the disagreements, undeclared, version differs, declared only,")
	row("", "not checkable, both — in turn (S shows all again)")
	row("w", "export the packages to a file: spdx, cyclonedx, json or text (asks first)")
	row("Esc, i", "close: the desk is as it was (Esc clears a search first)")
	out = append(out,
		"  Both lists come from the analysis already made: the SBOM's packages, read",
		"  as the archive streamed, and the image's own dpkg, apk or rpm database,",
		"  read in the layers the final filesystem keeps. They are compared within one",
		"  ecosystem: deb with dpkg, apk with apk, rpm with rpm. Anything else is not",
		"  checkable, never undeclared. What cannot be known is said, with the reason.",
	)

	sect("Space view (S, from any pane)")
	row("↑ ↓ PgUp PgDn", "move between images, containers and volumes")
	row("Enter", "close the view and go to the highlighted object in its own kind")
	row("r", "read the daemon's account again (GET /system/df) and refresh the lists")
	row("m", "measure every container's writable layer (GET /containers/json?size=1)")
	row("c", "cancel a measurement in progress")
	row("w", "export the totals and shared groups to a file: json or text (asks first)")
	if !m.readOnly {
		row("d", "the desk's own removal confirmation for the highlighted object")
		row("X", "the desk's own prune preview for its kind (images, volumes)")
	}
	row("S, Esc", "close: the desk is as it was")
	out = append(out,
		"  Totals are the daemon's, as `docker system df` prints them. Unique is what",
		"  only that image holds; shared bytes are listed once, against the images that",
		"  hold them, from the daemon's own per-layer sizes. Every figure names its",
		"  source and age; one taken before a refresh that changed something is STALE.",
	)

	for k := Kind(0); k < kindCount; k++ {
		sect(kindNames[k] + " (List or Details pane focused)")
		for _, a := range kindActions[k] {
			row(a.key, a.desc)
		}
	}

	sect("Events pane")
	row("Space or f", "hold the view: a still copy of the history, with a cursor")
	row("↑ ↓ PgUp PgDn", "move the cursor; ↑ PgUp g also hold a live view")
	row("Enter", "held: go to the event's object in its own list")
	row("↓ past the end, G", "resume following (Space and f too)")
	out = append(out,
		"  While held, new events keep being recorded and lists keep refreshing; the",
		"  title counts them (held +N). Enter selects the container, image, volume or",
		"  network the event is about, clearing a filter that hides it. An object not",
		"  listed yet is waited for two seconds. Daemon, plugin and swarm events, and",
		"  the stream's own markers, name no object. Going to one changes nothing.",
	)

	sect("Jobs pane")
	row("Enter", "read the job's full output")
	row("c", "cancel the highlighted running job")
	row("x", "clear finished jobs")

	sect("How changes are confirmed")
	out = append(out,
		"  No key changes anything by itself. Every action above opens a dialog that",
		"  names the exact object, the effect and the daemon.",
		"    • start, stop, restart, pause, unpause, kill, connect, disconnect,",
		"      stop project: press y. Enter does nothing.",
		"    • anything that deletes (remove, prune, remove project): type the word",
		"      shown and press Enter.",
		"    • a selection, whatever the action: type the word with the count shown",
		"      (stop 3, remove 7) and press Enter. The dialog lists every object, and",
		"      under \"Not included\" each selected one left out, with the reason.",
		"    • pull, new volume/network, shell: you type the name; Enter submits.",
		"  A dialog ignores those keys for its first quarter of a second, and a key",
		"  pressed while a confirmation is still being prepared drops it: nothing typed",
		"  ahead, or typed for something else, can consent. Esc always works at once.",
		"  Nothing is ever forced. If something is in use the dialog says by what and",
		"  refuses. A prune lists exactly what it will delete and deletes only that.",
	)
	sect("Inside a shell (e)")
	row("exit, Ctrl-D", "back to the desk")
	row("Ctrl-] ×3", "leave at once, even if the daemon has stopped answering")
	if m.readOnly {
		out = append(out, "", st.tone(toneWarn, "  This desk is READ-ONLY: every action that changes something is disabled."))
	}
	return out
}
