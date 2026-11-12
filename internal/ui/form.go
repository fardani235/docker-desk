package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/fardani235/docker-desk/internal/plan"
)

// ---- the form model ------------------------------------------------------------------
//
// Every form is a list of fields (docs/DECISIONS.md D26). Pull, volume,
// network and shell are one text field each; the create form has seven.
// One set of keys and one renderer serve them all.

type fieldKind int

const (
	fieldText   fieldKind = iota // one row of text
	fieldList                    // repeatable rows: Ctrl+N adds, Ctrl+X removes
	fieldChoice                  // one of choices, cycled with ← →; may own a text row
)

type formField struct {
	label string
	// hint is the field's syntax, drawn while it is focused.
	hint        string
	kind        fieldKind
	placeholder string
	limit       int
	// rows: one for a text field, one per entry for a list. For a choice
	// field, rows[0] is the extra value of the choices that take one (the
	// restart policy's retry limit for on-failure).
	rows    []textinput.Model
	choices []string
	choice  int
	// row is the focused row. For a choice field 0 is the choice itself and
	// 1 its extra value.
	row int
}

func (f *formField) newRow() textinput.Model {
	in := newInput(f.placeholder, f.limit)
	in.Blur()
	return in
}

func textField(label, placeholder string, limit int) formField {
	f := formField{label: label, kind: fieldText, placeholder: placeholder, limit: limit}
	f.rows = []textinput.Model{f.newRow()}
	return f
}

func listField(label, placeholder, hint string) formField {
	f := formField{label: label, kind: fieldList, placeholder: placeholder, limit: 300, hint: hint}
	f.rows = []textinput.Model{f.newRow()}
	return f
}

// rowCount is how many rows ↑ ↓ move between.
func (f *formField) rowCount() int {
	switch f.kind {
	case fieldList:
		return len(f.rows)
	case fieldChoice:
		if f.choiceTakesValue() {
			return 2
		}
		return 1
	}
	return 1
}

func (f *formField) choiceTakesValue() bool {
	return f.kind == fieldChoice && len(f.rows) > 0 && f.choices[f.choice] == "on-failure"
}

// input is the focused row's text input, or nil on a choice row.
func (f *formField) input() *textinput.Model {
	switch f.kind {
	case fieldChoice:
		if f.row == 1 && len(f.rows) > 0 {
			return &f.rows[0]
		}
		return nil
	default:
		f.row = clamp(f.row, 0, len(f.rows)-1)
		return &f.rows[f.row]
	}
}

func (f *formField) values() []string {
	out := make([]string, len(f.rows))
	for i, r := range f.rows {
		out[i] = r.Value()
	}
	return out
}

func (f *formField) value() string { return f.rows[0].Value() }

// formInput is the focused text input of the open form (nil on a choice).
func (d *dialog) formInput() *textinput.Model {
	if len(d.fields) == 0 {
		return nil
	}
	return d.fields[d.focus].input()
}

// refocus gives the cursor to exactly the focused row.
func (d *dialog) refocus() {
	cur := d.formInput()
	for i := range d.fields {
		for j := range d.fields[i].rows {
			r := &d.fields[i].rows[j]
			if r == cur {
				r.Focus()
			} else {
				r.Blur()
			}
		}
	}
}

// formText is the single-field forms' value.
func (d *dialog) formText() string { return d.fields[0].value() }

