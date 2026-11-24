package plan

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// BuildCachePreview is what the build-cache confirmation shows: the records
// the daemon's last account offered as candidates. It is display-only — the
// plan's one step is the request; nothing here is sent. Docker has no
// per-record build-cache delete, so the daemon, not this list, decides the
// final set (docs/DECISIONS.md).
type BuildCachePreview struct {
	// All is the scope: unused records of every type, internal and
	// frontend included (docker builder prune -a), instead of the default.
	All bool
	// Candidates are the records the scope would remove at the account,
	// largest first. In-use records are never among them.
	Candidates []engine.BuildCacheUsage
	// Kept counts the records in use at the account: the daemon never
	// removes them. Excluded counts unused internal/frontend records the
	// default scope leaves alone.
	Kept, Excluded int
	// Shared counts candidates whose bytes other records share.
	Shared int
	// At is when the account was read; Derived is set when an older
	// daemon's totals were summed by the client.
	At      time.Time
	Derived bool
	// Reclaimable is the daemon's own figure for the build cache, as
	// stated in the account. It is quoted, never computed by the desk.
	Reclaimable int64
}

// BuildCacheScope names a build-cache prune scope for display.
func BuildCacheScope(all bool) string {
	if all {
		return "all unused build-cache records, internal and frontend included"
	}
	return "unused build-cache records, except internal and frontend ones"
}

// buildCacheCandidate is the daemon's rule for POST /build/prune: a record
// in use is never removed; internal and frontend records only with all.
func buildCacheCandidate(r engine.BuildCacheUsage, all bool) bool {
	if r.InUse {
		return false
	}
	return all || (r.Type != "internal" && r.Type != "frontend")
}

// BuildCachePrune plans the daemon's build-cache prune from a fresh account
// read at at. Its one step is the only request: POST /build/prune with the
// scope and nothing else. The preview lists the candidates for the
// confirmation and for Execute's comparison with the daemon's report.
func BuildCachePrune(du engine.DiskUsage, all bool, at time.Time) Plan {
	scope := BuildCacheScope(all)
	pv := &BuildCachePreview{All: all, At: at, Derived: du.Derived, Reclaimable: du.BuildCache.Reclaimable}
	for _, r := range du.BuildItems {
		switch {
		case r.InUse:
			pv.Kept++
		case !buildCacheCandidate(r, all):
			pv.Excluded++
		default:
			pv.Candidates = append(pv.Candidates, r)
			if r.Shared {
				pv.Shared++
			}
		}
	}
	sort.SliceStable(pv.Candidates, func(i, j int) bool {
		if pv.Candidates[i].Size != pv.Candidates[j].Size {
			return pv.Candidates[i].Size > pv.Candidates[j].Size
		}
		return pv.Candidates[i].ID < pv.Candidates[j].ID
	})
	p := Plan{
		Title:      "Prune build cache",
		JobTitle:   "prune build cache",
		Tier:       TierTyped,
		Word:       "prune",
		Scope:      scope,
		BuildCache: pv,
	}
	if len(pv.Candidates) == 0 {
		switch {
		case !all && pv.Excluded > 0:
			p.Nothing = fmt.Sprintf("Nothing to prune in this scope: %s unused only as internal or frontend records (Tab includes them).",
				plural(pv.Excluded, "record is", "records are"))
		case pv.Kept > 0:
			p.Nothing = fmt.Sprintf("Nothing to prune: every build-cache record is in use (%d).", pv.Kept)
		default:
			p.Nothing = "Nothing to prune: the daemon's account lists no build-cache records."
		}
		return p
	}
	p.Steps = []Step{{Op: OpPruneBuildCache, Kind: "build cache", Name: scope, IDFrom: -1}}
	p.Target = Target{Kind: "cache", Name: plural(len(pv.Candidates), "record", "records")}
	p.JobTarget = p.Target.Name
	p.Effects = []string{
		"One request: the daemon's own build-cache prune (POST /build/prune), never forced and with no filter.",
		"The daemon decides the final set when it receives it: it re-checks which records are unused then.",
	}
	p.Warnings = []string{
		"The daemon may also remove cache created or released after this account, and may keep a listed record that has since come into use.",
		fmt.Sprintf("The list below is the account of %s, not a pinned set; the job reports what the daemon actually removed.", at.Format("15:04:05")),
	}
	if pv.Kept > 0 {
		p.Effects = append(p.Effects, fmt.Sprintf("%s in use at the account %s kept: the daemon never removes cache a build is using.",
			plural(pv.Kept, "record", "records"), map[bool]string{true: "is", false: "are"}[pv.Kept == 1]))
	}
	return p
}

// maxLoggedIDs bounds how many record IDs one log line group names; the
// count is always logged in full.
const maxLoggedIDs = 20

// pruneBuildCache sends the one build-cache prune and logs the daemon's
// report: what it deleted and reclaimed, what it removed that the preview
// did not list, and what the preview listed that it kept. The only figure
// claimed is the daemon's.
func pruneBuildCache(ctx context.Context, eng engine.Engine, pv *BuildCachePreview, run *jobs.Run) error {
	if pv == nil {
		return fmt.Errorf("internal error: a build-cache prune without its preview")
	}
	rep, err := eng.BuildCachePrune(ctx, pv.All)
	if err != nil {
		return err
	}
	run.Logf("the daemon deleted %s and reported %s reclaimed", plural(len(rep.Deleted), "build-cache record", "build-cache records"), humanBytes(rep.Reclaimed))
	listed := make(map[string]bool, len(pv.Candidates))
	for _, c := range pv.Candidates {
		listed[c.ID] = true
	}
	deleted := make(map[string]bool, len(rep.Deleted))
	var extra []string
	for _, id := range rep.Deleted {
		deleted[id] = true
		if !listed[id] {
			extra = append(extra, id)
		}
	}
	logIDs(run, "  deleted ", rep.Deleted)
	if len(extra) > 0 {
		run.Logf("the daemon also removed %s the preview did not list (created or released after the account):",
			plural(len(extra), "record", "records"))
		logIDs(run, "  also removed ", extra)
	}
	var kept []string
	for _, c := range pv.Candidates {
		if !deleted[c.ID] {
			kept = append(kept, c.ID)
		}
	}
	if len(kept) > 0 {
		run.Logf("the daemon kept %s the preview listed (in use when the prune arrived, or already gone):",
			plural(len(kept), "record", "records"))
		logIDs(run, "  kept ", kept)
	}
	return nil
}

// logIDs logs up to maxLoggedIDs identifiers, escaped: they are the
// daemon's strings and must not act on the terminal.
func logIDs(run *jobs.Run, prefix string, ids []string) {
	for i, id := range ids {
		if i == maxLoggedIDs {
			run.Logf("  … and %d more", len(ids)-maxLoggedIDs)
			return
		}
		run.Logf("%s%s", prefix, escape.Name(engine.ShortID(id)))
	}
}
