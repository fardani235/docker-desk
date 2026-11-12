package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Options configures the connection to the daemon.
type Options struct {
	// Host is the endpoint the client dials: unix:///var/run/docker.sock,
	// or tcp://host:port. (An ssh:// daemon is dialled through the local
	// socket of its tunnel; see internal/sshtunnel.)
	Host string
	// TLS secures a tcp:// endpoint. Nil means plain HTTP.
	TLS *TLS
	// ReadOnly makes every mutating method fail locally and additionally
	// rejects any HTTP request that is not GET or HEAD before it is sent.
	ReadOnly bool
	// UserAgent identifies the desk in the daemon's logs.
	UserAgent string
	// ShownAs is the endpoint as the operator named it, when that is not
	// Host: error messages then name it instead of the address dialled.
	ShownAs string
}

// TLS is the client side of a TLS connection to the daemon.
type TLS struct {
	// CAFile verifies the daemon's certificate. Empty means the system's
	// root certificates.
	CAFile string
	// CertFile and KeyFile are this client's certificate; both or neither.
	CertFile string
	KeyFile  string
	// SkipVerify accepts any server certificate.
	SkipVerify bool
}

// config builds the TLS client configuration. Files are read here, once, so
// that a missing or malformed certificate is a launch error and not the
// reason some later request fails.
func (t *TLS) config() (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: t.SkipVerify}
	if t.CAFile != "" && !t.SkipVerify {
		pem, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading the CA certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s contains no PEM certificate", t.CAFile)
		}
		cfg.RootCAs = pool
	}
	if t.CertFile != "" || t.KeyFile != "" {
		if t.CertFile == "" || t.KeyFile == "" {
			return nil, errors.New("a client certificate needs both its certificate and its key")
		}
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading the client certificate (%s, %s): %w", t.CertFile, t.KeyFile, err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// New connects to the daemon at opts.Host through Docker's own client
// library. No request is sent until a method is called.
func New(opts Options) (Engine, error) {
	if err := checkTestGuard(opts); err != nil {
		return nil, err
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "docker-desk"
	}
	var clientOpts []client.Opt
	if opts.TLS != nil {
		if !strings.HasPrefix(opts.Host, "tcp://") {
			return nil, fmt.Errorf("TLS applies to tcp:// endpoints, not to %s", opts.Host)
		}
		cfg, err := opts.TLS.config()
		if err != nil {
			return nil, fmt.Errorf("configuring TLS for %s: %w", opts.Host, err)
		}
		// The same transport settings the library gives its own default
		// client, with the TLS configuration added; WithHost, below,
		// installs the dialer on it. This must come first.
		clientOpts = append(clientOpts, client.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				TLSClientConfig: cfg, MaxIdleConns: 6, IdleConnTimeout: 30 * time.Second,
				// A peer that accepts the connection and then says
				// nothing must not hold a request — or the event
				// stream, which has no deadline of its own — for ever.
				TLSHandshakeTimeout: 15 * time.Second,
			},
			CheckRedirect: client.CheckRedirect,
		}))
	}
	clientOpts = append(clientOpts,
		client.WithHost(opts.Host),
		client.WithAPIVersionNegotiation(),
		client.WithUserAgent(ua),
	)
	if opts.ReadOnly {
		clientOpts = append(clientOpts, client.WithHTTPRequestHook(rejectMutatingRequest))
	}
	cli, err := client.New(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("creating Docker client for %s: %w", opts.Host, err)
	}
	var e Engine = &mobyEngine{cli: cli, dialed: opts.Host, shownAs: opts.ShownAs}
	if opts.ReadOnly {
		e = ReadOnly(e)
	}
	return e, nil
}

// rejectMutatingRequest is the second, lower layer of read-only mode: even a
// code path that bypassed the ReadOnly wrapper cannot get a mutating request
// onto the wire.
func rejectMutatingRequest(req *http.Request) error {
	if req.Method == http.MethodGet || req.Method == http.MethodHead {
		return nil
	}
	return fmt.Errorf("%w (blocked %s %s)", ErrReadOnly, req.Method, req.URL.Path)
}

type mobyEngine struct {
	cli *client.Client
	// dialed is what the client connects to and shownAs what the operator
	// calls the daemon. They differ for an ssh:// daemon, which is dialled
	// through the local socket of its tunnel; see wrap.
	dialed  string
	shownAs string
}

func (m *mobyEngine) Close() error { return m.cli.Close() }

// wrap classifies a client-library error. Context cancellation is passed
// through untouched so callers can tell "I stopped this" from a failure.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrReadOnly) {
		return err
	}
	if e, ok := err.(*Error); ok {
		if e.Op == "" {
			e.Op = op
		}
		return e
	}
	kind := KindOther
	var netErr net.Error
	switch {
	case cerrdefs.IsNotFound(err):
		kind = KindNotFound
	case cerrdefs.IsConflict(err), cerrdefs.IsPermissionDenied(err):
		// The daemon answers 409 for most state refusals and 403 for
		// network ones ("has active endpoints", "pre-defined network").
		kind = KindConflict
	case client.IsErrConnectionFailed(err),
		cerrdefs.IsUnavailable(err),
		errors.Is(err, syscall.ECONNREFUSED),
		errors.Is(err, syscall.ECONNRESET),
		errors.Is(err, syscall.EPIPE),
		errors.Is(err, fs.ErrNotExist),
		errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, io.EOF),
		errors.As(err, &netErr):
		kind = KindUnavailable
	}
	return &Error{Kind: kind, Op: op, Msg: cleanMessage(err), Err: err}
}

