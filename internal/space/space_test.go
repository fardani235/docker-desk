package space

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/fardani235/docker-desk/internal/engine"
)

func TestChainIDs(t *testing.T) {
	a, b := "sha256:aa", "sha256:bb"
	sum := sha256.Sum256([]byte(a + " " + b))
	want := []string{a, "sha256:" + hex.EncodeToString(sum[:])}
	if got := ChainIDs([]string{a, b}); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// The same layer on a different base is a different chain: not shared.
	if ChainIDs([]string{"sha256:cc", b})[1] == want[1] {
		t.Fatal("a layer's chain ID must depend on the layers below it")
	}
}

func TestHistoryMapsOnlyByExactCount(t *testing.T) {
	h := func(sizes ...int64) []engine.HistoryEntry {
		var out []engine.HistoryEntry
		for _, s := range sizes {
			out = append(out, engine.HistoryEntry{Size: s})
		}
		return out
	}
	cases := []struct {
		layers  int
		history []engine.HistoryEntry
		want    []int64
		ok      bool
	}{
		{2, h(10, 0, 20, 0), []int64{10, 20}, true}, // empty-layer entries skipped
		{3, h(10, 0, 20), []int64{10, 0, 20}, true}, // every entry made a layer, one of them empty
		{3, h(10, 0, 20, 0), nil, false},            // a zero-sized layer somewhere: which one is not known
		{1, h(10, 20), nil, false},                  // more sized entries than layers
		{0, nil, nil, false},
	}
	for i, c := range cases {
		got, ok := LayerSizes(c.layers, c.history)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("case %d: got %v %v, want %v %v", i, got, ok, c.want, c.ok)
		}
	}
}

// world: base is shared by a, b and c; mid by a and b only; each has a top
// of its own. d stands alone.
func world() []Image {
	base, mid := "sha256:base", "sha256:mid"
	img := func(id string, diff []string, sizes []int64, shared int64) Image {
		var total int64
		for _, s := range sizes {
			total += s
		}
		return Image{ID: id, Size: total, SharedSize: shared, Chains: ChainIDs(diff), Sizes: sizes}
	}
	return []Image{
		img("a", []string{base, mid, "sha256:a"}, []int64{100, 40, 5}, 140),
		img("b", []string{base, mid, "sha256:b"}, []int64{100, 40, 7}, 140),
		img("c", []string{base, "sha256:c"}, []int64{100, 9}, 100),
		img("d", []string{"sha256:d"}, []int64{11}, 0),
	}
}

func TestSharedBytesAreListedOnce(t *testing.T) {
	ix := Build(world())
	gs := ix.Groups()
	if len(gs) != 2 {
		t.Fatalf("groups: %+v", gs)
	}
	if !reflect.DeepEqual(gs[0].Holders, []string{"a", "b", "c"}) || gs[0].Bytes != 100 {
		t.Errorf("first group: %+v", gs[0])
	}
	if !reflect.DeepEqual(gs[1].Holders, []string{"a", "b"}) || gs[1].Bytes != 40 {
		t.Errorf("second group: %+v", gs[1])
	}
	// Every shared byte once: the groups add up to what is stored once.
	var sum int64
	for _, g := range gs {
		sum += g.Bytes
	}
	if sum != 140 {
		t.Errorf("shared bytes counted %d times over, want 140", sum)
	}
	if got := ix.Sharers("c"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("sharers of c: %v", got)
	}
	if got := ix.Sharers("d"); len(got) != 0 {
		t.Errorf("sharers of d: %v", got)
	}
}

func TestWhatASetFrees(t *testing.T) {
	ix := Build(world())
	cases := []struct {
		set  []string
		want int64
	}{
		{[]string{"a"}, 5},               // its own top only
		{[]string{"a", "b"}, 5 + 7 + 40}, // and the layer only they share
		{[]string{"a", "b", "c"}, 161},   // and the base: nothing else holds it
		{[]string{"c", "d"}, 9 + 11},     // the base stays for a and b
		{append([]string{"a"}, ix.Sharers("a")...), 161},
	}
	for _, c := range cases {
		f := ix.Frees(c.set)
		if !f.Exact || !f.Known || f.Bytes != c.want {
			t.Errorf("%v: %+v, want exactly %d", c.set, f, c.want)
		}
	}
}

func TestAnIndexThatDisagreesWithTheDaemonIsNotUsed(t *testing.T) {
	w := world()
	w[2].Size = 200 // c: the daemon counts more than its layers (containerd content, say)
	ix := Build(w)
	if ix.Agrees("c") || !ix.Agrees("a") {
		t.Fatalf("agreement: a=%v c=%v", ix.Agrees("a"), ix.Agrees("c"))
	}
	if got := ix.Disagreeing(); !reflect.DeepEqual(got, []string{"c"}) {
		t.Fatalf("disagreeing: %v", got)
	}
	// c's own top layer has no confirmed size: the set's figure is a floor.
	f := ix.Frees([]string{"a", "b", "c"})
	if f.Exact || !f.Known || f.Bytes != 5+7+(200-100) || f.Reason == "" {
		t.Fatalf("frees: %+v", f)
	}
	// Layers c does not hold are unaffected.
	if f := ix.Frees([]string{"a", "b"}); !f.Exact || f.Bytes != 52 {
		t.Fatalf("a+b: %+v", f)
	}
}

func TestAnUnreadImageMakesSetFiguresAFloor(t *testing.T) {
	w := world()
	w[3].Chains, w[3].Sizes = nil, nil // d's layers could not be read
	ix := Build(w)
	f := ix.Frees([]string{"a", "b"})
	if f.Exact || f.Bytes != 12 || f.Reason == "" {
		t.Fatalf("with an unread image, anything it might share is unknown: %+v", f)
	}
}
