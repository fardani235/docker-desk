package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/freshness"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/registry"
	"github.com/fardani235/docker-desk/internal/repository"
)

// The repository browser (B): the tags a registry lists for one repository,
// what each names, how it joins the images already here, and a pull of one
// build by its digest (docs/DECISIONS.md, round twelve, L1–L14).
//
// Opening asks for the tag list only — one page. The highlighted row is
// described once the highlight rests on it: one manifest request, kept for
// the session by host/path:tag with its age. d reads build times for the
// rows on screen as one cancellable job. Nothing polls. The view sends
// nothing to the daemon: Enter opens the desk's own pull confirmation.

type repoState struct {
	open bool
	// What the desk looked like when the view opened; put back on close.
	prevFocus Pane
	prevKind  Kind
	prevKey   string

	browser *repository.Browser

	// The name line. While editing, every key is text.
	name       textinput.Model
	editing    bool
	nameBefore string
	nameErr    string

	// The filter over the loaded rows.
	query, queryBefore string
	filtering          bool

	repo    registry.Ref
	have    bool // repo is set: a repository was asked for
	credSrc string
	listing *repository.Listing
	listErr string
	// wantTag is the tag to land on when the first page arrives: the one
	// the highlighted image carried.
	wantTag string

	listSeq    uint64
	listBusy   bool
	listCancel context.CancelFunc

	descSeq    uint64
	descTag    string // being described
	descCancel context.CancelFunc
	descErr    map[string]string

	batch   *repoBatch
	timeErr map[string]string

	cursorKey string
	off       int
	listH     int
	settledAt time.Time
	drawnSec  int64
}

type repoBatch struct {
	jobID int
	repo  registry.Ref
	total int
	done  atomic.Int64
	seen  int64

	mu   sync.Mutex
	errs map[string]string
}

type repoTagsMsg struct {
	seq   uint64
	repo  registry.Ref
	first bool
	page  registry.TagPage
	err   error
}

type repoDescMsg struct {
	seq  uint64
	repo registry.Ref
	tag  string
	err  error
}

func (m *Model) newRepository(client *registry.Client) {
	m.rb.browser = repository.New(repository.Options{Client: client, ConfigDir: m.opts.DockerConfigDir})
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 255
	in.Placeholder = "alpine, ghcr.io/owner/name, host:5000/team/app"
	m.rb.name = in
	m.rb.descErr = map[string]string{}
	m.rb.timeErr = map[string]string{}
}

// ---- opening and closing ------------------------------------------------------

// openRepository is B. It prefills the name line from the highlighted image
// and asks at once; with no image it opens on an empty name line.
func (m *Model) openRepository() tea.Cmd {
	if m.rb.browser.Disabled() {
		m.setStatus(toneWarn, "Registry access is switched off (--no-registry): the repository browser asks a registry for everything it shows, so it does not open.")
		return nil
	}
	m.rb.open = true
	m.rb.prevFocus, m.rb.prevKind = m.focus, m.kind
	m.rb.prevKey = m.sel[m.kind].key
	m.rb.nameErr = ""
	name, tag := m.highlightedRepository()
	if name == "" {
		// An empty name line, and nothing under it from an earlier visit:
		// what is listed always belongs to the name shown. (What was
		// described is kept by the browser, and reused if it is asked again.)
		m.dropRepoRequests()
		m.rb.have, m.rb.listing, m.rb.listErr, m.rb.query, m.rb.cursorKey = false, nil, "", "", ""
		m.rb.name.SetValue("")
		m.startEditing()
		return nil
	}
	m.rb.name.SetValue(name)
	m.rb.wantTag = tag
	return m.askRepository()
}

// highlightedRepository is the repository of the highlighted image, and the
// tag it carries.
func (m *Model) highlightedRepository() (name, tag string) {
	if m.kind != KindImages {
		return "", ""
	}
	r, ok := m.currentRow()
	if !ok {
		return "", ""
	}
	cands := []string{r.imageRef}
	for _, img := range m.snap.Images {
		if img.ID == r.imageID {
			cands = append(cands, img.RepoDigests...)
		}
	}
	for _, c := range cands {
		if c == "" {
			continue
		}
		repo, err := registry.ParseRepository(c)
		if err != nil {
			continue
		}
		if t, err := registry.ParseRef(c); err == nil && c == r.imageRef {
			tag = t.Tag
		}
		return repo.Repository(), tag
	}
	return "", ""
}

