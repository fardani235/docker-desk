package ui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fardani235/docker-desk/internal/composefile"
	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
)

// The Declared tab of a compose project: what its compose file says now,
// set against the containers that run (docs/DECISIONS.md, Y1–Y9).
//
// Drawn at once from what the labels and the container list already say;
// nothing is opened, not even stat'ed, before Enter. Enter reads the files
// the config-files label names, in order, as one job: each a regular file of
// at most 1 MiB, parsed and discarded. What is kept is the name, and each
// service's image and profiles. No byte of a file is ever rendered or
// logged except as part of the comparison: a service name, an image value.
// The containers' side is compared live, so a refresh moves it; the file's
// side is what it said when it was read.

const tabDeclared = "Declared"

// declaredCap bounds the comparisons kept; each is a few service entries.
const declaredCap = 32

type declaredState struct {
	run     *declaredRun
	results map[string]*declaredOutcome // by project key
	order   []string

	shownFor string // project the scroll offset belongs to
	view     textView
}

// declaredRun is the hand-over point between the reading job and the desk.
type declaredRun struct {
	key, project string
	jobID        int

	mu  sync.Mutex
	out *declaredOutcome
}

// declaredOutcome is what one Enter found.
type declaredOutcome struct {
	at    time.Time
	files []declaredFile // in the order read
	file  composefile.File
	// refusal is one sentence: why nothing is compared. No rows go with it.
	refusal string
	// ended is why the job ended without an answer (cancelled, failed).
	ended string
}

type declaredFile struct {
	path    string
	written time.Time
}

// declaredTarget is the highlighted project, with what the tab needs.
type declaredTarget struct {
	key   string
	proj  plan.Project
	paths []string
}

func (m *Model) declaredTarget() (declaredTarget, bool) {
	r, ok := m.currentRow()
	if !ok || m.kind != KindCompose {
		return declaredTarget{}, false
	}
	p, ok := m.projectByName(r.project)
	if !ok {
		return declaredTarget{}, false
	}
	return declaredTarget{key: p.Name + "\x00" + p.ConfigFiles, proj: p, paths: p.ConfigPaths()}, true
}

// declaredBlocked is the refusal known before anything is read: the flag,
// the transport, or labels that name no file. "" when Enter may read.
func (m *Model) declaredBlocked(t declaredTarget) string {
	switch {
	case m.opts.NoComposeFiles:
		return "Not compared: --no-compose-files forbids reading compose files."
	case m.link.Transport != dockerctx.TransportUnix:
		return "Not compared: the daemon is reached over " + m.link.Transport.String() + ", so the path belongs to the machine that ran compose. Nothing is read there or here."
	case len(t.paths) == 0:
		return "Not compared: the labels record no config file."
	}
	return ""
}

// readComposeFile is how the tab opens a file: the bounded reader, or the
// one a test put in its place.
func (m *Model) readComposeFile(path string) ([]byte, fs.FileInfo, error) {
	if m.opts.ComposeReader != nil {
		return m.opts.ComposeReader(path)
	}
	return composefile.ReadInfo(path)
}

// startDeclared reads, parses and compares, as one job. Its lines carry the
// desk's words, paths from the labels and counts — never the file's bytes.
func (m *Model) startDeclared(t declaredTarget) {
	run := &declaredRun{key: t.key, project: t.proj.Name}
	paths := append([]string{}, t.paths...)
	read := m.readComposeFile
	actual := plan.ComposeActual(m.snap, t.proj.Name)
	run.jobID = m.startJob("compare with the compose file", t.proj.Name, func(ctx context.Context, r *jobs.Run) error {
		out := &declaredOutcome{at: time.Now()}
		defer func() {
			run.mu.Lock()
			run.out = out
			run.mu.Unlock()
		}()
		var files []composefile.File
		for i, p := range paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			r.Status(fmt.Sprintf("reading %s (%d of %d)", p, i+1, len(paths)))
			b, st, err := read(p)
			if err != nil {
				out.refusal = readRefusal(p, err)
				return errors.New(strings.TrimPrefix(out.refusal, "Cannot be compared: "))
			}
			f, err := composefile.Parse(b)
			if err != nil {
				out.refusal = "Cannot be compared: " + p + " is " + err.Error() + "."
				return errors.New(strings.TrimPrefix(out.refusal, "Cannot be compared: "))
			}
			out.files = append(out.files, declaredFile{path: p, written: st.ModTime()})
			files = append(files, f)
			r.Logf("%s: %d bytes, %d services declared, written %s", p, len(b), len(f.Services), timeOrDash(st.ModTime()))
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		out.file = composefile.Merge(files...)
		res := composefile.Compare(out.file, actual)
		sum := declaredCounts(res)
		r.Logf("%s", sum)
		r.Status(sum)
		return nil
	})
	m.dc.run = run
}

