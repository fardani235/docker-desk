package collect

import (
	"context"
	"fmt"
	"slices"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/forensics"
	"github.com/fardani235/docker-desk/internal/report"
	"github.com/fardani235/docker-desk/internal/space"
)

// The command line's way to the same documents: the requests the views
// make, in the same order, with the same options. Every one is a GET.

// FetchImage analyses an image as the Findings tab does — inspect for the
// platform, the archive streamed once, the containers using it inspected —
// and returns its document. ref is what the operator named: an image
// reference or ID. The document names the image by ref when ref is one of
// its references, and otherwise by its short ID, as the tab names an
// untagged image.
func FetchImage(ctx context.Context, eng engine.Engine, daemon engine.Info, version, ref string) (report.Image, error) {
	d, err := eng.InspectImage(ctx, ref)
	if err != nil {
		if engine.IsNotFound(err) {
			return report.Image{}, fmt.Errorf("the daemon has no image %q", ref)
		}
		return report.Image{}, fmt.Errorf("inspecting %s: %w", ref, err)
	}
	images, err := eng.Images(ctx)
	if err != nil {
		return report.Image{}, fmt.Errorf("listing images: %w", err)
	}
	img := d.Image
	for _, x := range images {
		if x.ID == d.ID {
			img = x
		}
	}
	name := engine.ShortID(img.ID)
	if slices.Contains(img.Refs(), ref) {
		name = ref
	}
	rc, err := eng.SaveImage(ctx, img.ID, d.Platform())
	if err != nil {
		return report.Image{}, fmt.Errorf("asking the daemon for the archive: %w", err)
	}
	defer rc.Close()
	rep, err := forensics.Analyse(ctx, rc, AnalysisOptions(img.ID, name, d.Platform(), daemon), nil)
	if err != nil {
		return report.Image{}, fmt.Errorf("analysing the archive: %w", err)
	}
	containers, err := eng.Containers(ctx)
	if err != nil {
		return report.Image{}, fmt.Errorf("listing containers: %w", err)
	}
	facts := InspectFacts(ctx, eng, FactsTargets(img, containers))
	live := forensics.LiveFindings(rep, forensics.LiveInput{Image: img, Images: images, Containers: containers, Facts: facts})
	return Image(version, name, rep, live), nil
}

// FetchSpace takes the daemon's account and every image's layers, as the
// Space view's job does, and returns its document. Writable layers are not
// measured: in the view only m asks for them (U5).
func FetchSpace(ctx context.Context, eng engine.Engine, version string) (report.Space, error) {
	du, err := eng.DiskUsage(ctx)
	if err != nil {
		return report.Space{}, fmt.Errorf("asking the daemon for its account: %w", err)
	}
	imgs := make([]space.Image, 0, len(du.ImageItems))
	for _, it := range du.ImageItems {
		si := space.Image{ID: it.ID, Size: it.Size, SharedSize: it.SharedSize}
		chains, sizes, err := LayersOf(ctx, eng, it.ID)
		if err != nil && ctx.Err() != nil {
			return report.Space{}, ctx.Err()
		}
		if err == nil {
			si.Chains, si.Sizes = chains, sizes
		}
		imgs = append(imgs, si)
	}
	return Space(SpaceInput{Version: version, DU: du, Index: space.Build(imgs)}), nil
}
