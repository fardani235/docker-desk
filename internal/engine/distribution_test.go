package engine

import (
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

func TestDistributionInspect(t *testing.T) {
	for _, api := range []string{"", "1.40"} {
		srv, eng := startFake(t, func(s *fakeengine.Server) {
			s.AdvertiseAPI = api
			s.AddImage(1, 0, "acme/api:1")
			s.SetUpstream("acme/api:1", fakeengine.Upstream{Digest: "sha256:" + sixtyFour,
				Platforms: []fakeengine.UpstreamPlatform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "arm64", Variant: "v8"}}})
			s.SetUpstream("acme/api:gone", fakeengine.Upstream{Gone: true})
			s.SetUpstream("acme/api:private", fakeengine.Upstream{Denied: true})
		})
		ctx := ctxT(t)
		d, err := eng.DistributionInspect(ctx, "acme/api:1", "e30=")
		if err != nil || d.Digest != "sha256:"+sixtyFour || len(d.Platforms) != 2 || d.Platforms[1].Variant != "v8" {
			t.Fatalf("api %q: %+v %v", api, d, err)
		}
		if got := srv.LastRegistryAuth(); got != "e30=" {
			t.Errorf("X-Registry-Auth %q", got)
		}
		if _, err := eng.DistributionInspect(ctx, "acme/api:gone", ""); KindOf(err) != KindNotFound {
			t.Errorf("api %q gone: %v (kind %v)", api, err, KindOf(err))
		}
		if _, err := eng.DistributionInspect(ctx, "acme/api:private", ""); KindOf(err) != KindDenied {
			t.Errorf("api %q denied: %v", api, err)
		}
		srv.SetDistributionUnsupported(true)
		if _, err := eng.DistributionInspect(ctx, "acme/api:1", ""); KindOf(err) != KindUnsupported {
			t.Errorf("api %q unsupported: %v (kind %v)", api, err, KindOf(err))
		}
	}
}

func TestDistributionInspectReadOnly(t *testing.T) {
	srv := fakeengine.New()
	srv.AddImage(1, 0, "acme/api:1")
	srv.SetUpstream("acme/api:1", fakeengine.Upstream{})
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	eng, err := New(Options{Host: srv.Host(), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.DistributionInspect(ctxT(t), "acme/api:1", ""); err != nil {
		t.Fatalf("read-only: %v", err)
	}
	if m := srv.Mutations(); len(m) != 0 {
		t.Fatalf("mutations: %v", m)
	}
}

const sixtyFour = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
