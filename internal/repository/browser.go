package repository

import (
	"context"
	"errors"
	"sync"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/fardani235/docker-desk/internal/registry"
)

// Options configure a Browser.
type Options struct {
	Client *registry.Client
	// ConfigDir is the Docker configuration directory ("" for none).
	ConfigDir string
}

// Browser keeps what the repository view learned this session: each tag's
// description and build time, by host/path:tag, with the time it was
// obtained. It is safe for concurrent use. Nothing expires and nothing is
// asked again unless the operator asks.
type Browser struct {
	o Options

	mu    sync.Mutex
	descs map[string]registry.Description
	times map[string]BuildTime
}

// New returns a Browser.
func New(o Options) *Browser {
	if o.Client == nil {
		o.Client = registry.New(registry.Options{})
	}
	return &Browser{o: o, descs: map[string]registry.Description{}, times: map[string]BuildTime{}}
}

// Disabled reports whether registry access is switched off.
func (b *Browser) Disabled() bool { return b.o.Client.Disabled() }

// Requests is how many HTTP requests the browser's client has sent.
func (b *Browser) Requests() int64 { return b.o.Client.Requests() }

// CredentialSource names where a credential for repo would come from, or ""
// for an anonymous request. It runs no helper.
func (b *Browser) CredentialSource(repo registry.Ref) string {
	return registry.CredentialSource(b.o.ConfigDir, repo.Key)
}

// Credential resolves repo's credential afresh (a helper may run).
func (b *Browser) Credential(ctx context.Context, repo registry.Ref) (registry.Credential, error) {
	return registry.ResolveCredential(ctx, b.o.ConfigDir, repo.Key)
}

// Key is how a tag is kept: host/path:tag.
func Key(repo registry.Ref, tag string) string { return repo.Host + "/" + repo.Path + ":" + tag }

// ListTags asks for one page.
func (b *Browser) ListTags(ctx context.Context, repo registry.Ref, cursor string) (registry.TagPage, error) {
	cred, err := b.Credential(ctx, repo)
	if err != nil {
		return registry.TagPage{}, err
	}
	return b.o.Client.ListTags(ctx, repo, cred, cursor)
}

// Describe asks what tag names — one manifest request — and keeps the answer.
func (b *Browser) Describe(ctx context.Context, repo registry.Ref, tag string) (registry.Description, error) {
	cred, err := b.Credential(ctx, repo)
	if err != nil {
		return registry.Description{}, err
	}
	d, err := b.o.Client.Describe(ctx, repo.WithTag(tag), cred)
	if err != nil {
		return d, err
	}
	b.mu.Lock()
	b.descs[Key(repo, tag)] = d
	b.mu.Unlock()
	return d, nil
}

// Described returns the kept description of a tag.
func (b *Browser) Described(repo registry.Ref, tag string) (registry.Description, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.descs[Key(repo, tag)]
	return d, ok
}

// Forget drops everything kept about repo: r in the view asks again.
func (b *Browser) Forget(repo registry.Ref) {
	prefix := repo.Host + "/" + repo.Path + ":"
	b.mu.Lock()
	defer b.mu.Unlock()
	for k := range b.descs {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(b.descs, k)
		}
	}
	for k := range b.times {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			delete(b.times, k)
		}
	}
}

// BuildTime is when one platform's build of a tag says it was made.
type BuildTime struct {
	// Digest is what the tag named when this was read.
	Digest string
	// Platform names the build the time is from: os/arch[/variant], as its
	// config says. For an index that is the one child chosen for the
	// daemon's platform; the other children may differ.
	Platform string
	// Created is the config's own claim; zero when Missing or Epoch.
	Created        time.Time
	Missing, Epoch bool
	// PlatformMissing: the index publishes no build for the platform asked;
	// there is no time.
	PlatformMissing bool
	At              time.Time
}

// TimeOf returns the kept build time of a tag.
func (b *Browser) TimeOf(repo registry.Ref, tag string) (BuildTime, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.times[Key(repo, tag)]
	return t, ok
}

// BatchResult is how a build-time batch ended.
type BatchResult struct {
	Read, Failed int
	// Quota: the registry refused for quota and the batch stopped there.
	Quota bool
	Reset time.Time
	// Cancelled: the operator stopped it.
	Cancelled bool
	// Err is why nothing was asked: the credential could not be resolved.
	Err error
}

// BuildTimes reads, one tag after another, the build time of each tag's
// build for platform: up to three requests a tag (the tag's manifest, the
// platform's manifest, its config — Resolve skips the config when its
// digest was read before). It is shaped like freshness.Batch: a quota
// refusal stops it, cancellation stops it before the next request, and
// what was read is kept either way. Another failure is that tag's answer
// and the batch goes on. each is called after every tag.
func (b *Browser) BuildTimes(ctx context.Context, repo registry.Ref, tags []string, platform ocispec.Platform, each func(done int, tag string, t BuildTime, err error)) BatchResult {
	var res BatchResult
	cred, err := b.Credential(ctx, repo)
	if err != nil {
		if ctx.Err() != nil {
			res.Cancelled = true
			return res
		}
		res.Err = err
		return res
	}
	for i, tag := range tags {
		if ctx.Err() != nil {
			res.Cancelled = true
			return res
		}
		rem, err := b.o.Client.Resolve(ctx, repo.WithTag(tag), cred, registry.Local{Platform: platform})
		if ctx.Err() != nil {
			res.Cancelled = true
			return res
		}
		var bt BuildTime
		if err != nil {
			res.Failed++
		} else {
			res.Read++
			bt = buildTime(rem)
			d := registry.Description{Digest: rem.Digest, MediaType: rem.MediaType, Platforms: rem.Platforms, At: rem.At,
				Index: registry.IsIndex(rem.MediaType) || len(rem.Platforms) > 0}
			b.mu.Lock()
			b.times[Key(repo, tag)] = bt
			b.descs[Key(repo, tag)] = d
			b.mu.Unlock()
		}
		if each != nil {
			each(i+1, tag, bt, err)
		}
		var re *registry.Error
		if errors.As(err, &re) && re.Kind == registry.KindQuota {
			res.Quota, res.Reset = true, re.Reset
			return res
		}
	}
	return res
}

func buildTime(rem registry.Remote) BuildTime {
	bt := BuildTime{Digest: rem.Digest, At: rem.At, PlatformMissing: rem.PlatformMissing}
	if cfg := rem.Config; cfg != nil && !rem.PlatformMissing {
		bt.Platform = PlatformString(ocispec.Platform{OS: cfg.OS, Architecture: cfg.Arch, Variant: cfg.Variant})
		switch {
		case cfg.Created.IsZero():
			bt.Missing = true
		case cfg.Created.Unix() <= 0:
			bt.Epoch = true
		default:
			bt.Created = cfg.Created
		}
	}
	return bt
}
