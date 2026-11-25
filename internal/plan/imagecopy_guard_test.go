package plan

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/forensics/fixture"
	"github.com/fardani235/docker-desk/internal/jobs"
)

// guardRun passes an archive through the guard in pieces of the given size,
// as the copy's stream does, and returns the guard's refusal and how many
// bytes would have been handed on to the target.
func guardRun(want string, archive []byte, piece int) (refusal error, passed int) {
	g := newArchiveGuard(want)
	defer g.finish()
	for off := 0; off < len(archive); off += piece {
		p := archive[off:min(off+piece, len(archive))]
		if err := g.feed(p); err != nil {
			return err, passed
		}
		if off+piece >= len(archive) {
			// The last piece is held until the archive is known to end.
			if err := g.finish(); err != nil {
				return err, passed
			}
		}
		passed += len(p)
	}
	if len(archive) == 0 {
		return g.finish(), 0
	}
	return nil, passed
}

type tarEntry struct {
	name string
	body string
	typ  byte
	link string
}

func makeTar(entries ...tarEntry) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: e.typ, Linkname: e.link}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			panic(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	return buf.Bytes()
}

func manifestJSON(tags ...string) string {
	b, _ := json.Marshal([]map[string]any{{"Config": "c.json", "RepoTags": tags, "Layers": []string{"l/layer.tar"}}})
	return string(b)
}

// entryEnd is the offset just past the data of the named entry.
func entryEnd(t *testing.T, archive []byte, name string) int {
	t.Helper()
	r := bytes.NewReader(archive)
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err != nil {
			t.Fatalf("no %s in the archive: %v", name, err)
		}
		if h.Name == name {
			_, _ = io.Copy(io.Discard, tr)
			return len(archive) - r.Len()
		}
	}
}

var guardPieces = []int{1, 7, 512, 513, 4096, 32 << 10, 1 << 30}

// TestArchiveGuardPassesWhatADaemonWrites: both layouts, tagged and
// untagged, in pieces of every size, with nothing held back.
func TestArchiveGuardPassesWhatADaemonWrites(t *testing.T) {
	tagged := fixture.ChangesReport(copyRefName, true)
	untagged := fixture.ChangesReport("x:1", true)
	untagged.RepoTags = nil
	first := tagged
	first.ManifestFirst = true
	for name, c := range map[string]struct {
		want    string
		archive []byte
	}{
		"legacy, tagged":         {copyRefName, fixture.Legacy(tagged).Archive},
		"oci, tagged":            {copyRefName, fixture.OCI(tagged).Archive},
		"oci, manifest first":    {copyRefName, fixture.OCI(first).Archive},
		"legacy, untagged":       {"", fixture.Legacy(untagged).Archive},
		"oci, untagged":          {"", fixture.OCI(untagged).Archive},
		"the long form of a tag": {"alpine:3", makeTar(tarEntry{name: "manifest.json", body: manifestJSON("docker.io/library/alpine:3")})},
		"no tag means latest":    {"acme/api:latest", makeTar(tarEntry{name: "manifest.json", body: manifestJSON("acme/api")})},
		"padded with zeros":      {copyRefName, append(fixture.Legacy(tagged).Archive, make([]byte, 10240)...)},
		// What a daemon with the containerd image store writes when one
		// platform is asked for (seen on Docker 29.10, API 1.57): the
		// platform's manifest, named, and the attestation manifest that
		// refers to it, unnamed; RepoTags null when saved by ID.
		"containerd store, one platform, by tag": {"hello-world:latest", makeTar(
			tarEntry{name: "blobs/sha256/d1a8", body: "{}"},
			tarEntry{name: "index.json", body: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
				`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:d1a8","size":1023,"annotations":{"containerd.io/distribution.source.docker.io":"library/hello-world","io.containerd.image.name":"docker.io/library/hello-world:latest","org.opencontainers.image.base.name":"scratch","org.opencontainers.image.ref.name":"latest","org.opencontainers.image.version":"linux"},"platform":{"architecture":"amd64","os":"linux"}},` +
				`{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:8e75","size":566,"annotations":{"io.containerd.manifest.subject":"sha256:d1a8"}}]}`},
			tarEntry{name: "manifest.json", body: `[{"Config":"blobs/sha256/e2ac","RepoTags":["hello-world:latest"],"Layers":["blobs/sha256/4f55"]}]`},
			tarEntry{name: "oci-layout", body: `{"imageLayoutVersion":"1.0.0"}`})},
		"containerd store, one platform, by ID": {"", makeTar(
			tarEntry{name: "index.json", body: `{"schemaVersion":2,"manifests":[` +
				`{"digest":"sha256:d1a8","annotations":{"containerd.io/distribution.source.docker.io":"library/hello-world","org.opencontainers.image.version":"linux"},"platform":{"architecture":"amd64","os":"linux"}},` +
				`{"digest":"sha256:8e75","annotations":{"io.containerd.manifest.subject":"sha256:d1a8"}}]}`},
			tarEntry{name: "manifest.json", body: `[{"Config":"blobs/sha256/e2ac","RepoTags":null,"Layers":["blobs/sha256/4f55"]}]`})},
		"an OCI name that is the tag alone": {copyRefName, makeTar(tarEntry{name: "index.json",
			body: `{"manifests":[{"digest":"sha256:a","annotations":{"org.opencontainers.image.ref.name":"3.28.3","io.containerd.image.name":"` + copyRefName + `"}}]}`})},
	} {
		for _, piece := range guardPieces {
			if err, passed := guardRun(c.want, c.archive, piece); err != nil || passed != len(c.archive) {
				t.Errorf("%s, pieces of %d: refused (%v) after %d of %d bytes", name, piece, err, passed, len(c.archive))
			}
		}
	}
}

