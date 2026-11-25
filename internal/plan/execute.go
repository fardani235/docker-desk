package plan

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// Execute sends exactly the plan's steps to the daemon, in order, one
// request per step, never forced. It is the only place in the desk where a
// plan turns into requests.
//
// A step that fails is recorded and the remaining steps still run; the job
// then ends as failed with a summary. The exception is a step that depends
// on another (Step.DependsOn): it is attempted only when every step it
// depends on succeeded, and is otherwise skipped and logged with the step it
// waited on. A step with IDFrom sends the identifier its producing step
// returned. Cancellation is honoured between steps: what was already done is
// reported, nothing further is sent.
func Execute(ctx context.Context, eng engine.Engine, p Plan, run *jobs.Run) error {
	// An image copy acts on two daemons; this function has one. Refused
	// before anything else, so that nothing at all is sent.
	if p.ImageCopy != nil || hasCopyStep(p) {
		return errors.New("internal error: an image copy runs between two daemons (plan.ExecuteCopy), not on one; nothing was sent")
	}
	if !p.Runnable() {
		return errors.New("internal error: tried to execute a plan that is not runnable")
	}
	if err := checkPlanPin(ctx, eng, p, run); err != nil {
		return err
	}
	// A push is one request with its own pin, credential and reporting.
	if push, err := isPushPlan(p); push {
		if err != nil {
			return err
		}
		return executePush(ctx, eng, p, run)
	}
	total := len(p.Steps)
	multi := total > 1
	if multi {
		run.Steps(0, total)
	}
	chained := false
	for _, st := range p.Steps {
		chained = chained || len(st.DependsOn) > 0
	}
	outcomes := make([]outcome, total)
	produced := make([]string, total)
	shown := make([]Step, total) // each step as it was sent, for the log
	copy(shown, p.Steps)
	var failed, succeeded, skipped int
	var lastErr error
	var freed int64
	cancelled := func(at int) error {
		line := fmt.Sprintf("cancelled: %d succeeded", succeeded)
		if failed > 0 {
			line += fmt.Sprintf(", %d failed", failed)
		}
		if skipped > 0 {
			line += fmt.Sprintf(", %d skipped", skipped)
		}
		run.Logf("%s, the remaining %d were not attempted", line, total-at)
		if chained {
			for i := 0; i < at; i++ {
				if outcomes[i] == ok {
					run.Logf("  already done: %s", describe(shown[i]))
				}
			}
			leftCreated(run, p.Steps, shown, outcomes)
		}
		if multi {
			run.Status(fmt.Sprintf("cancelled after %d of %d", at, total))
		}
		return context.Canceled
	}
	for i, st := range p.Steps {
		if ctx.Err() != nil {
			return cancelled(i)
		}
		if waited, blocked := firstUnmet(st, i, func(j int) bool { return outcomes[j] == ok }); blocked {
			outcomes[i] = skip
			skipped++
			run.Logf("↷ skipped %s — %s did not succeed", describe(shown[i]), describe(shown[waited]))
			if multi {
				run.Steps(i+1, total)
			}
			continue
		}
		if st.IDFrom >= 0 && st.IDFrom < i && p.Steps[st.IDFrom].ProducesID {
			st.ID = produced[st.IDFrom]
			st.Name = shown[st.IDFrom].Name
			shown[i] = st
		}
		if multi {
			run.Status(fmt.Sprintf("%s %s", st.Op, st.Name))
		}
		var id string
		var err error
		if st.Op == OpPruneBuildCache {
			err = pruneBuildCache(ctx, eng, p.BuildCache, run)
		} else {
			id, err = executeStep(ctx, eng, st, run)
		}
		switch {
		case err == nil:
			outcomes[i] = ok
			succeeded++
			freed += st.Size
			if st.ProducesID {
				produced[i] = id
				shown[i] = createdStep(ctx, eng, st, id)
			}
			run.Logf("✓ %s %s", describe(shown[i]), "done")
		case errors.Is(err, context.Canceled):
			outcomes[i] = bad
			run.Logf("interrupted during: %s (the daemon may still complete it)", describe(shown[i]))
			return cancelled(i)
		default:
			outcomes[i] = bad
			failed++
			lastErr = err
			run.Logf("✗ %s — %s", describe(shown[i]), daemonMessage(err))
			if st.FailNote != "" {
				run.Logf("  %s", st.FailNote)
			}
		}
		if multi {
			run.Steps(i+1, total)
		}
	}
	if p.TotalSize > 0 && freed > 0 {
		run.Logf("reclaimed about %s", humanBytes(freed))
	}
	if chained {
		leftCreated(run, p.Steps, shown, outcomes)
		run.Logf("%d succeeded, %d failed, %d skipped", succeeded, failed, skipped)
	}
	switch {
	case failed == 0:
		if p.SuccessNote != "" && skipped == 0 {
			run.Logf("%s", p.SuccessNote)
		}
		if multi {
			run.Status(fmt.Sprintf("%d done", total))
		}
		return nil
	case !multi:
		return errors.New(daemonMessage(lastErr))
	case skipped > 0:
		run.Status(fmt.Sprintf("%d of %d failed, %d skipped", failed, total, skipped))
		return fmt.Errorf("%d of %d failed, %d skipped (last: %s)", failed, total, skipped, daemonMessage(lastErr))
	default:
		run.Status(fmt.Sprintf("%d of %d failed", failed, total))
		return fmt.Errorf("%d of %d failed (last: %s)", failed, total, daemonMessage(lastErr))
	}
}