// closeRepository puts the desk back as it was when the view opened. A
// build-time job keeps running (it is in Jobs, where c cancels it); a list
// or a description still being asked is dropped.
func (m *Model) closeRepository() {
	m.rb.open, m.rb.editing, m.rb.filtering = false, false, false
	m.rb.name.Blur()
	m.dropRepoRequests()
	m.focus = m.rb.prevFocus
	m.setKind(m.rb.prevKind)
	for i, r := range m.rows[m.kind] {
		if r.key == m.rb.prevKey {
			m.selectIndex(i)
			break
		}
	}
}

func (m *Model) dropRepoRequests() {
	if m.rb.listCancel != nil {
		m.rb.listCancel()
		m.rb.listCancel = nil
	}
	if m.rb.descCancel != nil {
		m.rb.descCancel()
		m.rb.descCancel = nil
	}
	m.rb.listSeq++
	m.rb.descSeq++
	m.rb.listBusy, m.rb.descTag = false, ""
}

func (m *Model) startEditing() {
	m.rb.editing = true
	m.rb.nameBefore = m.rb.name.Value()
	m.rb.name.Focus()
	m.rb.name.CursorEnd()
}

// askRepository reads the name line and asks for the first page of tags.
func (m *Model) askRepository() tea.Cmd {
	repo, err := registry.ParseRepository(m.rb.name.Value())
	if err != nil {
		m.rb.nameErr = "Not a repository name: " + freshness.Sanitize(err.Error()) + "."
		if !m.rb.editing {
			m.startEditing()
		}
		return nil
	}
	m.rb.editing, m.rb.nameErr = false, ""
	m.rb.name.Blur()
	m.rb.name.SetValue(repo.Repository())
	m.dropRepoRequests()
	if !m.rb.have || repo.Name != m.rb.repo.Name {
		m.rb.query, m.rb.cursorKey, m.rb.off = "", "", 0
	}
	m.rb.repo, m.rb.have = repo, true
	m.rb.credSrc = m.rb.browser.CredentialSource(repo)
	m.rb.listing, m.rb.listErr = nil, ""
	m.rb.descErr = map[string]string{}
	return m.listCmd("")
}

func (m *Model) listCmd(cursor string) tea.Cmd {
	m.rb.listSeq++
	seq, repo, b := m.rb.listSeq, m.rb.repo, m.rb.browser
	ctx, cancel := context.WithCancel(context.Background())
	m.rb.listCancel, m.rb.listBusy = cancel, true
	return func() tea.Msg {
		defer cancel()
		p, err := b.ListTags(ctx, repo, cursor)
		return repoTagsMsg{seq: seq, repo: repo, first: cursor == "", page: p, err: err}
	}
}

func (m *Model) onRepoTags(msg repoTagsMsg) {
	if msg.seq != m.rb.listSeq || !m.rb.open {
		return
	}
	m.rb.listBusy, m.rb.listCancel = false, nil
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return
		}
		m.rb.listErr = repoErrText(msg.err)
		return
	}
	if msg.first || m.rb.listing == nil {
		m.rb.listing = repository.NewListing(msg.repo)
	}
	m.rb.listing.Add(msg.page)
	if msg.first {
		// Land on the tag the image carried; else stay where the cursor
		// was (r); else the first row.
		held := func(t string) bool {
			for _, x := range m.rb.listing.Tags {
				if x == t {
					return true
				}
			}
			return false
		}
		switch {
		case m.rb.wantTag != "" && held(m.rb.wantTag):
			m.rb.cursorKey = m.rb.wantTag
		case m.rb.cursorKey != "" && held(m.rb.cursorKey):
		case len(m.rb.listing.Tags) > 0:
			m.rb.cursorKey = m.rb.listing.Tags[0]
		}
		m.rb.wantTag = ""
		m.rb.settledAt = time.Now()
	}
}

// repoErrText is a registry error as the view says it: the desk's own words,
// through the display sanitiser.
func repoErrText(err error) string {
	var re *registry.Error
	if errors.As(err, &re) {
		s := re.Reason
		if re.Host != "" {
			s = re.Host + ": " + s
		}
		return freshness.Sanitize(s) + "."
	}
	return freshness.Sanitize(err.Error()) + "."
}

