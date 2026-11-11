package forensics

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
)

type manifestEntry struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

type configDoc struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant"`
	Config       struct {
		User        string   `json:"User"`
		Env         []string `json:"Env"`
		Cmd         []string `json:"Cmd"`
		Entrypoint  []string `json:"Entrypoint"`
		WorkingDir  string   `json:"WorkingDir"`
		Healthcheck *struct {
			Test []string `json:"Test"`
		} `json:"Healthcheck"`
	} `json:"config"`
	RootFS struct {
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
	History []struct {
		CreatedBy  string `json:"created_by"`
		EmptyLayer bool   `json:"empty_layer"`
	} `json:"history"`
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Platform    *ociPlatform      `json:"platform"`
	Annotations map[string]string `json:"annotations"`
}

type ociPlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
}

type ociIndex struct {
	MediaType string          `json:"mediaType"`
	Manifests []ociDescriptor `json:"manifests"`
	// An image manifest has these instead.
	Config *ociDescriptor  `json:"config"`
	Layers []ociDescriptor `json:"layers"`
}

func blobPath(digest string) string {
	alg, hex, ok := strings.Cut(digest, ":")
	if !ok {
		return ""
	}
	return "blobs/" + alg + "/" + hex
}

func (a *analyser) finish() (*Report, error) {
	rep := &Report{ImageID: a.opts.ImageID, Platform: a.opts.Platform, BytesRead: a.read_.Load(), Layout: "docker-archive"}
	if _, ok := a.meta["index.json"]; ok {
		rep.Layout = "oci"
	} else if _, ok := a.meta["oci-layout"]; ok {
		rep.Layout = "oci"
	}
	entry, ok := a.manifest()
	if !ok {
		entry, ok = a.manifestFromIndex()
		if !ok {
			return nil, failf("the archive has no manifest.json, and index.json does not lead to a manifest whose blobs are in it")
		}
		a.note("there is no manifest.json; the config and layer order were read from index.json")
	}

	var cfg *configDoc
	if b, ok := a.json[a.resolve(entry.Config)]; ok {
		var c configDoc
		if json.Unmarshal(b, &c) == nil {
			cfg = &c
		}
	}
	if cfg == nil {
		a.note("the image config (%s) is not in the archive or is not readable: config rules did not run", sanitize(entry.Config, 80))
	} else {
		rep.Config = Config{
			OS: cfg.OS, Architecture: cfg.Architecture, Variant: cfg.Variant,
			User: cfg.Config.User, Cmd: cfg.Config.Cmd, Entrypoint: cfg.Config.Entrypoint,
			Env: cfg.Config.Env, WorkingDir: cfg.Config.WorkingDir, Present: true,
		}
		if hc := cfg.Config.Healthcheck; hc != nil {
			rep.Config.Healthcheck = append([]string{}, hc.Test...)
			if rep.Config.Healthcheck == nil {
				rep.Config.Healthcheck = []string{}
			}
		}
	}

	scans := make([]*layerScan, len(entry.Layers))
	for i, lp := range entry.Layers {
		l := Layer{Index: i + 1, Path: sanitize(lp, 200)}
		s := a.scans[a.resolve(lp)]
		if s == nil {
			l.Missing = true
			a.note("layer %d (%s) is listed by the manifest but not in the archive: its files are unknown", i+1, sanitize(lp, 80))
		} else {
			scans[i] = s
			l.Digest, l.DiffID, l.Compression = s.digest, s.diffID, s.compression
			l.Stored, l.Size, l.Files, l.Dirs = s.stored, s.size, s.files, s.dirs
			l.Whiteouts, l.Opaques, l.Unreadable = len(s.whiteouts), len(s.opaques), s.unreadable
			if s.unreadable != "" {
				a.note("layer %d could not be walked: %s", i+1, s.unreadable)
			}
			if cfg != nil && i < len(cfg.RootFS.DiffIDs) && s.diffID != "" {
				l.Verified = cfg.RootFS.DiffIDs[i] == s.diffID
				if !l.Verified {
					a.note("layer %d does not hash to the config's diff_id: its bytes are not the ones the config names", i+1)
				}
			}
			if s.pathsCapped {
				a.note("layer %d has more paths than the desk keeps: whether its files replace earlier ones is not fully known", i+1)
			}
		}
		rep.Layers = append(rep.Layers, l)
	}

	// The history array lists every instruction; those with empty_layer
	// false made the layers, in order. Only an exact count maps.
	instr := make([]string, len(entry.Layers))
	layerOfHistory := map[int]int{}
	if cfg != nil {
		var made []int
		for hi, h := range cfg.History {
			if !h.EmptyLayer {
				made = append(made, hi)
			}
		}
		if len(made) == len(entry.Layers) {
			for li, hi := range made {
				instr[li], _ = cleanInstruction(cfg.History[hi].CreatedBy)
				rep.Layers[li].Instruction = instr[li]
				layerOfHistory[hi] = li + 1
			}
		} else if len(cfg.History) > 0 {
			a.note("the history has %d layer-making entries for %d layers: build instructions are not shown", len(made), len(entry.Layers))
		}
	}

	rep.Instructions = instr
	a.attestations(rep)
	if a.files != nil {
		rep.Files = a.listing(scans, instr)
	}
	if a.opts.Mode == ModeFiles {
		// Headers only: no rule runs and no package list is made.
		rep.Notes = a.notes
		return rep, nil
	}
	rep.Packages = &PackageSet{SBOM: a.declared(rep), DBs: a.installed(scans)}

	b := &builder{lim: a.lim, count: map[string]int{}, overflow: map[string]*Finding{}}
	fs := &finalFS{scans: scans}
	a.secretRules(b, rep, scans, fs, instr)
	if cfg != nil {
		a.historyRules(b, cfg, layerOfHistory)
		a.configRules(b, rep, fs)
	}
	a.fileRules(b, scans, fs, instr)
	a.binaryRules(b, scans, fs, instr)
	a.layerRules(b, rep, scans, fs, instr)
	a.supplyRules(b, rep)
	a.packageRules(b, rep, instr)

	rep.Findings, rep.Omitted = b.done()
	Sort(rep.Findings, OrderLayer)
	rep.Notes = a.notes
	return rep, nil
}

