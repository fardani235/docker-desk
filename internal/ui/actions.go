package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/shell"
)

// action is something the operator can do to the highlighted object (or, for
// actions without needsRow, to the kind as a whole).
//
// No action sends a mutation. Each one ends in a dialog: a confirmation, a
// typed confirmation or a form. See invoke.
type action struct {
	key      string
	label    string
	desc     string
	needsRow bool
	mutating bool
	run      func(m *Model, r row) tea.Cmd
	// setOp is the operation applied to every selected object when the
	// kind has a selection. Actions without one refuse while there is a
	// selection — unless they ignore it (ignoresSet: pull and create act on
	// nothing that exists).
	setOp      plan.Op
	setCapable bool
	ignoresSet bool
}

func containerAction(op plan.Op) func(*Model, row) tea.Cmd {
	return func(m *Model, r row) tea.Cmd {
		id := r.containerID
		return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.Container(op, s, id) }, planMsg{})
	}
}

// containerActions builds the lifecycle actions, all of which apply to a
// selection as well as to one container.
func containerActions(defs ...action) []action {
	for i := range defs {
		if defs[i].run == nil {
			defs[i].run = containerAction(defs[i].setOp)
			defs[i].setCapable = true
		}
	}
	return defs
}

var kindActions = [kindCount][]action{
	KindContainers: containerActions(
		action{key: "s", label: "start", desc: "start the container (or every selected one)", needsRow: true, mutating: true, setOp: plan.OpStart},
		action{key: "t", label: "stop", desc: "stop it (signal, then kill after the grace period)", needsRow: true, mutating: true, setOp: plan.OpStop},
		action{key: "R", label: "restart", desc: "stop and start it again", needsRow: true, mutating: true, setOp: plan.OpRestart},
		action{key: "p", label: "pause", desc: "freeze its processes", needsRow: true, mutating: true, setOp: plan.OpPause},
		action{key: "u", label: "unpause", desc: "resume its processes", needsRow: true, mutating: true, setOp: plan.OpUnpause},
		action{key: "K", label: "kill", desc: "send SIGKILL now", needsRow: true, mutating: true, setOp: plan.OpKill},
		action{key: "d", label: "remove…", desc: "delete the container (never forced)", needsRow: true, mutating: true, setOp: plan.OpRemoveContainer},
		action{key: "e", label: "shell", desc: "open an interactive shell inside it (one container only)", needsRow: true, mutating: true, run: (*Model).openShellForm},
	),
	KindImages: {
		{key: "P", label: "pull…", desc: "pull an image by name", mutating: true, ignoresSet: true, run: func(m *Model, _ row) tea.Cmd { return m.openForm(formPull) }},
		{key: "d", label: "remove…", desc: "remove this reference / image (never forced)", needsRow: true, mutating: true,
			setOp: plan.OpRemoveImage, setCapable: true,
			run: func(m *Model, r row) tea.Cmd {
				id, ref := r.imageID, r.imageRef
				return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.ImageRemove(s, id, ref) }, planMsg{})
			}},
		{key: "X", label: "prune…", desc: "remove unused images, after showing exactly which", mutating: true,
			run: func(m *Model, _ row) tea.Cmd { return m.preparePrune(plan.ImagePrune, false) }},
	},
	KindVolumes: {
		{key: "n", label: "new…", desc: "create a volume", mutating: true, ignoresSet: true, run: func(m *Model, _ row) tea.Cmd { return m.openForm(formVolume) }},
		{key: "d", label: "remove…", desc: "delete the volume and its data (never forced)", needsRow: true, mutating: true,
			setOp: plan.OpRemoveVolume, setCapable: true,
			run: func(m *Model, r row) tea.Cmd {
				name := r.volume
				return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.VolumeRemove(s, name) }, planMsg{})
			}},
		{key: "X", label: "prune…", desc: "remove unused volumes, after showing exactly which", mutating: true,
			run: func(m *Model, _ row) tea.Cmd { return m.preparePrune(plan.VolumePrune, false) }},
	},
	KindNetworks: {
		{key: "n", label: "new…", desc: "create a bridge network", mutating: true, ignoresSet: true, run: func(m *Model, _ row) tea.Cmd { return m.openForm(formNetwork) }},
		{key: "d", label: "remove…", desc: "delete the network", needsRow: true, mutating: true,
			setOp: plan.OpRemoveNetwork, setCapable: true,
			run: func(m *Model, r row) tea.Cmd {
				id := r.networkID
				return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.NetworkRemove(s, id) }, planMsg{})
			}},
		{key: "X", label: "prune…", desc: "remove unused networks, after showing exactly which", mutating: true,
			run: func(m *Model, _ row) tea.Cmd {
				return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.NetworkPrune(s), nil }, planMsg{})
			}},
		{key: "c", label: "connect…", desc: "attach a container to this network (one network only)", needsRow: true, mutating: true,
			run: func(m *Model, r row) tea.Cmd { return m.preparePick(r, true) }},
		{key: "D", label: "disconnect…", desc: "detach a container from this network (one network only)", needsRow: true, mutating: true,
			run: func(m *Model, r row) tea.Cmd { return m.preparePick(r, false) }},
	},
	KindCompose: {
		{key: "t", label: "stop project", desc: "stop every running container of the project", needsRow: true, mutating: true,
			run: func(m *Model, r row) tea.Cmd {
				name := r.project
				return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.ProjectStop(s, name) }, planMsg{})
			}},
		{key: "d", label: "remove project…", desc: "remove every container of the project (not networks or volumes)", needsRow: true, mutating: true,
			run: func(m *Model, r row) tea.Cmd {
				name := r.project
				return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.ProjectRemove(s, name) }, planMsg{})
			}},
	},
}