// onFormKey edits a form. Every key that moves, adds or removes is taken
// here, before the text input can see it: Ctrl+N and Ctrl+X are never typed.
func (m *Model) onFormKey(k tea.KeyMsg) tea.Cmd {
	d := m.dlg
	key := k.String()
	switch key {
	case "esc":
		m.closeDialog()
		return nil
	case "enter":
		if (d.form == formCreate || d.form == formShell) && !m.armed() {
			// The form has not been on screen long enough to be read: an
			// Enter typed ahead of it was meant for something else. The
			// create form is a whole configuration to confirm, the shell's
			// command is pre-filled; in pull, volume and network the
			// operator types the whole value into the form itself (C5).
			return nil
		}
		return m.submitForm()
	}
	if d.formBusy {
		return nil
	}
	f := &d.fields[d.focus]
	switch key {
	case "tab", "shift+tab":
		n := len(d.fields)
		if key == "tab" {
			d.focus = (d.focus + 1) % n
		} else {
			d.focus = (d.focus + n - 1) % n
		}
		d.fields[d.focus].row = 0
		d.refocus()
		return nil
	case "up", "down":
		if key == "up" {
			f.row = max(f.row-1, 0)
		} else {
			f.row = min(f.row+1, f.rowCount()-1)
		}
		d.refocus()
		return nil
	case "ctrl+n":
		if f.kind == fieldList {
			f.rows = append(f.rows, f.newRow())
			f.row = len(f.rows) - 1
			d.formErr = ""
			d.refocus()
		}
		return nil
	case "ctrl+x":
		if f.kind == fieldList {
			if len(f.rows) == 1 {
				f.rows[0].SetValue("")
			} else {
				f.rows = append(f.rows[:f.row], f.rows[f.row+1:]...)
				f.row = min(f.row, len(f.rows)-1)
			}
			d.formErr = ""
			d.refocus()
		}
		return nil
	case "left", "right":
		if f.kind == fieldChoice && f.row == 0 {
			n := len(f.choices)
			if key == "right" {
				f.choice = (f.choice + 1) % n
			} else {
				f.choice = (f.choice + n - 1) % n
			}
			d.formErr = ""
			return nil
		}
	}
	in := d.formInput()
	if in == nil {
		return nil // a choice row takes no text
	}
	d.formErr = ""
	var cmd tea.Cmd
	*in, cmd = in.Update(k)
	return cmd
}

// ---- the create form -----------------------------------------------------------------

const (
	createImage = iota
	createName
	createPorts
	createEnv
	createMounts
	createRestart
	createCommand
)

func createFields() []formField {
	restart := formField{label: "Restart", kind: fieldChoice, choices: plan.RestartPolicies, placeholder: "no limit", limit: 6,
		hint: "← → choose the policy; on-failure takes an optional retry limit (↓, a whole number)"}
	restart.rows = []textinput.Model{restart.newRow()}
	f := []formField{
		textField("Image", "alpine:3.20", 200),
		textField("Name", "optional — the daemon assigns one", 128),
		listField("Ports", "8080:80", "[host-address:][host-port:]container-port[/tcp|udp] — e.g. 8080:80, 127.0.0.1:5432:5432, 53:53/udp, [::1]:8080:80"),
		listField("Environment", "NAME=value", "NAME=value, one per entry; the value is everything after the first ="),
		listField("Mounts", "data:/var/lib/data", "source:destination[:ro] — a source starting with / is a host path, else a volume name"),
		restart,
		textField("Command", "the image's own command", 300),
	}
	f[createImage].hint = "a reference as for docker pull; no tag means :latest"
	f[createName].hint = "letters, digits, _ . - ; empty lets the daemon name it"
	f[createCommand].hint = "arguments, quoted as in a shell ('a b', \"a b\", a\\ b); nothing is expanded; empty keeps the image's"
	return f
}

// createInput collects the form's text for plan.ParseCreate.
func (d *dialog) createInput() plan.CreateInput {
	f := d.fields
	r := f[createRestart]
	restart := r.choices[r.choice]
	if r.choiceTakesValue() {
		if v := strings.TrimSpace(r.rows[0].Value()); v != "" {
			restart += ":" + v
		}
	}
	return plan.CreateInput{
		Image:   f[createImage].value(),
		Name:    f[createName].value(),
		Ports:   f[createPorts].values(),
		Env:     f[createEnv].values(),
		Mounts:  f[createMounts].values(),
		Restart: restart,
		Command: f[createCommand].value(),
	}
}

func (m *Model) openCreateForm() tea.Cmd {
	d := &dialog{kind: dlgForm, form: formCreate, hasInput: true, openedAt: time.Now(), fields: createFields()}
	d.refocus()
	m.dlg = d
	return nil
}

// placeFieldError puts focus on the field, and entry, a refused value is in.
func (d *dialog) placeFieldError(err error) {
	d.formErr = err.Error()
	var fe *plan.FieldError
	if !errors.As(err, &fe) {
		return
	}
	idx := map[plan.Field]int{
		plan.FieldImage: createImage, plan.FieldName: createName, plan.FieldPorts: createPorts,
		plan.FieldEnv: createEnv, plan.FieldMounts: createMounts, plan.FieldRestart: createRestart,
		plan.FieldCommand: createCommand,
	}[fe.Field]
	d.focus = idx
	f := &d.fields[idx]
	f.row = 0
	if fe.Entry >= 0 && f.kind == fieldList {
		f.row = clamp(fe.Entry, 0, len(f.rows)-1)
	}
	if f.kind == fieldChoice && f.choiceTakesValue() {
		f.row = 1
	}
	d.refocus()
}

