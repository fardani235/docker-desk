package fakeengine

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/forensics/fixture"
)

func postLoad(s *Server, body []byte, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1.48/images/load?quiet=0"+query, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-tar")
	s.ServeHTTP(rec, req)
	return rec
}

func inspectDiffIDs(t *testing.T, s *Server, ref string) (id string, diffIDs []string, arch string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1.48/images/"+ref+"/json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect %s: %d %s", ref, rec.Code, rec.Body)
	}
	var doc struct {
		Id           string
		Architecture string
		RootFS       struct{ Layers []string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Id, doc.RootFS.Layers, doc.Architecture
}

// TestLoadRegistersTheArchive loads a fixture in both layouts: the image
// appears under the archive's reference with the config's diff IDs and
// platform, the answer names what was loaded, and one image event is sent.
func TestLoadRegistersTheArchive(t *testing.T) {
	f := fixture.ChangesReport("acme/report:1.4", true)
	f.Architecture, f.Variant = "arm64", "v8"
	var want []string
	for _, l := range f.Layers {
		want = append(want, l.DiffID())
	}
	for name, built := range map[string]fixture.Built{"legacy": fixture.Legacy(f), "oci": fixture.OCI(f)} {
		t.Run(name, func(t *testing.T) {
			s := New()
			rec := postLoad(s, built.Archive, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("load: %d %s", rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), `"stream":"Loaded image: acme/report:1.4\n"`) ||
				!strings.Contains(rec.Body.String(), `"status":"Loading layer"`) {
				t.Fatalf("answer:\n%s", rec.Body)
			}
			id, diffIDs, arch := inspectDiffIDs(t, s, "acme/report:1.4")
			if !reflect.DeepEqual(diffIDs, want) || arch != "arm64" {
				t.Fatalf("inspect: layers %v arch %s, want %v arm64", diffIDs, arch, want)
			}
			// A classic store names the image by its config's digest.
			if id != built.ConfigDigest {
				t.Fatalf("id %s, want the config digest %s", id, built.ConfigDigest)
			}
			reqs := s.LoadRequests()
			if len(reqs) != 1 || reqs[0].Bytes != int64(len(built.Archive)) || "sha256:"+reqs[0].ImageID != id ||
				!reflect.DeepEqual(reqs[0].Refs, []string{"acme/report:1.4"}) || len(reqs[0].MovedFrom) != 0 || reqs[0].Err != "" {
				t.Fatalf("recorded %+v", reqs)
			}
			// Loading the same archive again changes nothing.
			if rec := postLoad(s, built.Archive, ""); rec.Code != http.StatusOK || len(s.ImageIDs()) != 1 {
				t.Fatalf("second load: %d, images %v", rec.Code, s.ImageIDs())
			}
		})
	}
}

// TestLoadMovesAnExistingTag: a reference that names another image is moved
// to the loaded one without a refusal; the other image keeps its data.
func TestLoadMovesAnExistingTag(t *testing.T) {
	s := New()
	old := s.AddImage(1000, 0, "acme/report:1.4", "acme/report:old")
	built := fixture.Legacy(fixture.ChangesBase("acme/report:1.4"))
	if rec := postLoad(s, built.Archive, ""); rec.Code != http.StatusOK {
		t.Fatalf("load: %d %s", rec.Code, rec.Body)
	}
	id, _, _ := inspectDiffIDs(t, s, "acme/report:1.4")
	if id != built.ConfigDigest {
		t.Fatalf("the tag names %s, want the loaded image %s", id, built.ConfigDigest)
	}
	if oldID, _, _ := inspectDiffIDs(t, s, "acme/report:old"); oldID != "sha256:"+old {
		t.Fatalf("the image that held the tag is gone: %s", oldID)
	}
	if got := s.LoadRequests()[0].MovedFrom; got["acme/report:1.4"] != old {
		t.Fatalf("MovedFrom %v, want %s", got, old)
	}
}

// TestLoadUntagged: an archive saved by image ID carries no reference; the
// image arrives untagged and the answer names its ID.
func TestLoadUntagged(t *testing.T) {
	f := fixture.ChangesBase("x:1")
	f.RepoTags = nil
	built := fixture.Legacy(f)
	s := New()
	rec := postLoad(s, built.Archive, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Loaded image ID: "+built.ConfigDigest) {
		t.Fatalf("load: %d %s", rec.Code, rec.Body)
	}
	if len(s.ImageRefs()) != 0 || !s.HasImage(built.ConfigDigest) {
		t.Fatalf("refs %v", s.ImageRefs())
	}
}

// TestLoadRefusesAMalformedArchive: bytes that are not an image archive
// load nothing.
func TestLoadRefusesAMalformedArchive(t *testing.T) {
	good := fixture.Legacy(fixture.ChangesBase("acme/report:1.4")).Archive
	for name, body := range map[string][]byte{
		"not a tar":   []byte(strings.Repeat("this is not a tar archive ", 100)),
		"truncated":   good[:len(good)/2],
		"empty":       nil,
		"no manifest": fixture.Legacy(fixture.Image{}).Archive[:1024],
	} {
		s := New()
		rec := postLoad(s, body, "")
		if rec.Code == http.StatusOK || len(s.ImageIDs()) != 0 {
			t.Errorf("%s: status %d, images %v", name, rec.Code, s.ImageIDs())
		}
		if reqs := s.LoadRequests(); len(reqs) != 1 || reqs[0].Err == "" || reqs[0].ImageID != "" {
			t.Errorf("%s: recorded %+v", name, reqs)
		}
	}
}

// TestLoadInjectedFailure: a refusal before the archive is read (FailNext),
// and a failure reported inside the answer after it was (FailLoad). Neither
// registers anything, and the next load works.
func TestLoadInjectedFailure(t *testing.T) {
	built := fixture.Legacy(fixture.ChangesBase("acme/report:1.4"))
	s := New()
	s.FailNext(http.MethodPost, `^/images/load$`, http.StatusInternalServerError, "no space left on device", 1)
	if rec := postLoad(s, built.Archive, ""); rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "no space left") {
		t.Fatalf("FailNext: %d %s", rec.Code, rec.Body)
	}
	s.FailLoad("write /var/lib/docker/tmp/x: no space left on device")
	rec := postLoad(s, built.Archive, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"errorDetail":{"message":"write /var/lib/docker/tmp/x: no space left on device"}`) {
		t.Fatalf("FailLoad: %d %s", rec.Code, rec.Body)
	}
	if len(s.ImageIDs()) != 0 {
		t.Fatalf("a failed load registered %v", s.ImageIDs())
	}
	if rec := postLoad(s, built.Archive, "&platform=x"); rec.Code != http.StatusOK || !s.HasImage("acme/report:1.4") {
		t.Fatalf("after the failures: %d %s", rec.Code, rec.Body)
	}
	if got := s.LoadRequests(); got[len(got)-1].Platform != "x" {
		t.Fatalf("platform not recorded: %+v", got[len(got)-1])
	}
}