func (m *Model) actionFor(key string) (action, bool) {
	for _, a := range kindActions[m.kind] {
		if a.key == key {
			return a, true
		}
	}
	return action{}, false
}

// invoke is the entry point for an action key: the objects acted on are the
// kind's selection when it has one, and otherwise the row highlighted at this
// moment. Either is captured here, by identity, and nothing later re-reads
// the highlight or the selection.
func (m *Model) invoke(a action) tea.Cmd {
	r, ok := m.currentRow()
	return m.invokeOn(a, r, ok, m.markedRows(m.kind))
}

// invokeOn runs an action on a row, or a selection, captured earlier (the
// action menu names its object in its title; choosing from it must act on
// that object, not on whatever the highlight has moved to since).
func (m *Model) invokeOn(a action, r row, ok bool, set []row) tea.Cmd {
	if m.preparing {
		return nil
	}
	if a.mutating && m.readOnly {
		m.setStatus(toneWarn, fmt.Sprintf("Read-only mode: %q is disabled. Nothing was sent to the daemon.", a.label))
		return nil
	}
	if len(set) > 0 && !a.ignoresSet {
		label := strings.TrimSuffix(a.label, "…")
		switch {
		case a.setCapable:
			return m.prepareSet(a.setOp, set)
		case a.needsRow:
			m.setStatus(toneWarn, fmt.Sprintf("%s works on one object at a time and %d are selected: Esc clears the selection. Nothing was sent.", label, len(set)))
		default:
			m.setStatus(toneWarn, fmt.Sprintf("%s chooses its own objects, not the %d selected: Esc clears the selection first. Nothing was sent.", label, len(set)))
		}
		return nil
	}
	if a.needsRow && !ok {
		m.setStatus(toneWarn, "Nothing is highlighted.")
		return nil
	}
	return a.run(m, r)
}

// prepareSet builds one plan for every object of a selection, from a fresh
// snapshot, exactly as prepare does for one.
func (m *Model) prepareSet(op plan.Op, set []row) tea.Cmd {
	members := make([]plan.Member, len(set))
	for i, r := range set {
		members[i] = r.member()
	}
	return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.Set(op, s, members) }, planMsg{fromSet: true, setKind: m.kind})
}