// TestArchiveGuardRefuses: everything that is not the one confirmed image
// under the one confirmed name — and every refusal comes before the bytes
// that would complete the file at fault are handed on.
func TestArchiveGuardRefuses(t *testing.T) {
	good := manifestJSON(copyRefName)
	layer := tarEntry{name: "l/layer.tar", body: strings.Repeat("layer bytes ", 4000)}
	two, _ := json.Marshal([]map[string]any{{"RepoTags": []string{copyRefName}}, {"RepoTags": []string{}}})
	for name, c := range map[string]struct {
		want    string
		archive []byte
		// file, when set, is the entry whose last byte must not be passed.
		file string
		says string
	}{
		"another tag beside the confirmed one": {copyRefName, makeTar(layer, tarEntry{name: "manifest.json", body: manifestJSON(copyRefName, "postgres:16")}),
			"manifest.json", "names postgres:16, which is not " + copyRefName},
		"another tag instead": {copyRefName, makeTar(layer, tarEntry{name: "manifest.json", body: manifestJSON("postgres:16")}),
			"manifest.json", "names postgres:16"},
		"a tag on an untagged copy": {"", makeTar(layer, tarEntry{name: "manifest.json", body: good}),
			"manifest.json", "confirmed for an image that arrives untagged"},
		"a digest reference": {copyRefName, makeTar(tarEntry{name: "manifest.json", body: manifestJSON("host.test:5000/acme/api@sha256:" + strings.Repeat("a", 64))}),
			"manifest.json", "which is not " + copyRefName},
		"two images":                 {copyRefName, makeTar(layer, tarEntry{name: "manifest.json", body: string(two)}), "manifest.json", "lists 2 images"},
		"no image":                   {copyRefName, makeTar(tarEntry{name: "manifest.json", body: "[]"}), "manifest.json", "lists 0 images"},
		"a manifest that is not one": {copyRefName, makeTar(tarEntry{name: "manifest.json", body: `{"RepoTags":["x:1"]}`}), "manifest.json", "not the document a daemon writes"},
		"a second manifest.json": {copyRefName, makeTar(tarEntry{name: "manifest.json", body: good}, layer, tarEntry{name: "manifest.json", body: manifestJSON("postgres:16")}),
			"", "names postgres:16"},
		"a path that cleans to manifest.json": {copyRefName, makeTar(tarEntry{name: "a/../manifest.json", body: manifestJSON("postgres:16")}), "a/../manifest.json", "names postgres:16"},
		"./manifest.json":                     {copyRefName, makeTar(tarEntry{name: "./manifest.json", body: manifestJSON("postgres:16")}), "./manifest.json", "names postgres:16"},
		"another case":                        {copyRefName, makeTar(tarEntry{name: "Manifest.JSON", body: manifestJSON("postgres:16")}), "Manifest.JSON", "names postgres:16"},
		"manifest.json is a symlink": {copyRefName, makeTar(tarEntry{name: "blobs/x", body: manifestJSON("postgres:16")}, tarEntry{name: "manifest.json", typ: tar.TypeSymlink, link: "blobs/x"}),
			"", "manifest.json is a link"},
		"manifest.json is a hard link": {copyRefName, makeTar(tarEntry{name: "blobs/x", body: manifestJSON("postgres:16")}, tarEntry{name: "manifest.json", typ: tar.TypeLink, link: "blobs/x"}),
			"", "manifest.json is a link"},
		"manifest.json is a directory": {copyRefName, makeTar(tarEntry{name: "manifest.json/", typ: tar.TypeDir}), "", "not a regular file"},
		"placed through a link to the root": {copyRefName, makeTar(tarEntry{name: "x", typ: tar.TypeSymlink, link: "."}, tarEntry{name: "x/manifest.json", body: manifestJSON("postgres:16")}),
			"", "through a link"},
		"index.json names another": {copyRefName, makeTar(tarEntry{name: "index.json",
			body: `{"manifests":[{"digest":"sha256:a","annotations":{"io.containerd.image.name":"docker.io/library/postgres:16"}}]}`}), "index.json", "names docker.io/library/postgres:16"},
		"index.json's OCI name is another tag": {copyRefName, makeTar(tarEntry{name: "index.json",
			body: `{"manifests":[{"digest":"sha256:a","annotations":{"org.opencontainers.image.ref.name":"16"}}]}`}), "index.json", "names 16"},
		"index.json names an untagged copy": {"", makeTar(tarEntry{name: "index.json",
			body: `{"manifests":[{"digest":"sha256:a","annotations":{"org.opencontainers.image.ref.name":"1"}}]}`}), "index.json", "arrives untagged"},
		"index.json names a second image": {copyRefName, makeTar(tarEntry{name: "index.json",
			body: `{"manifests":[{"digest":"sha256:a","annotations":{"io.containerd.image.name":"` + copyRefName + `"}},{"digest":"sha256:b","annotations":{"io.containerd.image.name":"docker.io/library/postgres:16"}}]}`}),
			"index.json", "names docker.io/library/postgres:16"},
		"index.json that is not one":       {copyRefName, makeTar(tarEntry{name: "index.json", body: `[1,2]`}), "index.json", "not the document a daemon writes"},
		"repositories names another":       {copyRefName, makeTar(tarEntry{name: "repositories", body: `{"postgres":{"16":"abc"}}`}), "repositories", "names postgres:16"},
		"repositories on an untagged copy": {"", makeTar(tarEntry{name: "repositories", body: `{"host.test:5000/acme/api":{"3.28.3":"abc"}}`}), "repositories", "arrives untagged"},
		"a hostile name":                   {copyRefName, makeTar(tarEntry{name: "manifest.json", body: manifestJSON("evil\x1b[2J\nline:1")}), "manifest.json", `\x1b[2J\nline`},
		"nothing at all":                   {copyRefName, nil, "", "it is empty"},
		"only an end":                      {copyRefName, make([]byte, 1024), "", "it is empty"},
		"not a tar":                        {copyRefName, []byte(strings.Repeat("this is not a tar archive at all. ", 60)), "", "cannot be read as a tar archive"},
		"data after the end": {copyRefName, append(makeTar(tarEntry{name: "manifest.json", body: good}), makeTar(tarEntry{name: "manifest.json", body: manifestJSON("postgres:16")})...),
			"", "data after the archive's end"},
	} {
		for _, piece := range guardPieces {
			err, passed := guardRun(c.want, c.archive, piece)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Errorf("%s, pieces of %d: %v, want a refusal saying %q", name, piece, err, c.says)
				continue
			}
			if strings.ContainsAny(err.Error(), "\x1b\n\r") {
				t.Errorf("%s: the refusal carries control characters: %q", name, err)
			}
			if passed >= len(c.archive) && len(c.archive) > 0 {
				t.Errorf("%s, pieces of %d: the whole archive was handed on before the refusal", name, piece)
			}
			if c.file != "" {
				if end := entryEnd(t, c.archive, c.file); passed >= end {
					t.Errorf("%s, pieces of %d: %d bytes were handed on, the file at fault ends at %d", name, piece, passed, end)
				}
			}
		}
	}

	// The IDs a store may give the image are read from the archive: the
	// manifests of index.json but not the one that only refers to another,
	// and the config of manifest.json; nothing that is not an ID.
	a, b, cfg := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	g := newArchiveGuard("")
	archive := makeTar(
		tarEntry{name: "index.json", body: `{"manifests":[{"digest":"sha256:` + a + `"},{"digest":"sha256:` + b + `","annotations":{"io.containerd.manifest.subject":"sha256:` + a + `"}},{"digest":"../../etc/passwd"},{"digest":"sha256:` + a + `"}]}`},
		tarEntry{name: "manifest.json", body: `[{"Config":"blobs/sha256/` + cfg + `","RepoTags":null}]`})
	if err := g.feed(archive); err != nil {
		t.Fatal(err)
	}
	if err := g.finish(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(g.candidates(), ","); got != "sha256:"+a+",sha256:"+cfg {
		t.Errorf("candidates %q", got)
	}

	// A truncated archive is not passed off as a whole one: the stream's
	// end is refused, whatever was handed on before it.
	whole := fixture.Legacy(fixture.ChangesReport(copyRefName, true)).Archive
	for _, cut := range []int{len(whole) / 2, len(whole) - 1500, 700} {
		for _, piece := range guardPieces {
			if err, _ := guardRun(copyRefName, whole[:cut], piece); err == nil || !strings.Contains(err.Error(), "ended early") {
				t.Errorf("cut at %d, pieces of %d: %v", cut, piece, err)
			}
		}
	}

	// A naming file larger than any daemon writes is refused at its header.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Size: guardMaxMeta + 1, Mode: 0o644})
	_, _ = tw.Write(make([]byte, 4096))
	if err, passed := guardRun(copyRefName, buf.Bytes(), 512); err == nil || !strings.Contains(err.Error(), "larger than any daemon writes") || passed > 512 {
		t.Errorf("an oversized manifest.json: %v after %d bytes", err, passed)
	}
}