// createBody lays out the create form so that it fits the terminal: the
// daemon on top, the key hints at the bottom, and between them a window of
// fields around the focused one followed by the summary of what will be
// sent (D27). The focused field's syntax hint and the error are pinned
// above the keys.
func (m *Model) createBody(d *dialog, w, maxBody int) []string {
	st := m.st
	ident := m.daemonLines(w, true)

	// The fields, each a label line and its rows.
	type span struct{ from, to int }
	var lines []string
	spans := make([]span, len(d.fields))
	const lw = 12
	pad := strings.Repeat(" ", lw+2)
	for i := range d.fields {
		f := &d.fields[i]
		focused := i == d.focus
		mark := "  "
		if focused {
			mark = st.tone(toneAccent, "▸ ")
		}
		label := fit(f.label, lw)
		if focused {
			label = st.dialogTitle.Render(label)
		} else {
			label = st.tone(toneDim, label)
		}
		from := len(lines)
		switch f.kind {
		case fieldText:
			lines = append(lines, mark+label+f.rows[0].View())
		case fieldList:
			for j := range f.rows {
				prefix := pad
				if j == 0 {
					prefix = mark + label
				}
				lines = append(lines, prefix+f.rows[j].View())
			}
			if focused {
				lines = append(lines, pad+st.tone(toneDim, fmt.Sprintf("entry %d of %d · Ctrl+N add · Ctrl+X remove", f.row+1, len(f.rows))))
			}
		case fieldChoice:
			var opts []string
			for j, c := range f.choices {
				if j == f.choice {
					opts = append(opts, st.selected.Render("["+c+"]"))
				} else {
					opts = append(opts, st.tone(toneDim, " "+c+" "))
				}
			}
			cur := "  "
			if focused && f.row == 0 {
				cur = st.tone(toneAccent, "‹ ")
			}
			lines = append(lines, mark+label+cur+strings.Join(opts, ""))
			if f.choiceTakesValue() {
				lines = append(lines, pad+st.tone(toneDim, "retry limit ")+f.rows[0].View())
			}
		}
		spans[i] = span{from, len(lines)}
	}

	summary := append([]string{""}, m.createSummary(d, w)...)

	// What stays put at the bottom.
	var tail []string
	if hint := d.fields[d.focus].hint; hint != "" {
		for _, part := range wrapWords(hint, w-2) {
			tail = append(tail, st.tone(toneDim, part))
		}
	}
	if d.formErr != "" {
		tail = append(tail, bullets(st, "✗", toneBad, []string{d.formErr}, w)...)
	}
	pos := st.tone(toneDim, fmt.Sprintf("field %d of %d", d.focus+1, len(d.fields)))
	if d.formBusy {
		tail = append(tail, pos+"  "+st.tone(toneDim, "checking the daemon's current state…"))
	} else {
		keys := []string{"Tab ⇧Tab", "field", "↑↓", "row"}
		if d.fields[d.focus].kind == fieldChoice {
			keys = append(keys, "←→", "policy")
		}
		keys = append(keys, "Ctrl+N", "add", "Ctrl+X", "remove", "Enter", "create and start", "Esc", "cancel")
		tail = append(tail, m.packHints(pos, w, keys...)...)
	}

	// The window: everything fits, or the fields and the summary scroll as
	// one so that the focused field is always in view.
	middle := append(append([]string{}, lines...), summary...)
	room := max(maxBody-len(ident)-1-len(tail)-1, 3)
	if len(middle) <= room {
		d.formOff = 0
		return append(append(append(ident, ""), middle...), append([]string{""}, tail...)...)
	}
	sp := spans[d.focus]
	if sp.to-sp.from > room {
		// A field longer than the window: keep its focused row in view.
		row := sp.from + d.fields[d.focus].row
		sp = span{max(row-room/2, sp.from), min(row-room/2+room, sp.to)}
	}
	if sp.from < d.formOff {
		d.formOff = sp.from
	}
	if sp.to > d.formOff+room {
		d.formOff = sp.to - room
	}
	d.formOff = clamp(d.formOff, 0, len(middle)-room)
	window := middle[d.formOff : d.formOff+room]
	more := st.tone(toneDim, fmt.Sprintf("  lines %d–%d of %d · Tab moves the view with the focus", d.formOff+1, d.formOff+room, len(middle)))
	body := append(append(ident, ""), window...)
	return append(body, append([]string{more}, tail...)...)
}