// readRefusal says, in one sentence, why a path was not read.
func readRefusal(path string, err error) string {
	var why string
	switch {
	case errors.Is(err, composefile.ErrNotFound):
		why = "there is no file at " + path + " on this machine."
	case errors.Is(err, composefile.ErrDirectory), errors.Is(err, composefile.ErrNotRegular):
		why = path + " is not a regular file."
	case errors.Is(err, composefile.ErrTooLarge):
		why = path + " is larger than 1 MiB, more than a compose file needs."
	case errors.Is(err, composefile.ErrRelative):
		why = "the recorded path " + path + " is not absolute."
	default:
		var re *composefile.ReadError
		if errors.As(err, &re) && re.Cause != nil {
			var pe *fs.PathError
			cause := re.Cause
			if errors.As(re.Cause, &pe) {
				cause = pe.Err
			}
			why = path + " cannot be opened: " + cause.Error() + "."
		} else {
			why = path + " cannot be opened: " + err.Error() + "."
		}
	}
	return "Cannot be compared: " + why
}

func declaredCounts(res composefile.Result) string {
	var parts []string
	for _, v := range []composefile.Verdict{composefile.DeclaredOnly, composefile.Differs, composefile.PresentOnly, composefile.NotCompared, composefile.Match} {
		if n := res.Count(v); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, v))
		}
	}
	if len(parts) == 0 {
		return "nothing declared and nothing running"
	}
	return strings.Join(parts, ", ")
}

// collectDeclared takes the outcome of a reading job that has ended.
func (m *Model) collectDeclared() {
	run := m.dc.run
	if run == nil {
		return
	}
	j, ok := m.jobs.Get(run.jobID)
	if !ok || j.State == jobs.Running {
		return
	}
	m.dc.run = nil
	m.dirty = true
	run.mu.Lock()
	out := run.out
	run.mu.Unlock()
	if out == nil {
		out = &declaredOutcome{at: time.Now()}
	}
	switch {
	case j.State == jobs.Cancelled:
		out = &declaredOutcome{at: out.at, ended: "Cancelled; nothing was compared."}
	case j.State == jobs.Failed && out.refusal == "":
		out = &declaredOutcome{at: out.at, ended: "The comparison ended without a result: " + j.Err}
	}
	if m.dc.results == nil {
		m.dc.results = map[string]*declaredOutcome{}
	}
	if _, had := m.dc.results[run.key]; !had {
		m.dc.order = append(m.dc.order, run.key)
	}
	m.dc.results[run.key] = out
	for len(m.dc.order) > declaredCap {
		delete(m.dc.results, m.dc.order[0])
		m.dc.order = m.dc.order[1:]
	}
}

// openDeclared shows the Declared tab of the highlighted project and moves
// the focus there: the action menu's "compare with the compose file".
func (m *Model) openDeclared() {
	for i, t := range kindTabs[KindCompose] {
		if t == tabDeclared {
			m.tab[KindCompose] = i
		}
	}
	m.focus = PaneDetails
	m.dirty = true
}

// ---- keys -------------------------------------------------------------------

// onDeclaredKey handles Enter and c on the tab; it reports whether key was one.
func (m *Model) onDeclaredKey(key string) bool {
	switch key {
	case "enter":
		t, ok := m.declaredTarget()
		if !ok {
			return true
		}
		if blocked := m.declaredBlocked(t); blocked != "" {
			m.setStatus(toneWarn, blocked)
			return true
		}
		if run := m.dc.run; run != nil {
			if run.key == t.key {
				m.setStatus(toneDim, "The compose file is still being read.")
			} else {
				m.setStatus(toneDim, "Another comparison is still ending.")
			}
			return true
		}
		m.startDeclared(t)
		return true
	case "c":
		if run := m.dc.run; run != nil {
			m.jobs.Cancel(run.jobID)
			m.setStatus(toneWarn, "Cancelling: compare with the compose file "+run.project)
		}
		return true
	}
	return false
}

// ---- drawing ---------------------------------------------------------------------

