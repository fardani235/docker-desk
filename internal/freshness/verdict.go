// Package freshness answers one question about a local image: does the tag
// it carries name a different build upstream today, and if so what
// differs. It compares, caches and decides the daemon fallback; the wire is
// internal/registry. It never ranks two builds: it reports their times, as
// their producers wrote them, and the layers they share. See
// docs/DECISIONS.md, round eleven (U3, U8, U9, U10).
package freshness

import (
	"fmt"
	"time"
)

// State is a verdict's kind. There is no "stale" and no "newer".
type State int

const (
	NotApplicable State = iota
	Current
	Moved
	Gone
	Unknown
)

// Word is the state as the list column shows it.
func (s State) Word() string {
	switch s {
	case Current:
		return "current"
	case Moved:
		return "moved"
	case Gone:
		return "gone"
	case Unknown:
		return "unknown"
	}
	return "—"
}

// Source says who obtained an answer.
type Source int

const (
	// FromRegistry: the desk asked the registry itself.
	FromRegistry Source = iota
	// FromDaemon: the daemon's distribution endpoint answered.
	FromDaemon
)

// Verdict is one check's answer. Every field is the desk's own text or a
// value it parsed; response text never appears unsanitised.
type Verdict struct {
	State  State
	Source Source
	// Ref is the reference checked, as the operator types it; Host is the
	// host asked; Credential names where a credential came from, or "".
	Ref, Host, Credential string

	LocalDigest, RemoteDigest string
	// LocalCreated and RemoteCreated are the producers' claims; zero when
	// absent or the epoch.
	LocalCreated, RemoteCreated time.Time
	// RemoteCreatedMissing / RemoteCreatedEpoch say why RemoteCreated is
	// unusable when a config was read.
	RemoteCreatedMissing, RemoteCreatedEpoch bool

	// Layers: LayersKnown when both diff ID lists were compared.
	LayersKnown               bool
	Shared, Local, RemoteLyrs int

	// SameBuild: the tag names another manifest whose build for this
	// platform is the local one (another platform was rebuilt).
	SameBuild bool
	// PlatformMissing: the tag no longer publishes Platform.
	PlatformMissing bool
	Platform        string

	// Reason says why there is no answer (Unknown, NotApplicable), and
	// DaemonReason, when the fallback was tried too, why it gave none.
	Reason, DaemonReason string
	// Quota: a registry refused for quota; QuotaReset is when it allows.
	Quota      bool
	QuotaReset time.Time

	At time.Time
}

// usable reports whether a build time can be used for a direction.
func usable(t time.Time) bool { return !t.IsZero() && t.Unix() > 0 }

// Sentences is the verdict as the Upstream tab says it, one sentence a line.
func (v Verdict) Sentences() []string {
	var out []string
	via := ""
	if v.Source == FromDaemon {
		via = " (via the daemon)"
	}
	switch v.State {
	case NotApplicable:
		return []string{"Not applicable: " + v.Reason}
	case Current:
		if v.SameBuild {
			out = append(out, "Current"+via+": the tag names another manifest, but its "+v.Platform+" build is this image's.")
		} else {
			out = append(out, "Current"+via+": the tag still names this image's digest.")
		}
		return out
	case Gone:
		return []string{"Gone" + via + ": " + v.Host + " has no manifest for " + v.Ref + "."}
	case Unknown:
		out = append(out, "Unknown: "+v.Reason)
		if v.DaemonReason != "" {
			out[0] = "Unknown: the desk: " + v.Reason + "; the daemon: " + v.DaemonReason
		}
		if v.Quota && !v.QuotaReset.IsZero() {
			out = append(out, "The quota resets at "+v.QuotaReset.Local().Format("15:04")+".")
		}
		return out
	}
	out = append(out, "Moved"+via+": the tag names a different build than this image.")
	if v.Source == FromDaemon {
		out = append(out, "The daemon reports digests only: the upstream build time and the layer comparison are unknown.")
		return out
	}
	out = append(out, "Local build:    "+stamp(v.LocalCreated, false, false))
	out = append(out, "Upstream build: "+stamp(v.RemoteCreated, v.RemoteCreatedMissing, v.RemoteCreatedEpoch))
	switch {
	case !usable(v.LocalCreated) || !usable(v.RemoteCreated):
		out = append(out, "The build time is unknown, so no direction is inferred.")
	case v.RemoteCreated.After(v.LocalCreated):
		out = append(out, "The upstream build is later, by "+span(v.RemoteCreated.Sub(v.LocalCreated))+".")
	case v.RemoteCreated.Before(v.LocalCreated):
		out = append(out, "The upstream build is earlier, by "+span(v.LocalCreated.Sub(v.RemoteCreated))+".")
	default:
		out = append(out, "Both builds carry the same time.")
	}
	out = append(out, "The times are what each build's producer wrote in its config, not a fact.")
	if v.LayersKnown {
		out = append(out, fmt.Sprintf("First %d of %d layers shared; %d upstream layer%s differ%s (local has %d).",
			v.Shared, v.RemoteLyrs, v.RemoteLyrs-v.Shared, plural(v.RemoteLyrs-v.Shared), verbS(v.RemoteLyrs-v.Shared), v.Local))
	}
	return out
}

// Delta is the list column's "+4d": the upstream build's lead, only when
// both times were obtained and it is later.
func (v Verdict) Delta() string {
	if v.State != Moved || !usable(v.LocalCreated) || !usable(v.RemoteCreated) || !v.RemoteCreated.After(v.LocalCreated) {
		return ""
	}
	return "+" + span(v.RemoteCreated.Sub(v.LocalCreated))
}

func stamp(t time.Time, missing, epoch bool) string {
	switch {
	case missing:
		return "unknown (the config carries no created time)"
	case epoch || (!t.IsZero() && t.Unix() <= 0):
		return "unknown (the config carries the epoch, as reproducible builds do)"
	case t.IsZero():
		return "unknown"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC") + " (config created)"
}

func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func verbS(n int) string {
	if n == 1 {
		return "s"
	}
	return ""
}

// sharedPrefix counts the leading diff IDs two images have in common.
func sharedPrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}