// TestArchiveGuardReleasesItsReader: abandoning a stream midway — a cancel,
// a broken source — ends the guard's goroutine.
func TestArchiveGuardReleasesItsReader(t *testing.T) {
	whole := fixture.Legacy(fixture.ChangesReport(copyRefName, true)).Archive
	g := newArchiveGuard(copyRefName)
	if err := g.feed(whole[:1000]); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- g.finish() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("half an archive ended without a refusal")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the guard did not end when its stream was abandoned")
	}
	// Twice is harmless.
	if g.finish() == nil {
		t.Fatal("the verdict changed")
	}
}

// ---- the executor, against a source that sends more than was confirmed -------------

// smugglingSource is a source daemon whose archive names more than the
// image that was asked for.
type smugglingSource struct {
	engine.Engine
	archive []byte
}

func (s *smugglingSource) SaveImage(ctx context.Context, id string, p engine.Platform) (io.ReadCloser, error) {
	rc, err := s.Engine.SaveImage(ctx, id, p)
	if err != nil {
		return nil, err
	}
	rc.Close()
	return io.NopCloser(bytes.NewReader(s.archive)), nil
}

func TestExecuteCopyRefusesAnArchiveThatNamesMore(t *testing.T) {
	for name, tags := range map[string][]string{
		"another tag beside it": {copyRefName, "resident:1"},
		"another tag instead":   {"resident:1"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newImgCopyRig(t, copyRefName, nil)
			p := c.plan(copyRefName)
			residentBefore, err := c.dst.eng.InspectImage(context.Background(), "resident:1")
			if err != nil {
				t.Fatal(err)
			}
			// The same build, with the archive's names rewritten: without
			// the guard the fake target — like a daemon — would move
			// resident:1 to it.
			f := copyFixture(copyRefName)
			f.RepoTags = tags
			c.srcEng = &smugglingSource{Engine: c.src.eng, archive: fixture.Legacy(f).Archive}
			dstIDs := c.dst.srv.ImageIDs()
			j := c.run(p)
			log := logText(j)
			if j.State != jobs.Failed || !strings.Contains(j.Err, "sent an archive that is not what was confirmed") || !strings.Contains(j.Err, "names resident:1") {
				t.Fatalf("job %v %q\n%s", j.State, j.Err, log)
			}
			after, err := c.dst.eng.InspectImage(context.Background(), "resident:1")
			if err != nil || after.ID != residentBefore.ID {
				t.Fatalf("the target's resident:1 was moved: %v, %s (was %s)", err, after.ID, residentBefore.ID)
			}
			if strings.Join(c.dst.srv.ImageIDs(), ",") != strings.Join(dstIDs, ",") || c.dst.srv.HasImage(copyRefName) {
				t.Fatalf("the refused archive produced an image on the target: %v", c.dst.srv.ImageIDs())
			}
			deadline := time.Now().Add(5 * time.Second)
			for len(c.dst.srv.LoadRequests()) == 0 && time.Now().Before(deadline) {
				time.Sleep(2 * time.Millisecond)
			}
			if reqs := c.dst.srv.LoadRequests(); len(reqs) != 1 || reqs[0].ImageID != "" || reqs[0].Err == "" {
				t.Fatalf("the target's load: %+v", reqs)
			}
			for _, s := range []string{"before the archive was complete", "No image and no moved tag is claimed", CopySourceNote} {
				if !strings.Contains(log, s) {
					t.Errorf("log lacks %q:\n%s", s, log)
				}
			}
			if strings.Contains(log, "✓") {
				t.Fatalf("a refused archive claimed success:\n%s", log)
			}
			noControl(t, j)
		})
	}
}