// manifest picks the entry of manifest.json that describes the image.
func (a *analyser) manifest() (manifestEntry, bool) {
	b, ok := a.meta["manifest.json"]
	if !ok {
		return manifestEntry{}, false
	}
	var entries []manifestEntry
	if json.Unmarshal(b, &entries) != nil || len(entries) == 0 {
		a.note("manifest.json is not readable")
		return manifestEntry{}, false
	}
	if len(entries) == 1 {
		return entries[0], true
	}
	id := strings.TrimPrefix(a.opts.ImageID, "sha256:")
	for _, e := range entries {
		if id != "" && strings.Contains(e.Config, id) {
			return e, true
		}
		for _, t := range e.RepoTags {
			if a.opts.Ref != "" && t == a.opts.Ref {
				return e, true
			}
		}
	}
	a.note("the archive holds %d images and none is named by this image's ID or reference: the first was read", len(entries))
	return entries[0], true
}

// walkIndex visits every descriptor reachable from index.json. Nested
// indexes whose blobs are absent are skipped, never fatal.
func (a *analyser) walkIndex(visit func(d ociDescriptor, doc *ociIndex)) {
	b, ok := a.meta["index.json"]
	if !ok {
		return
	}
	var top ociIndex
	if json.Unmarshal(b, &top) != nil {
		a.note("index.json is not readable")
		return
	}
	seen := 0
	var walk func(ds []ociDescriptor, depth int)
	walk = func(ds []ociDescriptor, depth int) {
		for _, d := range ds {
			if seen++; seen > 512 || depth > 4 {
				return
			}
			var doc *ociIndex
			if raw, ok := a.json[blobPath(d.Digest)]; ok {
				var x ociIndex
				if json.Unmarshal(raw, &x) == nil {
					doc = &x
				}
			}
			visit(d, doc)
			if doc != nil && len(doc.Manifests) > 0 {
				walk(doc.Manifests, depth+1)
			}
		}
	}
	walk(top.Manifests, 0)
}

func isAttestation(d ociDescriptor) bool {
	return d.Annotations["vnd.docker.reference.type"] == "attestation-manifest"
}