// nextPage is Tab.
func (m *Model) nextPage() tea.Cmd {
	l := m.rb.listing
	switch {
	case l == nil || m.rb.listBusy:
		return nil
	case l.Capped:
		m.setStatus(toneDim, fmt.Sprintf("%d tags held — the browser's cap. The registry has more; they are not fetched.", repository.Cap))
	case l.LinkRefused:
		m.setStatus(toneWarn, "The registry named its next page on another host or path; it was not followed.")
	case !l.More():
		m.setStatus(toneDim, "That is every tag the registry lists.")
	default:
		return m.listCmd(l.Next)
	}
	return nil
}

// ---- rows ---------------------------------------------------------------------

// repoRows are the held tags the filter shows, in natural order.
func (m *Model) repoRows() []string {
	if m.rb.listing == nil {
		return nil
	}
	q := strings.ToLower(m.rb.query)
	if q == "" {
		return m.rb.listing.Tags
	}
	var out []string
	for _, t := range m.rb.listing.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			out = append(out, t)
		}
	}
	return out
}

func (m *Model) repoCursor(rows []string) int {
	for i, t := range rows {
		if t == m.rb.cursorKey {
			return i
		}
	}
	if len(rows) == 0 {
		return -1
	}
	return 0
}

func (m *Model) repoMove(rows []string, i int) {
	if len(rows) == 0 {
		return
	}
	i = clamp(i, 0, len(rows)-1)
	if rows[i] != m.rb.cursorKey {
		m.rb.cursorKey = rows[i]
		m.rb.settledAt = time.Now()
	}
}

// repoHighlighted is the tag under the cursor.
func (m *Model) repoHighlighted() (string, bool) {
	rows := m.repoRows()
	cur := m.repoCursor(rows)
	if cur < 0 {
		return "", false
	}
	return rows[cur], true
}

// ---- the frame: describing the row the highlight rests on ---------------------

// syncRepository collects the build-time job and, once the highlight has
// rested, describes its row: one request, never repeated for a row that
// has an answer.
func (m *Model) syncRepository(now time.Time) tea.Cmd {
	m.collectRepoBatch()
	if !m.rb.open {
		return nil
	}
	if sec := now.Unix(); sec != m.rb.drawnSec {
		m.rb.drawnSec = sec // the ages move
		m.dirty = true
	}
	if m.rb.editing || m.rb.listing == nil || now.Sub(m.rb.settledAt) < m.opts.SelectDelay {
		return nil
	}
	tag, ok := m.repoHighlighted()
	if !ok {
		return nil
	}
	if _, kept := m.rb.browser.Described(m.rb.repo, tag); kept || m.rb.descErr[tag] != "" || m.rb.descTag == tag {
		return nil
	}
	if m.rb.descCancel != nil {
		m.rb.descCancel() // the highlight moved on: that answer is no longer wanted
	}
	m.rb.descSeq++
	seq, repo, b := m.rb.descSeq, m.rb.repo, m.rb.browser
	ctx, cancel := context.WithCancel(context.Background())
	m.rb.descCancel, m.rb.descTag = cancel, tag
	return func() tea.Msg {
		defer cancel()
		_, err := b.Describe(ctx, repo, tag)
		return repoDescMsg{seq: seq, repo: repo, tag: tag, err: err}
	}
}

func (m *Model) onRepoDesc(msg repoDescMsg) {
	if msg.seq != m.rb.descSeq {
		return
	}
	m.rb.descTag, m.rb.descCancel = "", nil
	if msg.err != nil && !errors.Is(msg.err, context.Canceled) && msg.repo.Name == m.rb.repo.Name {
		m.rb.descErr[msg.tag] = repoErrText(msg.err)
	}
}

func (m *Model) collectRepoBatch() {
	b := m.rb.batch
	if b == nil {
		return
	}
	if n := b.done.Load(); n != b.seen {
		b.seen = n
		b.mu.Lock()
		for k, v := range b.errs {
			m.rb.timeErr[k] = v
		}
		b.mu.Unlock()
		m.dirty = true
	}
	if j, ok := m.jobs.Get(b.jobID); !ok || j.State.Finished() {
		b.mu.Lock()
		for k, v := range b.errs {
			m.rb.timeErr[k] = v
		}
		b.mu.Unlock()
		m.rb.batch = nil
		m.dirty = true
	}
}

// repoBusy reports whether something the view shows a spinner for runs.
func (m *Model) repoBusy() bool {
	return m.rb.open && (m.rb.listBusy || m.rb.descTag != "")
}

// ---- d: build times for the rows on screen ------------------------------------