// firstUnmet returns the first prerequisite of step i that did not succeed.
// Only earlier steps can be prerequisites; a forward or self reference is a
// plan error and blocks the step rather than being ignored.
func firstUnmet(st Step, i int, succeeded func(int) bool) (int, bool) {
	for _, d := range st.DependsOn {
		if d < 0 || d >= i {
			return max(0, min(d, i-1)), true
		}
		if !succeeded(d) {
			return d, true
		}
	}
	return 0, false
}

// createdStep is a create step as the log should name it once the daemon has
// answered: by the name the container now has (the daemon's choice when the
// form left it empty) and the short ID it returned.
func createdStep(ctx context.Context, eng engine.Engine, st Step, id string) Step {
	st.ID = id
	st.Detail = strings.TrimSpace(st.Detail + " " + engine.ShortID(id))
	if st.Create != nil && st.Create.Name != "" {
		st.Name = st.Create.Name
		return st
	}
	if st.Clone != nil && st.Clone.Name != "" {
		st.Name = st.Clone.Name
		return st
	}
	if d, err := eng.InspectContainer(ctx, id); err == nil && d.Name != "" {
		st.Name = strings.TrimPrefix(d.Name, "/")
	} else {
		st.Name = engine.ShortID(id)
	}
	return st
}

// leftCreated says so when a container was created and the step that was to
// start it did not: it exists, in the created state, and is the operator's
// to start or remove.
func leftCreated(run *jobs.Run, steps, shown []Step, outcomes []outcome) {
	for i, st := range steps {
		if !st.ProducesID || outcomes[i] != ok {
			continue
		}
		for j := i + 1; j < len(steps); j++ {
			if steps[j].IDFrom == i && outcomes[j] != ok {
				run.Logf("  %s was created but not started: it exists in the created state (%s)", shown[i].Name, shown[i].Detail)
				break
			}
		}
	}
}

// outcome is what became of one step.
type outcome int

const (
	notRun outcome = iota
	ok
	bad
	skip
)

func describe(st Step) string {
	switch st.Op {
	case OpCreateContainer:
		// Never the configuration: the environment's values stay in the
		// form (D29). The image, and the short ID once there is one.
		s := "create container " + st.Name
		if st.Create != nil {
			s += " from " + st.Create.Image
		} else if st.Clone != nil {
			s += " from " + escape.Name(st.Clone.Image)
		}
		if st.ID != "" {
			s += " (" + engine.ShortID(st.ID) + ")"
		}
		return s
	case OpCopyIn:
		// Paths and the container only: never a byte of what is copied.
		if st.Copy != nil {
			return fmt.Sprintf("copy %s into %s:%s", escape.Name(st.Copy.Source), escape.Name(st.Name), escape.Name(st.Copy.Dest))
		}
		return "copy into " + st.Name
	case OpRename:
		return fmt.Sprintf("rename container %s to %s", escape.Name(st.Name), escape.Name(st.Rename))
	case OpTagImage:
		return fmt.Sprintf("tag image %s as %s", engine.ShortID(st.ID), escape.Name(st.Name))
	case OpCopyImage:
		return fmt.Sprintf("copy image %s to another daemon", escape.Name(st.Name))
	case OpConnect:
		return fmt.Sprintf("connect %s to %s", st.OtherName, st.Name)
	case OpDisconnect:
		return fmt.Sprintf("disconnect %s from %s", st.OtherName, st.Name)
	}
	s := fmt.Sprintf("%s %s %s", st.Op, st.Kind, st.Name)
	if st.Detail != "" && st.Kind != "container" && st.Kind != "volume" {
		s += " (" + st.Detail + ")"
	}
	return s
}

