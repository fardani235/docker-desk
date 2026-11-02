package fixture

import "strings"

// The secrets the demo image carries. Tests look for these strings
// everywhere a secret must never appear.
const (
	DemoAWSKeyID  = "AKIAQX7DESKTESTK3Y9Z"
	DemoAWSSecret = "Zm9yZW5zaWNzLWRlbW8tc2VjcmV0LWtleS0wMDAx"
	DemoDBPass    = "s3cr3t-desk-db-pass"
	DemoNPMToken  = "npm_DeskDemoToken00000000000000000000000"
	DemoAPIToken  = "ghp_DeskDemoGitHubToken00000000000000001"
)

// Demo is an image with a little of everything the analysis reports: a
// credential file added and deleted in a later layer, a token replaced in a
// later layer, a build argument holding a token, package lists cleaned up
// one layer too late, setuid and capability bits, and binaries with and
// without hardening, one of them a Go program.
func Demo() Image {
	shell := ELF{Pad: 2048}
	ping := ELF{PIE: true, Dynamic: true, RELRO: true, BindNow: true, Canary: true}
	server := ELF{PIE: true, Dynamic: true, RELRO: true, Canary: false}
	tool := ELF{GoVersion: "go1.22.4", GoMod: "path\texample.com/desk/tool\nmod\texample.com/desk/tool\t(devel)\t\ndep\tgolang.org/x/sys\tv0.20.0\th1:abc=\ndep\tgithub.com/spf13/cobra\tv1.8.0\th1:def=\n"}
	packed := ELF{UPX: true}
	return Image{
		User:     "",
		Cmd:      []string{"/app/server"},
		Env:      []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "NODE_ENV=production"},
		RepoTags: []string{"shop/api:dev"},
		Layers: []Layer{
			{
				CreatedBy: "/bin/sh -c #(nop) ADD file:9a3c1f0e in / ",
				Files: []File{
					{Path: "bin", Type: '5'},
					{Path: "bin/sh", Body: shell.Bytes(), Mode: 0o755},
					{Path: "etc", Type: '5'},
					{Path: "etc/passwd", Body: []byte("root:x:0:0:root:/root:/bin/sh\n")},
					{Path: "usr/bin/passwd", Body: ping.Bytes(), Mode: 0o4755},
					{Path: "usr/bin/ping", Body: ping.Bytes(), Mode: 0o755, Xattrs: map[string]string{"security.capability": capNetRaw()}},
					{Path: "var/spool/drop", Type: '5', Mode: 0o777},
				},
			},
			{
				CreatedBy: "COPY . /root # buildkit",
				Files: []File{
					{Path: "root/.aws/credentials", Body: []byte("[default]\naws_access_key_id = " + DemoAWSKeyID + "\naws_secret_access_key = " + DemoAWSSecret + "\n")},
					{Path: "app/config.yml", Body: []byte("github:\n  token: " + DemoAPIToken + "\n")},
					{Path: "var/lib/apt/lists/deb.debian.org_debian_dists_bookworm_InRelease", Body: []byte(strings.Repeat("Package: x\n", 4000))},
					{Path: "var/lib/apt/lists/deb.debian.org_debian_dists_bookworm_main_binary-amd64_Packages.lz4", Body: []byte(strings.Repeat("P", 30000))},
				},
			},
			{
				CreatedBy: "RUN |1 NPM_TOKEN=" + DemoNPMToken + " /bin/sh -c rm -rf /root/.aws /var/lib/apt/lists/* # buildkit",
				Delete:    []string{"root/.aws", "var/lib/apt/lists/deb.debian.org_debian_dists_bookworm_InRelease", "var/lib/apt/lists/deb.debian.org_debian_dists_bookworm_main_binary-amd64_Packages.lz4"},
			},
			{
				CreatedBy: "COPY /out/ /app/ # buildkit",
				Gzip:      true,
				Files: []File{
					{Path: "app/server", Body: server.Bytes(), Mode: 0o755},
					{Path: "app/tool", Body: tool.Bytes(), Mode: 0o755},
					{Path: "app/helper", Body: packed.Bytes(), Mode: 0o755},
					{Path: "app/config.yml", Body: []byte("github:\n  token: ${GITHUB_TOKEN}\n")},
					{Path: "app/.env", Body: []byte("DB_PASSWORD=" + DemoDBPass + "\nLOG_LEVEL=info\n")},
					{Path: "app/.git/HEAD", Body: []byte("ref: refs/heads/main\n")},
					{Path: "app/.git/config", Body: []byte("[core]\n\tbare = false\n")},
				},
			},
		},
		History: []History{
			{CreatedBy: "/bin/sh -c #(nop) ADD file:9a3c1f0e in / "},
			{CreatedBy: `/bin/sh -c #(nop)  CMD ["sh"]`, EmptyLayer: true},
			{CreatedBy: "ARG NPM_TOKEN", EmptyLayer: true},
			{CreatedBy: "COPY . /root # buildkit"},
			{CreatedBy: "RUN |1 NPM_TOKEN=" + DemoNPMToken + " /bin/sh -c rm -rf /root/.aws /var/lib/apt/lists/* # buildkit"},
			{CreatedBy: "COPY /out/ /app/ # buildkit"},
			{CreatedBy: `CMD ["/app/server"]`, EmptyLayer: true},
		},
		SBOM: true, SBOMPackages: 12, Provenance: true,
		MissingAttestationBlobs: false,
		OtherPlatforms:          []string{"linux/arm64"},
	}
}

// DemoSecrets lists every secret value in Demo.
func DemoSecrets() []string {
	return []string{DemoAWSKeyID, DemoAWSSecret, DemoDBPass, DemoNPMToken, DemoAPIToken}
}

// capNetRaw is a security.capability value granting cap_net_raw, effective.
func capNetRaw() string {
	b := []byte{
		0x01, 0x00, 0x00, 0x02, // revision 2, effective
		0x00, 0x20, 0x00, 0x00, // permitted (low): bit 13, cap_net_raw
		0x00, 0x00, 0x00, 0x00, // inheritable (low)
		0x00, 0x00, 0x00, 0x00, // permitted (high)
		0x00, 0x00, 0x00, 0x00, // inheritable (high)
	}
	return string(b)
}

// Plain is a small, unremarkable image for the fake daemon's other images:
// a non-root user, a health check, and one small layer.
func Plain(ref string) Image {
	return Image{
		User:        "10001",
		Cmd:         []string{"/bin/app"},
		Healthcheck: []string{"CMD", "/bin/app", "-health"},
		RepoTags:    []string{ref},
		Layers: []Layer{{
			CreatedBy: "COPY app /bin/app # buildkit",
			Files: []File{
				{Path: "bin/app", Body: ELF{PIE: true, Dynamic: true, RELRO: true, BindNow: true, Canary: true}.Bytes(), Mode: 0o755},
				{Path: "etc/app.conf", Body: []byte("listen = :8080\n")},
			},
		}},
	}
}