// wrap classifies a client-library error and makes its message name the
// daemon as the operator knows it. The client library quotes the address it
// dialled; for an ssh:// daemon that is a socket in a temporary directory,
// which would read as if the desk were talking to something else.
func (m *mobyEngine) wrap(op string, err error) error {
	err = wrap(op, err)
	var e *Error
	if m.shownAs == "" || m.shownAs == m.dialed || !errors.As(err, &e) {
		return err
	}
	path := strings.TrimPrefix(m.dialed, "unix://")
	e.Msg = strings.NewReplacer(
		"http://"+url.PathEscape(path), m.shownAs,
		m.dialed, m.shownAs,
		path, m.shownAs,
	).Replace(e.Msg)
	return err
}

// ---- identity -------------------------------------------------------------

func (m *mobyEngine) Ping(ctx context.Context) error {
	_, err := m.cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	return m.wrap("ping", err)
}

func (m *mobyEngine) Info(ctx context.Context) (Info, error) {
	res, err := m.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return Info{}, m.wrap("info", err)
	}
	i := res.Info
	return Info{
		Name:            i.Name,
		ServerVersion:   i.ServerVersion,
		APIVersion:      m.cli.ClientVersion(),
		OperatingSystem: i.OperatingSystem,
		OSType:          i.OSType,
		Architecture:    i.Architecture,
		NCPU:            i.NCPU,
		MemTotal:        i.MemTotal,
		StorageDriver:   i.Driver,
		RootDir:         i.DockerRootDir,
	}, nil
}

// ---- reads ----------------------------------------------------------------

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

func prefixString(p netip.Prefix) string {
	if !p.IsValid() {
		return ""
	}
	return p.String()
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() <= 1 {
		return time.Time{}
	}
	return t
}

func convertMounts(in []container.MountPoint) []Mount {
	out := make([]Mount, 0, len(in))
	for _, mp := range in {
		out = append(out, Mount{
			Type:        string(mp.Type),
			Name:        mp.Name,
			Source:      mp.Source,
			Destination: mp.Destination,
			RW:          mp.RW,
		})
	}
	return out
}

func (m *mobyEngine) Containers(ctx context.Context) ([]Container, error) {
	res, err := m.cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, m.wrap("list containers", err)
	}
	out := make([]Container, 0, len(res.Items))
	for _, s := range res.Items {
		c := Container{
			ID:      s.ID,
			Image:   s.Image,
			ImageID: s.ImageID,
			Command: s.Command,
			Created: time.Unix(s.Created, 0),
			State:   string(s.State),
			Status:  s.Status,
			Labels:  s.Labels,
			Mounts:  convertMounts(s.Mounts),

			NetworkMode: s.HostConfig.NetworkMode,
		}
		if len(s.Names) > 0 {
			c.Name = strings.TrimPrefix(s.Names[0], "/")
		} else {
			c.Name = ShortID(s.ID)
		}
		if s.Health != nil && s.Health.Status != container.NoHealthcheck {
			c.Health = string(s.Health.Status)
		}
		for _, p := range s.Ports {
			c.Ports = append(c.Ports, Port{
				HostIP:      addrString(p.IP),
				HostPort:    p.PublicPort,
				PrivatePort: p.PrivatePort,
				Proto:       p.Type,
			})
		}
		sortPorts(c.Ports)
		if s.NetworkSettings != nil {
			for name, ep := range s.NetworkSettings.Networks {
				a := Attachment{Network: name}
				if ep != nil {
					a.NetworkID = ep.NetworkID
					a.IP = addrString(ep.IPAddress)
					a.Gateway = addrString(ep.Gateway)
					a.Mac = ep.MacAddress.String()
				}
				c.Networks = append(c.Networks, a)
			}
			sort.Slice(c.Networks, func(i, j int) bool { return c.Networks[i].Network < c.Networks[j].Network })
		}
		out = append(out, c)
	}
	return out, nil
}

func sortPorts(p []Port) {
	sort.Slice(p, func(i, j int) bool {
		if p[i].PrivatePort != p[j].PrivatePort {
			return p[i].PrivatePort < p[j].PrivatePort
		}
		if p[i].Proto != p[j].Proto {
			return p[i].Proto < p[j].Proto
		}
		if p[i].HostPort != p[j].HostPort {
			return p[i].HostPort < p[j].HostPort
		}
		return p[i].HostIP < p[j].HostIP
	})
}

func (m *mobyEngine) Images(ctx context.Context) ([]Image, error) {
	res, err := m.cli.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, m.wrap("list images", err)
	}
	// Ask the daemon which images it considers dangling instead of inferring
	// it from RepoTags.
	dres, err := m.cli.ImageList(ctx, client.ImageListOptions{
		Filters: client.Filters{}.Add("dangling", "true"),
	})
	if err != nil {
		return nil, m.wrap("list images", err)
	}
	dangling := make(map[string]bool, len(dres.Items))
	for _, s := range dres.Items {
		dangling[s.ID] = true
	}
	out := make([]Image, 0, len(res.Items))
	for _, s := range res.Items {
		out = append(out, Image{
			ID:          s.ID,
			RepoTags:    s.RepoTags,
			RepoDigests: s.RepoDigests,
			Size:        s.Size,
			Created:     time.Unix(s.Created, 0),
			Labels:      s.Labels,
			Dangling:    dangling[s.ID],
			Containers:  s.Containers,
		})
	}
	return out, nil
}

