package composefile

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCompare(t *testing.T) {
	t0 := time.Date(2026, 10, 27, 9, 13, 17, 0, time.UTC)
	ctr := func(service, name, image string) Actual {
		return Actual{Service: service, Container: name, Image: image, Normalized: Normalize(image), Created: t0, ConfigHash: "h-" + name}
	}
	cases := []struct {
		name    string
		file    File
		actual  []Actual
		verdict Verdict
		reason  string // substring
	}{
		{"literal image matches", File{Services: map[string]Service{"web": {Image: "nginx:1.27"}}},
			[]Actual{ctr("web", "p-web-1", "nginx:1.27")}, Match, ""},
		{"literal matches after normalising", File{Services: map[string]Service{"web": {Image: "docker.io/library/nginx"}}},
			[]Actual{ctr("web", "p-web-1", "nginx:latest")}, Match, ""},
		{"moved tag", File{Services: map[string]Service{"app": {Image: "app:1.2"}}},
			[]Actual{ctr("app", "p-app-1", "app:1.3")}, Differs, "tag"},
		{"other repository", File{Services: map[string]Service{"app": {Image: "app:1.2"}}},
			[]Actual{ctr("app", "p-app-1", "other:1.2")}, Differs, "repository"},
		{"template", File{Services: map[string]Service{"app": {Image: "app:${TAG:-latest}"}}},
			[]Actual{ctr("app", "p-app-1", "app:latest")}, NotCompared, "template"},
		{"bare variable", File{Services: map[string]Service{"app": {Image: "app:$TAG"}}},
			[]Actual{ctr("app", "p-app-1", "app:latest")}, NotCompared, "template"},
		{"profiled, no container", File{Services: map[string]Service{"ci": {Image: "runner:1", Profiles: []string{"ci"}}}},
			nil, NotCompared, "cannot know if they were enabled"},
		{"profiled, running", File{Services: map[string]Service{"ci": {Image: "runner:1", Profiles: []string{"ci"}}}},
			[]Actual{ctr("ci", "p-ci-1", "runner:1")}, Match, ""},
		{"declared only", File{Services: map[string]Service{"db": {Image: "postgres:16"}}},
			nil, DeclaredOnly, "no container"},
		{"present only", File{Services: map[string]Service{}},
			[]Actual{ctr("cache", "p-cache-1", "redis:7")}, PresentOnly, "does not declare it"},
		{"present only, but the file includes others", File{Includes: true, Services: map[string]Service{}},
			[]Actual{ctr("cache", "p-cache-1", "redis:7")}, NotCompared, "includes other files"},
		{"build without image", File{Services: map[string]Service{"api": {Build: true}}},
			[]Actual{ctr("api", "p-api-1", "p-api")}, NotCompared, "builds its image"},
		{"extends without image", File{Services: map[string]Service{"api": {Extends: true}}},
			[]Actual{ctr("api", "p-api-1", "p-api")}, NotCompared, "extends"},
		{"container known by ID", File{Services: map[string]Service{"web": {Image: "nginx:1.27"}}},
			[]Actual{ctr("web", "p-web-1", "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")}, NotCompared, "known only by ID"},
		{"one-off is not a container of the service", File{Services: map[string]Service{"web": {Image: "nginx:1.27"}}},
			[]Actual{{Service: "web", Container: "p-web-run-1a2b", Image: "nginx:1.27", OneOff: true}}, DeclaredOnly, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Compare(c.file, c.actual)
			if len(r.Rows) != 1 {
				t.Fatalf("%d rows: %+v", len(r.Rows), r.Rows)
			}
			row := r.Rows[0]
			if row.Verdict != c.verdict || !strings.Contains(row.Reason, c.reason) {
				t.Fatalf("%v (%q), want %v (%q)", row.Verdict, row.Reason, c.verdict, c.reason)
			}
			if r.Count(c.verdict) != 1 {
				t.Errorf("counts %v", r.Counts)
			}
		})
	}
}