// daemonMessage returns the daemon's own words, without the "remove
// container:" style prefix the engine layer adds for logs.
func daemonMessage(err error) string {
	var e *engine.Error
	if errors.As(err, &e) {
		return e.Msg
	}
	return err.Error()
}

// executeStep sends one step's request. The identifier is returned only by a
// step that creates an object addressed by a later step (ProducesID).
func executeStep(ctx context.Context, eng engine.Engine, st Step, run *jobs.Run) (string, error) {
	if st.Op == OpCreateContainer {
		if st.Clone != nil {
			return createClone(ctx, eng, st)
		}
		if st.Create == nil {
			return "", errors.New("internal error: a create step without its configuration")
		}
		return eng.CreateContainer(ctx, CreateOptions(*st.Create))
	}
	if st.Op == OpCopyIn {
		return "", copyIn(ctx, eng, st, run)
	}
	return "", executeRequest(ctx, eng, st, run)
}

// CreateOptions maps the form's specification to the engine's options.
func CreateOptions(sp ContainerCreateSpec) engine.ContainerCreateOptions {
	o := engine.ContainerCreateOptions{
		Image:   sp.Image,
		Name:    sp.Name,
		Env:     append([]string(nil), sp.Env...),
		Cmd:     append([]string(nil), sp.Command...),
		Restart: engine.RestartPolicy{Name: sp.Restart.Name, MaximumRetryCount: sp.Restart.MaximumRetryCount},
	}
	if len(sp.Command) == 0 {
		o.Cmd = nil
	}
	if len(sp.Env) == 0 {
		o.Env = nil
	}
	for _, p := range sp.Ports {
		o.Ports = append(o.Ports, engine.PortBinding{HostIP: p.HostIP, HostPort: p.HostPort, ContainerPort: p.ContainerPort, Proto: p.Proto})
	}
	for _, m := range sp.Mounts {
		o.Mounts = append(o.Mounts, engine.MountOption{Type: m.Type, Source: m.Source, Target: m.Destination, ReadOnly: m.ReadOnly})
	}
	return o
}

// createClone sends a re-create's create, by reference, once the reference
// has been checked again to name the build that was shown: the stop and the
// rename before it took time. A daemon's refusal is passed on without any
// environment value it might quote.
func createClone(ctx context.Context, eng engine.Engine, st Step) (string, error) {
	if st.Pin != "" {
		d, err := eng.InspectImage(ctx, st.Clone.Image)
		if err != nil {
			return "", fmt.Errorf("%s could not be checked before the create: %s", escape.Name(st.Clone.Image), redactEnvValues(daemonMessage(err), st.Clone))
		}
		if trimSHA(d.ID) != trimSHA(st.Pin) {
			return "", fmt.Errorf("%s now names %s, not the build %s that was shown; nothing was created",
				escape.Name(st.Clone.Image), engine.ShortID(d.ID), engine.ShortID(st.Pin))
		}
	}
	id, err := eng.CreateContainer(ctx, CloneOptions(*st.Clone))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		return "", &engine.Error{Kind: engine.KindOf(err), Op: "create container", Msg: redactEnvValues(daemonMessage(err), st.Clone)}
	}
	return id, nil
}

// checkPlanPin is the plan-level pin: before the first step, the reference
// must still name the image the plan was built for. Otherwise nothing at
// all is sent — not the stop that would precede a create from a build
// nobody was shown.
func checkPlanPin(ctx context.Context, eng engine.Engine, p Plan, run *jobs.Run) error {
	if p.PinRef == "" {
		return nil
	}
	ref := escape.Name(p.PinRef)
	d, err := eng.InspectImage(ctx, p.PinRef)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		msg := daemonMessage(err)
		if engine.IsNotFound(err) {
			msg = ref + " no longer names an image on this daemon"
		}
		run.Logf("✗ before the first step: %s; nothing was sent", msg)
		run.Status("not started: the image could not be checked")
		return fmt.Errorf("%s; nothing was stopped, renamed or created", msg)
	}
	if trimSHA(d.ID) != trimSHA(p.PinID) {
		msg := fmt.Sprintf("the tag moved: %s now names %s, not the build %s that was shown", ref, engine.ShortID(d.ID), engine.ShortID(p.PinID))
		run.Logf("✗ before the first step: %s; nothing was sent", msg)
		run.Status("not started: the tag moved")
		return fmt.Errorf("%s; nothing was stopped, renamed or created", msg)
	}
	return nil
}

