package space

import (
	"reflect"
	"testing"
)

// Sharing by content: the same diff ID at different depths names the other
// image, which chain IDs cannot see (docs/DECISIONS.md, LY4).
func TestContentHoldersIgnoreDepth(t *testing.T) {
	const empty = "sha256:empty"
	with := func(id string, diffs ...string) Image {
		return Image{ID: id, SharedSize: -1, Chains: ChainIDs(diffs), Diffs: diffs}
	}
	ix := Build([]Image{
		// z before a: holders are sorted, not in index order.
		with("z", "sha256:zbase", empty),
		with("a", "sha256:abase", "sha256:a1", empty, "sha256:a2", empty, empty),
		with("solo", "sha256:solo"),
		{ID: "unread", SharedSize: -1},
	})

	if got := ix.ContentHolders(empty); !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Errorf("holders of the empty layer: %v, want [a z] (sorted, a once)", got)
	}
	// By chain the two images share nothing: this is what the tab must not
	// conclude from.
	if got := ix.Sharers("a"); len(got) != 0 {
		t.Errorf("chain sharers of a: %v — the fixture no longer shows the difference", got)
	}

	a := ix.ContentSharers("a")
	if len(a) != 6 {
		t.Fatalf("a has 6 layers, got %d entries", len(a))
	}
	for i, want := range [][]string{nil, nil, {"z"}, nil, {"z"}, {"z"}} {
		if !reflect.DeepEqual(a[i], want) {
			t.Errorf("a layer %d: held by %v, want %v", i+1, a[i], want)
		}
	}
	z := ix.ContentSharers("z")
	if !reflect.DeepEqual(z, [][]string{nil, {"a"}}) {
		t.Errorf("z: %v, want its second layer held by a, once", z)
	}
	if got := ix.ContentSharers("solo"); !reflect.DeepEqual(got, [][]string{nil}) {
		t.Errorf("solo: %v", got)
	}
	if ix.ContentSharers("unread") != nil || ix.ContentSharers("absent") != nil {
		t.Error("an image whose diff IDs are not known has no answer, not an empty one")
	}
	if got := ix.ContentHolders("sha256:nobody"); len(got) != 0 {
		t.Errorf("holders of an unknown diff ID: %v", got)
	}
}

// The holder slices are the index's own: a caller that edits what it was
// given changes nothing.
func TestContentHoldersAreCopies(t *testing.T) {
	ix := Build([]Image{
		{ID: "a", Diffs: []string{"sha256:x"}},
		{ID: "b", Diffs: []string{"sha256:x"}},
	})
	got := ix.ContentHolders("sha256:x")
	got[0] = "tampered"
	if again := ix.ContentHolders("sha256:x"); !reflect.DeepEqual(again, []string{"a", "b"}) {
		t.Errorf("the index was changed through a returned slice: %v", again)
	}
}

// The byte accounting is by chain and does not read Diffs: the same world
// with and without them gives the same answers.
func TestDiffsDoNotChangeTheByteAccounting(t *testing.T) {
	plain := world()
	withDiffs := world()
	for i := range withDiffs {
		// Any diff IDs at all, shared across every image.
		withDiffs[i].Diffs = make([]string, len(withDiffs[i].Chains))
		for j := range withDiffs[i].Diffs {
			withDiffs[i].Diffs[j] = "sha256:same"
		}
	}
	p, d := Build(plain), Build(withDiffs)
	if !reflect.DeepEqual(p.Groups(), d.Groups()) {
		t.Errorf("groups differ:\n %+v\n %+v", p.Groups(), d.Groups())
	}
	for _, set := range [][]string{{"a"}, {"a", "b"}, {"a", "b", "c"}, {"c", "d"}} {
		if p.Frees(set) != d.Frees(set) {
			t.Errorf("%v: frees %+v without diffs, %+v with", set, p.Frees(set), d.Frees(set))
		}
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if p.Agrees(id) != d.Agrees(id) {
			t.Errorf("%s: agrees %v without diffs, %v with", id, p.Agrees(id), d.Agrees(id))
		}
	}
}
