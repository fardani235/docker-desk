// Package fixture builds image archives — the byte stream `docker save` (and
// the daemon's GET /images/get) produces — from a short description, in both
// of the layouts a daemon writes:
//
//   - Legacy: the classic docker-archive, one directory per layer holding an
//     uncompressed layer.tar, the config as <hex>.json, and manifest.json.
//   - OCI: an OCI image layout (blobs/sha256/…, oci-layout, index.json) with
//     Docker's manifest.json beside it, as the containerd image store and
//     Docker 25+ write. index.json points at a nested image index that also
//     lists attestation manifests and, optionally, platforms whose blobs are
//     not in the archive.
//
// It is used by the analyser's tests, by the fake daemon to serve archives,
// and so by the UI and end-to-end tests. Archives are built in memory.
package fixture

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// File is one entry of a layer.
type File struct {
	Path string // absolute or relative; stored without the leading slash
	Body []byte
	// Mode is the permission and special bits (0o4755 for setuid); 0 picks
	// 0644 for files and 0755 for directories.
	Mode int64
	// Type is a tar type flag; 0 means a regular file.
	Type byte
	// Link is the target of a symlink or hard link.
	Link string
	// Xattrs are extended attributes, e.g. "security.capability".
	Xattrs map[string]string
}

// Layer is one filesystem change set.
type Layer struct {
	Files []File
	// Delete names paths this layer removes (whiteouts).
	Delete []string
	// Opaque names directories whose lower contents this layer hides.
	Opaque []string
	// Gzip compresses the layer blob (OCI layout only; the legacy layout
	// always stores layers uncompressed, as the daemon does).
	Gzip bool
	// CreatedBy is the history entry that produced the layer.
	CreatedBy string
}

// History is one entry of the config's history array.
type History struct {
	CreatedBy  string
	EmptyLayer bool
}

// Image describes an image to build.
type Image struct {
	OS, Architecture, Variant string

	User        string
	Cmd         []string
	Entrypoint  []string
	Env         []string
	WorkingDir  string
	Healthcheck []string // nil: none; e.g. {"CMD", "true"}
	Labels      map[string]string

	Layers []Layer
	// History overrides the history derived from the layers (one non-empty
	// entry per layer, from CreatedBy).
	History []History

	RepoTags []string

	// SBOM and Provenance add attestation manifests (OCI layout only).
	SBOM, Provenance bool
	// SBOMPackages is the number of packages the SBOM lists.
	SBOMPackages int
	// MissingAttestationBlobs leaves the attestation layers out of the
	// archive while the attestation manifest still references them.
	MissingAttestationBlobs bool
	// OtherPlatforms lists platforms ("linux/arm64") the nested index
	// references but whose manifests are not in the archive.
	OtherPlatforms []string

	// ManifestFirst writes manifest.json (and index.json) before the blobs.
	ManifestFirst bool
}

// Built is an archive and the identifiers a daemon would report for it.
type Built struct {
	Archive []byte
	// ID is the image ID: the config digest in the legacy layout, the
	// digest of the image index in the OCI layout.
	ID           string
	ConfigDigest string
	// LayerPaths are the archive paths of the layers, in order.
	LayerPaths []string
}

const (
	mtOCIIndex    = "application/vnd.oci.image.index.v1+json"
	mtOCIManifest = "application/vnd.oci.image.manifest.v1+json"
	mtOCIConfig   = "application/vnd.oci.image.config.v1+json"
	mtLayerTar    = "application/vnd.oci.image.layer.v1.tar"
	mtLayerGzip   = "application/vnd.oci.image.layer.v1.tar+gzip"
	mtInToto      = "application/vnd.in-toto+json"
)

var epoch = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func hexOf(d string) string { return strings.TrimPrefix(d, "sha256:") }

