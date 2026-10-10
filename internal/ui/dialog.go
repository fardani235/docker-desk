package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/jobs"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/shell"
)

type dialogKind int

const (
	dlgPlan   dialogKind = iota // confirmation of a mutation
	dlgForm                     // pull / create / shell command
	dlgPicker                   // choose a container to connect or disconnect
	dlgMenu                     // actions available on the highlighted object
	dlgHelp
	dlgJob // a job's full output
	dlgQuit
)

type formKind int

const (
	formPull formKind = iota
	formVolume
	formNetwork
	formShell
)

type pickItem struct {
	id     string
	label  string
	detail string
	tone   tone
}

// dialog is whatever overlay is open. Only one can be open at a time, and
// while it is open it receives every key.
type dialog struct {
	kind dialogKind
	// openedAt is when the dialog appeared; see Model.armed.
	openedAt time.Time

	// dlgPlan
	plan     plan.Plan
	scope    func(plan.Snapshot, bool) plan.Plan // set for prunes with two scopes
	scopeAll bool
	listOff  int
	mismatch bool
	// recomputing is set while a prune preview is being rebuilt for the other
	// scope. Until the new preview arrives nothing can be confirmed: what is
	// on screen is about to be replaced.
	recomputing bool

	// text input (typed confirmations and forms)
	hasInput bool
	input    textinput.Model

	// dlgForm
	form     formKind
	formErr  string
	formBusy bool
	target   row // shell: the container

	// dlgPicker
	pickTitle string
	pickNote  string
	items     []pickItem
	pickSel   int
	onPick    func(m *Model, id string) tea.Cmd

	// dlgMenu
	menu       []action
	menuSel    int
	menuRow    row
	menuHasRow bool

	// dlgHelp, dlgJob
	title  string
	text   textView
	jobID  int
	follow bool
}

// planMsg carries a freshly built plan (or the reason there is none).
type planMsg struct {
	// seq identifies the request this answers. An answer to anything but
	// the latest request is dropped: a dialog must never appear — or a form
	// be submitted — because of something the operator has since moved on
	// from.
	seq      uint64
	plan     plan.Plan
	err      error
	fromForm bool
	scope    func(plan.Snapshot, bool) plan.Plan
	scopeAll bool
}

type pickMsg struct {
	seq    uint64
	title  string
	note   string
	items  []pickItem
	err    error
	onPick func(m *Model, id string) tea.Cmd
}

type shellDoneMsg struct {
	session *shell.Session
	name    string
	err     error
}

func newInput(placeholder string, limit int) textinput.Model {
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = placeholder
	in.CharLimit = limit
	in.Focus()
	return in
}

// prepare reads a fresh snapshot of the engine and builds a plan from it.
// Nothing is sent that could change the daemon.
func (m *Model) prepare(build func(plan.Snapshot) (plan.Plan, error), tmpl planMsg) tea.Cmd {
	m.preparing = true
	m.planSeq++
	tmpl.seq = m.planSeq
	eng := m.eng
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snap, err := plan.Take(ctx, eng)
		if err != nil {
			tmpl.err = fmt.Errorf("could not read the daemon's current state: %w", err)
			return tmpl
		}
		tmpl.plan, tmpl.err = build(snap)
		return tmpl
	}
}

func (m *Model) onPlan(msg planMsg) tea.Cmd {
	if msg.seq != m.planSeq {
		return nil // an answer to a request that has been superseded or abandoned
	}
	m.preparing = false
	if msg.fromForm {
		if m.dlg == nil || m.dlg.kind != dlgForm || !m.dlg.formBusy {
			return nil // the form was closed while the check ran: do nothing
		}
		m.dlg.formBusy = false
		if msg.err != nil {
			m.dlg.formErr = msg.err.Error()
			return nil
		}
		m.dlg = nil
		m.runPlan(msg.plan)
		return nil
	}
	// A confirmation may only appear where the operator expects one: on a
	// clear desk, or in place of the prune preview they asked to recompute.
	replacing := m.dlg != nil && m.dlg.kind == dlgPlan && m.dlg.recomputing
	if m.dlg != nil && !replacing {
		return nil
	}
	if msg.err != nil {
		if replacing {
			m.dlg.recomputing = false
		}
		var na *plan.NotApplicable
		if errors.As(msg.err, &na) {
			m.setStatus(toneWarn, na.Reason)
		} else {
			m.setStatus(toneBad, msg.err.Error())
		}
		return nil
	}
	d := &dialog{kind: dlgPlan, plan: msg.plan, scope: msg.scope, scopeAll: msg.scopeAll, openedAt: time.Now()}
	if msg.plan.Tier == plan.TierTyped && msg.plan.Runnable() {
		d.hasInput = true
		d.input = newInput("", 16) // no placeholder: nothing may look pre-typed
	}
	m.dlg = d
	return nil
}

