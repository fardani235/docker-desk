package freshness

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/authconfig"
	mobyregistry "github.com/moby/moby/api/types/registry"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/fardani235/docker-desk/internal/engine"
	"github.com/fardani235/docker-desk/internal/registry"
)

// Daemon is the part of the engine a check uses.
type Daemon interface {
	ImageFacts(ctx context.Context, id string) (engine.ImageFacts, error)
	DistributionInspect(ctx context.Context, ref, registryAuth string) (engine.Distribution, error)
}

// FallbackTimeout bounds the daemon's attempt.
var FallbackTimeout = 15 * time.Second

// Options configure a Checker.
type Options struct {
	Daemon Daemon
	// DaemonID names the daemon endpoint: fallback answers are kept per
	// daemon, because two daemons can see different mirrors.
	DaemonID string
	// ConfigDir is the Docker configuration directory ("" for none).
	ConfigDir string
	Client    *registry.Client
	Now       func() time.Time
}

// Checker runs checks and keeps their answers for the session. It is safe
// for concurrent use.
type Checker struct {
	o Options

	mu       sync.Mutex
	facts    map[string]engine.ImageFacts // by image ID
	remotes  map[string]registry.Remote   // by host/path:tag
	daemon   map[string]daemonAnswer      // by daemon|name:tag
	verdicts map[string]Verdict           // by image ID|ref — the list column
}

type daemonAnswer struct {
	d  engine.Distribution
	at time.Time
}

// New returns a Checker.
func New(o Options) *Checker {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Client == nil {
		o.Client = registry.New(registry.Options{})
	}
	return &Checker{o: o, facts: map[string]engine.ImageFacts{}, remotes: map[string]registry.Remote{},
		daemon: map[string]daemonAnswer{}, verdicts: map[string]Verdict{}}
}

// Disabled reports whether registry access is switched off.
func (c *Checker) Disabled() bool { return c.o.Client.Disabled() }

// Target is what a check would ask, said before it asks.
type Target struct {
	Ref, Host string
	// Credential names where a credential would come from, or "" when the
	// request would be anonymous. Resolving it never runs a helper.
	Credential string
	// NotApplicable holds the reason when there is nothing to ask.
	NotApplicable string
}

// Describe says, without a request and without running a helper, which
// host a check of ref would contact and whether a credential is involved.
func (c *Checker) Describe(ref string) Target {
	r, err := registry.ParseRef(ref)
	if err != nil {
		return Target{Ref: ref, NotApplicable: notApplicableReason(err)}
	}
	return Target{Ref: r.String(), Host: r.Host, Credential: registry.CredentialSource(c.o.ConfigDir, r.Key)}
}

// Cached returns the answer kept for an image and reference, if any.
func (c *Checker) Cached(imageID, ref string) (Verdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.verdicts[imageID+"|"+ref]
	return v, ok
}

// Any reports whether any check has answered this session.
func (c *Checker) Any() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.verdicts) > 0
}

// CheckOptions modify one check.
type CheckOptions struct {
	// Force asks again, replacing any kept answer.
	Force bool
	// NoQuotaFallback: a quota refusal is the answer (a batch stops on it
	// rather than spending the daemon's share of the same quota).
	NoQuotaFallback bool
}

// Check answers for one image and one of its references. The error is
// non-nil only when ctx was cancelled; every other outcome is a Verdict.
func (c *Checker) Check(ctx context.Context, imageID, ref string, o CheckOptions) (Verdict, error) {
	v, err := c.check(ctx, imageID, ref, o)
	if err != nil {
		return Verdict{}, err
	}
	c.mu.Lock()
	c.verdicts[imageID+"|"+ref] = v
	c.mu.Unlock()
	return v, nil
}

func notApplicableReason(err error) string {
	if errors.Is(err, registry.ErrNotApplicable) {
		return "the image is referenced by digest only: there is no tag to follow."
	}
	return "the reference cannot be parsed, so there is no tag to follow."
}