// layerTar returns the uncompressed tar of a layer.
func layerTar(l Layer) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	write := func(h *tar.Header, body []byte) {
		if err := tw.WriteHeader(h); err != nil {
			panic(err)
		}
		if len(body) > 0 {
			if _, err := tw.Write(body); err != nil {
				panic(err)
			}
		}
	}
	for _, d := range l.Opaque {
		d = strings.Trim(d, "/")
		write(&tar.Header{Name: d + "/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: epoch, Format: tar.FormatPAX}, nil)
		write(&tar.Header{Name: d + "/.wh..wh..opq", Typeflag: tar.TypeReg, Mode: 0o644, ModTime: epoch}, nil)
	}
	for _, p := range l.Delete {
		p = strings.Trim(p, "/")
		dir, base := "", p
		if i := strings.LastIndex(p, "/"); i >= 0 {
			dir, base = p[:i+1], p[i+1:]
		}
		write(&tar.Header{Name: dir + ".wh." + base, Typeflag: tar.TypeReg, Mode: 0o644, ModTime: epoch}, nil)
	}
	for _, f := range l.Files {
		name := strings.TrimPrefix(f.Path, "/")
		h := &tar.Header{Name: name, Mode: f.Mode, ModTime: epoch, Typeflag: f.Type, Linkname: f.Link, Format: tar.FormatPAX}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Mode == 0 {
			h.Mode = 0o644
			if h.Typeflag == tar.TypeDir {
				h.Mode = 0o755
			}
		}
		if h.Typeflag == tar.TypeDir && !strings.HasSuffix(h.Name, "/") {
			h.Name += "/"
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(f.Body))
		}
		if len(f.Xattrs) > 0 {
			h.PAXRecords = map[string]string{}
			for k, v := range f.Xattrs {
				h.PAXRecords["SCHILY.xattr."+k] = v
			}
		}
		write(h, f.Body)
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

func (img Image) config(diffIDs []string) []byte {
	type healthcheck struct {
		Test []string `json:"Test"`
	}
	cfg := map[string]any{
		"User":       img.User,
		"Env":        img.Env,
		"Cmd":        img.Cmd,
		"Entrypoint": img.Entrypoint,
		"WorkingDir": img.WorkingDir,
		"Labels":     img.Labels,
	}
	if img.Healthcheck != nil {
		cfg["Healthcheck"] = healthcheck{Test: img.Healthcheck}
	}
	hist := img.History
	if hist == nil {
		for i, l := range img.Layers {
			cb := l.CreatedBy
			if cb == "" {
				cb = fmt.Sprintf("/bin/sh -c #(nop) ADD layer-%d /", i+1)
			}
			hist = append(hist, History{CreatedBy: cb})
		}
	}
	var history []map[string]any
	for _, h := range hist {
		e := map[string]any{"created": epoch.Format(time.RFC3339), "created_by": h.CreatedBy}
		if h.EmptyLayer {
			e["empty_layer"] = true
		}
		history = append(history, e)
	}
	osName, arch := img.OS, img.Architecture
	if osName == "" {
		osName = "linux"
	}
	if arch == "" {
		arch = "amd64"
	}
	doc := map[string]any{
		"architecture": arch,
		"os":           osName,
		"created":      epoch.Format(time.RFC3339),
		"config":       cfg,
		"rootfs":       map[string]any{"type": "layers", "diff_ids": diffIDs},
		"history":      history,
	}
	if img.Variant != "" {
		doc["variant"] = img.Variant
	}
	b, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return b
}

type entry struct {
	name string
	body []byte
	link string // a symlink entry when set
}

func writeArchive(entries []entry) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	for _, e := range entries {
		if i := strings.LastIndex(e.name, "/"); i > 0 {
			parts := strings.Split(e.name[:i], "/")
			for j := range parts {
				d := strings.Join(parts[:j+1], "/") + "/"
				if !dirs[d] {
					dirs[d] = true
					_ = tw.WriteHeader(&tar.Header{Name: d, Typeflag: tar.TypeDir, Mode: 0o755, ModTime: epoch})
				}
			}
		}
		if e.link != "" {
			_ = tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeSymlink, Linkname: e.link, Mode: 0o644, ModTime: epoch})
			continue
		}
		_ = tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeReg, Size: int64(len(e.body)), Mode: 0o644, ModTime: epoch})
		_, _ = tw.Write(e.body)
	}
	_ = tw.Close()
	return buf.Bytes()
}