func executeRequest(ctx context.Context, eng engine.Engine, st Step, run *jobs.Run) error {
	switch st.Op {
	case OpRename:
		if st.Rename == "" {
			return errors.New("internal error: a rename step without its new name")
		}
		return eng.RenameContainer(ctx, st.ID, st.Rename)
	case OpStart:
		return eng.StartContainer(ctx, st.ID)
	case OpStop:
		return eng.StopContainer(ctx, st.ID)
	case OpRestart:
		return eng.RestartContainer(ctx, st.ID)
	case OpPause:
		return eng.PauseContainer(ctx, st.ID)
	case OpUnpause:
		return eng.UnpauseContainer(ctx, st.ID)
	case OpKill:
		return eng.KillContainer(ctx, st.ID)
	case OpRemoveContainer:
		return eng.RemoveContainer(ctx, st.ID)
	case OpPullImage:
		return eng.PullImage(ctx, st.ID, func(pr engine.PullProgress) {
			status := pr.Status
			if pr.Layer != "" {
				status = pr.Layer + ": " + pr.Status
			}
			run.Status(status)
			if pr.Total > 0 {
				run.Fraction(float64(pr.Current) / float64(pr.Total))
			}
		})
	case OpRemoveImage:
		if err := checkImagePin(ctx, eng, st); err != nil {
			return err
		}
		res, err := eng.RemoveImage(ctx, st.ID)
		for _, r := range res {
			if r.Untagged != "" {
				run.Logf("  untagged %s", r.Untagged)
			}
			if r.Deleted != "" {
				run.Logf("  deleted  %s", engine.ShortID(r.Deleted))
			}
		}
		return err
	case OpTagImage:
		return executeTag(ctx, eng, st, run)
	case OpCreateVolume:
		_, err := eng.CreateVolume(ctx, st.ID)
		return err
	case OpRemoveVolume:
		if st.Pin != "" {
			v, err := eng.InspectVolume(ctx, st.ID)
			if err != nil {
				return err
			}
			if v.CreatedAt != st.Pin {
				return fmt.Errorf("a different volume now has the name %s (created %s, the one shown was created %s); nothing was removed",
					st.ID, v.CreatedAt, st.Pin)
			}
		}
		return eng.RemoveVolume(ctx, st.ID)
	case OpCreateNetwork:
		id, err := eng.CreateNetwork(ctx, st.ID)
		if err == nil {
			run.Logf("  network id %s", engine.ShortID(id))
		}
		return err
	case OpRemoveNetwork:
		return eng.RemoveNetwork(ctx, st.ID)
	case OpConnect:
		return eng.ConnectNetwork(ctx, st.ID, st.Other)
	case OpDisconnect:
		return eng.DisconnectNetwork(ctx, st.ID, st.Other)
	}
	return fmt.Errorf("internal error: unknown operation %d", st.Op)
}

// checkImagePin re-checks, immediately before the request, the facts an image
// step addressed by reference was confirmed on. A reference is a name that
// can be moved to another image between the dialog and the request; the
// daemon would then happily act on an image nobody was shown.
func checkImagePin(ctx context.Context, eng engine.Engine, st Step) error {
	if st.Pin == "" {
		return nil
	}
	d, err := eng.InspectImage(ctx, st.ID)
	if err != nil {
		return err
	}
	if trimSHA(d.ID) != trimSHA(st.Pin) {
		return fmt.Errorf("the tag moved: %s now refers to a different image (%s) than the one that was shown (%s); nothing was removed",
			escape.Name(st.ID), escape.Name(engine.ShortID(d.ID)), escape.Name(engine.ShortID(st.Pin)))
	}
	if st.KeepsData && len(d.Refs()) <= 1 {
		return fmt.Errorf("%s has become the image's only reference: removing it now would delete the image, which is not what was confirmed", escape.Name(st.ID))
	}
	if st.KeepsTag {
		others := 0
		for _, r := range d.Refs() {
			if r != st.ID && !strings.Contains(r, "@") {
				others++
			}
		}
		if others == 0 {
			return fmt.Errorf("%s has become the image's only tag: removing it now would delete the image, which is not what was confirmed", escape.Name(st.ID))
		}
	}
	if st.MustBeUnused {
		cs, err := eng.Containers(ctx)
		if err != nil {
			return err
		}
		for _, c := range cs {
			if trimSHA(c.ImageID) == trimSHA(d.ID) {
				return fmt.Errorf("the image is now used by container %s (%s); left alone", c.Name, c.State)
			}
		}
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// hasCopyStep reports whether any step is an image copy.
func hasCopyStep(p Plan) bool {
	for _, st := range p.Steps {
		if st.Op == OpCopyImage {
			return true
		}
	}
	return false
}