// TestExecuteCopyRefusesATruncatedArchive: a source whose stream ends
// cleanly but early is the source's failure, not the target's.
func TestExecuteCopyRefusesATruncatedArchive(t *testing.T) {
	c := newImgCopyRig(t, copyRefName, nil)
	p := c.plan(copyRefName)
	whole := fixture.Legacy(copyFixture(copyRefName)).Archive
	c.srcEng = &smugglingSource{Engine: c.src.eng, archive: whole[:len(whole)/2]}
	j := c.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "the source laptop (unix:///src.sock) sent an archive that is not what was confirmed") || !strings.Contains(j.Err, "ended early") {
		t.Fatalf("job %v %q\n%s", j.State, j.Err, logText(j))
	}
	if c.dst.srv.HasImage(copyRefName) {
		t.Fatal("half an archive produced an image")
	}
}

// TestExecuteCopyChecksWhatTheTargetSaysItLoaded: the target's own account
// is the second check — a reference it reports beyond the confirmed one
// fails the job, whatever the archive's names were read as.
func TestExecuteCopyChecksWhatTheTargetSaysItLoaded(t *testing.T) {
	c := newImgCopyRig(t, copyRefName, nil)
	p := c.plan(copyRefName)
	c.dstEng = &boastingTarget{Engine: c.dst.eng, extra: "postgres:16\x1b[2J"}
	j := c.run(p)
	if j.State != jobs.Failed || !strings.Contains(j.Err, "says it loaded postgres:16") || !strings.Contains(j.Err, "was not confirmed for") {
		t.Fatalf("job %v %q\n%s", j.State, j.Err, logText(j))
	}
	if strings.Contains(logText(j), "✓") {
		t.Fatalf("claimed success:\n%s", logText(j))
	}
	noControl(t, j)
}