func (m *Model) startBuildTimes() {
	if m.rb.batch != nil {
		m.setStatus(toneDim, "Build times are already being read; c cancels.")
		return
	}
	rows := m.repoRows()
	var todo []string
	for i := m.rb.off; i < len(rows) && i < m.rb.off+max(m.rb.listH, 1); i++ {
		if _, kept := m.rb.browser.TimeOf(m.rb.repo, rows[i]); !kept {
			todo = append(todo, rows[i])
		}
	}
	if len(todo) == 0 {
		m.setStatus(toneDim, "Every row on screen already has its build time; r forgets them and asks again.")
		return
	}
	platform := repository.DaemonPlatform(m.info)
	repo, br := m.rb.repo, m.rb.browser
	b := &repoBatch{repo: repo, total: len(todo), errs: map[string]string{}}
	for _, t := range todo {
		delete(m.rb.timeErr, repository.Key(repo, t))
	}
	b.jobID = m.jobs.Start("read build times", fmt.Sprintf("%s · %d tags", repo.Repository(), len(todo)), func(ctx context.Context, r *jobs.Run) error {
		r.Status("asking " + repo.Host)
		res := br.BuildTimes(ctx, repo, todo, platform, func(done int, tag string, bt repository.BuildTime, err error) {
			words := buildWords(bt)
			if err != nil {
				words = "unknown: " + repoErrText(err)
				b.mu.Lock()
				b.errs[repository.Key(repo, tag)] = repoErrText(err)
				b.mu.Unlock()
			}
			b.done.Add(1)
			r.Steps(done, len(todo))
			r.Logf("%s  %s", tag, words)
			r.Status(fmt.Sprintf("%d of %d · %s", done, len(todo), tag))
		})
		switch {
		case res.Err != nil:
			return errors.New(repoErrText(res.Err))
		case res.Cancelled:
			return context.Canceled
		case res.Quota:
			msg := "stopped: the registry refused for quota"
			if !res.Reset.IsZero() {
				msg += "; it resets at " + res.Reset.Local().Format("15:04")
			}
			r.Logf("%s; the %d times read are kept", msg, res.Read)
			return errors.New(msg)
		}
		r.Status(fmt.Sprintf("%d read · %d without an answer", res.Read, res.Failed))
		return nil
	})
	m.rb.batch = b
	m.setStatus(toneDim, fmt.Sprintf("Reading the build times of %d tags on screen, one at a time (a job; c cancels).", len(todo)))
}

// buildWords is a build time as a job line says it.
func buildWords(bt repository.BuildTime) string {
	switch {
	case bt.PlatformMissing:
		return "no build for this daemon's platform"
	case bt.Missing:
		return "unknown: the " + bt.Platform + " build's config carries no created time"
	case bt.Epoch:
		return "unknown: the " + bt.Platform + " build's config carries the epoch"
	case bt.Created.IsZero():
		return "unknown"
	}
	return bt.Created.UTC().Format("2006-01-02 15:04 UTC") + " (" + bt.Platform + " build's config)"
}

// ---- Enter: the pinned pull ---------------------------------------------------

// repoPull opens the desk's own pull confirmation for the highlighted row:
// name:tag@sha256:…, typed with the tag. It goes through the guarded action
// path, so read-only mode refuses it there, as it refuses every pull.
func (m *Model) repoPull() tea.Cmd {
	tag, ok := m.repoHighlighted()
	if !ok {
		return nil
	}
	d, kept := m.rb.browser.Described(m.rb.repo, tag)
	if !kept {
		if e := m.rb.descErr[tag]; e != "" {
			m.setStatus(toneWarn, "What "+tag+" names is not known: "+e+" r asks again. Nothing was sent.")
		} else {
			m.setStatus(toneDim, "The digest of "+tag+" is not known yet — it is asked when the highlight rests on the row. Nothing was sent.")
		}
		return nil
	}
	p := plan.Pinned{Name: m.rb.repo.Name, Tag: tag, Digest: d.Digest}
	if daemon := repository.DaemonPlatform(m.info); repository.NoBuildFor(d, daemon) {
		p.NoBuildFor = repository.PlatformString(daemon)
	}
	return m.invokeOn(pinnedPullAction(p), row{}, false, nil)
}

