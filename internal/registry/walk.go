package registry

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Local is what the desk already knows of the image on the daemon.
type Local struct {
	// Digest is the image's RepoDigests entry for the repository — the
	// digest the tag named when it was pulled. Never the image ID (U3).
	Digest string
	// ConfigDigest is the local config's digest when known (a classic
	// store's image ID); it lets the config request be skipped.
	ConfigDigest string
	Platform     ocispec.Platform
}

// Config is the part of an image config the comparison reads.
type Config struct {
	Digest string
	// Created is the producer's own claim; zero when absent.
	Created time.Time
	DiffIDs []string
	OS      string
	Arch    string
	Variant string
}

// Remote is what the registry said about a tag.
type Remote struct {
	// Digest is what the tag names now.
	Digest    string
	MediaType string
	// Platforms lists an index's platforms; empty for a single manifest.
	Platforms []ocispec.Platform
	// ChildDigest is the platform manifest chosen from an index.
	ChildDigest  string
	ConfigDigest string
	// Config is nil when it was not needed or not reached.
	Config *Config
	// Same: the tag still names the local digest. SameConfig: it names a
	// different manifest whose build for this platform is the local one.
	Same, SameConfig bool
	// PlatformMissing: the index no longer publishes the local platform.
	PlatformMissing bool
	At              time.Time
}

type manifestDoc struct {
	MediaType string               `json:"mediaType"`
	Manifests []ocispec.Descriptor `json:"manifests"`
	Config    *ocispec.Descriptor  `json:"config"`
}

// Resolve walks tag → (index → platform manifest) → config, skipping every
// request whose answer is already known: one request when the tag still
// names the local digest. The whole walk is bounded by the client's timeout.
func (c *Client) Resolve(ctx context.Context, r Ref, cred Credential, local Local) (Remote, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	rem := Remote{At: time.Now()}

	doc, dg, mt, err := c.manifest(ctx, r, cred, r.Tag)
	if err != nil {
		return Remote{}, err
	}
	rem.Digest, rem.MediaType = dg, mt
	if local.Digest != "" && dg == local.Digest {
		rem.Same = true
		return rem, nil
	}

	if len(doc.Manifests) > 0 || mt == mediaOCIIndex || mt == mediaDockerList {
		var child *ocispec.Descriptor
		for i := range doc.Manifests {
			d := doc.Manifests[i]
			if d.Platform == nil || d.Annotations["vnd.docker.reference.type"] != "" {
				continue
			}
			rem.Platforms = append(rem.Platforms, *d.Platform)
			if child == nil && platformMatches(local.Platform, *d.Platform) {
				child = &doc.Manifests[i]
			}
		}
		if child == nil {
			rem.PlatformMissing = true
			return rem, nil
		}
		rem.ChildDigest = child.Digest.String()
		if rem.ChildDigest == local.Digest {
			rem.SameConfig = true
			return rem, nil
		}
		if doc, _, _, err = c.manifest(ctx, r, cred, rem.ChildDigest); err != nil {
			return Remote{}, err
		}
		if doc.Config == nil {
			return Remote{}, &Error{Kind: KindUnreadable, Host: r.Host, Reason: "the platform manifest names no config"}
		}
	}
	if doc.Config == nil {
		return Remote{}, &Error{Kind: KindUnreadable, Host: r.Host, Reason: "the manifest names no config"}
	}
	rem.ConfigDigest = doc.Config.Digest.String()
	if local.ConfigDigest != "" && rem.ConfigDigest == local.ConfigDigest {
		rem.SameConfig = true
		return rem, nil
	}
	cfg, err := c.config(ctx, r, cred, *doc.Config)
	if err != nil {
		return Remote{}, err
	}
	rem.Config = &cfg
	if rem.ChildDigest == "" && local.Platform.Architecture != "" && cfg.Arch != "" &&
		!platformMatches(local.Platform, ocispec.Platform{OS: cfg.OS, Architecture: cfg.Arch, Variant: cfg.Variant}) {
		rem.PlatformMissing = true
	}
	return rem, nil
}

func (c *Client) manifest(ctx context.Context, r Ref, cred Credential, ref string) (manifestDoc, string, string, error) {
	body, hdr, err := c.get(ctx, r, cred, "/v2/"+r.Path+"/manifests/"+ref, manifestAccept, ManifestLimit)
	if err != nil {
		return manifestDoc{}, "", "", err
	}
	dg := digestOf(body)
	if h := hdr.Get("Docker-Content-Digest"); strings.HasPrefix(h, "sha256:") && h != dg {
		return manifestDoc{}, "", "", &Error{Kind: KindUnreadable, Host: r.Host, Reason: "the manifest does not match the digest the registry named"}
	}
	if strings.HasPrefix(ref, "sha256:") && ref != dg {
		return manifestDoc{}, "", "", &Error{Kind: KindUnreadable, Host: r.Host, Reason: "the manifest does not match the digest it was asked for"}
	}
	var doc manifestDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return manifestDoc{}, "", "", &Error{Kind: KindUnreadable, Host: r.Host, Reason: "the manifest is not valid JSON"}
	}
	mt := doc.MediaType
	if mt == "" {
		mt, _, _ = strings.Cut(hdr.Get("Content-Type"), ";")
	}
	return doc, dg, mt, nil
}

// config fetches a config blob, from the cache when its digest was read
// before, and checks the digest before reading it.
func (c *Client) config(ctx context.Context, r Ref, cred Credential, d ocispec.Descriptor) (Config, error) {
	key := d.Digest.String()
	c.mu.Lock()
	cfg, ok := c.configs[key]
	c.mu.Unlock()
	if ok {
		return cfg, nil
	}
	body, _, err := c.get(ctx, r, cred, "/v2/"+r.Path+"/blobs/"+key, "", ConfigLimit)
	if err != nil {
		return Config{}, err
	}
	if digestOf(body) != key {
		return Config{}, &Error{Kind: KindUnreadable, Host: r.Host, Reason: "the config does not match the digest its manifest names"}
	}
	var img ocispec.Image
	if err := json.Unmarshal(body, &img); err != nil {
		return Config{}, &Error{Kind: KindUnreadable, Host: r.Host, Reason: "the config is not valid JSON"}
	}
	cfg = Config{Digest: key, OS: img.OS, Arch: img.Architecture, Variant: img.Variant}
	if img.Created != nil {
		cfg.Created = *img.Created
	}
	for _, id := range img.RootFS.DiffIDs {
		cfg.DiffIDs = append(cfg.DiffIDs, id.String())
	}
	c.mu.Lock()
	c.configs[key] = cfg
	c.mu.Unlock()
	return cfg, nil
}

// platformMatches compares OS and architecture, and the variant when both
// sides name one.
func platformMatches(local, p ocispec.Platform) bool {
	if local.Architecture == "" {
		return p.OS == "linux" && p.Architecture == "amd64"
	}
	if !strings.EqualFold(p.OS, orLinux(local.OS)) || !strings.EqualFold(p.Architecture, local.Architecture) {
		return false
	}
	return local.Variant == "" || p.Variant == "" || p.Variant == local.Variant
}

func orLinux(s string) string {
	if s == "" {
		return "linux"
	}
	return s
}