func (m *Model) preparePrune(build func(plan.Snapshot, bool) plan.Plan, all bool) tea.Cmd {
	return m.prepare(
		func(s plan.Snapshot) (plan.Plan, error) { return build(s, all), nil },
		planMsg{scope: build, scopeAll: all},
	)
}

// preparePick lists the containers that can be connected to, or disconnected
// from, the highlighted network.
func (m *Model) preparePick(r row, connect bool) tea.Cmd {
	m.preparing = true
	m.planSeq++
	seq := m.planSeq
	eng := m.eng
	netID, netName := r.networkID, r.name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snap, err := plan.Take(ctx, eng)
		if err != nil {
			return pickMsg{seq: seq, err: fmt.Errorf("could not read the daemon's current state: %w", err)}
		}
		var cands []engine.Container
		msg := pickMsg{seq: seq}
		if connect {
			cands, err = plan.ConnectCandidates(snap, netID)
			msg.title = "Connect a container to " + netName
			msg.note = "Containers not yet attached to this network:"
		} else {
			cands, err = plan.DisconnectCandidates(snap, netID)
			msg.title = "Disconnect a container from " + netName
			msg.note = "Containers attached to this network:"
		}
		if err != nil {
			return pickMsg{seq: seq, err: err}
		}
		for _, c := range cands {
			msg.items = append(msg.items, pickItem{id: c.ID, label: c.Name, detail: c.State, tone: stateTone(c.State)})
		}
		msg.onPick = func(m *Model, containerID string) tea.Cmd {
			return m.prepare(func(s plan.Snapshot) (plan.Plan, error) {
				if connect {
					return plan.NetworkConnect(s, netID, containerID)
				}
				return plan.NetworkDisconnect(s, netID, containerID)
			}, planMsg{})
		}
		return msg
	}
}

func (m *Model) openForm(kind formKind) tea.Cmd {
	d := &dialog{kind: dlgForm, form: kind, hasInput: true, openedAt: time.Now()}
	switch kind {
	case formPull:
		d.input = newInput("alpine:3.20", 200)
	case formVolume:
		d.input = newInput("my-volume", 100)
	case formNetwork:
		d.input = newInput("my-network", 100)
	}
	m.dlg = d
	return nil
}

func (m *Model) openShellForm(r row) tea.Cmd {
	if r.state != engine.StateRunning {
		m.setStatus(toneWarn, fmt.Sprintf("%s is not running (%s): there is nothing to open a shell in.", r.name, r.state))
		return nil
	}
	d := &dialog{kind: dlgForm, form: formShell, hasInput: true, target: r, openedAt: time.Now()}
	d.input = newInput("sh", 300)
	d.input.SetValue(shell.DefaultCommandText)
	d.input.CursorEnd()
	m.dlg = d
	return nil
}

// submitForm handles Enter in a form.
func (m *Model) submitForm() tea.Cmd {
	d := m.dlg
	if d.formBusy {
		return nil
	}
	text := d.input.Value()
	d.formErr = ""
	switch d.form {
	case formShell:
		argv, err := splitCommand(text)
		if err != nil {
			d.formErr = err.Error()
			return nil
		}
		target := d.target
		m.dlg = nil
		sess := shell.New(m.eng, target.containerID, target.name, argv)
		return tea.Exec(sess, func(err error) tea.Msg {
			return shellDoneMsg{session: sess, name: target.name, err: err}
		})
	case formPull:
		// Cheap syntax check first; no request for an obviously bad name.
		if _, err := plan.NormalizeRef(text); err != nil {
			d.formErr = err.Error()
			return nil
		}
		d.formBusy = true
		return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.Pull(s, text) }, planMsg{fromForm: true})
	case formVolume:
		d.formBusy = true
		return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.VolumeCreate(s, text) }, planMsg{fromForm: true})
	case formNetwork:
		d.formBusy = true
		return m.prepare(func(s plan.Snapshot) (plan.Plan, error) { return plan.NetworkCreate(s, text) }, planMsg{fromForm: true})
	}
	return nil
}
