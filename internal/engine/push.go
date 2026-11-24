package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/client"
)

// pushOp is how a push is named in its errors.
const pushOp = "push image"

// PushableRef checks that ref names exactly one tag, as a push must: a
// repository with an explicit tag and no digest. The client library would
// otherwise fill in ":latest" for a bare name — a tag nobody chose — and a
// digest cannot be pushed at all. It returns the reference in its familiar
// form, the tag, and the registry domain it will be sent to.
func PushableRef(ref string) (familiar, tag, domain string, err error) {
	if strings.TrimSpace(ref) != ref || ref == "" {
		return "", "", "", errors.New("a push names one reference, with no spaces around it")
	}
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", "", "", fmt.Errorf("not a valid image reference: %v", err)
	}
	if _, ok := named.(reference.Digested); ok {
		return "", "", "", errors.New("a reference with a digest cannot be pushed: push the tag")
	}
	tagged, ok := named.(reference.Tagged)
	if !ok {
		return "", "", "", errors.New("a push needs an explicit tag; the desk never assumes :latest")
	}
	return reference.FamiliarString(named), tagged.Tag(), reference.Domain(named), nil
}

func (m *mobyEngine) PushImage(ctx context.Context, ref, registryAuth string, progress func(PushProgress)) (PushResult, error) {
	_, wantTag, _, err := PushableRef(ref)
	if err != nil {
		return PushResult{}, &Error{Kind: KindOther, Op: pushOp, Msg: err.Error() + "; nothing was sent", Err: err}
	}
	scrub := newScrubber(registryAuth)
	// All stays false: one tag. No Platform: the daemon pushes what the tag
	// names. No PrivilegeFunc: a refused credential is reported, never
	// retried with another one.
	resp, err := m.cli.ImagePush(ctx, ref, client.ImagePushOptions{RegistryAuth: registryAuth})
	if err != nil {
		return PushResult{}, m.pushError(err, scrub)
	}
	defer resp.Close()

	type layer struct {
		cur, total int64
		done       bool
	}
	layers := map[string]*layer{}
	var order []string
	var agg PushProgress
	var res PushResult
	var exists, pushed int
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return PushResult{}, m.pushError(err, scrub)
		}
		if msg.Error != nil {
			text := scrub.clean(msg.Error.Message)
			return PushResult{}, &Error{Kind: streamKind(text), Op: pushOp, Msg: text, Err: errors.New(text)}
		}
		if msg.Aux != nil {
			var aux struct {
				Tag    string
				Digest string
				Size   int64
			}
			// Only a report about the tag that was sent counts.
			if json.Unmarshal(*msg.Aux, &aux) == nil && isManifestDigest(aux.Digest) && aux.Tag == wantTag {
				res.Tag, res.Digest, res.Size = aux.Tag, aux.Digest, aux.Size
			}
		}
		status, id := scrub.clean(msg.Status), scrub.clean(msg.ID)
		if res.Digest == "" {
			if t, d, n, ok := parsePushDigest(status); ok && t == wantTag {
				res.Tag, res.Digest, res.Size = t, d, n
			}
		}
		if status != "" {
			agg.Status, agg.Layer = status, id
		}
		if id != "" && isLayerStatus(msg.Status) && (layers[id] != nil || len(layers) < maxPushLayers) {
			l := layers[id]
			if l == nil {
				l = &layer{}
				layers[id] = l
				order = append(order, id)
			}
			if msg.Progress != nil && msg.Progress.Total > 0 {
				if msg.Progress.Total > l.total {
					l.total = msg.Progress.Total
				}
				if msg.Progress.Current > l.cur {
					l.cur = min(msg.Progress.Current, l.total)
				}
			}
			switch msg.Status {
			case "Pushed":
				if !l.done {
					l.done, l.cur = true, l.total
					pushed++
				}
			case "Layer already exists":
				if !l.done {
					l.done, l.cur = true, l.total
					exists++
				}
			}
			if strings.HasPrefix(msg.Status, "Mounted from ") && !l.done {
				l.done, l.cur = true, l.total
				exists++
			}
		}
		agg.Current, agg.Total, agg.Done = 0, 0, 0
		for _, l := range layers {
			agg.Current += l.cur
			agg.Total += l.total
			if l.done {
				agg.Done++
			}
		}
		agg.Layers = len(order)
		if progress != nil {
			progress(agg)
		}
	}
	// A cancelled context can end the stream as a clean EOF: it is still a
	// cancellation, and nothing may be claimed.
	if err := ctx.Err(); err != nil {
		return PushResult{}, err
	}
	if res.Digest == "" {
		msg := "the daemon ended the push without reporting a digest: whether the remote tag moved is not known"
		return PushResult{}, &Error{Kind: KindOther, Op: pushOp, Msg: msg, Err: errors.New(msg)}
	}
	res.Layers, res.Pushed, res.Exists = len(order), pushed, exists
	return res, nil
}

