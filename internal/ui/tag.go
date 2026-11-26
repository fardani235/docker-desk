package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/live"
	"github.com/fardani235/docker-desk/internal/plan"
)

// Images › t and u: give the highlighted image another local reference, or
// take one of several away (openspec change tag-or-untag-image, D1–D6).
//
// t opens a form bound to the image that was highlighted — its ID, never
// one of its references. The form restates the reference exactly as it will
// be created, and says what that reference names now: nothing, this image
// (Enter then sends nothing), or another image, which is named with the
// warning that the tag will move. Enter starts one job; the job inspects
// the target again and tags nothing if it names another image than the form
// showed (plan.Execute).
//
// u is the removal of one reference while another remains: the typed
// confirmation's word is the tag, the reference is pinned to the image, and
// on the image's last tag it refuses and points to d, because the daemon
// cannot remove the last reference without deleting the image.
//
// Both are local. Neither sends anything to a registry, so --no-registry
// leaves them available; read-only refuses them in invokeOn.

// tagAction and untagAction are Images' t and u. mutating makes read-only
// refuse them; needsRow without setCapable refuses a selection as
// one-at-a-time.
var (
	tagAction = action{key: "t", label: "tag…", desc: "give this image another local reference (no registry is contacted)",
		needsRow: true, mutating: true, run: (*Model).openTagForm}
	untagAction = action{key: "u", label: "untag…", desc: "remove this reference, keeping the image (refused on its last tag)",
		needsRow: true, mutating: true, run: (*Model).prepareUntag}
)

// tagForm is what the tag form is about: the image as the row named it when
// t was pressed. Nothing later re-reads the highlight.
type tagForm struct {
	imageID string
	name    string
	// refs are the image's references when the form opened.
	refs []string

	// target and owner are what the form last said about the text in its
	// field: the normalised reference and the ID of the image holding it
	// ("" for none). shownAt is when the form first said exactly that —
	// whether the operator's typing or a refresh of the image list changed
	// it.
	target  string
	owner   string
	shownAt time.Time
}

// tagFieldLimit bounds the field: a repository name is at most 255
// characters and a tag 128.
const tagFieldLimit = 400

// openTagForm is the t action: the image is captured now, by ID, and the
// form opens. Nothing is sent.
func (m *Model) openTagForm(r row) tea.Cmd {
	if m.kind != KindImages || r.imageID == "" {
		return nil
	}
	t := &tagForm{imageID: r.imageID, name: r.name}
	for _, img := range m.snap.Images {
		if img.ID == r.imageID {
			t.refs = img.Refs()
		}
	}
	d := &dialog{kind: dlgForm, form: formTag, hasInput: true, openedAt: time.Now(), tg: t,
		fields: []formField{textField("New reference", "acme/api:1.2", tagFieldLimit)}}
	d.refocus()
	m.dlg = d
	return nil
}

// tagLook reads the form's text against the image list as it is now: the
// reference that would be created and the image holding it. It records what
// it found and when it first found it, so that Enter can tell a move the
// operator has had on screen from one whose warning has only just appeared.
func (m *Model) tagLook(t *tagForm, text string) (target string, owner plan.TagOwner, err error) {
	target, err = plan.NormalizeTag(text)
	if err != nil {
		t.target, t.owner, t.shownAt = "", "", time.Time{}
		return "", plan.TagOwner{}, err
	}
	owner = plan.Snapshot{Images: m.snap.Images}.TagOwner(t.imageID, target)
	if target != t.target || owner.ID != t.owner || t.shownAt.IsZero() {
		t.target, t.owner, t.shownAt = target, owner.ID, time.Now()
	}
	return target, owner, nil
}

// tagMoveUnread reports whether the form's move warning has not been on
// screen long enough to have been read: the same quarter second every
// confirmation waits (Model.armed), counted from when the form first named
// this holder for this target.
func (m *Model) tagMoveUnread(t *tagForm) bool {
	return m.opts.ArmDelay >= 0 && time.Since(t.shownAt) < m.opts.ArmDelay
}