// TestExecuteCopyFindsAnUntaggedImageByTheArchivesOwnIDs: a target that
// says nothing of what it loaded, and names the image otherwise than the
// source does, is still asked for the right image: the archive carries the
// IDs a store would give it.
func TestExecuteCopyFindsAnUntaggedImageByTheArchivesOwnIDs(t *testing.T) {
	c := newImgCopyRig(t, "", nil)
	p := c.plan("")
	c.dstEng = &silentTarget{Engine: c.dst.eng}
	j := c.run(p)
	log := logText(j)
	if j.State != jobs.Succeeded || !strings.Contains(log, "untagged on the target") || !strings.Contains(log, "name this build differently") {
		t.Fatalf("job %v %q\n%s", j.State, j.Err, log)
	}
}

// silentTarget loads, and reports no ID for what it loaded.
type silentTarget struct{ engine.Engine }

func (s *silentTarget) LoadImage(ctx context.Context, r io.Reader, p engine.Platform, progress func(engine.LoadProgress)) (engine.LoadResult, error) {
	_, err := s.Engine.LoadImage(ctx, r, p, func(engine.LoadProgress) {})
	return engine.LoadResult{}, err
}

type boastingTarget struct {
	engine.Engine
	extra string
}

func (b *boastingTarget) LoadImage(ctx context.Context, r io.Reader, p engine.Platform, progress func(engine.LoadProgress)) (engine.LoadResult, error) {
	res, err := b.Engine.LoadImage(ctx, r, p, progress)
	res.Refs = append(res.Refs, b.extra)
	return res, err
}