func (m *Model) renderDeclared(w, h int) []string {
	st := m.st
	t, ok := m.declaredTarget()
	if !ok {
		return []string{st.tone(toneDim, "This project no longer has any containers.")}
	}
	if t.key != m.dc.shownFor {
		m.dc.shownFor, m.dc.view.off = t.key, 0
	}
	lines := m.declaredLines(t, w)
	bodyH := h
	if len(lines) > h {
		bodyH = h - 1
	}
	m.dc.view.set(lines)
	out := append([]string{}, m.dc.view.render(w, bodyH)...)
	if pos := m.dc.view.position(bodyH); pos != "" {
		for len(out) < bodyH {
			out = append(out, "")
		}
		out = append(out, st.tone(toneDim, fit("↑↓ scroll · "+pos, w)))
	}
	return out
}

// declaredLines is the whole tab as lines w cells wide, for scrolling.
func (m *Model) declaredLines(t declaredTarget, w int) []string {
	st := m.st
	var out []string
	const lw = 13
	hang := func(label string, lw int, tn tone, text string) {
		pad := strings.Repeat(" ", lw)
		for i, part := range wrapSpaces(text, max(w-lw, 10)) {
			l := pad
			if i == 0 {
				l = st.tone(toneDim, fit(label, lw))
			}
			out = append(out, l+st.tone(tn, part))
		}
	}
	prose := func(tn tone, s string) {
		for _, part := range wrapSpaces(s, w) {
			out = append(out, st.tone(tn, part))
		}
	}
	now := time.Now()
	p := t.proj
	actual := plan.ComposeActual(m.snap, p.Name)
	oneOffs := 0
	for _, c := range p.Containers {
		if plan.IsOneOff(c) {
			oneOffs++
		}
	}
	var newest composefile.Actual
	for _, a := range actual {
		if a.Created.After(newest.Created) {
			newest = a
		}
	}

	hang("project", lw, toneNormal, fmt.Sprintf("%s · %d containers, %d running", p.Name, len(p.Containers), p.Running))
	hang("working dir", lw, toneNormal, orDash(p.WorkingDir))
	if len(t.paths) == 0 {
		hang("config file", lw, toneDim, "none recorded")
	}
	for i, path := range t.paths {
		label, text := "", path
		if i == 0 {
			label = "config file"
		}
		if len(t.paths) > 1 {
			label = strings.Replace(label, "file", "files", 1)
			text = fmt.Sprintf("%d.%s", i+1, path)
		}
		hang(label, lw, toneNormal, text)
	}
	if m.link.Transport == dockerctx.TransportUnix {
		hang("daemon", lw, toneNormal, "local (unix socket): the paths are on this machine")
	} else {
		hang("daemon", lw, toneWarn, "reached over "+m.link.Transport.String()+": the paths are on the machine that ran compose")
	}
	if newest.Container != "" {
		hang("newest", lw, toneNormal, fmt.Sprintf("%s, created %s (%s ago)", newest.Container, timeOrDash(newest.Created), humanAge(newest.Created, now)))
	}
	if oneOffs > 0 {
		hang("one-offs", lw, toneDim, fmt.Sprintf("%d left by `docker compose run`; never compared", oneOffs))
	}

	res := m.dc.results[t.key]
	run := m.dc.run
	running := run != nil && run.key == t.key
	blocked := m.declaredBlocked(t)

	// The file's write time: a fact once read, never a verdict.
	switch {
	case res != nil && len(res.files) > 0:
		for i, f := range res.files {
			label := ""
			if i == 0 {
				label = "written"
			}
			text := timeOrDash(f.written)
			if len(res.files) > 1 {
				text = fmt.Sprintf("%d.%s", i+1, text)
			}
			hang(label, lw, toneNormal, text+", "+composefile.WrittenRelative(f.written, newest.Created))
		}
	default:
		hang("written", lw, toneDim, "unknown: nothing has been read")
	}

	switch {
	case blocked != "":
		out = append(out, "")
		prose(toneWarn, blocked)
		return out
	case running:
		out = append(out, "")
		hang("compare", lw, toneAccent, spinnerFrames[m.spinner]+" reading the compose file… c cancels.")
		return out
	case res == nil:
		out = append(out, "")
		prose(toneDim, "Not compared yet: nothing has been read. Enter reads the file once, as a job: a regular file of at most 1 MiB on this machine, parsed and discarded. Its contents are never shown; it is never run and never interpolated.")
		prose(toneDim, "The file is compared as it is now: nothing records what it said when the containers were created.")
		return out
	case res.ended != "":
		out = append(out, "")
		prose(toneBad, res.ended)
		prose(toneDim, "Enter reads the file again.")
		return out
	case res.refusal != "":
		out = append(out, "")
		prose(toneWarn, res.refusal)
		prose(toneDim, "Enter reads the file again.")
		return out
	}

	cmp := composefile.Compare(res.file, actual)
	hang("read", lw, toneNormal, fmt.Sprintf("%s (%s ago), compared as it is now; Enter reads again", res.at.Local().Format("15:04:05"), humanAge(res.at, now)))
	if cmp.DeclaredName != "" && cmp.DeclaredName != p.Name {
		hang("name", lw, toneWarn, "the file names the project "+cmp.DeclaredName+", the labels "+p.Name)
	}
	for i, f := range res.files {
		if composefile.FileIsNewer(f.written, newest.Created) {
			which := "The file"
			if len(res.files) > 1 {
				which = fmt.Sprintf("File %d", i+1)
			}
			hang("", lw, toneWarn, fmt.Sprintf("%s was written %s, after %s was created %s. That alone does not make it wrong.",
				which, timeOrDash(f.written), newest.Container, timeOrDash(newest.Created)))
		}
	}
	hang("services", lw, toneNormal, fmt.Sprintf("%d: %s", len(cmp.Rows), declaredCounts(cmp)))
	if res.file.Includes {
		hang("", lw, toneDim, "The file includes others, which are not read: a container it does not declare is not compared.")
	}
	out = append(out, "")
	// lead hangs text after a prefix already drawn pw cells wide.
	lead := func(prefix string, pw int, tn tone, text string) {
		pad := strings.Repeat(" ", pw)
		for i, part := range wrapSpaces(text, max(w-pw, 10)) {
			l := pad
			if i == 0 && prefix != "" {
				l = prefix
			}
			out = append(out, l+st.tone(tn, part))
		}
	}
	for _, row := range cmp.Rows {
		m.declaredRow(row, lead)
	}
	out = append(out, "")
	prose(toneDim, "Compared: which services exist, and literal images. Not compared: scale, ports, volumes, environment, command, health checks. Config hashes are compose's own, shown as recorded.")
	return out
}

