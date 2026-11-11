package fixture

import "archive/tar"

// DemoHostileName is the file name in DemoFiles that tries to act on a
// terminal: an ANSI colour, a newline that would forge a second listing
// row, and a byte that is not UTF-8.
const DemoHostileName = "\x1b[31mred\nforged-row\xff.html"

// DemoFiles is an nginx image with every case the Files view draws: a deep
// tree, a file a later layer replaces, a path a later layer deletes, an
// opaque directory, a symbolic link, a hard link, a character device, a
// FIFO and a hostile name. Its paths are the ones the sandbox's shop-web-1
// container changes (fakeengine.SeedDemo).
func DemoFiles(ref string) Image {
	dir := func(p string) File { return File{Path: p, Type: tar.TypeDir} }
	return Image{
		User: "", Cmd: []string{"nginx", "-g", "daemon off;"}, RepoTags: []string{ref},
		ExposedPorts: []string{"80/tcp"},
		Layers: []Layer{
			{CreatedBy: "/bin/sh -c #(nop) ADD file:6e5c1b0e31a7 in / ", Files: []File{
				dir("bin"), dir("dev"), dir("etc"), dir("run"), dir("tmp"), dir("usr"), dir("usr/bin"), dir("var"), dir("var/lib"),
				dir("var/cache"), dir("var/cache/apt"), dir("var/cache/apt/archives"), dir("var/lib/apt"), dir("var/lib/apt/lists"),
				{Path: "usr/bin/bash", Body: make([]byte, 1_265), Mode: 0o755},
				{Path: "usr/bin/gzip", Body: make([]byte, 940), Mode: 0o755},
				{Path: "usr/bin/gunzip", Type: tar.TypeLink, Link: "usr/bin/gzip"},
				{Path: "bin/sh", Type: tar.TypeSymlink, Link: "/usr/bin/bash"},
				{Path: "dev/null", Type: tar.TypeChar, Mode: 0o666},
				{Path: "dev/initctl", Type: tar.TypeFifo, Mode: 0o600},
				{Path: "etc/os-release", Body: []byte("PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\n")},
				{Path: "etc/passwd", Body: []byte("root:x:0:0:root:/root:/bin/bash\n")},
				{Path: "tmp/seed", Body: []byte("seed data for the first boot\n")},
				{Path: "var/cache/apt/archives/base-files_12.4.deb", Body: make([]byte, 2_000)},
				{Path: "var/lib/apt/lists/deb.debian.org_bookworm_InRelease", Body: make([]byte, 1_500)},
			}},
			{CreatedBy: "RUN /bin/sh -c apt-get update && apt-get install -y nginx=1.27.2 && rm -rf /var/lib/apt/lists/* && apt-get clean # buildkit",
				// The lists are deleted file by file; apt-get clean empties
				// the archive cache, which the snapshotter records as an
				// opaque directory.
				Delete: []string{"var/lib/apt/lists/deb.debian.org_bookworm_InRelease"},
				Opaque: []string{"var/cache/apt"},
				Files: []File{
					dir("etc/nginx"), dir("etc/nginx/conf.d"), dir("usr/sbin"), dir("var/cache/nginx"), dir("var/lib/nginx"),
					dir("usr/share"), dir("usr/share/nginx"), dir("usr/share/nginx/html"),
					dir("usr/lib"), dir("usr/lib/nginx"), dir("usr/lib/nginx/modules"),
					dir("var/cache/apt/archives"), {Path: "var/cache/apt/archives/lock"},
					{Path: "usr/sbin/nginx", Body: make([]byte, 1_430), Mode: 0o755},
					{Path: "etc/nginx/nginx.conf", Body: []byte("user nginx;\nworker_processes auto;\nevents {}\nhttp { include /etc/nginx/conf.d/*.conf; }\n")},
					{Path: "etc/nginx/conf.d/default.conf", Body: []byte("server { listen 80; root /usr/share/nginx/html; }\n")},
					{Path: "usr/lib/nginx/modules/ngx_http_geoip_module.so", Body: make([]byte, 820), Mode: 0o644},
					{Path: "usr/share/nginx/html/index.html", Body: []byte("<h1>Welcome to nginx!</h1>\n")},
				}},
			{CreatedBy: "COPY docker-entrypoint.sh html/ / # buildkit", Files: []File{
				{Path: "docker-entrypoint.sh", Body: []byte("#!/bin/sh\nexec \"$@\"\n"), Mode: 0o755},
				{Path: "usr/share/nginx/html/index.html", Body: []byte("<h1>shop</h1>\n<p>the sandbox's front page</p>\n")},
				{Path: "usr/share/nginx/html/" + DemoHostileName, Body: []byte("<p>a name chosen to break a listing</p>\n")},
				{Path: "usr/share/nginx/html/../../../../etc/nginx/traversal.conf", Body: []byte("# a name shaped like a traversal\n")},
			}},
		},
	}
}
