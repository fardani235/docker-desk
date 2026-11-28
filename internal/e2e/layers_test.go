package e2e

import (
	"strings"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// TestImageLayers opens Images › Layers on the sandbox's application image:
// the build's steps, the ones that made no layer, the holders after `s` —
// and nothing sent that could change the daemon. At 80 columns it runs in
// read-only mode, where the tab works the same.
func TestImageLayers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rows, cols int
		args       []string
	}{
		{"93-image-layers", 42, 120, nil},
		{"94-image-layers-80x24-read-only", 24, 80, []string{"--read-only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startFake(t, func(s *fakeengine.Server) {
				s.SeedDemo()
				s.SeedLayers()
			})
			d := launch(t, srv, tc.rows, tc.cols, tc.args...)
			d.see("2 Containers (13)")
			d.keys("1", ptyscreen.Down, "2", "/", "s", "t", "e", "p", "s", ptyscreen.Enter)
			d.see("3 Details: acme/steps:1")
			d.keys("3", "[") // from Info, back round to the last tab
			d.see("[Layers]", "8 steps · 5 layers", "Holders not known: s reads the layers of 17 other images")
			// The steps, oldest first, and the ones that made no layer.
			d.see("1 L1", "ADD file:51c0ffee in /", "2 config", "ENV LANG=C.UTF-8")
			if tc.rows >= 40 {
				d.see("3 L2", "RUN make build  (build args: BUILD_TOKEN)", "4 L3", "WORKDIR /app",
					"6 config", "7 L5", "8 config", `CMD ["/app/server"]`,
					"the archive's history (exact")
			}
			d.keys("s")
			d.see("3 of 5 layers are held by another image here")
			// The highlighted step's block names the holder.
			d.see("held by", "acme/steps-base:1")
			screen := d.shot(tc.name)
			if strings.Contains(screen, "tok-5ecr3t-value-0123456789") {
				t.Fatalf("the build argument's value is on screen:\n%s", screen)
			}
			// The same content at another depth: the empty layer is L3 here
			// and the base's second layer.
			d.keys("j", "j", "j")
			d.see("4 of 8", "L3 of 5")
			d.see("held by", "acme/steps-base:1")

			if n := srv.CountRequests("GET", `/images(/.+)?/get$`); n != 1 {
				t.Errorf("%d archive requests, want 1", n)
			}
			// One inspect and one history per image, each image once.
			if n := srv.CountRequests("GET", `^/images/.+/history$`); n != 18 {
				t.Errorf("%d history requests for 18 images", n)
			}
			for _, r := range srv.Requests() {
				if r.Mutating() {
					t.Fatalf("the Layers tab sent %s", r)
				}
				if r.Method != "GET" && r.Method != "HEAD" {
					t.Errorf("the Layers tab sent %s", r)
				}
			}
			d.keys("q")
			d.quit()
		})
	}
}
