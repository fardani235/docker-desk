package fixture

import "sort"

// The Changes demo: one tag, two builds. The first is what a container
// was created from; the second added two layers on the same base and
// changed the config. Values of environment variables differ between the
// builds and must never be shown (ChangesEnvValues lists them for tests).

// ChangesEnvValues are the environment values the two builds set; no view,
// job or log may contain any of them.
var ChangesEnvValues = []string{"rk-live-0ld-9f3c1d2e7a", "rk-live-n3w-4b8e6f0c2d", "/srv/report/fonts-z7q"}

// ChangesToken is the GitHub-shaped token the newer build's added layer
// carries in a file (a finding that appears).
const ChangesToken = "ghp_cHaNgEs0demo0token000000000000000000"

var reportBase = []Layer{
	{CreatedBy: "/bin/sh -c #(nop) ADD file:9e2b17ac43d0 in / ", Files: []File{
		{Path: "etc/os-release", Body: []byte("PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\nVERSION_ID=\"12\"\n")},
		{Path: "var/lib/dpkg/status", Body: DpkgStatus(debBase)},
	}},
	{CreatedBy: "RUN /bin/sh -c apt-get update && apt-get install -y --no-install-recommends ca-certificates openssl netbase python3 && rm -rf /var/lib/apt/lists/* # buildkit", Files: []File{
		{Path: "var/lib/dpkg/status", Body: DpkgStatus(reportRuntime(false))},
		{Path: "usr/bin/python3", Body: ELF{PIE: true, Dynamic: true, RELRO: true, BindNow: true, Canary: true}.Bytes(), Mode: 0o755},
	}},
	{CreatedBy: "COPY . /app # buildkit", Files: []File{
		{Path: "app/report/__init__.py", Body: []byte("VERSION = '1.4.0'\n")},
	}},
}

func reportRuntime(newer bool) []Pkg {
	out := append([]Pkg{}, debBase...)
	for _, p := range debRuntime {
		if newer && p.Name == "netbase" {
			continue // purged by the newer build
		}
		if !newer && (p.Name == "openssl" || p.Name == "libssl3") {
			p.Version = "3.0.11-1~deb12u2"
		}
		out = append(out, p)
	}
	out = append(out, Pkg{Name: "python3", Version: "3.11.2-1+b1", License: "PSF-2.0"})
	if newer {
		out = append(out,
			Pkg{Name: "curl", Version: "7.88.1-10+deb12u8", License: "curl"},
			Pkg{Name: "libcurl4", Version: "7.88.1-10+deb12u8", License: "curl"},
			Pkg{Name: "fonts-dejavu-core", Version: "2.37-6", Arch: "all", License: "Bitstream-Vera"},
		)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ChangesBase is a single build of the report image, for the cases where
// the tab has nothing to compare it with.
func ChangesBase(ref string) Image { return ChangesReport(ref, false) }

// ChangesReport is the older (newer=false) or newer build of the report
// image.
func ChangesReport(ref string, newer bool) Image {
	img := Image{
		Cmd:        []string{"python3", "-m", "report"},
		WorkingDir: "/app",
		Env:        []string{"PATH=/usr/local/bin:/usr/bin:/bin", "DEBUG=1", "REPORT_API_KEY=" + ChangesEnvValues[0]},
		Labels: map[string]string{
			"org.opencontainers.image.version":  "1.4.0",
			"org.opencontainers.image.revision": "3f9c2a1",
			"com.example.team":                  "reporting",
		},
		RepoTags: []string{ref},
		Layers:   append([]Layer{}, reportBase...),
	}
	if !newer {
		return img
	}
	img.User = "report"
	img.Cmd = []string{"python3", "-m", "report", "--serve"}
	img.ExposedPorts = []string{"8000/tcp"}
	img.Healthcheck = []string{"CMD", "curl", "-fsS", "http://localhost:8000/health"}
	img.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "REPORT_API_KEY=" + ChangesEnvValues[1], "FONT_DIR=" + ChangesEnvValues[2]}
	img.Labels = map[string]string{
		"org.opencontainers.image.version":  "1.4.2",
		"org.opencontainers.image.revision": "8d41e07",
		"com.example.team":                  "reporting",
	}
	img.Layers = append(img.Layers,
		Layer{CreatedBy: "RUN /bin/sh -c apt-get update && apt-get install -y curl fonts-dejavu-core && apt-get purge -y netbase && apt-get upgrade -y openssl && rm -rf /var/lib/apt/lists/* # buildkit", Files: []File{
			{Path: "var/lib/dpkg/status", Body: DpkgStatus(reportRuntime(true))},
			{Path: "usr/bin/curl", Body: ELF{PIE: true, Dynamic: true, RELRO: true, BindNow: true, Canary: true}.Bytes(), Mode: 0o755},
		}},
		Layer{CreatedBy: "COPY deploy/config.yml /app/config.yml # buildkit", Files: []File{
			{Path: "app/config.yml", Body: []byte("github:\n  token: " + ChangesToken + "\n")},
		}},
	)
	return img
}

// ChangesRebased is a newer build of the report image made on another
// base: only the first layer is shared.
func ChangesRebased(ref string) Image {
	img := ChangesReport(ref, false)
	img.Layers = []Layer{reportBase[0],
		{CreatedBy: "RUN /bin/sh -c apt-get update && apt-get install -y --no-install-recommends python3 && rm -rf /var/lib/apt/lists/* # buildkit", Files: []File{
			{Path: "var/lib/dpkg/status", Body: DpkgStatus(append(append([]Pkg{}, debBase...), Pkg{Name: "python3", Version: "3.11.2-1+b1"}))},
		}},
		{CreatedBy: "COPY . /app # buildkit", Files: []File{
			{Path: "app/report/__init__.py", Body: []byte("VERSION = '1.5.0'\n")},
		}},
	}
	return img
}