// pinnedPullAction is the one way a pinned pull starts: the repository
// browser's Enter and Images › Upstream's P both come here.
func pinnedPullAction(p plan.Pinned) action {
	return action{key: "P", label: "pull by digest…", mutating: true, ignoresSet: true,
		run: func(m *Model, _ row) tea.Cmd {
			return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.PullPinned(s, p) }, planMsg{})
		}}
}

// ---- keys ---------------------------------------------------------------------

func (m *Model) onRepoKey(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	if m.rb.editing {
		switch k.Type {
		case tea.KeyEnter:
			return m.askRepository()
		case tea.KeyEsc:
			// Out of the line, back to what was asked; nothing is asked.
			m.rb.editing, m.rb.nameErr = false, ""
			m.rb.name.Blur()
			m.rb.name.SetValue(m.rb.nameBefore)
			return nil
		}
		m.rb.nameErr = ""
		var cmd tea.Cmd
		m.rb.name, cmd = m.rb.name.Update(k)
		return cmd
	}
	if m.rb.filtering {
		m.onRepoFilterKey(k)
		return nil
	}
	rows := m.repoRows()
	cur := m.repoCursor(rows)
	page := max(m.rb.listH-1, 1)
	switch key {
	case "esc":
		if m.rb.query != "" {
			m.rb.query = "" // the filter first, then the view
			return nil
		}
		m.closeRepository()
	case "B":
		m.closeRepository()
	case "q":
		return m.requestQuit()
	case "?":
		m.dlg = &dialog{kind: dlgHelp}
		m.dlg.text.wrap = true
	case "T":
		m.setTheme(nextTheme(m.st.theme))
	case "e":
		m.startEditing()
	case "/":
		if m.rb.listing != nil {
			m.rb.filtering, m.rb.queryBefore = true, m.rb.query
		}
	case "down", "j":
		m.repoMove(rows, cur+1)
	case "up", "k":
		m.repoMove(rows, cur-1)
	case "pgdown", "ctrl+f", " ":
		m.repoMove(rows, cur+page)
	case "pgup", "ctrl+b":
		m.repoMove(rows, cur-page)
	case "g", "home":
		m.repoMove(rows, 0)
	case "G", "end":
		m.repoMove(rows, len(rows)-1)
	case "tab":
		return m.nextPage()
	case "enter":
		return m.repoPull()
	case "d":
		if m.rb.listing != nil {
			m.startBuildTimes()
		}
	case "c":
		if m.rb.batch != nil {
			m.jobs.Cancel(m.rb.batch.jobID)
			m.setStatus(toneWarn, "Cancelling the build times; those already read are kept.")
		} else {
			m.setStatus(toneDim, "Nothing is being read.")
		}
	case "r":
		if m.rb.have {
			m.rb.browser.Forget(m.rb.repo)
			m.rb.wantTag = m.rb.cursorKey
			for k := range m.rb.timeErr {
				if strings.HasPrefix(k, m.rb.repo.Host+"/"+m.rb.repo.Path+":") {
					delete(m.rb.timeErr, k)
				}
			}
			return m.askRepository()
		}
	}
	return nil
}

func (m *Model) onRepoFilterKey(k tea.KeyMsg) {
	switch k.Type {
	case tea.KeyEnter:
		m.rb.filtering = false
	case tea.KeyEsc:
		m.rb.filtering, m.rb.query = false, m.rb.queryBefore
	case tea.KeyBackspace:
		if r := []rune(m.rb.query); len(r) > 0 {
			m.rb.query = string(r[:len(r)-1])
		}
	case tea.KeyCtrlU:
		m.rb.query = ""
	case tea.KeyRunes, tea.KeySpace:
		if len(m.rb.query) < 80 {
			m.rb.query += string(k.Runes)
		}
	}
	m.rb.off = 0
}

func (m *Model) repoHints() []string {
	switch {
	case m.rb.editing:
		return []string{"type", "a repository", "Enter", "ask", "Esc", "step out"}
	case m.rb.filtering:
		return []string{"type", "to filter", "Enter", "keep", "Esc", "undo"}
	}
	pairs := []string{"↑↓", "move", "e", "name", "/", "filter"}
	if l := m.rb.listing; l != nil {
		if l.More() {
			pairs = append(pairs, "Tab", "next page")
		}
		pairs = append(pairs, "d", "build times")
	}
	if m.rb.batch != nil {
		pairs = append(pairs, "c", "cancel")
	}
	if !m.readOnly {
		pairs = append(pairs, "⏎", "pull by digest…")
	}
	return append(pairs, "r", "ask again")
}

