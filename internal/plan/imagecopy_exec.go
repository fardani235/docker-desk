package plan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// copyCheckTimeout bounds each inspect a copy makes around its stream: the
// pin, the target's state and the verification are small answers. The
// stream itself has no deadline; it is cancellable.
const copyCheckTimeout = 30 * time.Second

// copyStream is the source's archive on its way to the target: it counts
// what the target's request has read, remembers a failure of the source,
// and stops handing out bytes once the job is cancelled. It never holds
// more than the caller's buffer.
//
// Every piece goes through the archive guard before it is handed on
// (imagecopy_guard.go): a piece the guard refuses is never passed to the
// target, and the read fails instead, which breaks the target's request
// off before the archive is complete.
type copyStream struct {
	ctx   context.Context
	r     io.Reader
	guard *archiveGuard
	n     atomic.Int64 // bytes handed on

	mu      sync.Mutex
	err     error // the source's first failure
	refused error // the guard's refusal of what the source sent
}

// errArchiveRefused is what the target's request is told when the guard
// stops the stream; the reason is kept in copyStream.refused.
var errArchiveRefused = errors.New("the archive was refused before it was sent whole")

func (c *copyStream) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	if c.guardErr() != nil {
		return 0, errArchiveRefused
	}
	n, err := c.r.Read(p)
	refuse := func(why error) (int, error) {
		c.mu.Lock()
		if c.refused == nil {
			c.refused = why
		}
		c.mu.Unlock()
		return 0, errArchiveRefused
	}
	if n > 0 {
		if why := c.guard.feed(p[:n]); why != nil {
			return refuse(why)
		}
	}
	switch {
	case err == io.EOF:
		// The source's stream ended: the guard says whether the archive
		// did. The last piece is held back until it has.
		if why := c.guard.finish(); why != nil {
			return refuse(why)
		}
	case err != nil:
		c.mu.Lock()
		if c.err == nil {
			c.err = err
		}
		c.mu.Unlock()
	}
	c.n.Add(int64(n))
	return n, err
}

func (c *copyStream) sourceErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *copyStream) guardErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refused
}

// copyInspect is one bounded ImageFacts read.
func copyInspect(ctx context.Context, eng engine.Engine, name string) (engine.ImageFacts, error) {
	ictx, cancel := context.WithTimeout(ctx, copyCheckTimeout)
	defer cancel()
	return eng.ImageFacts(ictx, name)
}