func (c *Checker) check(ctx context.Context, imageID, ref string, o CheckOptions) (Verdict, error) {
	now := c.o.Now()
	r, err := registry.ParseRef(ref)
	if err != nil {
		return Verdict{State: NotApplicable, Ref: ref, Reason: notApplicableReason(err), At: now}, nil
	}
	v := Verdict{Ref: r.String(), Host: r.Host, At: now}

	facts, err := c.localFacts(ctx, imageID)
	if err != nil {
		if ctx.Err() != nil {
			return Verdict{}, ctx.Err()
		}
		v.State, v.Reason = Unknown, "the daemon could not describe the local image ("+err.Error()+")."
		return v, nil
	}
	v.LocalCreated = facts.Created
	v.Platform = facts.Platform().String()
	local := registry.Local{Platform: ocispec.Platform{OS: facts.OS, Architecture: facts.Architecture, Variant: facts.Variant}}
	for _, rd := range facts.RepoDigests {
		if d, ok := r.SameRepository(rd); ok {
			local.Digest = d
			break
		}
	}
	if local.Digest == "" {
		v.State, v.Reason = NotApplicable, "the image has no recorded digest for "+r.Name+": it was built here or never pulled, so there is no tag to follow."
		return v, nil
	}
	v.LocalDigest = local.Digest
	// On a classic store the image ID is the config digest; on a containerd
	// store it is the index digest and matches no config. Either way it is
	// only a hint for skipping a request, never the comparison (U3).
	local.ConfigDigest = facts.ID

	if c.Disabled() {
		v.State, v.Reason = Unknown, "registry access is switched off (--no-registry); nothing was asked."
		return v, nil
	}

	key := r.Host + "/" + r.Path + ":" + r.Tag
	c.mu.Lock()
	rem, ok := c.remotes[key]
	c.mu.Unlock()
	if ok && !o.Force {
		return compare(v, facts, rem), nil
	}

	cred, credErr := registry.ResolveCredential(ctx, c.o.ConfigDir, r.Key)
	if errors.Is(credErr, context.Canceled) {
		return Verdict{}, credErr
	}
	v.Credential = cred.Source
	deskErr := credErr
	if credErr == nil {
		rem, deskErr = c.o.Client.Resolve(ctx, r, cred, local)
		if ctx.Err() != nil {
			return Verdict{}, ctx.Err()
		}
		if deskErr == nil {
			c.mu.Lock()
			c.remotes[key] = rem
			c.mu.Unlock()
			return compare(v, facts, rem), nil
		}
	}

	var re *registry.Error
	errors.As(deskErr, &re)
	if re != nil && re.Kind == registry.KindQuota {
		v.Quota, v.QuotaReset = true, re.Reset
	}
	if !registry.Fallback(deskErr) || (v.Quota && o.NoQuotaFallback) {
		return answerFromError(v, deskErr), nil
	}
	v.Reason = reason(deskErr)
	return c.fallback(ctx, v, r, cred, local, o)
}

// answerFromError turns a definitive refusal into a verdict.
func answerFromError(v Verdict, err error) Verdict {
	switch registry.KindOf(err) {
	case registry.KindNotFound:
		v.State = Gone
		return v
	}
	v.State, v.Reason = Unknown, reason(err)
	return v
}

// reason is an error's text for the tab, through the display sanitiser.
func reason(err error) string {
	var re *registry.Error
	if errors.As(err, &re) {
		s := re.Reason
		if re.Host != "" {
			s = re.Host + " " + s
		}
		return Sanitize(s) + "."
	}
	return Sanitize(err.Error()) + "."
}

func (c *Checker) localFacts(ctx context.Context, id string) (engine.ImageFacts, error) {
	c.mu.Lock()
	f, ok := c.facts[id]
	c.mu.Unlock()
	if ok {
		return f, nil
	}
	f, err := c.o.Daemon.ImageFacts(ctx, id)
	if err != nil {
		return f, err
	}
	c.mu.Lock()
	c.facts[id] = f
	c.mu.Unlock()
	return f, nil
}