// manifestFromIndex finds the image manifest for the platform asked for.
func (a *analyser) manifestFromIndex() (manifestEntry, bool) {
	var found *manifestEntry
	want := a.opts.Platform
	a.walkIndex(func(d ociDescriptor, doc *ociIndex) {
		if found != nil || isAttestation(d) || doc == nil || doc.Config == nil {
			return
		}
		if d.Platform != nil && want != "" {
			p := d.Platform.OS + "/" + d.Platform.Architecture
			if d.Platform.Variant != "" {
				p += "/" + d.Platform.Variant
			}
			if !strings.HasPrefix(p, want) && !strings.HasPrefix(want, p) {
				return
			}
		}
		e := manifestEntry{Config: blobPath(doc.Config.Digest)}
		for _, l := range doc.Layers {
			e.Layers = append(e.Layers, blobPath(l.Digest))
		}
		found = &e
	})
	if found == nil {
		return manifestEntry{}, false
	}
	return *found, true
}

func (a *analyser) attestations(rep *Report) {
	missing := 0
	a.walkIndex(func(d ociDescriptor, doc *ociIndex) {
		if !isAttestation(d) {
			if doc == nil && d.Platform != nil && d.MediaType != "" && !strings.Contains(d.MediaType, "index") {
				missing++
			}
			return
		}
		if doc == nil {
			a.note("an attestation manifest (%s) is listed but not in the archive", shortDigest(d.Digest))
			return
		}
		for _, l := range doc.Layers {
			at := Attestation{PredicateType: sanitize(l.Annotations["in-toto.io/predicate-type"], 120), Digest: l.Digest, Packages: -1}
			p := blobPath(l.Digest)
			_, small := a.json[p]
			_, big := a.bigJSON[p]
			at.Present = small || big
			if sc, ok := a.sboms[p]; ok && sc.err == "" && sc.format != "" {
				at.Format, at.Packages = sc.format, sc.total
			}
			if len(rep.Attestations) < 32 {
				rep.Attestations = append(rep.Attestations, at)
			}
		}
	})
	if missing > 0 {
		a.note("index.json lists %d other manifest(s) whose blobs are not in the archive (other platforms): skipped", missing)
	}
}

func shortDigest(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return sanitize(d, 12)
}