func (m *mobyEngine) Volumes(ctx context.Context) ([]Volume, error) {
	res, err := m.cli.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return nil, m.wrap("list volumes", err)
	}
	out := make([]Volume, 0, len(res.Items))
	for _, v := range res.Items {
		out = append(out, Volume{
			Name:       v.Name,
			Driver:     v.Driver,
			Mountpoint: v.Mountpoint,
			Scope:      v.Scope,
			CreatedAt:  v.CreatedAt,
			Labels:     v.Labels,
			Options:    v.Options,
		})
	}
	return out, nil
}

func (m *mobyEngine) Networks(ctx context.Context) ([]Network, error) {
	res, err := m.cli.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, m.wrap("list networks", err)
	}
	out := make([]Network, 0, len(res.Items))
	for _, s := range res.Items {
		n := Network{
			ID:       s.ID,
			Name:     s.Name,
			Driver:   s.Driver,
			Scope:    s.Scope,
			Internal: s.Internal,
			IPv6:     s.EnableIPv6,
			Created:  s.Created,
			Labels:   s.Labels,
		}
		for _, c := range s.IPAM.Config {
			if p := prefixString(c.Subnet); p != "" {
				n.Subnets = append(n.Subnets, p)
			}
			if g := addrString(c.Gateway); g != "" {
				n.Gateways = append(n.Gateways, g)
			}
		}
		out = append(out, n)
	}
	return out, nil
}

func (m *mobyEngine) InspectContainer(ctx context.Context, id string) (ContainerDetail, error) {
	res, err := m.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return ContainerDetail{}, m.wrap("inspect container", err)
	}
	in := res.Container
	d := ContainerDetail{Raw: res.Raw}
	d.ID = in.ID
	d.Name = strings.TrimPrefix(in.Name, "/")
	d.ImageID = in.Image
	d.Created = parseTime(in.Created)
	d.Path = in.Path
	d.Args = in.Args
	d.RestartCount = in.RestartCount
	d.Mounts = convertMounts(in.Mounts)
	if in.State != nil {
		d.State = string(in.State.Status)
		d.Pid = in.State.Pid
		d.ExitCode = in.State.ExitCode
		d.OOMKilled = in.State.OOMKilled
		d.Error = in.State.Error
		d.StartedAt = parseTime(in.State.StartedAt)
		d.FinishedAt = parseTime(in.State.FinishedAt)
		if in.State.Health != nil && in.State.Health.Status != container.NoHealthcheck {
			d.Health = string(in.State.Health.Status)
		}
	}
	if in.Config != nil {
		d.Image = in.Config.Image
		d.Labels = in.Config.Labels
		d.Entrypoint = in.Config.Entrypoint
		d.Cmd = in.Config.Cmd
		d.WorkingDir = in.Config.WorkingDir
		d.User = in.Config.User
		d.Hostname = in.Config.Hostname
		d.TTY = in.Config.Tty
		d.EnvCount = len(in.Config.Env)
	}
	if in.HostConfig != nil {
		d.NetworkMode = string(in.HostConfig.NetworkMode)
		d.Privileged = in.HostConfig.Privileged
		d.RestartPolicy = string(in.HostConfig.RestartPolicy.Name)
		if d.RestartPolicy == "" {
			d.RestartPolicy = "no"
		}
		if in.HostConfig.RestartPolicy.MaximumRetryCount > 0 {
			d.RestartPolicy += ":" + strconv.Itoa(in.HostConfig.RestartPolicy.MaximumRetryCount)
		}
	}
	if ns := in.NetworkSettings; ns != nil {
		for port, bindings := range ns.Ports {
			if len(bindings) == 0 {
				d.Ports = append(d.Ports, Port{PrivatePort: port.Num(), Proto: string(port.Proto())})
				continue
			}
			for _, b := range bindings {
				hp, _ := strconv.ParseUint(b.HostPort, 10, 16)
				d.Ports = append(d.Ports, Port{
					HostIP:      addrString(b.HostIP),
					HostPort:    uint16(hp),
					PrivatePort: port.Num(),
					Proto:       string(port.Proto()),
				})
			}
		}
		sortPorts(d.Ports)
		for name, ep := range ns.Networks {
			a := Attachment{Network: name}
			if ep != nil {
				a.NetworkID = ep.NetworkID
				a.IP = addrString(ep.IPAddress)
				a.Gateway = addrString(ep.Gateway)
				a.Mac = ep.MacAddress.String()
			}
			d.Networks = append(d.Networks, a)
		}
		sort.Slice(d.Networks, func(i, j int) bool { return d.Networks[i].Network < d.Networks[j].Network })
	}
	return d, nil
}