// declaredRow draws one service: verdict, service, what each side says, and
// beneath them why and the recorded config hashes.
func (m *Model) declaredRow(row composefile.Row, lead func(string, int, tone, string)) {
	const vw, sw = 16, 15
	st := m.st
	label := row.Verdict.String()
	tn := toneNormal
	switch row.Verdict {
	case composefile.DeclaredOnly:
		tn = toneWarn
	case composefile.Differs:
		tn = toneBad
		label = "differs"
	case composefile.PresentOnly:
		tn = toneWarn
	case composefile.NotCompared:
		tn = toneDim
	case composefile.Match:
		tn = toneGood
	}
	var parts []string
	if row.Declared != "" {
		parts = append(parts, "declares "+oneLine(row.Declared))
	}
	if len(row.Containers) > 0 {
		parts = append(parts, strings.Join(row.Containers, ", ")+" "+map[bool]string{true: "runs", false: "run"}[len(row.Containers) == 1]+" "+strings.Join(row.Images, ", "))
	}
	if row.Verdict == composefile.Differs {
		label = "differs (" + row.Reason + ")"
	}
	prefix := st.tone(tn, fit(label, vw)) + fit(oneLine(row.Service), sw)
	lead(prefix, vw+sw, toneNormal, strings.Join(parts, " · "))
	if row.Reason != "" && row.Verdict != composefile.Differs {
		lead("", vw+sw, toneDim, oneLine(row.Reason))
	}
	if len(row.ConfigHashes) > 0 {
		hs := make([]string, len(row.ConfigHashes))
		for i, h := range row.ConfigHashes {
			hs[i] = shortHash(h)
		}
		sort.Strings(hs)
		lead("", vw+sw, toneDim, "config hash "+strings.Join(hs, ", "))
	}
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// declaredReadNote is the Info tab's sentence on whether the project's file
// has been read, in lines that fit beside the others.
func (m *Model) declaredReadNote(p plan.Project) []string {
	if res := m.dc.results[p.Name+"\x00"+p.ConfigFiles]; res != nil {
		return []string{
			"what the labels record. The Declared tab read them at " + res.at.Local().Format("15:04:05") + ";",
			"nothing ran them.",
		}
	}
	return []string{
		"what the labels record. No file has been read yet: the Declared tab",
		"reads one when asked (Enter), and never runs it.",
	}
}
