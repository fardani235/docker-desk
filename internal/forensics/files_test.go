package forensics

import (
	"archive/tar"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/forensics/fixture"
)

// TestFilesModeReadsHeadersOnly is D1: in ModeFiles the archive is walked
// for its headers and nothing else — a secret in a file and a package
// database the Findings analysis would read are both passed over.
func TestFilesModeReadsHeadersOnly(t *testing.T) {
	img := fixture.Image{Layers: []fixture.Layer{{Files: []fixture.File{
		{Path: "root/.aws/credentials", Body: []byte("[default]\naws_access_key_id = " + fixture.DemoAWSKeyID + "\naws_secret_access_key = " + fixture.DemoAWSSecret + "\n")},
		{Path: "var/lib/dpkg/status", Body: fixture.DpkgStatus([]fixture.Pkg{{Name: "libc6", Version: "2.36-9", Arch: "amd64"}})},
		{Path: "usr/bin/su", Mode: 0o4755, Body: []byte("not really su")},
	}}}}
	for _, lay := range layouts {
		t.Run(lay.name, func(t *testing.T) {
			b := lay.build(img)
			// The same archive in the default mode does find all three, so
			// the silence below is the mode's, not the fixture's.
			full := analyse(t, b, Options{})
			if len(find(full, "secret.file", "")) == 0 || len(find(full, "fs.setuid", "")) == 0 {
				t.Fatalf("the default mode should report the secret and the setuid bit:\n%s", dump(full))
			}
			if d, ok := full.Packages.DB("deb"); !ok || len(d.Packages) == 0 {
				t.Fatalf("the default mode should read the dpkg database: %+v", full.Packages)
			}
			if full.Files != nil {
				t.Fatal("ModeFindings must not keep a listing")
			}

			rep := analyse(t, b, Options{Mode: ModeFiles})
			if len(rep.Findings) != 0 {
				t.Fatalf("ModeFiles produced findings:\n%s", dump(rep))
			}
			if rep.Packages != nil {
				t.Fatalf("ModeFiles produced a package list: %+v", rep.Packages)
			}
			if rep.Files == nil || len(rep.Files.Layers) != 1 || len(rep.Files.Layers[0].Entries) != 3 {
				t.Fatalf("listing: %+v", rep.Files)
			}

			both := analyse(t, b, Options{Mode: ModeBoth})
			if len(both.Findings) != len(full.Findings) || both.Files == nil || len(both.Files.Layers[0].Entries) != 3 {
				t.Fatalf("ModeBoth: %d findings (want %d), listing %+v", len(both.Findings), len(full.Findings), both.Files)
			}
		})
	}
}

// TestFilesModeAllocatesNoContentBuffer: the buffers file bodies are read
// into for the secret scan and the ELF headers do not exist in ModeFiles.
func TestFilesModeAllocatesNoContentBuffer(t *testing.T) {
	a := newAnalyser(t.Context(), Options{Mode: ModeFiles, Limits: DefaultLimits()}, nil)
	if a.head != nil || a.elf != nil {
		t.Fatalf("ModeFiles allocated content buffers: head %d, elf %d bytes", len(a.head), len(a.elf))
	}
}

func names(es []FileEntry) []string {
	var out []string
	for _, e := range es {
		s := fmt.Sprintf("%c %s", e.Type, e.Path)
		if e.Link != "" {
			s += " -> " + e.Link
		}
		if e.Type == tar.TypeReg {
			s += fmt.Sprintf(" (%d)", e.Size)
		}
		out = append(out, s)
	}
	return out
}