// cleanInstruction turns a history created_by into one readable line, and
// returns the build arguments it records. Argument values are never in the
// line; secret-shaped text in it is masked.
func cleanInstruction(createdBy string) (string, [][2]string) {
	s := strings.TrimSpace(createdBy)
	s = strings.TrimSpace(strings.TrimSuffix(s, "# buildkit"))
	run := false
	if strings.HasPrefix(s, "RUN ") {
		s, run = strings.TrimSpace(s[4:]), true
	}
	var args [][2]string
	if strings.HasPrefix(s, "|") {
		num, rest, _ := strings.Cut(s[1:], " ")
		if n, err := strconv.Atoi(num); err == nil && n > 0 && n < 256 {
			toks := strings.SplitN(rest, " ", n+1)
			if len(toks) >= n {
				for _, t := range toks[:n] {
					k, v, _ := strings.Cut(t, "=")
					args = append(args, [2]string{k, v})
				}
				s = ""
				if len(toks) > n {
					s = toks[n]
				}
				run = true
			}
		}
	}
	switch {
	case strings.HasPrefix(s, "/bin/sh -c #(nop) "):
		s = strings.TrimSpace(strings.TrimPrefix(s, "/bin/sh -c #(nop) "))
	case strings.HasPrefix(s, "/bin/sh -c "):
		s = "RUN " + strings.TrimPrefix(s, "/bin/sh -c ")
	case run:
		s = "RUN " + s
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(args) > 0 {
		var names []string
		for _, kv := range args {
			names = append(names, kv[0])
		}
		s += "  (build args: " + strings.Join(names, ", ") + ")"
	}
	return sanitize(maskText(s), 240), args
}

// ---- the final filesystem ------------------------------------------------------

// finalFS answers "is this version of a path still there at the end?" from
// the layers' whiteouts, opaque markers and path fingerprints.
type finalFS struct {
	scans []*layerScan
}

type fate struct {
	visible bool
	unknown bool   // a later layer could not be read
	by      int    // the 1-based layer that deleted or replaced it
	how     string // "deleted" or "replaced"
}

func (fs *finalFS) fate(i int, p string) fate {
	for j := i + 1; j < len(fs.scans); j++ {
		s := fs.scans[j]
		if s == nil || s.unreadable != "" {
			return fate{unknown: true, by: j + 1}
		}
		if s.whiteouts[p] {
			return fate{by: j + 1, how: "deleted"}
		}
		if s.has(p) {
			return fate{by: j + 1, how: "replaced"}
		}
		for anc := path.Dir(p); ; anc = path.Dir(anc) {
			if s.whiteouts[anc] || s.opaques[anc] {
				return fate{by: j + 1, how: "deleted"}
			}
			if _, ok := s.symlinks[anc]; ok {
				// A directory replaced by a link: what was under it is
				// gone from view (Fedora's /var/lib/rpm, for one).
				return fate{by: j + 1, how: "deleted"}
			}
			if anc == "/" {
				break
			}
		}
	}
	return fate{visible: true}
}

// complete reports whether every layer was read and fingerprinted whole.
func (fs *finalFS) complete() bool {
	for _, s := range fs.scans {
		if s == nil || s.unreadable != "" || s.pathsCapped {
			return false
		}
	}
	return true
}

// exists reports whether p is in the final filesystem, following symbolic
// links the layers recorded.
func (fs *finalFS) exists(p string) bool {
	for hop := 0; hop < 16; hop++ {
		next, moved := fs.followLinks(p)
		if !moved {
			break
		}
		p = next
	}
	for i := len(fs.scans) - 1; i >= 0; i-- {
		if fs.scans[i].has(p) {
			return fs.fate(i, p).visible
		}
	}
	return false
}

// followLinks rewrites the first symlinked component of p, if any.
func (fs *finalFS) followLinks(p string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for k := 1; k <= len(parts); k++ {
		prefix := "/" + strings.Join(parts[:k], "/")
		for i := len(fs.scans) - 1; i >= 0; i-- {
			s := fs.scans[i]
			if !s.has(prefix) {
				continue
			}
			target, ok := s.symlinks[prefix]
			if !ok || !fs.fate(i, prefix).visible {
				break
			}
			if !strings.HasPrefix(target, "/") {
				target = path.Join(path.Dir(prefix), target)
			}
			return path.Clean(target + "/" + strings.Join(parts[k:], "/")), true
		}
	}
	return p, false
}

// ---- findings --------------------------------------------------------------------

// builder bounds the findings: Limits.PerRule per rule and layer are listed,
// the rest counted in one summary finding; Limits.Findings in all.
type builder struct {
	lim      Limits
	out      []Finding
	count    map[string]int
	overflow map[string]*Finding
	order    []string
	omitted  int
}

func (b *builder) add(f Finding) {
	f.Path = sanitize(f.Path, 400)
	f.Evidence = sanitize(f.Evidence, 300)
	k := fmt.Sprintf("%s|%d", f.Rule, f.Layer)
	b.count[k]++
	if b.count[k] > b.lim.PerRule {
		ov := b.overflow[k]
		if ov == nil {
			ov = &Finding{Rule: f.Rule, Group: f.Group, Source: f.Source, Certainty: f.Certainty, Layer: f.Layer, Instruction: f.Instruction, Unknown: f.Unknown}
			b.overflow[k] = ov
			b.order = append(b.order, k)
		}
		return
	}
	if len(b.out) >= b.lim.Findings {
		b.omitted++
		return
	}
	b.out = append(b.out, f)
}

func (b *builder) done() ([]Finding, int) {
	for _, k := range b.order {
		ov := b.overflow[k]
		n := b.count[k] - b.lim.PerRule
		ov.Path = "…"
		ov.Evidence = fmt.Sprintf("%d more", n)
		ov.Reason = fmt.Sprintf("%d more %s findings in this layer are counted, not listed.", n, ov.Rule)
		b.out = append(b.out, *ov)
	}
	return b.out, b.omitted
}

func (a *analyser) secretRules(b *builder, rep *Report, scans []*layerScan, fs *finalFS, instr []string) {
	for i, s := range scans {
		if s == nil {
			continue
		}
		for _, o := range s.secrets {
			ft := fs.fate(i, o.path)
			f := Finding{
				Group: GroupSecrets, Source: SourceImage, Certainty: o.hit.certainty, Layer: i + 1,
				Path: o.path, Evidence: o.hit.kind + ": " + o.hit.masked, Instruction: instr[i], Unknown: o.hit.unknown,
			}
			if o.hit.value != "" {
				f.Secret = NewSecret(o.hit.value)
			}
			switch {
			case ft.how == "deleted":
				f.Rule = "secret.deleted"
				f.Reason = fmt.Sprintf("Added in layer %d and deleted in layer %d, yet still in the image bytes: anyone with the image can read layer %d.", i+1, ft.by, i+1)
			case ft.how == "replaced":
				f.Rule = "secret.overwritten"
				f.Reason = fmt.Sprintf("Replaced in layer %d, yet the earlier version is still in the image bytes of layer %d.", ft.by, i+1)
			case ft.unknown:
				f.Rule = "secret.file"
				f.Reason = fmt.Sprintf("In layer %d's bytes; whether a later layer removes it is unknown (layer %d could not be read).", i+1, ft.by)
			default:
				f.Rule = "secret.file"
				f.Reason = "In the final filesystem: readable in every container started from this image."
				rep.FinalSecrets++
			}
			b.add(f)
		}
		if n := s.dropped["secret"]; n > 0 {
			a.note("layer %d: %d more secret matches were not kept", i+1, n)
		}
	}
}

func (a *analyser) historyRules(b *builder, cfg *configDoc, layerOfHistory map[int]int) {
	for hi, h := range cfg.History {
		line, args := cleanInstruction(h.CreatedBy)
		for _, kv := range args {
			k, v := kv[0], kv[1]
			if v == "" {
				continue
			}
			hits := scanContent([]byte(v), a.lim.SecretReveal, 1)
			named := secretName.MatchString(k) && !strings.HasSuffix(strings.ToUpper(k), "_FILE") && !placeholder(v)
			if len(hits) == 0 && !named {
				continue
			}
			c := Likely
			if len(hits) > 0 && hits[0].certainty == Certain {
				c = Certain
			}
			b.add(Finding{
				Rule: "secret.buildarg", Group: GroupSecrets, Source: SourceImage, Certainty: c,
				Layer: layerOfHistory[hi], Evidence: sanitize(k, 64) + "=" + maskValue(v, 0),
				Reason:      "This build argument's value is recorded in the image's history: every copy of the image carries it.",
				Instruction: line, Unknown: unknownLive, Secret: NewSecret(boundReveal(v, a.lim.SecretReveal)),
			})
		}
	}
	for _, e := range cfg.Config.Env {
		k, v, _ := strings.Cut(e, "=")
		if v == "" || !secretName.MatchString(k) || strings.HasSuffix(strings.ToUpper(k), "_FILE") || placeholder(v) {
			continue
		}
		b.add(Finding{
			Rule: "secret.env", Group: GroupSecrets, Source: SourceImage, Certainty: Likely,
			Evidence: sanitize(k, 64) + "=" + maskValue(v, 0),
			Reason:   "Set by ENV in the image config: every container starts with it, and inspecting the image shows it.",
			Unknown:  unknownLive, Secret: NewSecret(boundReveal(v, a.lim.SecretReveal)),
		})
	}
}

// normArch maps the kernel's names to the image spec's.
func normArch(a string) string {
	switch a {
	case "x86_64", "x86-64":
		return "amd64"
	case "aarch64", "armv8", "arm64v8":
		return "arm64"
	case "armv7l", "armv6l", "armhf":
		return "arm"
	case "i386", "i686":
		return "386"
	}
	return a
}

func isShell(s string) bool {
	switch path.Base(s) {
	case "sh", "bash", "ash", "dash", "zsh", "ksh":
		return true
	}
	return false
}

func quoteArgv(argv []string) string {
	b, _ := json.Marshal(argv)
	return sanitize(string(b), 160)
}

func (a *analyser) configRules(b *builder, rep *Report, fs *finalFS) {
	c := rep.Config
	uid, _, _ := strings.Cut(c.User, ":")
	if uid == "" || uid == "root" || uid == "0" {
		rep.RootUser = true
		b.add(Finding{
			Rule: "config.root-user", Group: GroupConfig, Source: SourceImage, Certainty: Certain,
			Evidence: fmt.Sprintf("User %q", sanitize(c.User, 40)),
			Reason:   "Processes run as root (UID 0) unless the container is started with --user.",
			Unknown:  "whether containers are started with --user (the live findings say, for this daemon's)",
		})
	}
	if a.opts.DaemonOS != "" && c.OS != "" {
		da, ia := normArch(a.opts.DaemonArch), normArch(c.Architecture)
		if a.opts.DaemonOS != c.OS || da != ia {
			b.add(Finding{
				Rule: "config.platform", Group: GroupConfig, Source: SourceImage, Certainty: Certain,
				Evidence: fmt.Sprintf("image %s, daemon %s/%s", sanitize(c.Platform(), 40), a.opts.DaemonOS, da),
				Reason:   "Built for another platform: containers need emulation to run, if they run at all.",
				Unknown:  "whether the host has binfmt emulation installed",
			})
		}
	}
	argv := append(append([]string{}, c.Entrypoint...), c.Cmd...)
	switch {
	case len(argv) == 0:
		b.add(Finding{
			Rule: "config.inert", Group: GroupConfig, Source: SourceImage, Certainty: Certain,
			Evidence: "no ENTRYPOINT and no CMD",
			Reason:   "Nothing to run: a container from this image fails to start unless a command is given.",
			Unknown:  "whether every container is started with its own command",
		})
	case len(c.Entrypoint) == 0 && len(c.Cmd) == 1 && isShell(c.Cmd[0]):
		b.add(Finding{
			Rule: "config.inert", Group: GroupConfig, Source: SourceImage, Certainty: Likely,
			Evidence: "CMD " + quoteArgv(c.Cmd),
			Reason:   "The command is a bare shell: with no terminal attached it reads end of input and exits at once.",
			Unknown:  "whether containers are started with -it or with another command",
		})
	case fs.complete() && !lookPath(fs, argv[0], c.Env):
		b.add(Finding{
			Rule: "config.inert", Group: GroupConfig, Source: SourceImage, Certainty: Likely,
			Path: sanitize(argv[0], 200), Evidence: fmt.Sprintf("%s is not in the final filesystem", sanitize(argv[0], 120)),
			Reason:  "The program the command names is not in the image: a container cannot start.",
			Unknown: "programs a volume provides at run time, and links through more than 16 hops",
		})
	}
	switch {
	case c.Healthcheck == nil:
		b.add(Finding{
			Rule: "config.healthcheck", Group: GroupConfig, Source: SourceImage, Certainty: Certain,
			Evidence: "no HEALTHCHECK",
			Reason:   "The daemon cannot tell a hung container from a working one: no health status unless one is set at run time.",
			Unknown:  "whether a health check is set at run time or by an orchestrator",
		})
	case len(c.Healthcheck) > 0 && c.Healthcheck[0] == "NONE":
		b.add(Finding{
			Rule: "config.healthcheck", Group: GroupConfig, Source: SourceImage, Certainty: Certain,
			Evidence: "HEALTHCHECK NONE",
			Reason:   "Health checking is switched off explicitly, overriding any the base image had.",
			Unknown:  "whether a health check is set at run time or by an orchestrator",
		})
	}
}

// lookPath resolves a command the way exec does: an absolute or relative
// path as given, a bare name through PATH from the image's environment.
func lookPath(fs *finalFS, name string, env []string) bool {
	if strings.Contains(name, "/") {
		if !strings.HasPrefix(name, "/") {
			return true // relative to a working directory the run may change: not judged
		}
		return fs.exists(path.Clean(name))
	}
	dirs := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			dirs = v
		}
	}
	for _, d := range strings.Split(dirs, ":") {
		if strings.HasPrefix(d, "/") && fs.exists(path.Join(d, name)) {
			return true
		}
	}
	return false
}