func order(img Image, blobs, meta []entry) []entry {
	if img.ManifestFirst {
		return append(meta, blobs...)
	}
	return append(blobs, meta...)
}

// Legacy builds the classic docker-archive layout.
func Legacy(img Image) Built {
	var blobs []entry
	var diffIDs, paths []string
	seen := map[string]string{} // diff ID → path of its first copy
	for i, l := range img.Layers {
		t := layerTar(l)
		id := digest(t)
		diffIDs = append(diffIDs, id)
		// The daemon names layer directories by a v1 ID; any stable hex does.
		dir := hexOf(digest([]byte(fmt.Sprintf("v1-%d-%s", i, id))))
		p := dir + "/layer.tar"
		paths = append(paths, p)
		blobs = append(blobs, entry{name: dir + "/VERSION", body: []byte("1.0")})
		blobs = append(blobs, entry{name: dir + "/json", body: []byte(`{"id":"` + dir + `"}`)})
		if first, ok := seen[id]; ok {
			// An identical layer is stored once and linked, as the daemon does.
			blobs = append(blobs, entry{name: p, link: "../" + first})
			continue
		}
		seen[id] = p
		blobs = append(blobs, entry{name: p, body: t})
	}
	cfg := img.config(diffIDs)
	cd := digest(cfg)
	blobs = append(blobs, entry{name: hexOf(cd) + ".json", body: cfg})
	man, _ := json.Marshal([]map[string]any{{
		"Config": hexOf(cd) + ".json", "RepoTags": img.RepoTags, "Layers": paths,
	}})
	meta := []entry{{name: "manifest.json", body: man}}
	if len(img.RepoTags) > 0 {
		repos := map[string]map[string]string{}
		for _, t := range img.RepoTags {
			repo, tag := t, "latest"
			if c := strings.LastIndex(t, ":"); c > strings.LastIndex(t, "/") {
				repo, tag = t[:c], t[c+1:]
			}
			if repos[repo] == nil {
				repos[repo] = map[string]string{}
			}
			repos[repo][tag] = hexOf(digest([]byte(paths[len(paths)-1])))
		}
		rb, _ := json.Marshal(repos)
		meta = append(meta, entry{name: "repositories", body: rb})
	}
	return Built{Archive: writeArchive(order(img, blobs, meta)), ID: cd, ConfigDigest: cd, LayerPaths: paths}
}

