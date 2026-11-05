package freshness

import (
	"context"
	"time"
)

// Item is one image and the reference a batch checks it under.
type Item struct{ ImageID, Ref string }

// BatchResult is how a batch ended.
type BatchResult struct {
	Checked, Skipped int
	// Quota: a registry refused for quota and the batch stopped there;
	// Reset is when it allows again, if the registry said.
	Quota bool
	Reset time.Time
	// Cancelled: the operator stopped it.
	Cancelled bool
}

// Batch checks items one at a time, asking again for each (a batch is how
// the operator asks for fresh answers). Ineligible images are skipped
// without a request; a quota refusal stops the batch, and is not retried
// through the daemon, which shares the quota more often than not.
// Cancellation stops it before the next request. Answers already obtained
// are kept either way. each is called after every image.
func (c *Checker) Batch(ctx context.Context, items []Item, each func(done int, it Item, v Verdict)) BatchResult {
	var res BatchResult
	for i, it := range items {
		if ctx.Err() != nil {
			res.Cancelled = true
			return res
		}
		v, err := c.Check(ctx, it.ImageID, it.Ref, CheckOptions{Force: true, NoQuotaFallback: true})
		if err != nil {
			res.Cancelled = true
			return res
		}
		if v.State == NotApplicable {
			res.Skipped++
		} else {
			res.Checked++
		}
		if each != nil {
			each(i+1, it, v)
		}
		if v.Quota {
			res.Quota, res.Reset = true, v.QuotaReset
			return res
		}
	}
	return res
}
