package fakeengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type historyItem struct {
	CreatedBy  string
	Size       int64
	EmptyLayer bool
}

func history(t *testing.T, s *Server, ref string) []historyItem {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1.48/images/"+ref+"/history", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("history %s: %d %s", ref, rec.Code, rec.Body)
	}
	var items []historyItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	return items
}

// TestSeedLayersServesStepsAndASharedLayerAtTwoDepths: what Images › Layers
// is tested against. The application's history has entries that made no
// layer beside those that did, reported as the engine reports them — newest
// first, size 0 for both a config-only step and a layer that holds nothing,
// and no EmptyLayer to tell them apart. Its layers and the base's hold the
// same content at the same depth (the base layer) and at different ones
// (the empty layer).
func TestSeedLayersServesStepsAndASharedLayerAtTwoDepths(t *testing.T) {
	s := New()
	s.SeedLayers()

	items := history(t, s, LayersApp)
	if len(items) != 8 {
		t.Fatalf("%d history entries, want 8", len(items))
	}
	// Newest first: CMD on top, the base ADD at the bottom.
	if got := items[0].CreatedBy; got != `/bin/sh -c #(nop)  CMD ["/app/server"]` {
		t.Errorf("newest entry %q", got)
	}
	if got := items[7].CreatedBy; got != "/bin/sh -c #(nop) ADD file:51c0ffee in / " {
		t.Errorf("oldest entry %q", got)
	}
	sized := 0
	for _, it := range items {
		if it.Size > 0 {
			sized++
		}
		if it.EmptyLayer {
			t.Errorf("%q is reported with EmptyLayer set: the engine this stands for never sets it", it.CreatedBy)
		}
	}
	_, app, _ := inspectDiffIDs(t, s, LayersApp)
	_, base, _ := inspectDiffIDs(t, s, LayersBase)
	if len(app) != 5 || len(base) != 2 {
		t.Fatalf("%d and %d layers, want 5 and 2", len(app), len(base))
	}
	if sized != 3 {
		t.Errorf("%d entries carry a size for %d layers: the history is meant not to bind by counting", sized, len(app))
	}

	// The base layer, at the same depth in both.
	if app[0] != base[0] {
		t.Errorf("the base layer differs: %s and %s", app[0], base[0])
	}
	// The empty layer: second in the base, third and fifth in the app.
	empty := base[1]
	if app[2] != empty || app[4] != empty {
		t.Errorf("the empty layer %s is not at the app's third and fifth depth: %v", empty, app)
	}
	if app[1] == empty || app[3] == empty || app[0] == empty {
		t.Errorf("the empty layer is where a layer with files should be: %v", app)
	}
	// By chain the two images share only the base: the empty layer sits on
	// different stacks.
	s.mu.Lock()
	_, holders := s.chainUseLocked()
	s.mu.Unlock()
	shared := 0
	for _, n := range holders {
		if n > 1 {
			shared++
		}
	}
	if shared != 1 {
		t.Errorf("%d chains are shared, want only the base's", shared)
	}
}

// An image with no history of its own keeps the fake's derived one, and one
// given an empty history reports none.
func TestImageHistoryOverride(t *testing.T) {
	s := New()
	s.AddImage(3_000_000, 0, "plain:1")
	if items := history(t, s, "plain:1"); len(items) != 3 {
		t.Errorf("derived history: %d entries, want two layers and CMD", len(items))
	}
	if !s.SetImageHistory("plain:1") {
		t.Fatal("no such image")
	}
	if items := history(t, s, "plain:1"); len(items) != 0 {
		t.Errorf("an empty history is reported with %d entries", len(items))
	}
	s.SetImageHistory("plain:1", HistoryStep{CreatedBy: "one", Size: 5}, HistoryStep{CreatedBy: "two"})
	items := history(t, s, "plain:1")
	if len(items) != 2 || items[0].CreatedBy != "two" || items[1].CreatedBy != "one" || items[1].Size != 5 {
		t.Errorf("history: %+v", items)
	}
	if s.SetImageHistory("absent:1") {
		t.Error("an unknown image was given a history")
	}
}