// submitTag is Enter in the tag form. A refused target keeps the form open
// and sends nothing; so does a target that already names this image.
// Otherwise one job is started, carrying what the form showed.
func (m *Model) submitTag() tea.Cmd {
	d := m.dlg
	t := d.tg
	if m.readOnly {
		// invokeOn refused the key; this is the form's own word on it.
		m.dlg = nil
		m.setStatus(toneWarn, `Read-only mode: "tag…" is disabled. Nothing was sent to the daemon.`)
		return nil
	}
	target, owner, err := m.tagLook(t, d.formText())
	if err != nil {
		d.formErr = capitalise(err.Error()) + ". Nothing was sent."
		return nil
	}
	if owner.Kind == plan.TagOtherImage && m.tagMoveUnread(t) {
		// A move is the one outcome of this form that takes something from
		// another image. Its warning appears with the last character typed,
		// or when the image list changes under the form: an Enter in that
		// same moment was pressed for what the form said before.
		d.formErr = fmt.Sprintf("%s names image %s: the warning that the tag will move has only just appeared. Read it, then press Enter again. Nothing was sent.",
			target, escape.Name(owner.Short))
		return nil
	}
	if owner.Kind == plan.TagThisImage {
		d.formErr = target + " already names this image: there is nothing to tag. Nothing was sent."
		return nil
	}
	p, err := plan.ImageTag(plan.Snapshot{Images: m.snap.Images}, t.imageID, target)
	if err != nil {
		d.formErr = capitalise(escape.Name(err.Error()))
		return nil
	}
	m.dlg = nil
	if m.runPlan(p) != 0 {
		// The list shows the new reference as soon as it can: now, and
		// again on the daemon's tag event and when the job ends.
		m.pending |= live.DirtyImages
	}
	return nil
}

// tagBody lays out the tag form: the image and its references, the daemon,
// the field, the reference exactly as it will be created, what it names
// now, and the rules.
func (m *Model) tagBody(d *dialog, w int) (string, []string) {
	st := m.st
	t := d.tg
	short := escID(t.imageID)
	var body []string
	kv := func(k, v string, tn tone) {
		for i, part := range wrapLine(v, max(w-11, 10)) {
			label := "           "
			if i == 0 {
				label = st.tone(toneDim, fit(k, 10)) + " "
			}
			body = append(body, label+st.tone(tn, part))
		}
	}
	kv("image", t.name+"  ("+short+")", toneNormal)
	body = append(body, m.daemonLines(w, true)...)
	if len(t.refs) == 0 {
		kv("tags", "none: the image is untagged, and is tagged by its ID", toneDim)
	} else {
		kv("tags", escape.Name(strings.Join(t.refs, ", ")), toneNormal)
	}

	f := &d.fields[0]
	f.rows[0].Width = max(w-4, 10)
	body = append(body, "", "New reference, repository[:tag]:")
	m.recordDialogRow(len(body), hitFormRow, 0, 0, "")
	body = append(body, escInput(f.rows[0].View()))
	note := func(glyph string, tn tone, text string) {
		body = append(body, bullets(st, glyph, tn, []string{text}, w)...)
	}
	if text := d.formText(); strings.TrimSpace(text) == "" {
		note(" ", toneDim, "A name without a tag means :latest. A digest cannot be a tag.")
	} else if target, owner, err := m.tagLook(t, text); err != nil {
		note("✗", toneWarn, capitalise(err.Error())+".")
	} else {
		body = append(body, "  "+st.tone(toneDim, "will create")+" "+st.dialogTitle.Render(target))
		switch owner.Kind {
		case plan.TagThisImage:
			note("•", toneWarn, "Already names this image: there is nothing to tag, and Enter sends nothing.")
		case plan.TagOtherImage:
			other := escape.Name(owner.Short)
			also := ""
			if n := len(owner.Refs) - 1; n > 0 {
				also = fmt.Sprintf(", which keeps its %s", pluralWord(n, "other reference", "other references"))
			} else {
				also = ", which is left untagged"
			}
			note("▲", toneWarn, fmt.Sprintf("It names image %s now%s: the tag will MOVE to image %s. Image %s keeps its data.", other, also, short, other))
		default:
			note("•", toneNormal, "No image has this reference: it is created.")
		}
	}
	body = append(body, "")
	body = append(body, bullets(st, "•", toneDim, []string{
		"Local only: no registry is contacted, and nothing is pulled or pushed.",
		"The image is tagged by its ID. The target is checked again just before the request: if it then names another image than shown here, nothing is tagged.",
	}, w)...)
	if d.formErr != "" {
		body = append(body, "")
		body = append(body, bullets(st, "✗", toneBad, []string{d.formErr}, w)...)
	}
	body = append(body, "", m.hints("Enter", "tag", "Esc", "cancel"))
	return "Tag image", body
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// prepareUntag is the u action: an untagged row is refused here; the last
// reference, a reference that is gone and a digest reference are refused by
// the plan, from a fresh snapshot, before any dialog. Otherwise the typed
// confirmation opens. Nothing is sent that could change the daemon.
func (m *Model) prepareUntag(r row) tea.Cmd {
	if m.kind != KindImages || r.imageID == "" {
		return nil
	}
	if r.imageRef == "" {
		m.setStatus(toneWarn, "This image has no tag to remove; nothing was sent. (d removes an untagged image.)")
		return nil
	}
	id, ref := r.imageID, r.imageRef
	return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.ImageUntag(s, id, ref) }, planMsg{})
}