// ExecuteCopy runs a copy plan between two daemons: src is only read, dst
// receives one load. It is the only place in the desk where an image
// crosses from one daemon to another.
//
// In order: read-only is refused before any request; the source reference
// is inspected and must still name the pinned image, as the platform shown;
// the target is inspected and must hold under the copy's name what the
// dialog showed; then the archive SaveImage returns is the body LoadImage
// sends, through a counting reader and the archive guard — no file, no
// buffer of the whole, and no reference in it but the confirmed one; and
// finally the target is inspected and the image it holds must be the
// source's build (SameBuild). Cancelling ctx closes both
// streams. Nothing is claimed about the target unless that last inspect
// agreed.
//
// Every string of either daemon is escaped before it is logged, and the
// error returned is plain text the Jobs pane may draw.
func ExecuteCopy(ctx context.Context, src, dst engine.Engine, p Plan, run *jobs.Run) error {
	c, err := checkCopyPlan(p)
	if err != nil {
		return err
	}
	if src == nil || dst == nil || src == dst {
		return errors.New("internal error: a copy needs the engines of two different daemons; nothing was sent")
	}
	// Read-only: no request at all, to either daemon.
	if engine.IsReadOnly(dst) || engine.IsReadOnly(src) {
		run.Logf("✗ %s", engine.ErrReadOnly.Error())
		return engine.ErrReadOnly
	}

	by := c.By()
	shownBy := escape.Name(by)
	short := engine.ShortID(c.ImageID)
	from, to := daemonWords(c.Source), daemonWords(c.Target)
	plat := c.Platform
	platWords := escape.Name(plat.String())
	hasPlatform := plat.OS != "" && plat.Architecture != ""
	if !hasPlatform {
		platWords = "the daemon's own (the source reported none)"
	}
	if c.Ref != "" {
		run.Logf("image %s, by its tag, pinned to %s", shownBy, short)
	} else {
		run.Logf("image %s, untagged, by its ID", short)
	}
	run.Logf("platform %s", platWords)
	run.Logf("from %s", from)
	run.Logf("to   %s", to)

	notSent := func(status, msg string) error {
		run.Logf("✗ before the stream: %s; nothing was sent to the target", msg)
		run.Logf("%s", CopySourceNote)
		run.Status("not started: " + status)
		return fmt.Errorf("%s; nothing was copied", msg)
	}
	cancelledBefore := func() error {
		run.Logf("cancelled before the stream: nothing was sent to the target")
		run.Logf("%s", CopySourceNote)
		return context.Canceled
	}

	// ---- the pin: what the source names now ----------------------------
	run.Status("checking " + shownBy + " on the source")
	want, err := copyInspect(ctx, src, by)
	switch {
	case err != nil && ctx.Err() != nil:
		return cancelledBefore()
	case err != nil && engine.IsNotFound(err):
		return notSent("the image is gone", fmt.Sprintf("%s no longer exists on the source %s", shownBy, from))
	case err != nil:
		return notSent("the source could not be read", fmt.Sprintf("the source %s could not be read: %s", from, escape.Name(daemonMessage(err))))
	case trimSHA(want.ID) != trimSHA(c.ImageID):
		return notSent("the tag moved", fmt.Sprintf("the tag moved: %s now names %s on the source, not the image %s that was shown",
			shownBy, escape.Name(engine.ShortID(want.ID)), short))
	case want.Platform() != plat:
		return notSent("the platform changed", fmt.Sprintf("%s is now inspected as %s on the source, not the %s that was shown",
			shownBy, escape.Name(want.Platform().String()), platWords))
	}

	// ---- the target: what it holds under the copy's name now -----------
	run.Status("checking the target")
	shownOwner := "no image"
	if c.TargetOwner != "" {
		shownOwner = "image " + engine.ShortID(c.TargetOwner)
	}
	held, err := copyInspect(ctx, dst, by)
	switch {
	case err != nil && ctx.Err() != nil:
		return cancelledBefore()
	case err != nil && engine.IsNotFound(err):
		if c.TargetOwner != "" {
			return notSent("the target changed", fmt.Sprintf("the target changed: %s named %s on %s when the dialog was read and names no image now",
				shownBy, shownOwner, to))
		}
	case err != nil:
		return notSent("the target could not be read", fmt.Sprintf("the target %s could not be read: %s", to, escape.Name(daemonMessage(err))))
	case held.ID == "":
		return notSent("the target could not be read", fmt.Sprintf("the target %s answered for %s without an image ID", to, shownBy))
	case trimSHA(held.ID) != trimSHA(c.TargetOwner):
		return notSent("the target changed", fmt.Sprintf("the target changed: %s named %s on %s when the dialog was read and names image %s now",
			shownBy, shownOwner, to, escape.Name(engine.ShortID(held.ID))))
	case SameBuild(want, held) == "":
		// The dialog blocks this; only a hand-made plan gets here.
		return notSent("already present", fmt.Sprintf("%s already holds this build as %s (image %s)", to, shownBy, escape.Name(engine.ShortID(held.ID))))
	}
	if c.TargetOwner != "" {
		run.Logf("the target's %s names image %s now: the load moves it to the copied image", shownBy, engine.ShortID(c.TargetOwner))
	}

	// ---- the stream ----------------------------------------------------
	// One context for both requests: ending it closes the source's answer
	// and the target's request, whichever way this function is left.
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	run.Status("asking the source for the archive")
	rc, err := src.SaveImage(sctx, by, plat)
	if err != nil {
		if ctx.Err() != nil {
			return cancelledBefore()
		}
		return notSent("the source refused", fmt.Sprintf("the source %s did not give the archive: %s", from, escape.Name(daemonMessage(err))))
	}
	defer rc.Close()
	// The archive may name the confirmed reference and nothing else.
	guard := newArchiveGuard(c.Ref)
	defer guard.finish() // releases the guard's reader on every way out
	stream := &copyStream{ctx: sctx, r: rc, guard: guard}

	var mu sync.Mutex // the status line has two writers: the bytes and the target
	var targetSays string
	var last time.Time
	// ended is set once the load has returned: the transport may still be
	// letting go of the request body, and a late read must not put
	// "streamed …" over the job's final status.
	ended := false
	status := func(force bool) {
		mu.Lock()
		defer mu.Unlock()
		if ended {
			return
		}
		if now := time.Now(); force || now.Sub(last) >= 100*time.Millisecond {
			last = now
			s := "streamed " + humanBytes(stream.n.Load())
			if targetSays != "" {
				s += " · target: " + targetSays
			}
			run.Status(s)
		}
	}
	// What the target said, for the log: bounded, and only its plain lines
	// (a progress message per layer per tick would bury everything else).
	var said []string
	const maxSaid = 32
	run.Logf("streaming: GET /images/get on the source → POST /images/load on the target")
	res, err := dst.LoadImage(sctx, &progressReader{stream: stream, tick: func() { status(false) }}, plat, func(lp engine.LoadProgress) {
		mu.Lock()
		switch {
		case lp.Text != "":
			targetSays = escape.Name(lp.Text)
			if len(said) < maxSaid {
				said = append(said, targetSays)
			}
		case lp.Status != "":
			targetSays = escape.Name(lp.Status)
			if lp.ID != "" {
				targetSays += " " + escape.Name(lp.ID)
			}
		}
		mu.Unlock()
		status(false)
	})
	// Both streams end here, on every path.
	srcErr, refused := stream.sourceErr(), stream.guardErr()
	mu.Lock()
	ended = true
	lines := append([]string(nil), said...)
	mu.Unlock()
	stop()
	_ = rc.Close()
	n := stream.n.Load()
	for _, l := range lines {
		run.Logf("  target: %s", l)
	}
	unclaimed := "no image and no moved tag is claimed on the target; layers it already accepted may remain in its store"
	switch {
	case ctx.Err() != nil:
		run.Logf("%s", CopyCancelledNote)
		run.Logf("%s were streamed before the cancel", humanBytes(n))
		run.Logf("%s", CopySourceNote)
		run.Status("cancelled after " + humanBytes(n))
		return context.Canceled
	case refused != nil:
		// What the source sent is not the one image, under the one name,
		// that was confirmed. The guard's words are already escaped.
		run.Logf("✗ the archive the source %s sent is not what was confirmed: %s", from, refused.Error())
		run.Logf("the stream was closed after %s, before the archive was complete: the target was not given an archive it can load. %s",
			humanBytes(n), strings.ToUpper(unclaimed[:1])+unclaimed[1:])
		run.Logf("%s", CopySourceNote)
		run.Status("failed: the source's archive was refused after " + humanBytes(n))
		return fmt.Errorf("the source %s sent an archive that is not what was confirmed: %s. The stream was closed before the archive was complete", from, refused.Error())
	case srcErr != nil:
		// The archive stopped arriving: the source's failure, whatever the
		// target then said about the request it lost.
		msg := escape.Name(daemonMessage(srcErr))
		run.Logf("✗ the source %s stopped sending the archive after %s — %s", from, humanBytes(n), msg)
		run.Logf("the target's request was closed: %s", unclaimed)
		run.Logf("%s", CopySourceNote)
		run.Status("failed: the source's stream broke after " + humanBytes(n))
		return fmt.Errorf("the source %s stopped sending the archive after %s: %s", from, humanBytes(n), msg)
	case err != nil:
		msg := escape.Name(daemonMessage(err))
		what := "refused or failed the load"
		if engine.IsUnavailable(err) {
			what = "became unreachable during the load"
		}
		run.Logf("✗ the target %s %s after %s were streamed — %s", to, what, humanBytes(n), msg)
		run.Logf("the source's stream was closed: %s", unclaimed)
		run.Logf("%s", CopySourceNote)
		run.Status("failed: the target " + what + " after " + humanBytes(n))
		return fmt.Errorf("the target %s %s after %s were streamed: %s", to, what, humanBytes(n), msg)
	}
	run.Logf("the target's load ended after %s (%d bytes) were streamed", humanBytes(n), n)

	// What the target says it loaded: the confirmed reference, and no
	// other. The guard read the archive's names as it passed; this is the
	// target's own account of what it did with them.
	var extra []string
	for _, ref := range res.Refs {
		if !guard.allowed(ref) && len(extra) < 8 {
			extra = append(extra, escape.Name(clipName(ref)))
		}
	}
	if len(extra) > 0 {
		msg := fmt.Sprintf("the target %s says it loaded %s, which the copy was not confirmed for: references there may have been created or moved. Inspect the target",
			to, strings.Join(extra, ", "))
		run.Logf("✗ after the load: %s", msg)
		run.Logf("%s", CopySourceNote)
		run.Status("failed: the target loaded other references")
		return errors.New(msg)
	}

	// ---- the verification: what the target holds now -------------------
	run.Status("verifying the image on the target")
	candidates := []string{by}
	if c.Ref == "" {
		// There is no tag to ask by, and a store may give the build
		// another ID than the source's. Asked for, in order: the IDs in
		// the target's own "Loaded image ID" lines; the IDs the archive
		// itself carries — the manifest a containerd store names an image
		// by, the config a classic store does; then the source's. Only
		// strings in the ID form are sent, and whatever answers is still
		// held to SameBuild.
		candidates = nil
		add := func(id string) {
			if !imageIDForm.MatchString(id) || len(candidates) >= 12 {
				return
			}
			for _, have := range candidates {
				if have == id {
					return
				}
			}
			candidates = append(candidates, id)
		}
		for _, id := range res.IDs {
			add(id)
		}
		for _, id := range guard.candidates() {
			add(id)
		}
		add(by)
	}
	var got engine.ImageFacts
	var verr error
	found := false
	for _, name := range candidates {
		got, verr = copyInspect(ctx, dst, name)
		if verr == nil {
			found = true
			break
		}
		if !engine.IsNotFound(verr) {
			break
		}
	}
	name := shownBy
	if c.Ref == "" {
		name = "the image"
	}
	unverified := func(status, msg string) error {
		run.Logf("✗ after the load: %s", msg)
		run.Logf("%s", CopySourceNote)
		run.Status("failed: " + status)
		return errors.New(msg)
	}
	switch {
	case !found && ctx.Err() != nil:
		run.Logf("cancelled after the target's load ended and before it was verified: whether %s is on the target as shown is not known", name)
		run.Logf("%s", CopySourceNote)
		return context.Canceled
	case !found && engine.IsNotFound(verr):
		return unverified("the load did not produce the image",
			fmt.Sprintf("the load did not produce the image: the target %s ended its load without an error and holds no %s", to, shownBy))
	case !found:
		return unverified("the target could not be read after the load",
			fmt.Sprintf("the target %s could not be read after the load (%s): the load may have completed, and nothing is claimed — inspect the target when it answers", to, escape.Name(daemonMessage(verr))))
	}
	gotShort := escape.Name(engine.ShortID(got.ID))
	if diff := SameBuild(want, got); diff != "" {
		return unverified("the target holds another image",
			fmt.Sprintf("the target %s holds another image as %s after the load: image %s, where %s. It is not the source's image %s", to, name, gotShort, diff, short))
	}

	run.Logf("✓ copy image %s done: %s streamed, platform %s", escape.Name(p.Steps[0].Name), humanBytes(n), platWords)
	if len(want.DiffIDs) == 0 {
		// Nothing was compared that the content could be told by.
		run.Logf("  the image has no layers: only its platform and config were compared with the source's, and they are the same")
	} else {
		run.Logf("  verified on the target: the same %s, platform and config as the source", plural(len(want.DiffIDs), "layer", "layers"))
	}
	run.Logf("  source image %s on %s", short, from)
	run.Logf("  target image %s on %s", gotShort, to)
	if trimSHA(got.ID) != trimSHA(c.ImageID) {
		run.Logf("  the two stores name this build differently: that is expected, the content is the same")
	}
	switch {
	case c.Ref == "":
		run.Logf("  it is untagged on the target (t tags it there)")
	case c.TargetOwner != "":
		run.Logf("  %s moved on the target: from image %s to image %s", shownBy, engine.ShortID(c.TargetOwner), gotShort)
	default:
		run.Logf("  %s was created on the target", shownBy)
	}
	run.Logf("%s", CopySourceNote)
	run.Status(strings.TrimSpace("copied " + humanBytes(n) + " · " + platWords))
	run.Fraction(1)
	return nil
}

// progressReader reports each read of the stream.
type progressReader struct {
	stream *copyStream
	tick   func()
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.stream.Read(b)
	p.tick()
	return n, err
}
