package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// The repository browser's two questions: which tags a repository lists,
// and what one tag names — each answered by GET requests on the registry
// itself (docs/DECISIONS.md, round twelve, L1–L6). Nothing here searches,
// asks a catalogue, or speaks to a registry's web API (hub.docker.com).

// PageSize is how many tags one tags/list request asks for.
const PageSize = 100

// TagsLimit bounds one tags/list answer.
const TagsLimit = 4 << 20

// Unauthorized401 is what a 401 on a repository means, said without
// claiming either half: Docker Hub, GHCR and others answer 401 both for a
// repository that does not exist and for one this credential may not see.
const Unauthorized401 = "the registry answered 401 - it does not have this repository, or this credential is not allowed to see it"

// ParseRepository normalises a repository name as the operator types it. A
// tag or digest typed with it is dropped: the browser lists the
// repository's tags. The result's Tag is empty.
func ParseRepository(s string) (Ref, error) {
	named, err := reference.ParseNormalizedNamed(strings.TrimSpace(s))
	if err != nil {
		return Ref{}, err
	}
	named = reference.TrimNamed(named)
	domain := reference.Domain(named)
	r := Ref{Name: named.Name(), Domain: domain, Host: domain, Path: reference.Path(named), Key: domain}
	if domain == "docker.io" {
		r.Host, r.Key = dockerHubHost, DockerHubKey
	}
	return r, nil
}

// Repository is the repository as the operator would type it: alpine,
// ghcr.io/acme/api, 127.0.0.1:5000/acme/api.
func (r Ref) Repository() string {
	if n := mustNamed(r.Name); n != nil {
		return reference.FamiliarName(n)
	}
	return r.Name
}

// WithTag is r naming tag.
func (r Ref) WithTag(tag string) Ref { r.Tag = tag; return r }

// tagName is the grammar of a tag. A name outside it is not drawn: it came
// from the registry, and may hold anything.
var tagName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// ValidTag reports whether s is a tag by the reference grammar.
func ValidTag(s string) bool { return tagName.MatchString(s) }

// TagPage is one tags/list answer.
type TagPage struct {
	// Tags are the valid tag names, in the registry's order.
	Tags []string
	// Invalid counts names that are not tags by the grammar; they are not
	// kept, and never drawn.
	Invalid int
	// Next is the cursor for the next page, from the Link header; "" when
	// the registry named none.
	Next string
	// LinkRefused says the registry named a next page on another host or
	// path, which was not followed.
	LinkRefused bool
	At          time.Time
}

// ListTags asks for one page of r's tags: the first when cursor is "",
// else the page the previous answer's Link named. The request is
// GET /v2/<path>/tags/list?n=100, and the next page is only the one the
// registry links to — on the same host and path.
func (c *Client) ListTags(ctx context.Context, r Ref, cred Credential, cursor string) (TagPage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	base := "/v2/" + r.Path + "/tags/list"
	path := base + "?n=" + strconv.Itoa(PageSize)
	if cursor != "" {
		if !strings.HasPrefix(cursor, base+"?") {
			return TagPage{}, &Error{Kind: KindRefused, Host: r.Host, Reason: "the page cursor names another repository"}
		}
		path = cursor
	}
	body, hdr, err := c.get(ctx, r, cred, path, "application/json", TagsLimit)
	if err != nil {
		return TagPage{}, repositoryError(err, r)
	}
	var doc struct {
		Tags []string `json:"tags"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return TagPage{}, &Error{Kind: KindUnreadable, Host: r.Host, Reason: "the tag list is not valid JSON"}
	}
	p := TagPage{At: time.Now()}
	for _, t := range doc.Tags {
		if ValidTag(t) {
			p.Tags = append(p.Tags, t)
		} else {
			p.Invalid++
		}
	}
	if link := nextLink(hdr.Get("Link")); link != "" {
		if next, ok := sameListing(r, base, path, link); ok {
			p.Next = next
		} else {
			p.LinkRefused = true
		}
	}
	return p, nil
}

// repositoryError rewrites the classes a repository request can mean
// differently from a manifest request: a 401 is never taken for absence.
func repositoryError(err error, r Ref) error {
	var e *Error
	if !errors.As(err, &e) {
		return err
	}
	switch e.Kind {
	case KindUnauthorized:
		reason := Unauthorized401
		if e.Anonymous {
			reason += " (the request was anonymous: no credential is configured for this host)"
		}
		return &Error{Kind: KindUnauthorized, Host: e.Host, Anonymous: e.Anonymous, Reason: reason, Err: e.Err}
	case KindNotFound:
		return &Error{Kind: KindNotFound, Host: e.Host, Reason: "the registry has no repository " + r.Path + " (404)", Err: e.Err}
	}
	return err
}

// nextLink is the target of a rel="next" link in a Link header.
func nextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "<") {
			continue
		}
		end := strings.IndexByte(part, '>')
		if end < 0 {
			continue
		}
		target, params := part[1:end], part[end+1:]
		for _, p := range strings.Split(params, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
			if strings.EqualFold(strings.TrimSpace(k), "rel") && strings.EqualFold(strings.Trim(strings.TrimSpace(v), `"`), "next") {
				return target
			}
		}
	}
	return ""
}