// maxPushLayers bounds the layers a push tracks: a daemon that names more
// is not believed, and its extra layers are not counted (an image has at
// most a few hundred).
const maxPushLayers = 4096

// isLayerStatus reports whether a push status line is about one layer.
func isLayerStatus(s string) bool {
	switch s {
	case "Preparing", "Waiting", "Pushing", "Pushed", "Layer already exists", "Retrying":
		return true
	}
	return strings.HasPrefix(s, "Mounted from ") || strings.HasPrefix(s, "Retrying in ")
}

var (
	manifestDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// The daemon's final status: "<tag>: digest: sha256:… size: <n>".
	pushDigestLine = regexp.MustCompile(`^(\S+): digest: (sha256:[0-9a-f]{64}) size: ([0-9]+)$`)
)

func isManifestDigest(d string) bool { return manifestDigest.MatchString(d) }

func parsePushDigest(status string) (tag, digest string, size int64, ok bool) {
	m := pushDigestLine.FindStringSubmatch(status)
	if m == nil {
		return "", "", 0, false
	}
	n, err := strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		n = 0
	}
	return m[1], m[2], n, true
}

// streamKind classifies an error the daemon reported inside the push
// stream, where there is no HTTP status: the registry's refusals of the
// credential or the repository are KindDenied.
func streamKind(msg string) Kind {
	l := strings.ToLower(msg)
	for _, w := range []string{"unauthorized", "denied", "authentication required", "forbidden"} {
		if strings.Contains(l, w) {
			return KindDenied
		}
	}
	return KindOther
}

// pushError classifies a push request's failure and removes the credential
// from every part of it, the wrapped cause included: the daemon, or a proxy
// in front of it, may quote what it was sent.
func (m *mobyEngine) pushError(err error, s scrubber) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrReadOnly) {
		return err
	}
	kind := KindOf(wrap(pushOp, err))
	if cerrdefs.IsUnauthorized(err) || cerrdefs.IsPermissionDenied(err) {
		kind = KindDenied
	}
	w := m.wrap(pushOp, err)
	msg := cleanMessage(err)
	var e *Error
	if errors.As(w, &e) {
		msg = e.Msg
	}
	msg = s.clean(msg)
	return &Error{Kind: kind, Op: pushOp, Msg: msg, Err: errors.New(msg)}
}

// scrubber removes a registry credential from text the daemon sent back.
type scrubber struct{ secrets []string }

// newScrubber collects the encoded header and every secret field it
// carries, whatever their length: a very short secret mangles the text it is
// removed from, which is the lesser harm.
func newScrubber(registryAuth string) scrubber {
	var s scrubber
	add := func(v string) {
		if v != "" {
			s.secrets = append(s.secrets, v)
		}
	}
	add(registryAuth)
	if registryAuth != "" {
		if ac, err := authconfig.Decode(registryAuth); err == nil && ac != nil {
			add(ac.Password)
			add(ac.IdentityToken)
			add(ac.RegistryToken)
			add(ac.Auth)
		}
	}
	// Longest first: a secret inside another must not leave a fragment.
	sort.Slice(s.secrets, func(i, j int) bool { return len(s.secrets[i]) > len(s.secrets[j]) })
	return s
}

func (s scrubber) clean(text string) string {
	for _, v := range s.secrets {
		text = strings.ReplaceAll(text, v, "[credential]")
	}
	return text
}