func (m *mobyEngine) InspectImage(ctx context.Context, ref string) (ImageDetail, error) {
	var raw bytes.Buffer
	res, err := m.cli.ImageInspect(ctx, ref, client.ImageInspectWithRawResponse(&raw))
	if err != nil {
		return ImageDetail{}, m.wrap("inspect image", err)
	}
	d := ImageDetail{Raw: raw.Bytes()}
	d.ID = res.ID
	d.RepoTags = res.RepoTags
	d.RepoDigests = res.RepoDigests
	d.Size = res.Size
	d.Created = parseTime(res.Created)
	d.Architecture = res.Architecture
	d.OS = res.Os
	d.Variant = res.Variant
	d.Author = res.Author
	d.Layers = len(res.RootFS.Layers)
	if res.Config != nil {
		d.Labels = res.Config.Labels
		d.Entrypoint = res.Config.Entrypoint
		d.Cmd = res.Config.Cmd
		d.WorkingDir = res.Config.WorkingDir
		d.User = res.Config.User
		for p := range res.Config.ExposedPorts {
			d.ExposedPorts = append(d.ExposedPorts, p)
		}
		sort.Strings(d.ExposedPorts)
	}
	return d, nil
}

// platformAPI is the first API version whose image export takes a platform.
const platformAPI = "1.48"

func (m *mobyEngine) SaveImage(ctx context.Context, id string, platform Platform) (io.ReadCloser, error) {
	var opts []client.ImageSaveOption
	// The platform selects one variant of a multi-platform image. A daemon
	// older than the parameter has only one variant per image ID to give,
	// and the library would refuse to send the request at all.
	if platform.OS != "" && platform.Architecture != "" && !apiOlder(m.cli.ClientVersion(), platformAPI) {
		opts = append(opts, client.ImageSaveWithPlatforms(ocispec.Platform{
			OS: platform.OS, Architecture: platform.Architecture, Variant: platform.Variant,
		}))
	}
	res, err := m.cli.ImageSave(ctx, []string{id}, opts...)
	if err != nil {
		return nil, m.wrap("export image", err)
	}
	return res, nil
}

// totalsAPI is the first API version whose /system/df states totals.
const totalsAPI = "1.52"

func (m *mobyEngine) DiskUsage(ctx context.Context) (DiskUsage, error) {
	res, err := m.cli.DiskUsage(ctx, client.DiskUsageOptions{
		Images: true, Containers: true, Volumes: true, BuildCache: true, Verbose: true,
	})
	if err != nil {
		return DiskUsage{}, m.wrap("disk usage", err)
	}
	usage := func(active, total, size, reclaimable int64) Usage {
		return Usage{TotalCount: total, ActiveCount: active, TotalSize: size, Reclaimable: reclaimable}
	}
	du := DiskUsage{
		Images:     usage(res.Images.ActiveCount, res.Images.TotalCount, res.Images.TotalSize, res.Images.Reclaimable),
		Containers: usage(res.Containers.ActiveCount, res.Containers.TotalCount, res.Containers.TotalSize, res.Containers.Reclaimable),
		Volumes:    usage(res.Volumes.ActiveCount, res.Volumes.TotalCount, res.Volumes.TotalSize, res.Volumes.Reclaimable),
		BuildCache: usage(res.BuildCache.ActiveCount, res.BuildCache.TotalCount, res.BuildCache.TotalSize, res.BuildCache.Reclaimable),
		// The version is known once the request has been negotiated.
		Derived: apiOlder(m.cli.ClientVersion(), totalsAPI),
	}
	for _, s := range res.Images.Items {
		du.ImageItems = append(du.ImageItems, ImageUsage{
			ID: s.ID, RepoTags: s.RepoTags, Created: time.Unix(s.Created, 0),
			Size: s.Size, SharedSize: s.SharedSize, Containers: s.Containers,
		})
	}
	for _, v := range res.Volumes.Items {
		vu := VolumeUsage{Name: v.Name, Driver: v.Driver, Size: -1, RefCount: -1}
		if v.UsageData != nil {
			vu.Size, vu.RefCount = v.UsageData.Size, v.UsageData.RefCount
		}
		du.VolumeItems = append(du.VolumeItems, vu)
	}
	for _, b := range res.BuildCache.Items {
		bu := BuildCacheUsage{ID: b.ID, Type: b.Type, Description: b.Description, Size: b.Size, InUse: b.InUse, Shared: b.Shared}
		if b.LastUsedAt != nil {
			bu.LastUsed = *b.LastUsedAt
		}
		du.BuildItems = append(du.BuildItems, bu)
	}
	return du, nil
}

func (m *mobyEngine) ImageSizes(ctx context.Context) ([]ImageUsage, error) {
	res, err := m.cli.ImageList(ctx, client.ImageListOptions{SharedSize: true})
	if err != nil {
		return nil, m.wrap("list images", err)
	}
	out := make([]ImageUsage, 0, len(res.Items))
	for _, s := range res.Items {
		out = append(out, ImageUsage{
			ID: s.ID, RepoTags: s.RepoTags, Created: time.Unix(s.Created, 0),
			Size: s.Size, SharedSize: s.SharedSize, Containers: s.Containers,
		})
	}
	return out, nil
}

func (m *mobyEngine) ImageLayers(ctx context.Context, id string) (ImageLayers, error) {
	insp, err := m.cli.ImageInspect(ctx, id)
	if err != nil {
		return ImageLayers{}, m.wrap("inspect image", err)
	}
	hist, err := m.cli.ImageHistory(ctx, id)
	if err != nil {
		return ImageLayers{}, m.wrap("image history", err)
	}
	out := ImageLayers{ID: insp.ID, DiffIDs: append([]string(nil), insp.RootFS.Layers...)}
	// The daemon lists history newest first.
	for i := len(hist.Items) - 1; i >= 0; i-- {
		h := hist.Items[i]
		out.History = append(out.History, HistoryEntry{CreatedBy: h.CreatedBy, Size: h.Size})
	}
	return out, nil
}