var fileRuleText = map[string][2]string{
	"fs.setuid":         {"Set-user-ID: it runs with its owner's privileges, whoever starts it.", "whether the program can be made to misuse those privileges"},
	"fs.setgid":         {"Set-group-ID: it runs with its group's privileges, whoever starts it.", "whether the program can be made to misuse those privileges"},
	"fs.world-writable": {"Any user in the container can change it.", "whether anything trusted reads or runs what is there"},
	"fs.capabilities":   {"File capabilities: whoever runs it gets these, without setuid.", "whether the runtime's capability bounding set lets them through (Docker drops most by default)"},
}

func (a *analyser) fileRules(b *builder, scans []*layerScan, fs *finalFS, instr []string) {
	for i, s := range scans {
		if s == nil {
			continue
		}
		for _, o := range s.fileObs {
			if ft := fs.fate(i, o.path); !ft.visible {
				continue // replaced or deleted: not what a container sees
			}
			t := fileRuleText[o.rule]
			reason := t[0]
			if o.rule == "fs.world-writable" && strings.HasPrefix(o.evidence, "directory") {
				reason = "Any user in the container can create or replace files in it: it has no sticky bit."
			}
			b.add(Finding{Rule: o.rule, Group: GroupFilesystem, Source: SourceImage, Certainty: Certain, Layer: i + 1,
				Path: o.path, Evidence: o.evidence, Reason: reason, Instruction: instr[i], Unknown: t[1]})
		}
		n := 0
		for rule, c := range s.dropped {
			if strings.HasPrefix(rule, "fs.") {
				n += c
			}
		}
		if n > 0 {
			a.note("layer %d: %d more files with special modes were not kept", i+1, n)
		}
	}
}