// TestExecuteCopyOfALayerlessImage: with no layers there is nothing the
// content can be told by, and the job says what was compared instead of
// "the same 0 layers".
func TestExecuteCopyOfALayerlessImage(t *testing.T) {
	const ref = "acme/empty:1"
	var id string
	src := newRig(t, func(s *fakeengine.Server) {
		id = s.AddImage(0, time.Hour, ref)
		s.SetImageFixture(id, fixture.Image{RepoTags: []string{ref}, Cmd: []string{"/pause"}})
	})
	dst := newRig(t, nil)
	c := &imgCopyRig{t: t, src: src, dst: dst, jm: src.jm, srcEng: src.eng, dstEng: dst.eng, imageID: "sha256:" + id}
	j := c.run(c.plan(ref))
	log := logText(j)
	if j.State != jobs.Succeeded || !strings.Contains(log, "the image has no layers: only its platform and config were compared") || strings.Contains(log, "the same 0 layers") {
		t.Fatalf("job %v %q\n%s", j.State, j.Err, log)
	}
}

// TestCopyPlanRefusesAnOddPlatform: a platform is sent on to the target; one
// that is not made of plain words is refused when the plan is built, and
// again where it would be sent.
func TestCopyPlanRefusesAnOddPlatform(t *testing.T) {
	id := hexID('a')
	snap := Snapshot{Images: []engine.Image{{ID: id, RepoTags: []string{copyRefName}}}}
	for _, arch := range []string{"amd64&names=x", "arm64/../..", "a b", "\x1b[2J", strings.Repeat("a", 33), `{"os":"x"}`} {
		f := copyFacts(id, "sha256:l1")
		f.Architecture = arch
		_, err := CopyImage(snap, CopySource{Daemon: copySrcDaemon, ImageID: id, Ref: copyRefName, Facts: f}, CopyDestination{Daemon: copyDstDaemon})
		if err == nil || !strings.Contains(err.Error(), "cannot name exactly") || !strings.Contains(err.Error(), "nothing was sent") || strings.Contains(err.Error(), "\x1b") {
			t.Errorf("%q: %v", arch, err)
		}
	}
	p, err := CopyImage(snap, CopySource{Daemon: copySrcDaemon, ImageID: id, Ref: copyRefName, Facts: copyFacts(id, "sha256:l1")}, CopyDestination{Daemon: copyDstDaemon})
	if err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Plan){
		"an odd platform": func(p *Plan) { c := *p.ImageCopy; c.Platform.OS = "linux?x=1"; p.ImageCopy = &c },
		"not typed":       func(p *Plan) { p.Tier = TierConfirm },
		"no word":         func(p *Plan) { p.Word = "" },
	} {
		q := p
		mut(&q)
		if _, err := checkCopyPlan(q); err == nil || !strings.Contains(err.Error(), "nothing was sent") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := checkCopyPlan(p); err != nil {
		t.Fatalf("the plan as built: %v", err)
	}
}