func (m *mobyEngine) ImageFacts(ctx context.Context, id string) (ImageFacts, error) {
	res, err := m.cli.ImageInspect(ctx, id)
	if err != nil {
		return ImageFacts{}, m.wrap("inspect image", err)
	}
	f := ImageFacts{
		ID: res.ID, RepoTags: res.RepoTags, RepoDigests: res.RepoDigests, Size: res.Size,
		Created: parseTime(res.Created), OS: res.Os, Architecture: res.Architecture, Variant: res.Variant,
		DiffIDs: append([]string(nil), res.RootFS.Layers...),
	}
	if c := res.Config; c != nil {
		f.Cmd, f.Entrypoint, f.User, f.WorkingDir = c.Cmd, c.Entrypoint, c.User, c.WorkingDir
		f.Labels = c.Labels
		for p := range c.ExposedPorts {
			f.ExposedPorts = append(f.ExposedPorts, p)
		}
		sort.Strings(f.ExposedPorts)
		if c.Healthcheck != nil && len(c.Healthcheck.Test) > 0 && c.Healthcheck.Test[0] != "NONE" {
			f.Healthcheck = append([]string(nil), c.Healthcheck.Test...)
		}
		f.EnvNames = EnvNames(c.Env)
	}
	hist, err := m.cli.ImageHistory(ctx, id)
	if err != nil {
		return ImageFacts{}, m.wrap("image history", err)
	}
	for i := len(hist.Items) - 1; i >= 0; i-- {
		h := hist.Items[i]
		f.History = append(f.History, HistoryEntry{CreatedBy: h.CreatedBy, Size: h.Size})
	}
	return f, nil
}

func (m *mobyEngine) DistributionInspect(ctx context.Context, ref, registryAuth string) (Distribution, error) {
	res, err := m.cli.DistributionInspect(ctx, ref, client.DistributionInspectOptions{EncodedRegistryAuth: registryAuth})
	if err != nil {
		return Distribution{}, m.wrap("ask the daemon's registry", distributionError(err))
	}
	d := Distribution{Digest: res.Descriptor.Digest.String(), MediaType: res.Descriptor.MediaType}
	for _, p := range res.Platforms {
		d.Platforms = append(d.Platforms, Platform{OS: p.OS, Architecture: p.Architecture, Variant: p.Variant})
	}
	return d, nil
}

// distributionError tells the daemon's own "page not found" — it does not
// serve the endpoint — from the registry's missing manifest, and a refused
// credential from a state conflict.
func distributionError(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	msg := cleanMessage(err)
	switch {
	case cerrdefs.IsNotFound(err) && strings.Contains(strings.ToLower(msg), "page not found"):
		return &Error{Kind: KindUnsupported, Msg: "the daemon does not serve /distribution (too old, or a proxy in front of it)", Err: err}
	case cerrdefs.IsUnauthorized(err), cerrdefs.IsPermissionDenied(err):
		return &Error{Kind: KindDenied, Msg: msg, Err: err}
	}
	return err
}