// ---- drawing ------------------------------------------------------------------

var repoColumns = []column{
	{title: "TAG", min: 12, grow: 3},
	{title: "DIGEST", min: 19},
	{title: "PLATFORMS", min: 12, grow: 2, prio: 2},
	{title: "HERE", min: 10, grow: 3, prio: 1},
	{title: "BUILT", min: 22, grow: 1, prio: 3},
}

func (m *Model) repoBox(w, h int) []string {
	st := m.st
	inner := w - 4
	bodyH := h - 2
	now := time.Now()

	head := m.repoHeader(inner, now)
	detail := m.repoDetail(inner, now)
	var body []string
	body = append(body, head...)

	rows := m.repoRows()
	listH := max(bodyH-len(head)-len(detail)-2, 3)
	m.rb.listH = listH
	switch {
	case m.rb.listing == nil:
		var msg []string
		switch {
		case m.rb.listBusy:
			msg = append(msg, st.tone(toneAccent, fmt.Sprintf("%s Asking %s for the tags of %s… Esc closes.", spinnerFrames[m.spinner], m.rb.repo.Host, m.rb.repo.Repository())))
		case m.rb.listErr != "":
			msg = append(msg, wrapLine(st.tone(toneWarn, "The registry gave no tag list: "+m.rb.listErr), inner)...)
			msg = append(msg, st.tone(toneDim, "r asks again; e edits the name."))
		case m.rb.editing || !m.rb.have:
			msg = append(msg, wrapWords(st.tone(toneDim, "Type a repository — alpine, ghcr.io/owner/name, host:5000/team/app — and press Enter: the registry is asked for its tags. Esc steps out of the line; Esc again closes."), inner)...)
		}
		for len(msg) < listH+1 {
			msg = append(msg, "")
		}
		body = append(body, msg[:listH+1]...)
	default:
		idx, widths := layoutColumns(repoColumns, inner-2)
		title := make([]string, len(idx))
		for i, ci := range idx {
			title[i] = fit(repoColumns[ci].title, widths[i])
		}
		body = append(body, st.tone(toneDim, "  "+strings.Join(title, "  ")))
		cur := m.repoCursor(rows)
		if cur >= 0 {
			m.rb.off = clamp(m.rb.off, cur-listH+1, cur)
		}
		m.rb.off = clamp(m.rb.off, 0, max(len(rows)-listH, 0))
		n := 0
		for i := m.rb.off; i < len(rows) && n < listH; i++ {
			cells, tn := m.repoCells(rows[i], now)
			parts := make([]string, len(idx))
			for j, ci := range idx {
				parts[j] = fit(cells[ci], widths[j])
			}
			line := strings.Join(parts, "  ")
			if i == cur {
				body = append(body, st.selected.Render("▸ "+fit(line, inner-2)))
			} else {
				body = append(body, "  "+st.tone(tn, line))
			}
			n++
		}
		if len(rows) == 0 {
			body = append(body, st.tone(toneDim, "  No held tag matches the filter."))
			n++
		}
		for ; n < listH; n++ {
			body = append(body, "")
		}
	}
	pos := ""
	if len(rows) > 0 {
		pos = fmt.Sprintf(" rows %d–%d of %d ", m.rb.off+1, min(m.rb.off+listH, len(rows)), len(rows))
	}
	body = append(body, st.tone(toneDim, strings.Repeat("─", max(inner-ansi.StringWidth(pos)-1, 0))+pos+"─"))
	body = append(body, detail...)

	padded := make([]string, 0, bodyH)
	for _, l := range body {
		if len(padded) == bodyH {
			break
		}
		padded = append(padded, " "+fit(l, inner)+" ")
	}
	for len(padded) < bodyH {
		padded = append(padded, strings.Repeat(" ", inner+2))
	}
	right := ""
	switch {
	case m.rb.listBusy:
		right = spinnerFrames[m.spinner] + " listing tags"
	case m.rb.batch != nil:
		right = fmt.Sprintf("%s build times %d of %d", spinnerFrames[m.spinner], m.rb.batch.done.Load(), m.rb.batch.total)
	case m.rb.descTag != "":
		right = spinnerFrames[m.spinner] + " asking what " + m.rb.descTag + " names"
	}
	title := "Repository — the tags a registry lists, and one build by its digest"
	if w < wideMinWidth {
		title = "Repository"
	}
	return st.box(title, right, padded, w, h, true)
}

