package plan

import (
	"reflect"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

// The seeded shop project, projected for the compose-file comparison, with a
// `docker compose run` container added: that one is left out.
func TestComposeActualOverSeededShop(t *testing.T) {
	r := newRig(t, func(s *fakeengine.Server) {
		s.SeedDemo()
		s.AddContainer(fakeengine.ContainerSpec{
			Name: "shop-api-run-3f2a", Image: "shop/api:dev", State: "exited", Age: time.Minute,
			Labels: map[string]string{
				"com.docker.compose.project": "shop", "com.docker.compose.service": "api",
				"com.docker.compose.oneoff": "True",
			},
		})
	})
	snap := r.snap()
	got := ComposeActual(snap, "shop")
	var names, services, normal []string
	for _, a := range got {
		names = append(names, a.Container)
		services = append(services, a.Service)
		normal = append(normal, a.Normalized)
		if len(a.ConfigHash) != 64 || a.Created.IsZero() || a.Image == "" {
			t.Errorf("%s: %+v", a.Container, a)
		}
	}
	if want := []string{"shop-api-1", "shop-db-1", "shop-web-1", "shop-worker-1"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("containers %v, want %v (the one-off excluded)", names, want)
	}
	if want := []string{"api", "db", "web", "worker"}; !reflect.DeepEqual(services, want) {
		t.Errorf("services %v", services)
	}
	if want := []string{"shop/api:dev", "postgres:16", "nginx:1.27", "shop/worker:dev"}; !reflect.DeepEqual(normal, want) {
		t.Errorf("normalised %v", normal)
	}
	p, _ := project(snap, "shop")
	if paths := p.ConfigPaths(); len(paths) != 1 || paths[0] != "/home/dev/src/shop/compose.yaml" {
		t.Errorf("config paths %v", paths)
	}
	if got := (Project{ConfigFiles: "/a/compose.yaml, /a/compose.override.yaml,"}).ConfigPaths(); !reflect.DeepEqual(got, []string{"/a/compose.yaml", "/a/compose.override.yaml"}) {
		t.Errorf("two paths: %v", got)
	}
}