// armed reports whether the open dialog has been on screen long enough to
// accept the input that proceeds. A dialog that has only just appeared
// cannot have been read: a `y` or Enter arriving in its first moments was
// typed for something else, or ahead of it, and is ignored.
func (m *Model) armed() bool {
	if m.dlg == nil {
		return false
	}
	if m.opts.ArmDelay < 0 {
		return true
	}
	return time.Since(m.dlg.openedAt) >= m.opts.ArmDelay
}

func (m *Model) onPick(msg pickMsg) {
	if msg.seq != m.planSeq {
		return
	}
	m.preparing = false
	if m.dlg != nil {
		return // something else was opened meanwhile; do not replace it
	}
	if msg.err != nil {
		var na *plan.NotApplicable
		if errors.As(msg.err, &na) {
			m.setStatus(toneWarn, na.Reason)
		} else {
			m.setStatus(toneBad, msg.err.Error())
		}
		return
	}
	m.dlg = &dialog{kind: dlgPicker, pickTitle: msg.title, pickNote: msg.note, items: msg.items, onPick: msg.onPick, openedAt: time.Now()}
}

// runPlan starts the job that executes exactly this plan. It is reached only
// from a completed confirmation or form.
func (m *Model) runPlan(p plan.Plan) {
	if !p.Runnable() {
		return
	}
	eng := m.eng
	id := m.jobs.Start(p.JobTitle, p.JobTarget, func(ctx context.Context, run *jobs.Run) error {
		return plan.Execute(ctx, eng, p, run)
	})
	m.jobSel = id
	m.setStatus(toneAccent, "started: "+p.JobTitle+" "+p.JobTarget)
}

func (m *Model) onShellDone(msg shellDoneMsg) tea.Cmd {
	m.pending |= 0xf // everything may have changed while the terminal was away
	res := msg.session.Result
	lastLine := func() string {
		lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(res.Tail, "\r", "")), "\n")
		return strings.TrimSpace(lines[len(lines)-1])
	}
	switch {
	case msg.err != nil:
		m.setStatus(toneBad, "Shell in "+msg.name+" could not start: "+plainError(msg.err))
	case (res.ExitCode == 126 || res.ExitCode == 127) && res.Duration < 2*time.Second:
		// The daemon reports a process that cannot start inside the stream
		// and through these exit codes, not as an error.
		m.setStatus(toneBad, fmt.Sprintf("Shell in %s could not start (exit %d): %s", msg.name, res.ExitCode, lastLine()))
	case res.Detached:
		m.setStatus(toneWarn, "Detached from the shell in "+msg.name+"; the process is still running in the container.")
	case res.ExitCode == 0:
		m.setStatus(toneGood, fmt.Sprintf("Back from the shell in %s (%s).", msg.name, humanDuration(res.Duration)))
	case res.ExitCode < 0:
		m.setStatus(toneWarn, "The shell in "+msg.name+" ended; its exit code could not be read.")
	default:
		m.setStatus(toneWarn, fmt.Sprintf("The shell in %s ended with exit code %d.", msg.name, res.ExitCode))
	}
	return nil
}

func plainError(err error) string {
	var e *engine.Error
	if errors.As(err, &e) {
		return e.Msg
	}
	return err.Error()
}