func (a *analyser) binaryRules(b *builder, scans []*layerScan, fs *finalFS, instr []string) {
	for i, s := range scans {
		if s == nil {
			continue
		}
		for _, o := range s.elfs {
			if !fs.fate(i, o.path).visible {
				continue
			}
			f := o.facts
			base := Finding{Group: GroupBinaries, Source: SourceImage, Layer: i + 1, Path: o.path, Instruction: instr[i]}
			add := func(rule string, c Certainty, evidence, reason, unknown string, detail []string) {
				x := base
				x.Rule, x.Certainty, x.Evidence, x.Reason, x.Unknown, x.Detail = rule, c, evidence, reason, unknown, detail
				b.add(x)
			}
			if f.goVersion != "" {
				ev := f.goVersion
				var detail []string
				if f.goModsKnown {
					if f.goMain != "" {
						ev += " · " + f.goMain
					}
					ev += fmt.Sprintf(" · %d modules", len(f.goModules))
					for _, m := range f.goModules[:min(len(f.goModules), 200)] {
						detail = append(detail, m)
					}
				}
				add("bin.go", Certain, ev, "A Go program: the toolchain and module versions it was built from are recorded in it.",
					"whether any of those versions has a known vulnerability: no vulnerability database is consulted", detail)
			}
			if f.packed {
				add("bin.packed", Likely, "UPX! marker in the first 4 kB", "Packed: the code on disk is compressed, so file scanners see only the unpacker.",
					"what the packed code does", nil)
			}
			if !f.headersKnown || !f.exec {
				continue // a shared library, or headers past the bytes read
			}
			if !f.pie {
				reason := "Not position-independent: address randomisation cannot move its code."
				if f.goVersion != "" {
					reason += " (The Go toolchain's default unless built with -buildmode=pie.)"
				}
				add("bin.pie", Certain, "ELF type EXEC: a fixed load address", reason,
					"whether the program has a bug a fixed address would help exploit", nil)
			}
			if !f.dynamic {
				continue
			}
			switch {
			case !f.relro:
				add("bin.relro", Certain, "no PT_GNU_RELRO segment", "No RELRO: relocations and the GOT stay writable while it runs.",
					"whether the program has a write bug that could reach them", nil)
			case !f.bindNow:
				add("bin.relro", Certain, "PT_GNU_RELRO without BIND_NOW", "Partial RELRO: the GOT stays writable for lazy binding.",
					"whether the program has a write bug that could reach it", nil)
			}
			if f.canaryKnown && !f.canary && f.goVersion == "" {
				add("bin.canary", Likely, "no __stack_chk_fail among its dynamic symbols", "No stack protector imported: stack overflows are not caught at run time.",
					"whether any function needed one; some toolchains protect without this symbol", nil)
			}
		}
		if n := s.dropped["binary"]; n > 0 {
			a.note("layer %d: %d more ELF files were not examined", i+1, n)
		}
	}
}