// EnvNames returns the sorted, distinct names of NAME=value entries. The
// values are not kept.
func EnvNames(env []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range env {
		name, _, _ := strings.Cut(e, "=")
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (m *mobyEngine) ContainerSizes(ctx context.Context, ids []string) ([]ContainerSize, error) {
	opts := client.ContainerListOptions{All: true, Size: true}
	if len(ids) > 0 {
		opts.Filters = client.Filters{}.Add("id", ids...)
	}
	res, err := m.cli.ContainerList(ctx, opts)
	if err != nil {
		return nil, m.wrap("measure writable layers", err)
	}
	out := make([]ContainerSize, 0, len(res.Items))
	for _, s := range res.Items {
		cs := ContainerSize{ID: s.ID, State: string(s.State), SizeRw: s.SizeRw, SizeRootFs: s.SizeRootFs}
		if len(s.Names) > 0 {
			cs.Name = strings.TrimPrefix(s.Names[0], "/")
		} else {
			cs.Name = ShortID(s.ID)
		}
		out = append(out, cs)
	}
	return out, nil
}

// apiOlder reports whether API version a is older than b ("1.47" < "1.48").
// An unparsable version is treated as current.
func apiOlder(a, b string) bool {
	pa, pb := strings.SplitN(a, ".", 2), strings.SplitN(b, ".", 2)
	if len(pa) != 2 || len(pb) != 2 {
		return false
	}
	ma, err1 := strconv.Atoi(pa[0])
	na, err2 := strconv.Atoi(pa[1])
	mb, err3 := strconv.Atoi(pb[0])
	nb, err4 := strconv.Atoi(pb[1])
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return false
	}
	return ma < mb || (ma == mb && na < nb)
}

func (m *mobyEngine) InspectVolume(ctx context.Context, name string) (VolumeDetail, error) {
	res, err := m.cli.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil {
		return VolumeDetail{}, m.wrap("inspect volume", err)
	}
	v := res.Volume
	return VolumeDetail{
		Volume: Volume{
			Name:       v.Name,
			Driver:     v.Driver,
			Mountpoint: v.Mountpoint,
			Scope:      v.Scope,
			CreatedAt:  v.CreatedAt,
			Labels:     v.Labels,
			Options:    v.Options,
		},
		Raw: res.Raw,
	}, nil
}

func (m *mobyEngine) InspectNetwork(ctx context.Context, id string) (NetworkDetail, error) {
	res, err := m.cli.NetworkInspect(ctx, id, client.NetworkInspectOptions{})
	if err != nil {
		return NetworkDetail{}, m.wrap("inspect network", err)
	}
	s := res.Network
	d := NetworkDetail{Raw: res.Raw, Options: s.Options}
	d.ID = s.ID
	d.Name = s.Name
	d.Driver = s.Driver
	d.Scope = s.Scope
	d.Internal = s.Internal
	d.IPv6 = s.EnableIPv6
	d.Created = s.Created
	d.Labels = s.Labels
	for _, c := range s.IPAM.Config {
		if p := prefixString(c.Subnet); p != "" {
			d.Subnets = append(d.Subnets, p)
		}
		if g := addrString(c.Gateway); g != "" {
			d.Gateways = append(d.Gateways, g)
		}
	}
	for cid, ep := range s.Containers {
		d.Endpoints = append(d.Endpoints, NetworkEndpoint{
			ContainerID: cid,
			Name:        ep.Name,
			IPv4:        prefixString(ep.IPv4Address),
			IPv6:        prefixString(ep.IPv6Address),
			Mac:         ep.MacAddress.String(),
		})
	}
	sort.Slice(d.Endpoints, func(i, j int) bool { return d.Endpoints[i].Name < d.Endpoints[j].Name })
	return d, nil
}

// ---- mutations --------------------------------------------------------------
//
// Every Force / RemoveVolumes / All option of the client library is left at
// its zero value, on purpose and everywhere.

// CreateContainer maps the desk's options onto container.Config and
// container.HostConfig. Everything else in HostConfig stays zero: no force,
// no privilege, no auto-remove.
func (m *mobyEngine) CreateContainer(ctx context.Context, o ContainerCreateOptions) (string, error) {
	cfg, host, err := createConfig(o)
	if err != nil {
		return "", &Error{Kind: KindOther, Op: "create container", Msg: err.Error(), Err: err}
	}
	res, err := m.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: host, Name: o.Name})
	if err != nil {
		return "", m.wrap("create container", err)
	}
	return res.ID, nil
}

func createConfig(o ContainerCreateOptions) (*container.Config, *container.HostConfig, error) {
	cfg := &container.Config{Image: o.Image, Env: o.Env, Cmd: o.Cmd}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{
			Name:              container.RestartPolicyMode(o.Restart.Name),
			MaximumRetryCount: o.Restart.MaximumRetryCount,
		},
	}
	if host.RestartPolicy.Name == "" {
		host.RestartPolicy.Name = container.RestartPolicyDisabled
	}
	for _, p := range o.Ports {
		proto := p.Proto
		if proto == "" {
			proto = "tcp"
		}
		port, ok := network.PortFrom(p.ContainerPort, network.IPProtocol(proto))
		if !ok {
			return nil, nil, fmt.Errorf("not a port: %d/%s", p.ContainerPort, proto)
		}
		if cfg.ExposedPorts == nil {
			cfg.ExposedPorts = network.PortSet{}
			host.PortBindings = network.PortMap{}
		}
		cfg.ExposedPorts[port] = struct{}{}
		b := network.PortBinding{}
		if p.HostIP != "" {
			addr, err := netip.ParseAddr(p.HostIP)
			if err != nil {
				return nil, nil, fmt.Errorf("not an IP address: %s", p.HostIP)
			}
			b.HostIP = addr
		}
		if p.HostPort != 0 {
			b.HostPort = strconv.Itoa(int(p.HostPort))
		}
		host.PortBindings[port] = append(host.PortBindings[port], b)
	}
	for _, mt := range o.Mounts {
		t := mount.TypeVolume
		if mt.Type == "bind" {
			t = mount.TypeBind
		}
		host.Mounts = append(host.Mounts, mount.Mount{Type: t, Source: mt.Source, Target: mt.Target, ReadOnly: mt.ReadOnly})
	}
	return cfg, host, nil
}

func (m *mobyEngine) StartContainer(ctx context.Context, id string) error {
	_, err := m.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return m.wrap("start container", err)
}

func (m *mobyEngine) StopContainer(ctx context.Context, id string) error {
	_, err := m.cli.ContainerStop(ctx, id, client.ContainerStopOptions{})
	return m.wrap("stop container", err)
}

func (m *mobyEngine) RestartContainer(ctx context.Context, id string) error {
	_, err := m.cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{})
	return m.wrap("restart container", err)
}

func (m *mobyEngine) PauseContainer(ctx context.Context, id string) error {
	_, err := m.cli.ContainerPause(ctx, id, client.ContainerPauseOptions{})
	return m.wrap("pause container", err)
}

func (m *mobyEngine) UnpauseContainer(ctx context.Context, id string) error {
	_, err := m.cli.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
	return m.wrap("unpause container", err)
}

func (m *mobyEngine) KillContainer(ctx context.Context, id string) error {
	_, err := m.cli.ContainerKill(ctx, id, client.ContainerKillOptions{Signal: "SIGKILL"})
	return m.wrap("kill container", err)
}