// splitCommand turns the shell form's text into an argument vector. It
// understands single and double quotes; nothing is expanded or interpreted.
func splitCommand(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	in := false
	quote := rune(0)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				args = append(args, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	if in {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, errors.New("enter the command to run, e.g. sh")
	}
	return args, nil
}

// ---- rendering ---------------------------------------------------------------------------

const dialogMaxWidth = 86

// renderDialog returns the dialog as a box and its size.
func (m *Model) renderDialog() (lines []string, w, h int) {
	d := m.dlg
	w = clamp(m.width-4, 40, dialogMaxWidth)
	inner := w - 4
	maxBody := m.height - 6
	if maxBody < 6 {
		maxBody = 6
	}
	var body []string
	title := ""
	switch d.kind {
	case dlgPlan:
		title, body = m.planBody(d, inner, maxBody)
	case dlgForm:
		title, body = m.formBody(d, inner)
	case dlgPicker:
		title, body = m.pickerBody(d, inner, maxBody)
	case dlgMenu:
		title, body = m.menuBody(d, inner)
	case dlgHelp:
		title = "Keys"
		d.text.set(m.helpLines())
		body = d.text.render(inner, maxBody-2)
		pos := d.text.position(maxBody - 2)
		body = append(append([]string{}, body...), "", m.hints("↑↓ PgUp PgDn", "scroll", "Esc", "close")+"  "+m.st.tone(toneDim, pos))
	case dlgJob:
		title, body = m.jobBody(d, inner, maxBody)
	case dlgQuit:
		title, body = m.quitBody(inner)
	}
	padded := make([]string, 0, len(body)+2)
	padded = append(padded, "")
	for _, l := range body {
		padded = append(padded, " "+fit(l, inner))
	}
	h = len(padded) + 2
	return m.st.box(title, "", padded, w, h, true), w, h
}

func (m *Model) hints(pairs ...string) string {
	var sb strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			sb.WriteString("  ")
		}
		sb.WriteString(m.st.hintKey.Render(pairs[i]))
		sb.WriteString(" ")
		sb.WriteString(m.st.hintText.Render(pairs[i+1]))
	}
	return sb.String()
}

func bullets(st styles, glyph string, t tone, items []string, w int) []string {
	var out []string
	for _, it := range items {
		parts := wrapWords(it, w-2)
		for i, p := range parts {
			prefix := "  "
			if i == 0 {
				prefix = glyph + " "
			}
			out = append(out, st.tone(t, prefix+p))
		}
	}
	return out
}