func (a *analyser) layerRules(b *builder, rep *Report, scans []*layerScan, fs *finalFS, instr []string) {
	total := rep.TotalSize()
	th := a.opts.Thresholds
	for i, l := range rep.Layers {
		if l.Missing {
			continue
		}
		if len(rep.Layers) > 1 && total >= th.SizeMin && float64(l.Size) >= th.SizeShare*float64(total) {
			b.add(Finding{Rule: "layer.size-share", Group: GroupLayer, Source: SourceImage, Certainty: Certain, Layer: i + 1,
				Evidence: fmt.Sprintf("%s of %s (%.0f%%)", humanSize(l.Size), humanSize(total), 100*float64(l.Size)/float64(total)),
				Reason:   "This one layer holds most of the image's bytes.", Instruction: instr[i],
				Unknown: "whether those bytes are needed at run time"})
		}
		if l.Files >= th.FileCount {
			b.add(Finding{Rule: "layer.file-count", Group: GroupLayer, Source: SourceImage, Certainty: Certain, Layer: i + 1,
				Evidence: fmt.Sprintf("%d files", l.Files), Reason: fmt.Sprintf("This layer adds %d files.", l.Files), Instruction: instr[i],
				Unknown: "whether they are needed at run time"})
		}
	}
	for i, s := range scans {
		if s == nil {
			continue
		}
		for _, g := range sortedAggs(s.aggs) {
			ft := fs.fate(i, g.root)
			if ft.visible {
				ft = fs.fate(i, g.example)
			}
			cache := strings.Contains(g.kind, "cache") || strings.Contains(g.kind, "package")
			f := Finding{Group: GroupLayer, Source: SourceImage, Layer: i + 1, Path: g.root, Instruction: instr[i],
				Evidence: fmt.Sprintf("%d files, %s", g.files, humanSize(g.bytes))}
			if cache {
				f.Rule, f.Certainty = "layer.cache", Certain
				f.Unknown = "whether anything reads the cache at run time (normally nothing does)"
			} else {
				f.Rule, f.Certainty = "layer.junk", Likely
				f.Unknown = "whether the application reads these files at run time"
			}
			switch {
			case ft.how != "":
				f.Reason = fmt.Sprintf("%s: removed in layer %d, but the bytes stay in layer %d. Clean up in the instruction that makes them.", capitalise(g.kind), ft.by, i+1)
			case cache:
				f.Reason = fmt.Sprintf("%s left in the image: downloads nothing needs at run time.", capitalise(g.kind))
			default:
				f.Reason = fmt.Sprintf("%s copied into the image.", capitalise(g.kind))
			}
			b.add(f)
		}
	}
}