// repoHeader names the repository, the host, the credential, how many tags
// are held, and their age — and says what the order is not.
func (m *Model) repoHeader(w int, now time.Time) []string {
	st := m.st
	label := func(s string) string { return st.tone(toneDim, fit(s, 11)) }
	var out []string
	if m.rb.editing {
		m.rb.name.Width = max(w-12, 10)
		out = append(out, label("repository")+m.rb.name.View())
	} else {
		name := m.rb.name.Value()
		if name == "" {
			name = st.tone(toneDim, "(none — e to type one)")
		} else {
			name = st.dialogTitle.Render(name)
		}
		out = append(out, label("repository")+name)
	}
	if m.rb.nameErr != "" {
		out = append(out, label("")+st.tone(toneWarn, m.rb.nameErr))
	}
	if !m.rb.have {
		return append(out, "")
	}
	who := "anonymously"
	if m.rb.credSrc != "" {
		who = "with the credential from " + m.rb.credSrc
	}
	parts := []string{m.rb.repo.Host + " " + who}
	if l := m.rb.listing; l != nil {
		held := fmt.Sprintf("%d tags held", len(l.Tags))
		switch {
		case l.Capped:
			held += fmt.Sprintf(" — the cap of %d; the registry has more", repository.Cap)
		case l.More():
			held += " · Tab asks for the next page"
		case l.LinkRefused:
			held += " · the next page was named elsewhere and not followed"
		default:
			held += " · every tag the registry lists"
		}
		if l.Invalid > 0 {
			held += fmt.Sprintf(" · %d names that are not tags left out", l.Invalid)
		}
		parts = append(parts, held, "listed "+humanAge(l.At, now)+" ago")
	}
	for i, part := range wrapWords(strings.Join(parts, " · "), w-11) {
		if i == 0 {
			out = append(out, label("asks")+part)
		} else {
			out = append(out, label("")+part)
		}
	}
	order := "Tags are in text order, numbers by value (3.9 before 3.10): never by recency, which a tag list does not carry."
	if l := m.rb.listing; l != nil && (l.Capped || l.More()) {
		order += " Pages come in the registry's own order."
	}
	if w < wideMinWidth-4 {
		order = "Text order, numbers by value: never by recency."
	}
	for _, part := range wrapWords(order, w-11) {
		out = append(out, label("")+st.tone(toneDim, part))
	}
	if m.rb.filtering || m.rb.query != "" {
		q := "/" + m.rb.query
		if m.rb.filtering {
			q += "▏"
		}
		out = append(out, label("filter")+q+st.tone(toneDim, fmt.Sprintf("  %d of %d held", len(m.repoRows()), len(m.rb.listing.Tags))))
	}
	return out
}

// repoCells are a row's cells and its tone.
func (m *Model) repoCells(tag string, now time.Time) ([]string, tone) {
	cells := []string{tag, "", "", "", ""}
	tn := toneNormal
	d, kept := m.rb.browser.Described(m.rb.repo, tag)
	switch {
	case kept:
		cells[1] = shortDigest(d.Digest)
		cells[2] = m.platformsText(d)
		j := repository.JoinLocal(m.rb.repo, tag, d.Digest, m.snap.Images)
		cells[3] = j.Words()
		switch j.Here {
		case repository.Present:
			tn = toneGood
		case repository.TagMoved:
			tn = toneAccent
		}
		if repository.NoBuildFor(d, repository.DaemonPlatform(m.info)) {
			tn = toneWarn
		}
	case m.rb.descTag == tag:
		cells[1] = spinnerFrames[m.spinner] + " asking"
	case m.rb.descErr[tag] != "":
		cells[1] = "unknown"
		tn = toneWarn
	}
	if bt, ok := m.rb.browser.TimeOf(m.rb.repo, tag); ok {
		cells[4] = builtCell(bt)
	} else if e := m.rb.timeErr[repository.Key(m.rb.repo, tag)]; e != "" {
		cells[4] = "unknown"
	}
	return cells, tn
}

func shortDigest(d string) string {
	if len(d) > 7+12 {
		return d[:7+12]
	}
	return d
}

// platformsText lists an index's builds; the daemon's own, when missing, is
// said first.
func (m *Model) platformsText(d registry.Description) string {
	if !d.Index {
		return "one manifest"
	}
	var names []string
	for _, p := range d.Platforms {
		s := repository.PlatformString(p)
		names = append(names, strings.TrimPrefix(s, "linux/"))
	}
	s := strings.Join(names, " ")
	if daemon := repository.DaemonPlatform(m.info); repository.NoBuildFor(d, daemon) {
		s = "no " + strings.TrimPrefix(repository.PlatformString(daemon), "linux/") + " · " + s
	}
	return s
}