func (m *mobyEngine) RemoveContainer(ctx context.Context, id string) error {
	_, err := m.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{})
	return m.wrap("remove container", err)
}

func (m *mobyEngine) PullImage(ctx context.Context, ref string, progress func(PullProgress)) error {
	resp, err := m.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return m.wrap("pull image", err)
	}
	defer resp.Close()

	type layer struct{ cur, total int64 }
	layers := map[string]*layer{}
	var agg PullProgress
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return m.wrap("pull image", err)
		}
		if msg.Error != nil {
			return &Error{Kind: KindOther, Op: "pull image", Msg: msg.Error.Message, Err: errors.New(msg.Error.Message)}
		}
		if msg.Status != "" {
			agg.Status = msg.Status
			agg.Layer = msg.ID
		}
		if msg.ID != "" && msg.Progress != nil && msg.Progress.Total > 0 {
			l := layers[msg.ID]
			if l == nil {
				l = &layer{}
				layers[msg.ID] = l
			}
			// A layer reports download then extraction; keep the larger total
			// and never let its contribution move backwards.
			if msg.Progress.Total > l.total {
				l.total = msg.Progress.Total
			}
			if strings.HasPrefix(msg.Status, "Downloading") && msg.Progress.Current > l.cur {
				l.cur = msg.Progress.Current
			}
		}
		if msg.ID != "" {
			switch msg.Status {
			case "Download complete", "Pull complete", "Already exists":
				if l := layers[msg.ID]; l != nil {
					l.cur = l.total
				}
			}
		}
		agg.Current, agg.Total = 0, 0
		for _, l := range layers {
			agg.Current += l.cur
			agg.Total += l.total
		}
		if progress != nil {
			progress(agg)
		}
	}
	return nil
}

func (m *mobyEngine) RemoveImage(ctx context.Context, ref string) ([]ImageDelete, error) {
	// PruneChildren mirrors the CLI default: untagged parent layers that
	// nothing else references go with the image. Force stays false.
	res, err := m.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{PruneChildren: true})
	if err != nil {
		return nil, m.wrap("remove image", err)
	}
	out := make([]ImageDelete, 0, len(res.Items))
	for _, it := range res.Items {
		out = append(out, ImageDelete{Untagged: it.Untagged, Deleted: it.Deleted})
	}
	return out, nil
}

func (m *mobyEngine) CreateVolume(ctx context.Context, name string) (Volume, error) {
	res, err := m.cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Driver: "local"})
	if err != nil {
		return Volume{}, m.wrap("create volume", err)
	}
	v := res.Volume
	return Volume{
		Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, Scope: v.Scope,
		CreatedAt: v.CreatedAt, Labels: v.Labels, Options: v.Options,
	}, nil
}

func (m *mobyEngine) RemoveVolume(ctx context.Context, name string) error {
	_, err := m.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{})
	return m.wrap("remove volume", err)
}

func (m *mobyEngine) CreateNetwork(ctx context.Context, name string) (string, error) {
	res, err := m.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge"})
	if err != nil {
		return "", m.wrap("create network", err)
	}
	return res.ID, nil
}

func (m *mobyEngine) RemoveNetwork(ctx context.Context, id string) error {
	_, err := m.cli.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
	return m.wrap("remove network", err)
}

func (m *mobyEngine) ConnectNetwork(ctx context.Context, networkID, containerID string) error {
	_, err := m.cli.NetworkConnect(ctx, networkID, client.NetworkConnectOptions{Container: containerID})
	return m.wrap("connect network", err)
}

func (m *mobyEngine) DisconnectNetwork(ctx context.Context, networkID, containerID string) error {
	_, err := m.cli.NetworkDisconnect(ctx, networkID, client.NetworkDisconnectOptions{Container: containerID})
	return m.wrap("disconnect network", err)
}

// ---- streams ----------------------------------------------------------------

func (m *mobyEngine) Events(ctx context.Context, since time.Time, fn func(Event)) error {
	opts := client.EventsListOptions{}
	if !since.IsZero() {
		opts.Since = strconv.FormatInt(since.Unix(), 10)
	}
	res := m.cli.Events(ctx, opts)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-res.Err:
			if err == nil {
				err = io.EOF
			}
			return m.wrap("events", err)
		case msg := <-res.Messages:
			t := time.Unix(0, msg.TimeNano)
			if msg.TimeNano == 0 {
				t = time.Unix(msg.Time, 0)
			}
			fn(Event{
				Time:       t,
				Type:       string(msg.Type),
				Action:     string(msg.Action),
				ID:         msg.Actor.ID,
				Attributes: msg.Actor.Attributes,
			})
		}
	}
}

func (m *mobyEngine) Logs(ctx context.Context, id string, tail int, follow bool, w io.Writer) error {
	// Whether the stream is multiplexed depends on whether the container has
	// a TTY, which only inspect can tell.
	insp, err := m.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return m.wrap("logs", err)
	}
	tty := insp.Container.Config != nil && insp.Container.Config.Tty

	rc, err := m.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       strconv.Itoa(tail),
	})
	if err != nil {
		return m.wrap("logs", err)
	}
	defer rc.Close()

	if tty {
		_, err = io.Copy(w, rc)
	} else {
		_, err = stdcopy.StdCopy(w, w, rc)
	}
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return m.wrap("logs", err)
}