// TestListingPerLayer is task 1.2: one entry per header, per layer, with
// whiteouts and opaques kept beside them, the layers in the manifest's
// order whichever order the archive stores them in.
func TestListingPerLayer(t *testing.T) {
	img := fixture.Image{Layers: []fixture.Layer{
		{Files: []fixture.File{
			{Path: "usr/", Type: tar.TypeDir},
			{Path: "usr/bin/", Type: tar.TypeDir},
			{Path: "usr/bin/app", Body: []byte("v1"), Mode: 0o755},
			{Path: "tmp/seed", Body: []byte("seed")},
		}},
		{Files: []fixture.File{
			{Path: "usr/bin/app", Body: []byte("version2"), Mode: 0o750},
			{Path: "bin", Type: tar.TypeSymlink, Link: "usr/bin"},
		}},
		{Delete: []string{"tmp/seed"}, Opaque: []string{"var/cache"}, Files: []fixture.File{
			{Path: "usr/bin/app2", Type: tar.TypeLink, Link: "usr/bin/app"},
		}},
	}}
	want := [][]string{
		{"5 /usr", "5 /usr/bin", "0 /usr/bin/app (2)", "0 /tmp/seed (4)"},
		{"0 /usr/bin/app (8)", "2 /bin -> usr/bin"},
		{"5 /var/cache", "1 /usr/bin/app2 -> /usr/bin/app"},
	}
	for _, lay := range layouts {
		for _, first := range []bool{false, true} {
			img.ManifestFirst = first
			t.Run(fmt.Sprintf("%s/manifest-first=%v", lay.name, first), func(t *testing.T) {
				rep := analyse(t, lay.build(img), Options{Mode: ModeFiles})
				l := rep.Files
				if len(l.Layers) != 3 {
					t.Fatalf("want 3 layers, got %d", len(l.Layers))
				}
				for i, w := range want {
					if got := names(l.Layers[i].Entries); !reflect.DeepEqual(got, w) {
						t.Errorf("layer %d entries:\n got %q\nwant %q", i+1, got, w)
					}
					if l.Layers[i].Index != i+1 || l.Layers[i].Unread != "" {
						t.Errorf("layer %d: index %d, unread %q", i+1, l.Layers[i].Index, l.Layers[i].Unread)
					}
				}
				if got := l.Layers[2].Whiteouts; !reflect.DeepEqual(got, []string{"/tmp/seed"}) {
					t.Errorf("whiteouts %q", got)
				}
				if got := l.Layers[2].Opaques; !reflect.DeepEqual(got, []string{"/var/cache"}) {
					t.Errorf("opaques %q", got)
				}
				e := l.Layers[1].Entries[0]
				if e.Mode != 0o750 || e.ModTime.IsZero() {
					t.Errorf("mode %o, mtime %v", e.Mode, e.ModTime)
				}
				// 4 + 2 + 1 entries, one whiteout and one opaque marker.
				if l.Seen != 10 || l.Kept != 10 || l.Cut() {
					t.Errorf("seen %d kept %d cut %v", l.Seen, l.Kept, l.Cut())
				}
			})
		}
	}
}

// TestListingBounds is task 1.3: each bound, set tiny, drops and counts
// what lies past it, and the others still hold.
func TestListingBounds(t *testing.T) {
	var files []fixture.File
	for i := 0; i < 10; i++ {
		files = append(files, fixture.File{Path: fmt.Sprintf("d/f%02d", i), Body: []byte("x")})
	}
	files = append(files, fixture.File{Path: "d/" + strings.Repeat("long", 10), Body: []byte("x")})
	b := fixture.OCI(fixture.Image{Layers: []fixture.Layer{{Files: files}}})

	run := func(f func(*Limits)) *FileListing {
		lim := DefaultLimits()
		f(&lim)
		return analyse(t, b, Options{Mode: ModeFiles, Limits: lim}).Files
	}
	l := run(func(l *Limits) { l.FileEntries = 4 })
	if l.Seen != 11 || l.Kept != 4 || l.DroppedEntries != 7 || l.DroppedLongPath != 0 || l.DroppedText != 0 || !l.Cut() {
		t.Errorf("entries bound: %+v", *l)
	}
	l = run(func(l *Limits) { l.FilePath = 20 })
	if l.Seen != 11 || l.Kept != 10 || l.DroppedLongPath != 1 || l.DroppedEntries != 0 || l.DroppedText != 0 {
		t.Errorf("path bound: %+v", *l)
	}
	l = run(func(l *Limits) { l.FileText = int64(3 * len("/d/f00")) })
	if l.Seen != 11 || l.Kept != 3 || l.DroppedText != 8 || l.DroppedEntries != 0 || l.DroppedLongPath != 0 {
		t.Errorf("text bound: %+v", *l)
	}
	if l.MaxText != int64(3*len("/d/f00")) || l.MaxEntries != 200_000 || l.MaxPath != 4096 {
		t.Errorf("bounds recorded: %d %d %d", l.MaxEntries, l.MaxPath, l.MaxText)
	}
}