// sameListing resolves link against the request and accepts it only when
// it is the same host and the same tags/list path: a registry cannot send
// the desk — and its credential — anywhere else by way of a page.
func sameListing(r Ref, base, requested, link string) (string, bool) {
	from, err := url.Parse(schemeFor(r.Host) + "://" + r.Host + requested)
	if err != nil {
		return "", false
	}
	to, err := url.Parse(link)
	if err != nil {
		return "", false
	}
	u := from.ResolveReference(to)
	if u.Host != r.Host || u.Scheme != from.Scheme || u.Path != base || u.RawQuery == "" {
		return "", false
	}
	return u.Path + "?" + u.RawQuery, true
}

// Description is what one manifest request says about a tag: its digest,
// its media type and, for an index, the platforms it publishes. Nothing is
// walked to a child manifest or a config.
type Description struct {
	Digest    string
	MediaType string
	// Index: the tag names an index (or a manifest list); Platforms is
	// then what it publishes, attestations left out.
	Index     bool
	Platforms []ocispec.Platform
	At        time.Time
}

// Describe asks for the tag's manifest — one GET — and nothing else.
func (c *Client) Describe(ctx context.Context, r Ref, cred Credential) (Description, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	doc, dg, mt, err := c.manifest(ctx, r, cred, r.Tag)
	if err != nil {
		return Description{}, repositoryError(err, r)
	}
	d := Description{Digest: dg, MediaType: mt, At: time.Now()}
	if len(doc.Manifests) > 0 || mt == mediaOCIIndex || mt == mediaDockerList {
		d.Index = true
		d.Platforms = published(doc.Manifests)
	}
	return d, nil
}

// published lists an index's platforms, leaving out attestation manifests.
func published(ms []ocispec.Descriptor) []ocispec.Platform {
	var out []ocispec.Platform
	for _, m := range ms {
		if m.Platform == nil || m.Annotations["vnd.docker.reference.type"] != "" {
			continue
		}
		if m.Platform.OS == "unknown" && m.Platform.Architecture == "unknown" {
			continue
		}
		out = append(out, *m.Platform)
	}
	return out
}

// PlatformMatches reports whether p is a build for want, as Resolve
// chooses one: OS and architecture, and the variant when both name one.
func PlatformMatches(want, p ocispec.Platform) bool { return platformMatches(want, p) }

// IsIndex reports whether a media type is an index or a manifest list.
func IsIndex(mediaType string) bool {
	return mediaType == mediaOCIIndex || mediaType == mediaDockerList
}