type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int               `json:"size"`
	Platform    map[string]string `json:"platform,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func desc(mt string, b []byte) descriptor {
	return descriptor{MediaType: mt, Digest: digest(b), Size: len(b)}
}

func manifestDoc(cfg descriptor, layers []descriptor) []byte {
	b, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": mtOCIManifest, "config": cfg, "layers": layers,
	})
	return b
}

// OCI builds an OCI image layout with Docker's manifest.json beside it.
func OCI(img Image) Built {
	var blobs []entry
	var diffIDs, paths []string
	var layerDescs []descriptor
	have := map[string]bool{}
	addBlob := func(b []byte) string {
		d := digest(b)
		p := "blobs/sha256/" + hexOf(d)
		if !have[p] {
			have[p] = true
			blobs = append(blobs, entry{name: p, body: b})
		}
		return p
	}
	for _, l := range img.Layers {
		t := layerTar(l)
		diffIDs = append(diffIDs, digest(t))
		blob, mt := t, mtLayerTar
		if l.Gzip {
			blob, mt = gz(t), mtLayerGzip
		}
		paths = append(paths, addBlob(blob))
		layerDescs = append(layerDescs, desc(mt, blob))
	}
	cfg := img.config(diffIDs)
	addBlob(cfg)
	cfgDesc := desc(mtOCIConfig, cfg)
	man := manifestDoc(cfgDesc, layerDescs)
	addBlob(man)
	manDesc := desc(mtOCIManifest, man)
	osName, arch := img.OS, img.Architecture
	if osName == "" {
		osName = "linux"
	}
	if arch == "" {
		arch = "amd64"
	}
	manDesc.Platform = map[string]string{"os": osName, "architecture": arch}
	if img.Variant != "" {
		manDesc.Platform["variant"] = img.Variant
	}
	nested := []descriptor{manDesc}

	attest := func(predicate string, body []byte) {
		stmt, _ := json.Marshal(map[string]any{
			"_type": "https://in-toto.io/Statement/v0.1", "predicateType": predicate,
			"subject":   []map[string]any{{"name": "pkg:docker/image", "digest": map[string]string{"sha256": hexOf(manDesc.Digest)}}},
			"predicate": json.RawMessage(body),
		})
		ld := desc(mtInToto, stmt)
		ld.Annotations = map[string]string{"in-toto.io/predicate-type": predicate}
		if !img.MissingAttestationBlobs {
			addBlob(stmt)
		}
		acfg := []byte(`{"architecture":"unknown","os":"unknown","config":{},"rootfs":{"type":"layers","diff_ids":[]}}`)
		addBlob(acfg)
		am := manifestDoc(desc(mtOCIConfig, acfg), []descriptor{ld})
		addBlob(am)
		ad := desc(mtOCIManifest, am)
		ad.Platform = map[string]string{"os": "unknown", "architecture": "unknown"}
		ad.Annotations = map[string]string{
			"vnd.docker.reference.digest": manDesc.Digest,
			"vnd.docker.reference.type":   "attestation-manifest",
		}
		nested = append(nested, ad)
	}
	if img.SBOM {
		var pkgs []map[string]string
		for i := 0; i < img.SBOMPackages; i++ {
			pkgs = append(pkgs, map[string]string{"name": fmt.Sprintf("pkg-%d", i), "versionInfo": "1.0"})
		}
		body, _ := json.Marshal(map[string]any{"spdxVersion": "SPDX-2.3", "name": "sbom", "packages": pkgs})
		attest("https://spdx.dev/Document", body)
	}
	if img.Provenance {
		attest("https://slsa.dev/provenance/v0.2", []byte(`{"builder":{"id":"https://example.invalid/builder"},"buildType":"https://mobyproject.org/buildkit@v1"}`))
	}
	for _, p := range img.OtherPlatforms {
		parts := strings.SplitN(p, "/", 3)
		fake := []byte("absent manifest for " + p)
		d := desc(mtOCIManifest, fake) // never written: the blob is absent
		d.Platform = map[string]string{"os": parts[0], "architecture": parts[1]}
		nested = append(nested, d)
	}
	idx, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mtOCIIndex, "manifests": nested})
	addBlob(idx)
	idxDesc := desc(mtOCIIndex, idx)
	if len(img.RepoTags) > 0 {
		ref := img.RepoTags[0]
		idxDesc.Annotations = map[string]string{
			"io.containerd.image.name":          ref,
			"org.opencontainers.image.ref.name": ref[strings.LastIndex(ref, ":")+1:],
		}
	}
	top, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mtOCIIndex, "manifests": []descriptor{idxDesc}})
	dm, _ := json.Marshal([]map[string]any{{
		"Config": "blobs/sha256/" + hexOf(cfgDesc.Digest), "RepoTags": img.RepoTags, "Layers": paths,
	}})
	meta := []entry{
		{name: "index.json", body: top},
		{name: "manifest.json", body: dm},
		{name: "oci-layout", body: []byte(`{"imageLayoutVersion":"1.0.0"}`)},
	}
	return Built{Archive: writeArchive(order(img, blobs, meta)), ID: idxDesc.Digest, ConfigDigest: cfgDesc.Digest, LayerPaths: paths}
}