func (m *mobyEngine) Stats(ctx context.Context, id string, fn func(RawStats)) error {
	res, err := m.cli.ContainerStats(ctx, id, client.ContainerStatsOptions{Stream: true})
	if err != nil {
		return m.wrap("stats", err)
	}
	defer res.Body.Close()
	dec := json.NewDecoder(res.Body)
	for {
		var s container.StatsResponse
		if err := dec.Decode(&s); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return nil // the container stopped; the daemon ended the stream
			}
			return m.wrap("stats", err)
		}
		fn(rawStats(s))
	}
}

func (m *mobyEngine) Processes(ctx context.Context, id string) (ProcessList, error) {
	res, err := m.cli.ContainerTop(ctx, id, client.ContainerTopOptions{})
	if err != nil {
		return ProcessList{}, m.wrap("list processes", err)
	}
	return ProcessList{Titles: res.Titles, Rows: res.Processes}, nil
}

func (m *mobyEngine) ContainerChanges(ctx context.Context, id string) ([]Change, error) {
	res, err := m.cli.ContainerDiff(ctx, id, client.ContainerDiffOptions{})
	if err != nil {
		return nil, m.wrap("list changes", err)
	}
	out := make([]Change, 0, len(res.Changes))
	for _, c := range res.Changes {
		out = append(out, Change{Path: c.Path, Kind: ChangeKind(c.Kind)})
	}
	return out, nil
}

func rawStats(s container.StatsResponse) RawStats {
	r := RawStats{
		Read:       s.Read,
		CPUTotal:   s.CPUStats.CPUUsage.TotalUsage,
		CPUSystem:  s.CPUStats.SystemUsage,
		OnlineCPUs: s.CPUStats.OnlineCPUs,
		MemUsage:   s.MemoryStats.Usage,
		MemLimit:   s.MemoryStats.Limit,
		Pids:       s.PidsStats.Current,
	}
	if r.OnlineCPUs == 0 {
		r.OnlineCPUs = uint32(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	// Same rule as `docker stats`: subtract reclaimable file cache.
	// cgroup v2 reports inactive_file, cgroup v1 total_inactive_file.
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok {
		r.MemCache = v
	} else if v, ok := s.MemoryStats.Stats["total_inactive_file"]; ok {
		r.MemCache = v
	}
	if r.MemCache > r.MemUsage {
		r.MemCache = 0
	}
	for _, n := range s.Networks {
		r.NetRx += n.RxBytes
		r.NetTx += n.TxBytes
	}
	for _, e := range s.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			r.BlkRead += e.Value
		case "write":
			r.BlkWrite += e.Value
		}
	}
	return r
}

// ---- exec -------------------------------------------------------------------

func (m *mobyEngine) ExecCreate(ctx context.Context, containerID string, cmd []string, rows, cols uint) (string, error) {
	res, err := m.cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		TTY:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		ConsoleSize:  client.ConsoleSize{Height: rows, Width: cols},
		Cmd:          cmd,
	})
	if err != nil {
		return "", m.wrap("exec", err)
	}
	return res.ID, nil
}

type hijackConn struct {
	h client.HijackedResponse
}

func (c *hijackConn) Read(p []byte) (int, error)  { return c.h.Reader.Read(p) }
func (c *hijackConn) Write(p []byte) (int, error) { return c.h.Conn.Write(p) }
func (c *hijackConn) CloseWrite() error           { return c.h.CloseWrite() }
func (c *hijackConn) Close() error                { c.h.Close(); return nil }

// ExecAttach is bounded by ctx even where the client library is not: for a
// TLS endpoint it dials the hijacked connection without a context, so a peer
// that accepts and then stalls would otherwise hold the caller — and with it
// the terminal the shell was handed — indefinitely.
func (m *mobyEngine) ExecAttach(ctx context.Context, execID string, rows, cols uint) (ExecConn, error) {
	type result struct {
		res client.ExecAttachResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := m.cli.ExecAttach(ctx, execID, client.ExecAttachOptions{
			TTY:         true,
			ConsoleSize: client.ConsoleSize{Height: rows, Width: cols},
		})
		done <- result{res, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, m.wrap("exec", r.err)
		}
		return &hijackConn{h: r.res.HijackedResponse}, nil
	case <-ctx.Done():
		// Whenever the library does come back, nobody is waiting: a
		// connection that got through after all is closed again.
		go func() {
			if r := <-done; r.err == nil {
				r.res.Close()
			}
		}()
		return nil, m.wrap("exec", fmt.Errorf("the daemon did not attach the process in time: %w", ctx.Err()))
	}
}

func (m *mobyEngine) ExecResize(ctx context.Context, execID string, rows, cols uint) error {
	_, err := m.cli.ExecResize(ctx, execID, client.ExecResizeOptions{Height: rows, Width: cols})
	return m.wrap("resize exec", err)
}

func (m *mobyEngine) ExecInspect(ctx context.Context, execID string) (ExecStatus, error) {
	res, err := m.cli.ExecInspect(ctx, execID, client.ExecInspectOptions{})
	if err != nil {
		return ExecStatus{}, m.wrap("inspect exec", err)
	}
	return ExecStatus{Running: res.Running, ExitCode: res.ExitCode}, nil
}