func (m *Model) planBody(d *dialog, w, maxBody int) (string, []string) {
	p := d.plan
	st := m.st
	var head, tail []string

	// What, exactly, and where.
	if p.Target.Name != "" {
		line := st.tone(toneDim, fit(p.Target.Kind, 10)) + " " + st.dialogTitle.Render(p.Target.Name)
		if p.Target.ID != "" {
			line += st.tone(toneDim, "  ("+p.Target.ID+")")
		}
		head = append(head, line)
	}
	head = append(head, st.tone(toneDim, fit("daemon", 10))+" "+m.endpoint.Host+st.tone(toneDim, "  (context "+m.endpoint.Context+")"))
	if p.Scope != "" {
		line := st.tone(toneDim, fit("scope", 10)) + " " + p.Scope
		head = append(head, line)
		if d.scope != nil {
			other := "all unused"
			if d.scopeAll {
				other = "the default scope"
			}
			head = append(head, strings.Repeat(" ", 11)+m.hints("Tab", "switch to "+other))
		}
	}
	head = append(head, "")
	if p.Nothing != "" {
		head = append(head, p.Nothing)
	}
	head = append(head, bullets(st, "•", toneNormal, p.Effects, w)...)
	if len(p.Warnings) > 0 {
		head = append(head, "")
		head = append(head, bullets(st, "▲", toneWarn, p.Warnings, w)...)
	}

	// How to proceed.
	switch {
	case d.recomputing:
		tail = append(tail, "", st.tone(toneDim, spinnerFrames[m.spinner]+" recomputing the list for the other scope…"), "", m.hints("Esc", "cancel"))
	case len(p.Blockers) > 0:
		tail = append(tail, "", st.danger.Render("✗ REFUSED — nothing will be sent to the daemon"))
		tail = append(tail, bullets(st, " ", toneBad, p.Blockers, w)...)
		tail = append(tail, "", m.hints("Esc", "close"))
	case p.Nothing != "":
		tail = append(tail, "", m.hints("Esc", "close"))
	case p.Tier == plan.TierTyped:
		prompt := "Type " + st.dialogTitle.Render(p.Word) + " and press Enter to proceed:"
		tail = append(tail, "", prompt, d.input.View())
		if d.mismatch {
			tail = append(tail, st.tone(toneBad, fmt.Sprintf("That is not %q. Nothing was sent.", p.Word)))
		}
		tail = append(tail, "", m.hints("Enter", "proceed", "Esc", "cancel"))
	default:
		tail = append(tail, "", m.hints("y", "proceed", "n / Esc", "cancel")+st.tone(toneDim, "   (Enter does nothing here)"))
	}

	// The exact objects, when the plan is a set.
	var list []string
	if p.Listed && len(p.Steps) > 0 {
		room := maxBody - len(head) - len(tail) - 3
		if room < 3 {
			room = 3
		}
		total := len(p.Steps)
		d.listOff = clamp(d.listOff, 0, max(total-room, 0))
		outcome := "acted on"
		switch p.Steps[0].Op {
		case plan.OpRemoveContainer, plan.OpRemoveImage, plan.OpRemoveVolume, plan.OpRemoveNetwork:
			outcome = "deleted"
		case plan.OpStop:
			outcome = "stopped"
		}
		headline := fmt.Sprintf("Exactly these %d will be %s:", total, outcome)
		if p.TotalSize > 0 {
			headline = fmt.Sprintf("Exactly these %d will be %s (about %s):", total, outcome, humanBytes(p.TotalSize))
		}
		list = append(list, "", st.dialogTitle.Render(headline))
		end := min(d.listOff+room, total)
		for i := d.listOff; i < end; i++ {
			s := p.Steps[i]
			line := fmt.Sprintf("  %-7s %s", s.Op.String(), s.Name)
			meta := s.Detail
			if s.Size > 0 {
				meta = strings.TrimSpace(meta + "  " + humanBytes(s.Size))
			}
			if meta != "" {
				line = fit(line, w-len([]rune(meta))-2) + "  " + st.tone(toneDim, meta)
			}
			list = append(list, line)
		}
		if total > room {
			list = append(list, st.tone(toneDim, fmt.Sprintf("  rows %d–%d of %d · ↑↓ PgUp PgDn scroll the list", d.listOff+1, end, total)))
		}
	}
	body := append(append(head, list...), tail...)
	return p.Title, body
}

func (m *Model) formBody(d *dialog, w int) (string, []string) {
	st := m.st
	daemon := st.tone(toneDim, fit("daemon", 10)) + " " + m.endpoint.Host + st.tone(toneDim, "  (context "+m.endpoint.Context+")")
	var title, prompt, verb string
	var notes []string
	text := d.input.Value()
	switch d.form {
	case formPull:
		title, prompt, verb = "Pull image", "Image reference:", "pull"
		if strings.TrimSpace(text) != "" {
			if ref, err := plan.NormalizeRef(text); err == nil {
				notes = append(notes, "Will pull exactly: "+st.dialogTitle.Render(ref))
			} else {
				notes = append(notes, st.tone(toneWarn, err.Error()))
			}
		} else {
			notes = append(notes, st.tone(toneDim, "A name without a tag means :latest — one tag, never all of them."))
		}
		notes = append(notes, st.tone(toneDim, "The image is downloaded into this daemon's image store, as a background job."))
	case formVolume:
		title, prompt, verb = "Create volume", "Volume name:", "create"
		notes = append(notes, st.tone(toneDim, "An empty volume with the local driver is created on this daemon."))
	case formNetwork:
		title, prompt, verb = "Create network", "Network name:", "create"
		notes = append(notes, st.tone(toneDim, "A bridge network is created on this daemon; the engine assigns its subnet."))
	case formShell:
		title, prompt, verb = "Open a shell", "Command to run inside the container:", "open the shell"
		daemon = st.tone(toneDim, fit("container", 10)) + " " + st.dialogTitle.Render(d.target.name) +
			st.tone(toneDim, "  ("+engine.ShortID(d.target.containerID)+")") + "\n" + daemon
		notes = append(notes,
			st.tone(toneDim, "The desk hands your terminal to this process. When it exits — or the"),
			st.tone(toneDim, "container stops — you are back here, exactly where you left."),
		)
	}
	body := strings.Split(daemon, "\n")
	body = append(body, "", prompt, d.input.View(), "")
	body = append(body, notes...)
	if d.formErr != "" {
		body = append(body, "")
		body = append(body, bullets(st, "✗", toneBad, []string{d.formErr}, w)...)
	}
	if d.formBusy {
		body = append(body, "", st.tone(toneDim, "checking the daemon's current state…"))
	} else {
		body = append(body, "", m.hints("Enter", verb, "Esc", "cancel"))
	}
	return title, body
}