// TestListingPathAtLinuxsBound: a path of 4 KiB is kept, one byte more is
// dropped and counted as the length bound's.
func TestListingPathAtLinuxsBound(t *testing.T) {
	long := func(n int) string { // an absolute path of exactly n bytes
		s := "/" + strings.Repeat("seg456789/", (n-1)/10)
		return s + strings.Repeat("z", n-len(s))
	}
	at, over := long(4096), long(4097)
	if len(at) != 4096 || len(over) != 4097 {
		t.Fatalf("lengths %d %d", len(at), len(over))
	}
	b := fixture.OCI(fixture.Image{Layers: []fixture.Layer{{Files: []fixture.File{
		{Path: at, Body: []byte("a")}, {Path: over, Body: []byte("b")}, {Path: "etc/ok"},
	}}}})
	l := analyse(t, b, Options{Mode: ModeFiles}).Files
	if l.Seen != 3 || l.Kept != 2 || l.DroppedLongPath != 1 {
		t.Fatalf("listing: seen %d kept %d long %d", l.Seen, l.Kept, l.DroppedLongPath)
	}
	for _, e := range l.Layers[0].Entries {
		if e.Path == over {
			t.Fatal("the 4097-byte path was kept")
		}
	}
	if l.Layers[0].Entries[0].Path != at {
		t.Fatal("the 4096-byte path was not kept")
	}
}

// TestInstructionByLayer is task 1.4: the report says each layer's build
// instruction by 1-based layer, and "unknown" where the history is empty.
func TestInstructionByLayer(t *testing.T) {
	var img fixture.Image
	for i := 1; i <= 12; i++ {
		img.Layers = append(img.Layers, fixture.Layer{Files: []fixture.File{{Path: fmt.Sprintf("l%d", i)}}})
		cb := fmt.Sprintf("/bin/sh -c #(nop) ADD layer-%d /", i)
		switch i {
		case 7:
			cb = "RUN /bin/sh -c pip install --no-cache-dir flask # buildkit"
		case 3:
			cb = ""
		}
		img.History = append(img.History, fixture.History{CreatedBy: cb})
	}
	for _, mode := range []Mode{ModeFindings, ModeFiles} {
		rep := analyse(t, fixture.OCI(img), Options{Mode: mode})
		if got := rep.Instruction(7); got != "RUN pip install --no-cache-dir flask" {
			t.Errorf("mode %d: layer 7 is %q", mode, got)
		}
		if got := rep.Instruction(3); got != "unknown" {
			t.Errorf("mode %d: layer 3 is %q, want unknown", mode, got)
		}
		for _, n := range []int{0, 13} {
			if got := rep.Instruction(n); got != "unknown" {
				t.Errorf("layer %d is %q", n, got)
			}
		}
		if mode == ModeFiles && rep.Files.Layers[6].Instruction != rep.Instruction(7) {
			t.Errorf("listing's layer 7: %q", rep.Files.Layers[6].Instruction)
		}
	}
	// A history that does not map gives no instruction for any layer.
	img.History = img.History[:11]
	rep := analyse(t, fixture.OCI(img), Options{Mode: ModeFiles})
	if got := rep.Instruction(7); got != "unknown" {
		t.Errorf("unmapped history: layer 7 is %q", got)
	}
}

// TestListingNamesAZstdLayer is task 1.5: the layer the desk cannot decode
// yields no entry and is named, and every other layer is complete.
func TestListingNamesAZstdLayer(t *testing.T) {
	img := fixture.Image{Layers: []fixture.Layer{
		{Files: []fixture.File{{Path: "etc/os-release", Body: []byte("ID=demo")}}},
		{Zstd: true, Files: []fixture.File{{Path: "opt/app/run", Body: []byte("x")}}},
		{Gzip: true, Files: []fixture.File{{Path: "opt/app/config", Body: []byte("y")}, {Path: "opt/app/data", Body: []byte("z")}}},
	}}
	l := analyse(t, fixture.OCI(img), Options{Mode: ModeFiles}).Files
	un := l.Unread()
	if len(un) != 1 || un[0].Index != 2 || !strings.Contains(un[0].Unread, "zstd") {
		t.Fatalf("unread: %+v", un)
	}
	if len(l.Layers[1].Entries) != 0 {
		t.Fatalf("the zstd layer yielded entries: %v", names(l.Layers[1].Entries))
	}
	if len(l.Layers[0].Entries) != 1 || len(l.Layers[2].Entries) != 2 {
		t.Fatalf("other layers: %v / %v", names(l.Layers[0].Entries), names(l.Layers[2].Entries))
	}
}