// createSummary restates, as it is now, everything that will be sent: the
// form is the confirmation (D27). It reads the last image list for the pull
// note; on Enter the plan is built from a fresh snapshot, which decides.
func (m *Model) createSummary(d *dialog, w int) []string {
	st := m.st
	in := d.createInput()
	out := []string{st.dialogTitle.Render("Will create and start, on this daemon:")}
	row := func(label, text string, t tone) {
		for i, part := range wrapLine(text, max(w-13, 8)) {
			l := strings.Repeat(" ", 13)
			if i == 0 {
				l = "  " + st.tone(toneDim, fit(label, 10)) + " "
			}
			out = append(out, l+st.tone(t, part))
		}
	}
	bad := func(label, entry string, err error) {
		row(label, "✗ "+err.Error(), toneBad)
	}

	if strings.TrimSpace(in.Image) == "" {
		row("image", "required", toneWarn)
	} else if ref, err := plan.NormalizeRef(in.Image); err != nil {
		bad("image", in.Image, err)
	} else {
		note := "not in the last image list: it will be pulled first"
		t := toneWarn
		if (plan.Snapshot{Images: m.snap.Images}).ImagePresent(ref) {
			note, t = "present on this daemon", toneGood
		}
		row("image", ref, toneNormal)
		row("", note, t)
	}
	if name := strings.TrimSpace(in.Name); name != "" {
		row("name", name, toneNormal)
	} else {
		row("name", "none given: the daemon assigns one", toneDim)
	}
	list := func(label string, entries []string, parse func(string) (string, error)) {
		n := 0
		for _, e := range entries {
			if strings.TrimSpace(e) == "" {
				continue
			}
			lbl := ""
			if n == 0 {
				lbl = label
			}
			n++
			if s, err := parse(e); err != nil {
				bad(lbl, e, err)
			} else {
				row(lbl, s, toneNormal)
			}
		}
		if n == 0 {
			row(label, "none", toneDim)
		}
	}
	list("ports", in.Ports, func(s string) (string, error) {
		p, err := plan.ParsePort(s)
		return p.String(), err
	})
	// The one place environment values are drawn (D29).
	list("env", in.Env, plan.ParseEnv)
	list("mounts", in.Mounts, func(s string) (string, error) {
		mt, err := plan.ParseMount(s)
		return mt.String(), err
	})
	if r, err := plan.ParseRestart(in.Restart); err != nil {
		bad("restart", in.Restart, err)
	} else {
		row("restart", r.String(), toneNormal)
	}
	if args, err := plan.SplitCommand(in.Command); err != nil {
		bad("command", in.Command, err)
	} else if len(args) == 0 {
		row("command", "the image's own", toneDim)
	} else {
		row("command", quoteArgs(args), toneNormal)
	}
	return out
}

// quoteArgs shows an argument vector unambiguously.
func quoteArgs(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t'\"\\") {
			q[i] = fmt.Sprintf("%q", a)
		} else {
			q[i] = a
		}
	}
	return strings.Join(q, " ")
}

// packHints lays key hints out on as many lines as the width needs, after a
// lead text, never cutting a pair: every key must stay readable.
func (m *Model) packHints(lead string, w int, pairs ...string) []string {
	var lines []string
	cur, curW := lead, ansi.StringWidth(lead)
	for i := 0; i+1 < len(pairs); i += 2 {
		h := m.hints(pairs[i], pairs[i+1])
		hw := ansi.StringWidth(h)
		if curW > 0 && curW+2+hw > w {
			lines = append(lines, cur)
			cur, curW = h, hw
			continue
		}
		if curW > 0 {
			cur += "  "
			curW += 2
		}
		cur += h
		curW += hw
	}
	return append(lines, cur)
}