func TestCompareOneOffsAreCountedNotCompared(t *testing.T) {
	r := Compare(File{Services: map[string]Service{"web": {Image: "nginx"}}}, []Actual{
		{Service: "web", Container: "p-web-1", Image: "nginx:latest"},
		{Service: "web", Container: "p-web-run-1", Image: "nginx:1.0", OneOff: true, Created: time.Now()},
	})
	if r.OneOffs != 1 || r.Rows[0].Verdict != Match || !reflect.DeepEqual(r.Rows[0].Containers, []string{"p-web-1"}) {
		t.Fatalf("%+v", r)
	}
	if r.NewestName != "p-web-1" {
		t.Errorf("a one-off set the newest creation: %q", r.NewestName)
	}
}

// Two files: service names are their union, and a service's image comes from
// the last file that declares one.
func TestCompareTwoFilesLastDeclarationWins(t *testing.T) {
	base := File{Name: "shop", Services: map[string]Service{
		"web": {Image: "nginx:1.26"},
		"db":  {Image: "postgres:16"},
	}}
	override := File{Services: map[string]Service{
		"web":   {Image: "nginx:1.27"},
		"debug": {Image: "busybox", Profiles: []string{"debug"}},
		"db":    {}, // mentions db without an image: base's stays
	}}
	f := Merge(base, override)
	r := Compare(f, []Actual{
		{Service: "web", Container: "shop-web-1", Image: "nginx:1.27"},
		{Service: "db", Container: "shop-db-1", Image: "postgres:16"},
	})
	got := map[string]Verdict{}
	for _, row := range r.Rows {
		got[row.Service] = row.Verdict
	}
	want := map[string]Verdict{"web": Match, "db": Match, "debug": NotCompared}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
	if r.DeclaredName != "shop" {
		t.Errorf("name %q", r.DeclaredName)
	}
	// The other order gives the other image.
	r = Compare(Merge(override, base), []Actual{{Service: "web", Container: "shop-web-1", Image: "nginx:1.27"}})
	if r.Rows[0].Service != "db" || r.Rows[0].Verdict != DeclaredOnly {
		t.Fatalf("rows %+v", r.Rows)
	}
	for _, row := range r.Rows {
		if row.Service == "web" && (row.Verdict != Differs || row.Declared != "nginx:1.26") {
			t.Fatalf("web: %+v", row)
		}
	}
}

func TestCompareOrdersDisagreementsFirst(t *testing.T) {
	f := File{Services: map[string]Service{
		"a-match":   {Image: "x:1"},
		"b-tmpl":    {Image: "x:${T}"},
		"c-differs": {Image: "x:1"},
		"d-missing": {Image: "x:1"},
		"e-missing": {Image: "x:1"},
	}}
	r := Compare(f, []Actual{
		{Service: "a-match", Container: "1", Image: "x:1"},
		{Service: "b-tmpl", Container: "2", Image: "x:1"},
		{Service: "c-differs", Container: "3", Image: "x:2"},
		{Service: "z-extra", Container: "4", Image: "x:1"},
	})
	var order []string
	for _, row := range r.Rows {
		order = append(order, row.Service)
	}
	want := []string{"d-missing", "e-missing", "c-differs", "z-extra", "b-tmpl", "a-match"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("%v, want %v", order, want)
	}
}

func TestWrittenRelative(t *testing.T) {
	c := time.Date(2026, 10, 27, 9, 13, 17, 0, time.UTC)
	for _, tc := range []struct {
		w    time.Time
		want string
	}{
		{c.Add(-4 * time.Second), "written 4s before these were created"},
		{c.Add(3 * time.Hour), "written 3h after these were created"},
		{c, "written the same second these were created"},
		{c.Add(-72 * time.Hour), "written 3d before these were created"},
	} {
		if got := WrittenRelative(tc.w, c); got != tc.want {
			t.Errorf("%q, want %q", got, tc.want)
		}
	}
	if !FileIsNewer(c.Add(time.Second), c) || FileIsNewer(c.Add(-time.Second), c) || FileIsNewer(c, time.Time{}) {
		t.Error("FileIsNewer")
	}
}
