package plan

import (
	"context"
	"errors"
	"fmt"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// Execute sends exactly the plan's steps to the daemon, in order, one
// request per step, never forced. It is the only place in the desk where a
// plan turns into requests.
//
// A step that fails is recorded and the remaining steps still run; the job
// then ends as failed with a summary. Cancellation is honoured between
// steps: what was already done is reported, nothing further is sent.
func Execute(ctx context.Context, eng engine.Engine, p Plan, run *jobs.Run) error {
	if !p.Runnable() {
		return errors.New("internal error: tried to execute a plan that is not runnable")
	}
	total := len(p.Steps)
	multi := total > 1
	if multi {
		run.Steps(0, total)
	}
	var failed, succeeded int
	var lastErr error
	var freed int64
	cancelled := func(at int) error {
		line := fmt.Sprintf("cancelled: %d succeeded", succeeded)
		if failed > 0 {
			line += fmt.Sprintf(", %d failed", failed)
		}
		run.Logf("%s, the remaining %d were not attempted", line, total-at)
		if multi {
			run.Status(fmt.Sprintf("cancelled after %d of %d", at, total))
		}
		return context.Canceled
	}
	for i, st := range p.Steps {
		if ctx.Err() != nil {
			return cancelled(i)
		}
		if multi {
			run.Status(fmt.Sprintf("%s %s", st.Op, st.Name))
		}
		err := executeStep(ctx, eng, st, run)
		switch {
		case err == nil:
			succeeded++
			freed += st.Size
			run.Logf("✓ %s %s", describe(st), "done")
		case errors.Is(err, context.Canceled):
			run.Logf("interrupted during: %s (the daemon may still complete it)", describe(st))
			return cancelled(i)
		default:
			failed++
			lastErr = err
			run.Logf("✗ %s — %s", describe(st), daemonMessage(err))
		}
		if multi {
			run.Steps(i+1, total)
		}
	}
	if p.TotalSize > 0 && freed > 0 {
		run.Logf("reclaimed about %s", humanBytes(freed))
	}
	switch {
	case failed == 0:
		if multi {
			run.Status(fmt.Sprintf("%d done", total))
		}
		return nil
	case !multi:
		return errors.New(daemonMessage(lastErr))
	default:
		run.Status(fmt.Sprintf("%d of %d failed", failed, total))
		return fmt.Errorf("%d of %d failed (last: %s)", failed, total, daemonMessage(lastErr))
	}
}

func describe(st Step) string {
	switch st.Op {
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

func executeStep(ctx context.Context, eng engine.Engine, st Step, run *jobs.Run) error {
	switch st.Op {
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
		return fmt.Errorf("%s now refers to a different image (%s) than the one that was shown (%s); nothing was removed",
			st.ID, engine.ShortID(d.ID), engine.ShortID(st.Pin))
	}
	if st.KeepsData && len(d.Refs()) <= 1 {
		return fmt.Errorf("%s has become the image's only reference: removing it now would delete the image, which is not what was confirmed", st.ID)
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