func capitalise(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-32) + s[1:]
}

func sortedAggs(m map[string]*aggregate) []*aggregate {
	out := make([]*aggregate, 0, len(m))
	for _, g := range m {
		out = append(out, g)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].kind+out[j].root < out[j-1].kind+out[j-1].root; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (a *analyser) supplyRules(b *builder, rep *Report) {
	var sbom, prov []Attestation
	for _, at := range rep.Attestations {
		switch {
		case at.IsSBOM():
			sbom = append(sbom, at)
		case at.IsProvenance():
			prov = append(prov, at)
		}
	}
	if len(sbom) > 0 {
		at := sbom[0]
		ev := "SBOM present"
		switch {
		case at.Present && at.Format != "" && at.Packages >= 0:
			ev += fmt.Sprintf(": %s, %d packages", at.Format, at.Packages)
		case at.Present:
			ev += ": " + at.PredicateType
		default:
			ev = "SBOM listed: " + at.PredicateType + "; its blob is not in the archive"
		}
		if len(sbom) > 1 {
			ev += fmt.Sprintf(" (+%d more)", len(sbom)-1)
		}
		unknown := "whether the SBOM is complete: it could not be checked against the image's package databases"
		if checked := checkedAgainst(rep.Packages); checked != "" {
			unknown = "whether it is complete for anything but " + checked + ": only those package databases are read (i opens the list)"
		}
		b.add(Finding{Rule: "supply.sbom", Group: GroupSupply, Source: SourceImage, Certainty: Certain, Evidence: ev,
			Reason:  "A software bill of materials travels with the image.",
			Unknown: unknown})
	}
	if len(prov) > 0 {
		ev := "provenance attestation: " + prov[0].PredicateType
		if !prov[0].Present {
			ev += "; its blob is not in the archive"
		}
		b.add(Finding{Rule: "supply.provenance", Group: GroupSupply, Source: SourceImage, Certainty: Certain, Evidence: ev,
			Reason:  "A record of how the image was built travels with it.",
			Unknown: "whether the builder is trustworthy: the attestation's signature is not checked"})
	}
	if len(sbom)+len(prov) == 0 {
		f := Finding{Rule: "supply.attestations", Group: GroupSupply, Source: SourceImage, Certainty: Hint}
		if rep.Layout == "oci" {
			f.Evidence = "no attestation manifest under index.json"
			f.Reason = "No SBOM or provenance travels with this image."
			f.Unknown = "whether the registry holds attestations this daemon did not keep"
		} else {
			f.Evidence = "classic docker-archive layout"
			f.Reason = "This layout cannot carry attestations: the classic image store does not keep them."
			f.Unknown = "whether the registry the image came from has them"
		}
		b.add(f)
	}
}