func compare(v Verdict, facts engine.ImageFacts, rem registry.Remote) Verdict {
	v.RemoteDigest, v.At = rem.Digest, rem.At
	switch {
	case rem.Same:
		v.State = Current
	case rem.SameConfig:
		v.State, v.SameBuild = Current, true
	case rem.PlatformMissing:
		v.State, v.PlatformMissing = Unknown, true
		v.Reason = "the tag no longer publishes " + v.Platform + "; nothing was compared."
	default:
		v.State = Moved
		if cfg := rem.Config; cfg != nil {
			v.RemoteCreated = cfg.Created
			v.RemoteCreatedMissing = cfg.Created.IsZero()
			v.RemoteCreatedEpoch = !cfg.Created.IsZero() && cfg.Created.Unix() <= 0
			v.LayersKnown = true
			v.Local, v.RemoteLyrs = len(facts.DiffIDs), len(cfg.DiffIDs)
			v.Shared = sharedPrefix(facts.DiffIDs, cfg.DiffIDs)
		}
	}
	return v
}

// fallback asks the daemon, with the same credential encoded as the daemon
// decodes it.
func (c *Checker) fallback(ctx context.Context, v Verdict, r registry.Ref, cred registry.Credential, local registry.Local, o CheckOptions) (Verdict, error) {
	key := c.o.DaemonID + "|" + r.Name + ":" + r.Tag
	c.mu.Lock()
	ans, ok := c.daemon[key]
	c.mu.Unlock()
	if !ok || o.Force {
		auth, err := EncodeCredential(r, cred)
		if err != nil {
			v.State, v.Reason = Unknown, "the credential could not be encoded for the daemon."
			return v, nil
		}
		fctx, cancel := context.WithTimeout(ctx, FallbackTimeout)
		d, err := c.o.Daemon.DistributionInspect(fctx, r.Name+":"+r.Tag, auth)
		cancel()
		if ctx.Err() != nil {
			return Verdict{}, ctx.Err()
		}
		if err != nil {
			return daemonFailure(v, err), nil
		}
		ans = daemonAnswer{d: d, at: c.o.Now()}
		c.mu.Lock()
		c.daemon[key] = ans
		c.mu.Unlock()
	}
	v.Source, v.At, v.RemoteDigest = FromDaemon, ans.at, ans.d.Digest
	v.Quota, v.QuotaReset, v.Reason = false, time.Time{}, ""
	if ans.d.Digest == local.Digest {
		v.State = Current
		return v, nil
	}
	if len(ans.d.Platforms) > 0 {
		found := false
		for _, p := range ans.d.Platforms {
			if strings.EqualFold(p.Architecture, local.Platform.Architecture) && strings.EqualFold(p.OS, orLinux(local.Platform.OS)) &&
				(local.Platform.Variant == "" || p.Variant == "" || p.Variant == local.Platform.Variant) {
				found = true
			}
		}
		if !found {
			v.State, v.PlatformMissing = Unknown, true
			v.Reason = "the tag no longer publishes " + v.Platform + " (via the daemon); nothing was compared."
			return v, nil
		}
	}
	v.State = Moved
	return v, nil
}

func orLinux(s string) string {
	if s == "" {
		return "linux"
	}
	return s
}

// daemonFailure combines the desk's reason with the daemon's into one
// Unknown verdict — except where the daemon's answer is itself an answer.
func daemonFailure(v Verdict, err error) Verdict {
	switch engine.KindOf(err) {
	case engine.KindNotFound:
		v.Source, v.State, v.Reason = FromDaemon, Gone, ""
		v.Quota, v.QuotaReset = false, time.Time{}
		return v
	case engine.KindUnsupported:
		v.DaemonReason = "the daemon cannot ask: it does not serve the distribution endpoint."
	case engine.KindDenied:
		v.DaemonReason = "the registry refused the daemon's request (" + Sanitize(firstLine(err.Error())) + ")."
	default:
		v.DaemonReason = Sanitize(firstLine(err.Error())) + "."
	}
	v.State = Unknown
	return v
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// EncodeCredential is the credential as the daemon decodes X-Registry-Auth:
// authconfig.Encode, the daemon's own encoding. "" for no credential.
func EncodeCredential(r registry.Ref, cred registry.Credential) (string, error) {
	if !cred.Present() {
		return "", nil
	}
	return authconfig.Encode(mobyregistry.AuthConfig{
		Username:      cred.Username,
		Password:      cred.Password.Reveal(),
		IdentityToken: cred.IdentityToken.Reveal(),
		RegistryToken: cred.RegistryToken.Reveal(),
		ServerAddress: r.Key,
	})
}
