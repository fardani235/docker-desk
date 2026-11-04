package engine_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics"
)

// TestReadOnlyExportsAnImage: exporting an image is a GET, so read-only
// mode streams it unchanged — the wrapper passes it through, and the request
// hook under it lets it onto the wire — and the daemon receives nothing that
// could change it. (An external test: forensics imports engine.)
func TestReadOnlyExportsAnImage(t *testing.T) {
	srv := fakeengine.New()
	srv.SeedDemo()
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	eng, err := engine.New(engine.Options{Host: srv.Host(), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if !engine.IsReadOnly(eng) {
		t.Fatal("not read-only")
	}
	d, err := eng.InspectImage(ctx, "shop/api:dev")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		rc, err := eng.SaveImage(ctx, d.ID, d.Platform())
		if err != nil {
			t.Fatalf("export in read-only mode: %v", err)
		}
		rep, err := forensics.Analyse(ctx, rc, forensics.Options{ImageID: d.ID}, nil)
		rc.Close()
		if err != nil || len(rep.Layers) != 4 {
			t.Fatalf("analysis of the exported archive: %v %+v", err, rep)
		}
	}
	if muts := srv.Mutations(); len(muts) != 0 {
		t.Fatalf("exporting sent %v", muts)
	}
	var sawPlatform bool
	for _, r := range srv.Requests() {
		if r.Path == "/images/get" && strings.Contains(r.Query, "platform=") && strings.Contains(r.Query, "names=") {
			sawPlatform = true
		}
	}
	if !sawPlatform {
		t.Errorf("no export carried its platform: %v", srv.Requests())
	}
	if _, err := eng.SaveImage(ctx, "sha256:nosuchimage", d.Platform()); !engine.IsNotFound(err) {
		t.Errorf("exporting a missing image: %v", err)
	}
}

// TestRealDaemonExportReadOnly is opt-in, like TestRealDaemonReadOnly (see
// `make real-readonly-check`): it exports the smallest image of a real
// daemon — a GET, through a read-only engine whose request hook refuses
// anything else — and analyses the archive the real daemon wrote. It logs
// counts only: evidence, even masked, stays out of the test log.
func TestRealDaemonExportReadOnly(t *testing.T) {
	host := os.Getenv("DOCKER_DESK_REAL_HOST")
	if host == "" {
		t.Skip("set DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock to run the read-only export check against a real daemon")
	}
	eng, err := engine.New(engine.Options{Host: host, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	info, err := eng.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	images, err := eng.Images(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pick *engine.Image
	for i := range images {
		if images[i].Size > 0 && images[i].Size < 300_000_000 && (pick == nil || images[i].Size < pick.Size) {
			pick = &images[i]
		}
	}
	if pick == nil {
		t.Skip("no image under 300 MB to export")
	}
	d, err := eng.InspectImage(ctx, pick.ID)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := eng.SaveImage(ctx, d.ID, d.Platform())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	defer rc.Close()
	rep, err := forensics.Analyse(ctx, rc, forensics.Options{ImageID: d.ID, Platform: d.Platform().String(), DaemonOS: info.OSType, DaemonArch: info.Architecture}, nil)
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(rep.Layers) != d.Layers {
		t.Errorf("archive has %d layers, inspect says %d", len(rep.Layers), d.Layers)
	}
	verified := 0
	for _, l := range rep.Layers {
		if l.Verified {
			verified++
		}
	}
	rules := map[string]int{}
	for _, f := range rep.Findings {
		rules[f.Rule]++
	}
	t.Logf("image %s (%d bytes): %s layout, %s read, %d layers (%d verified against diff_ids), %d findings, %d notes",
		engine.ShortID(d.ID), pick.Size, rep.Layout, strconv.FormatInt(rep.BytesRead, 10), len(rep.Layers), verified, len(rep.Findings), len(rep.Notes))
	for r, n := range rules {
		t.Logf("  %-22s %d", r, n)
	}
	if verified != len(rep.Layers) {
		t.Errorf("only %d of %d layers hash to their diff_id", verified, len(rep.Layers))
	}
}

// TestRealDaemonPackagesReadOnly analyses one rpm-based image from a real
// daemon, through the read-only engine, and lists what its package view
// would show. When the image is the one the committed rpm fixture was taken
// from, the installed list must be exactly what rpm reported for it.
//
//	DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock \
//	DOCKER_DESK_REAL_PACKAGES_IMAGE=registry.access.redhat.com/ubi9/ubi-micro:latest \
//	go test ./internal/engine/ -run TestRealDaemonPackagesReadOnly -v
func TestRealDaemonPackagesReadOnly(t *testing.T) {
	host := os.Getenv("DOCKER_DESK_REAL_HOST")
	if host == "" {
		t.Skip("set DOCKER_DESK_REAL_HOST to run the read-only package check against a real daemon")
	}
	ref := os.Getenv("DOCKER_DESK_REAL_PACKAGES_IMAGE")
	if ref == "" {
		ref = "registry.access.redhat.com/ubi9/ubi-micro:latest"
	}
	eng, err := engine.New(engine.Options{Host: host, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := eng.InspectImage(ctx, ref)
	if err != nil {
		t.Skipf("%s is not on this daemon: %v", ref, err)
	}
	rc, err := eng.SaveImage(ctx, d.ID, d.Platform())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	defer rc.Close()
	rep, err := forensics.Analyse(ctx, rc, forensics.Options{ImageID: d.ID, Ref: ref, Platform: d.Platform().String()}, nil)
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	for _, db := range rep.Packages.DBs {
		t.Logf("%s: state %d · %s (layer %d) · %d packages %s", db.Manager, db.State, db.Path, db.Layer, db.Total, db.Unknown)
	}
	sb := rep.Packages.SBOM
	t.Logf("SBOM: listed %v · %s · %d packages %s", sb.Listed, sb.Format, sb.Total, sb.Unknown)
	n := forensics.PkgCounts(forensics.JoinPackages(rep.Packages))
	t.Logf("rows by state (both, differs, declared only, undeclared, not checkable): %v", n)

	const fixtureImage = "sha256:5be877a2a280ae36eb9ab80ca4efb28bbf77c57abbba38137cdc37a03b8465e6"
	if d.ID != fixtureImage {
		t.Logf("%s is %s, not the fixture's image: listed, not compared", ref, engine.ShortID(d.ID))
		return
	}
	want, err := os.ReadFile("../forensics/testdata/rpm/ubi9-micro.rpm-qa.tsv")
	if err != nil {
		t.Fatal(err)
	}
	db, _ := rep.Packages.DB("rpm")
	if db.State != forensics.DBRead {
		t.Fatalf("rpm: %s", db.Unknown)
	}
	got := map[string]bool{}
	for _, p := range db.Packages {
		got[p.Name+" "+p.Version+" "+p.Arch] = true
	}
	lines := strings.Split(strings.TrimSpace(string(want)), "\n")
	if len(db.Packages) != len(lines) {
		t.Errorf("%d packages, rpm reported %d", len(db.Packages), len(lines))
	}
	for _, l := range lines {
		f := strings.Split(l, "\t")
		v := f[2] + "-" + f[3]
		if f[1] != "(none)" {
			v = f[1] + ":" + v
		}
		arch := f[4]
		if arch == "(none)" {
			arch = ""
		}
		if !got[f[0]+" "+v+" "+arch] {
			t.Errorf("rpm reported %s %s %s; the analysis did not", f[0], v, arch)
		}
	}
}