func builtCell(bt repository.BuildTime) string {
	switch {
	case bt.PlatformMissing:
		return "no build here"
	case bt.Missing || bt.Epoch || bt.Created.IsZero():
		return "unknown (" + strings.TrimPrefix(bt.Platform, "linux/") + ")"
	}
	return bt.Created.UTC().Format("2006-01-02") + " " + strings.TrimPrefix(bt.Platform, "linux/")
}

// repoDetail describes the highlighted row in full.
func (m *Model) repoDetail(w int, now time.Time) []string {
	const lines = 6
	st := m.st
	var out []string
	kv := func(k, v string, t tone) {
		for i, part := range wrapWords(v, max(w-11, 10)) {
			label := strings.Repeat(" ", 11)
			if i == 0 {
				label = st.tone(toneDim, fit(k, 11))
			}
			out = append(out, label+st.tone(t, part))
		}
	}
	tag, ok := m.repoHighlighted()
	if !ok || !m.rb.have {
		for len(out) < lines {
			out = append(out, "")
		}
		return out
	}
	ref := m.rb.repo.Repository() + ":" + tag
	d, kept := m.rb.browser.Described(m.rb.repo, tag)
	switch {
	case kept:
		out = append(out, st.dialogTitle.Render(ref)+st.tone(toneDim, "  · described "+humanAge(d.At, now)+" ago · r asks again"))
		kv("digest", d.Digest, toneNormal)
		daemon := repository.DaemonPlatform(m.info)
		pub := "one manifest: its platform is in its config, which d reads"
		if d.Index {
			var all []string
			for _, p := range d.Platforms {
				all = append(all, repository.PlatformString(p))
			}
			pub = strings.Join(all, ", ")
		}
		kv("publishes", pub, toneNormal)
		if repository.NoBuildFor(d, daemon) {
			kv("", "No build for "+repository.PlatformString(daemon)+", this daemon's platform: a pull would fail, and Enter refuses it.", toneWarn)
		}
		j := repository.JoinLocal(m.rb.repo, tag, d.Digest, m.snap.Images)
		here := j.Words() + " — " + joinSentence(j, m.rb.repo)
		if j.Here == repository.Absent {
			here = joinSentence(j, m.rb.repo)
		}
		kv("here", here, joinTone(j))
	case m.rb.descTag == tag:
		out = append(out, st.dialogTitle.Render(ref))
		kv("digest", spinnerFrames[m.spinner]+" asking "+m.rb.repo.Host+" what the tag names (one request)", toneAccent)
	case m.rb.descErr[tag] != "":
		out = append(out, st.dialogTitle.Render(ref))
		kv("digest", "unknown: "+m.rb.descErr[tag], toneWarn)
	default:
		out = append(out, st.dialogTitle.Render(ref))
		kv("digest", "asked when the highlight rests on the row", toneDim)
	}
	if bt, ok := m.rb.browser.TimeOf(m.rb.repo, tag); ok {
		kv("built", buildWords(bt)+" — the producer's claim, read "+humanAge(bt.At, now)+" ago", toneNormal)
	} else if e := m.rb.timeErr[repository.Key(m.rb.repo, tag)]; e != "" {
		kv("built", "unknown: "+e, toneWarn)
	} else {
		kv("built", "d reads the build times of the rows on screen (up to 3 requests each)", toneDim)
	}
	if len(out) > lines {
		out = out[:lines]
	}
	for len(out) < lines {
		out = append(out, "")
	}
	return out
}

func joinSentence(j repository.Join, repo registry.Ref) string {
	id := engine.ShortID(j.ImageID)
	switch j.Here {
	case repository.Present:
		return "local image " + id + " records this digest"
	case repository.TagMoved:
		return "local image " + id + " carries the tag but records another digest"
	case repository.SameBuild:
		return "local image " + id + " records this digest as " + repo.Repository() + ":" + j.Other
	}
	return "no local image of " + repo.Repository() + " records this digest"
}

func joinTone(j repository.Join) tone {
	switch j.Here {
	case repository.Present:
		return toneGood
	case repository.TagMoved:
		return toneAccent
	}
	return toneNormal
}