// TestRealDaemonImageCopyGuardReadOnly runs archives a REAL daemon writes
// through the archive guard: the guard must pass what a daemon saves by a
// tag when that tag was confirmed, and by an ID when nothing was, and must
// refuse the tagged archive for an untagged copy. Only GETs are sent — the
// engine is read-only, and its HTTP hook refuses anything else — and only
// counts are logged. Opt-in: DOCKER_DESK_REAL_HOST (make real-readonly-check).
func TestRealDaemonImageCopyGuardReadOnly(t *testing.T) {
	host := os.Getenv("DOCKER_DESK_REAL_HOST")
	if host == "" {
		t.Skip("set DOCKER_DESK_REAL_HOST=unix:///var/run/docker.sock to run the read-only check against a real daemon")
	}
	eng, err := engine.New(engine.Options{Host: host, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if !engine.IsReadOnly(eng) {
		t.Fatal("engine is not read-only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	images, err := eng.Images(ctx)
	if err != nil {
		t.Fatalf("images: %v", err)
	}
	through := func(want, by string, plat engine.Platform) (error, int64) {
		rc, err := eng.SaveImage(ctx, by, plat)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		defer rc.Close()
		g := newArchiveGuard(want)
		defer g.finish()
		buf := make([]byte, 32<<10)
		var n int64
		for {
			k, rerr := rc.Read(buf)
			if k > 0 {
				if gerr := g.feed(buf[:k]); gerr != nil {
					return gerr, n
				}
				n += int64(k)
			}
			if rerr == io.EOF {
				return g.finish(), n
			}
			if rerr != nil {
				t.Fatalf("reading the archive: %v", rerr)
			}
		}
	}
	checked := 0
	for _, img := range images {
		if checked >= 3 || img.Size <= 0 || img.Size > 300_000_000 || !imageIDForm.MatchString(img.ID) {
			continue
		}
		var tag string
		for _, r := range img.Refs() {
			if n, err := CopyName(img.ID, r); err == nil && n == r {
				tag = r
				break
			}
		}
		facts, err := eng.ImageFacts(ctx, img.ID)
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		checked++
		if err, n := through("", img.ID, facts.Platform()); err != nil {
			t.Errorf("image %d: saved by its ID, the guard refused it for an untagged copy after %d bytes: %v", checked, n, err)
		} else {
			t.Logf("image %d: by ID, %d bytes, passed for an untagged copy", checked, n)
		}
		if tag == "" {
			continue
		}
		if err, n := through(tag, tag, facts.Platform()); err != nil {
			t.Errorf("image %d: saved by its tag, the guard refused it for that tag after %d bytes: %v", checked, n, err)
		} else {
			t.Logf("image %d: by tag, %d bytes, passed for that tag", checked, n)
		}
		if err, _ := through("", tag, facts.Platform()); err == nil {
			t.Errorf("image %d: saved by its tag, the guard passed it for an untagged copy", checked)
		}
		if err, _ := through("docker-desk.invalid/not-this:1", tag, facts.Platform()); err == nil {
			t.Errorf("image %d: saved by its tag, the guard passed it for another reference", checked)
		}
	}
	if checked == 0 {
		t.Skip("no image under 300 MB to save")
	}
}