func (m *Model) pickerBody(d *dialog, w, maxBody int) (string, []string) {
	st := m.st
	body := []string{st.tone(toneDim, fit("daemon", 10)) + " " + m.endpoint.Host}
	if d.pickNote != "" {
		body = append(body, st.tone(toneDim, d.pickNote))
	}
	body = append(body, "")
	room := clamp(maxBody-len(body)-3, 3, 14)
	off := 0
	if d.pickSel >= room {
		off = d.pickSel - room + 1
	}
	for i := off; i < len(d.items) && i < off+room; i++ {
		it := d.items[i]
		line := fit(it.label, w-22) + "  " + fit(it.detail, 18)
		if i == d.pickSel {
			body = append(body, st.selected.Render("▸ "+line))
		} else {
			body = append(body, "  "+st.tone(it.tone, line))
		}
	}
	if len(d.items) > room {
		body = append(body, st.tone(toneDim, fmt.Sprintf("  %d of %d", d.pickSel+1, len(d.items))))
	}
	body = append(body, "", m.hints("↑↓", "choose", "Enter", "continue to confirmation", "Esc", "cancel"))
	return d.pickTitle, body
}

func (m *Model) menuBody(d *dialog, w int) (string, []string) {
	st := m.st
	var body []string
	for i, a := range d.menu {
		line := fit(a.key, 3) + " " + fit(a.label, 22) + " " + a.desc
		line = fit(line, w-2)
		if i == d.menuSel {
			body = append(body, st.selected.Render("▸ "+line))
		} else {
			body = append(body, "  "+line)
		}
	}
	body = append(body, "", st.tone(toneDim, "Every action that changes something asks for confirmation first."))
	body = append(body, m.hints("↑↓", "choose", "Enter", "run", "Esc", "close"))
	title := "Actions"
	if d.menuRow.name != "" {
		title += ": " + d.menuRow.name
	}
	return title, body
}

func (m *Model) jobBody(d *dialog, w, maxBody int) (string, []string) {
	j, ok := m.jobs.Get(d.jobID)
	if !ok {
		return "Job", []string{m.st.tone(toneDim, "This job was cleared."), "", m.hints("Esc", "close")}
	}
	viewH := maxBody - 2
	d.text.set(jobOutput(j, m.st))
	d.text.layout(w)
	if d.follow {
		d.text.off = max(len(d.text.wrapped)-viewH, 0)
	}
	body := append([]string{}, d.text.render(w, viewH)...)
	hint := m.hints("↑↓ PgUp PgDn", "scroll", "Esc", "close")
	if j.State == jobs.Running {
		hint = m.hints("↑↓ PgUp PgDn", "scroll", "c", "cancel job", "Esc", "close")
	}
	body = append(body, "", hint+"  "+m.st.tone(toneDim, d.text.position(viewH)))
	return fmt.Sprintf("Job #%d output", j.ID), body
}

func (m *Model) quitBody(w int) (string, []string) {
	st := m.st
	body := []string{st.tone(toneWarn, "Jobs are still running:"), ""}
	for _, j := range m.jobs.List() {
		if j.State == jobs.Running {
			body = append(body, "  "+spinnerFrames[m.spinner]+" "+j.Title+" "+j.Target+st.tone(toneDim, "  "+jobProgress(j)))
		}
	}
	body = append(body, "")
	body = append(body, bullets(st, "•", toneNormal, []string{
		"Quitting cancels them: no further requests are sent.",
		"A request the daemon has already accepted completes or fails on the daemon's side; multi-object jobs stop where they are.",
	}, w)...)
	body = append(body, "", m.hints("y", "quit anyway", "n / Esc", "stay"))
	return "Quit Docker Desk?", body
}
