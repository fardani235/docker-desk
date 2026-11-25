package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fardani235/docker-desk/internal/dockerctx"
	"github.com/fardani235/docker-desk/internal/escape"
	"github.com/fardani235/docker-desk/internal/freshness"
	"github.com/fardani235/docker-desk/internal/plan"
	"github.com/fardani235/docker-desk/internal/registry"
)

// Images › p: push the highlighted row's existing tag to the registry it
// names (docs/DECISIONS.md, push-image P1–P8). The action ends in the typed
// confirmation every outward action uses; the job is plan.Execute's.

// pushRefusedNoRegistry is what --no-registry says to p.
const pushRefusedNoRegistry = "--no-registry: a push is a registry request, and none is made. Nothing was sent."

// pushAction is Images' p. mutating makes read-only refuse it in invokeOn;
// needsRow without setCapable refuses a selection as one-at-a-time.
var pushAction = action{key: "p", label: "push…", desc: "upload this tag to its registry; the remote tag is created or moved",
	needsRow: true, mutating: true, run: (*Model).preparePush}

// preparePush builds the push plan from a fresh snapshot and reads the
// credential's source once, for the dialog. Nothing is sent to the daemon
// that could change it, and no credential value is kept: the plan carries
// the source and a closure that resolves the credential again when the job
// starts.
func (m *Model) preparePush(r row) tea.Cmd {
	if m.opts.NoRegistry {
		m.setStatus(toneWarn, pushRefusedNoRegistry)
		return nil
	}
	if r.imageRef == "" {
		m.setStatus(toneWarn, "This image has no tag to push; nothing was sent. (t gives it a local tag first.)")
		return nil
	}
	id, ref := r.imageID, r.imageRef
	cfgDir := m.opts.DockerConfigDir
	warning := pushLinkWarning(m.endpoint, m.link)
	return m.prepare(func(s plan.Snapshot) (plan.Plan, error) {
		p, err := plan.Push(s, id, ref)
		if err != nil {
			return p, err
		}
		target, err := registry.ParseRef(ref)
		if err != nil {
			return plan.Plan{}, &plan.NotApplicable{Reason: fmt.Sprintf("%s cannot be pushed: %v", escape.Name(ref), err)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), registry.HelperTimeout+2*time.Second)
		defer cancel()
		cred, err := registry.ResolveCredential(ctx, cfgDir, target.Key)
		if err != nil {
			// How it failed, never what a helper said: registry.Error
			// carries no output.
			p.Blockers = append(p.Blockers, fmt.Sprintf("The credential for %s could not be read: %s. Nothing will be sent.",
				escape.Name(target.Key), escape.Name(err.Error())))
			return p, nil
		}
		source := credSource(cred)
		p.CredSource = source
		p.RegistryAuth = pushCredential(cfgDir, target, source)
		if source == "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("No credential for %s was found in the Docker configuration: the push is sent without one, and the registry may refuse it.", escape.Name(target.Domain)))
		}
		if warning != "" {
			p.Warnings = append(p.Warnings, warning)
		}
		return p, nil
	}, planMsg{})
}

// credSource is where a resolved credential came from, or "" when there is
// none to send.
func credSource(c registry.Credential) string {
	if !c.Present() {
		return ""
	}
	return c.Source
}

// pushCredential is the plan's RegistryAuth: the credential resolved again
// when the job starts — a `docker login` since the dialog opened is
// honoured — and encoded as the daemon reads X-Registry-Auth. If the
// configuration now names another source than the one confirmed, nothing is
// sent: the operator agreed to that source, not to whatever replaced it.
func pushCredential(cfgDir string, target registry.Ref, confirmed string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		cred, err := registry.ResolveCredential(ctx, cfgDir, target.Key)
		if err != nil {
			return "", err
		}
		if now := credSource(cred); now != confirmed {
			return "", fmt.Errorf("the credential source changed since the confirmation (was %s, now %s); push again to confirm it",
				plan.CredLabel(confirmed), plan.CredLabel(now))
		}
		return freshness.EncodeCredential(target, cred)
	}
}

// pushLinkWarning says so when the credential would travel to the daemon
// over a connection that does not protect it: TCP without TLS, or TLS that
// does not verify the daemon. A unix socket and an SSH tunnel need no
// warning.
func pushLinkWarning(ep dockerctx.Endpoint, link dockerctx.Link) string {
	if link.Transport != dockerctx.TransportTCP {
		return ""
	}
	host := escape.Name(ep.Host)
	switch {
	case ep.TLS == nil:
		return "The daemon is reached over unencrypted tcp:// (" + host + "): the credential (X-Registry-Auth) is sent to it over that unencrypted connection, readable by anyone on the path."
	case ep.TLS.SkipVerify:
		return "The daemon's TLS certificate is not verified (" + host + "): the credential (X-Registry-Auth) could be sent to an impostor."
	}
	return ""
}
