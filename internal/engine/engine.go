package engine

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

// Engine is everything the desk can ask of a Docker daemon.
//
// Methods are grouped into reads, mutations, streams and exec. Every mutating
// method but one takes the daemon identifier of exactly one object; none of
// them can force or cascade. The exception is BuildCachePrune: Docker has no
// per-record build-cache delete, so that one request acts on a set the daemon
// chooses (unused records only), and its report says what it removed.
// LoadImage takes no identifier at all: the image and the references it
// writes are named by the archive it is given.
type Engine interface {
	// ---- identity -------------------------------------------------------
	Ping(ctx context.Context) error
	Info(ctx context.Context) (Info, error)

	// ---- reads ----------------------------------------------------------
	Containers(ctx context.Context) ([]Container, error)
	Images(ctx context.Context) ([]Image, error)
	Volumes(ctx context.Context) ([]Volume, error)
	Networks(ctx context.Context) ([]Network, error)

	InspectContainer(ctx context.Context, id string) (ContainerDetail, error)
	InspectImage(ctx context.Context, ref string) (ImageDetail, error)
	InspectVolume(ctx context.Context, name string) (VolumeDetail, error)
	InspectNetwork(ctx context.Context, id string) (NetworkDetail, error)

	// SaveImage streams the image's archive, as `docker save` would write
	// it, for one platform. It is a GET: the daemon reads the image store
	// and changes nothing. The caller reads the stream and must close it.
	SaveImage(ctx context.Context, id string, platform Platform) (io.ReadCloser, error)

	// DiskUsage is the daemon's own disk account (GET /system/df), with
	// per-image, per-volume and build-cache items. The daemon walks
	// container and volume filesystems to answer: callers run it as a job.
	DiskUsage(ctx context.Context) (DiskUsage, error)
	// ImageSizes lists every image with its shared size computed
	// (GET /images/json?shared-size=1).
	ImageSizes(ctx context.Context) ([]ImageUsage, error)
	// ImageLayers returns an image's diff IDs (inspect) and history.
	ImageLayers(ctx context.Context, id string) (ImageLayers, error)
	// ImageFacts is what the Changes tab compares of one image: its
	// identity, config and layers (inspect, then history: two GETs).
	// Environment variables come back as names only; their values never
	// leave this method.
	ImageFacts(ctx context.Context, id string) (ImageFacts, error)
	// ContainerSizes lists containers with their writable layers measured
	// (GET /containers/json?all=1&size=1) — all of them, or only ids. The
	// daemon walks each filesystem to answer; never part of a list refresh.
	ContainerSizes(ctx context.Context, ids []string) ([]ContainerSize, error)
	// DistributionInspect asks the daemon to ask the registry what ref's
	// tag names now (GET /distribution/{ref}/json). It is a GET; the daemon
	// changes nothing. registryAuth is the credential as authconfig.Encode
	// writes it, sent as X-Registry-Auth, or "" for none. A daemon that does
	// not serve the endpoint is KindUnsupported, which is distinct from a
	// registry that has no such manifest (KindNotFound).
	DistributionInspect(ctx context.Context, ref, registryAuth string) (Distribution, error)

	// CopyFromContainer streams a tar of path in the container — its single
	// top-level entry is the path's base name — with the daemon's stat of
	// it (GET /containers/{id}/archive, what `docker cp` reads). It is a
	// GET: nothing in the container changes. The caller reads the stream
	// and must close it. A path that does not exist is KindNotFound.
	CopyFromContainer(ctx context.Context, id, path string) (Archive, error)

	// ---- mutations ------------------------------------------------------
	// CopyToContainer extracts content, a tar whose entries are rooted at
	// base(dest), into the container at parent(dest) (PUT
	// /containers/{id}/archive): dest is created, or its same-named files
	// replaced. A directory is never replaced by a file or a file by a
	// directory, and ownership is the daemon's default. dest must be an
	// absolute path other than "/", and its parent must exist.
	CopyToContainer(ctx context.Context, id, dest string, content io.Reader) error
	StartContainer(ctx context.Context, id string) error
	// StopContainer asks the container to stop with its configured signal and
	// grace period; the daemon kills it when the grace period expires.
	StopContainer(ctx context.Context, id string) error
	RestartContainer(ctx context.Context, id string) error
	PauseContainer(ctx context.Context, id string) error
	UnpauseContainer(ctx context.Context, id string) error
	// KillContainer sends SIGKILL.
	KillContainer(ctx context.Context, id string) error
	// RenameContainer gives a container a new name (POST
	// /containers/{id}/rename). The daemon refuses a name another container
	// holds.
	RenameContainer(ctx context.Context, id, newName string) error
	// RemoveContainer removes a container that is not running. Its anonymous
	// volumes are kept. A running container is refused by the daemon.
	RemoveContainer(ctx context.Context, id string) error
	// CreateContainer creates a container from a resolved configuration and
	// returns the ID the daemon assigned. It never starts it, never pulls,
	// and sets nothing the options do not name: no privilege, no
	// auto-remove, the daemon's default network.
	CreateContainer(ctx context.Context, opts ContainerCreateOptions) (string, error)

	// PullImage pulls exactly one reference (":latest" when no tag or digest
	// is given, never all tags) and reports aggregated progress.
	PullImage(ctx context.Context, ref string, progress func(PullProgress)) error
	// PushImage asks the daemon to upload exactly one tagged reference to the
	// registry it names (POST /images/{name}/push?tag=…): never all tags, no
	// platform, no retag. ref must carry an explicit tag and no digest;
	// anything else is refused before a request is sent. registryAuth is
	// the credential as authconfig.Encode writes it, sent as
	// X-Registry-Auth, or "" for none. It never appears in an error or a
	// progress report: every string the daemon sends back is scrubbed of
	// it. The result is the daemon's own final report; a stream that ends
	// without one is an error, because only the daemon can say the push
	// completed. The local image and tag are not changed.
	PushImage(ctx context.Context, ref, registryAuth string, progress func(PushProgress)) (PushResult, error)
	// RemoveImage removes a reference (untagging) or an image ID. An image in
	// use is refused by the daemon.
	RemoveImage(ctx context.Context, ref string) ([]ImageDelete, error)
	// TagImage gives the image source (an ID or reference) the target tag
	// locally (POST /images/{name}/tag?repo=…&tag=…). No registry is
	// contacted. target must be a named reference without a digest; a
	// target with no tag means ":latest". A target that already names
	// another image is moved by the daemon without a word: callers that
	// must not move a tag unseen inspect the target first (plan.Execute
	// does). Nothing is forced and exactly one reference is written.
	TagImage(ctx context.Context, source, target string) error
	// LoadImage sends one image archive — the stream SaveImage returns, as
	// `docker save` writes it — to the daemon, which adds the image to its
	// store (POST /images/load, a raw application/x-tar body). It is a
	// write: the image is created, and every reference the archive carries
	// is created or, when it names another image here, moved to the loaded
	// one without a refusal. Docker has no conditional load: a caller that
	// must not move a tag unseen inspects the reference first, and inspects
	// it again afterwards to learn what it names (plan.ExecuteCopy does).
	//
	// One archive, one platform: platform selects the variant to load from
	// a multi-platform archive, and is sent only to a daemon whose API has
	// the parameter (1.48); an older daemon loads the one variant the
	// archive carries. content is read once, to its end, and never written
	// anywhere else; the caller owns it and closes it. No registry is
	// contacted and nothing is forced.
	//
	// progress receives every message of the daemon's answer as it arrives.
	// An error message in the stream ends the load as an *Error carrying
	// the daemon's text. Cancelling ctx closes the request and returns
	// ctx's error: layers the daemon already accepted may remain in its
	// store, and nothing may be claimed about the image or its references.
	// The result is only what the daemon said it loaded.
	LoadImage(ctx context.Context, content io.Reader, platform Platform, progress func(LoadProgress)) (LoadResult, error)

	CreateVolume(ctx context.Context, name string) (Volume, error)
	RemoveVolume(ctx context.Context, name string) error

	// CreateNetwork creates a bridge network and returns its ID.
	CreateNetwork(ctx context.Context, name string) (string, error)
	RemoveNetwork(ctx context.Context, id string) error
	ConnectNetwork(ctx context.Context, networkID, containerID string) error
	DisconnectNetwork(ctx context.Context, networkID, containerID string) error

	// BuildCachePrune asks the daemon to remove unused build-cache records
	// (POST /build/prune). all includes internal/frontend records. The daemon
	// chooses the records — it never removes one in use — and the report
	// names what it removed. No filter, size threshold or force is sent.
	// This is the only mutating method whose request acts on a set.
	BuildCachePrune(ctx context.Context, all bool) (BuildCachePruneReport, error)

	// ---- streams (block until ctx is cancelled or the stream ends) -------
	// Events delivers engine events from since (zero means "from now").
	Events(ctx context.Context, since time.Time, fn func(Event)) error
	// Logs writes the container's combined stdout and stderr to w, starting
	// with the last tail lines, and keeps writing while follow is set.
	Logs(ctx context.Context, id string, tail int, follow bool, w io.Writer) error
	// Stats delivers roughly one raw sample per second.
	Stats(ctx context.Context, id string, fn func(RawStats)) error
	// Processes is one snapshot of the daemon's default process listing for
	// a running container (GET /containers/{id}/top, no ps arguments). A
	// container that is not running is refused with a KindConflict error.
	Processes(ctx context.Context, id string) (ProcessList, error)
	// ContainerChanges is the daemon's list of the paths a container has
	// added, changed or deleted since its image (GET
	// /containers/{id}/changes, what `docker diff` prints). It is a GET and
	// cheap: the storage driver compares the writable layer with the
	// image's, and no file's bytes cross the connection. Paths are the
	// daemon's, unescaped and untrusted.
	ContainerChanges(ctx context.Context, id string) ([]Change, error)

	// ---- exec ------------------------------------------------------------
	// ExecCreate prepares an interactive TTY process in a running container.
	ExecCreate(ctx context.Context, containerID string, cmd []string, rows, cols uint) (string, error)
	// ExecAttach starts the prepared process and returns its terminal stream.
	ExecAttach(ctx context.Context, execID string, rows, cols uint) (ExecConn, error)
	ExecResize(ctx context.Context, execID string, rows, cols uint) error
	ExecInspect(ctx context.Context, execID string) (ExecStatus, error)

	Close() error
}

// ExecConn is the bidirectional terminal stream of an exec'd process.
type ExecConn interface {
	io.Reader
	io.Writer
	// CloseWrite signals end of input to the process.
	CloseWrite() error
	Close() error
}

// ErrReadOnly is returned by every mutating method in read-only mode.
var ErrReadOnly = errors.New("docker desk is in read-only mode: nothing was sent to the daemon")

// Kind classifies an engine error for the UI.
type Kind int

const (
	KindOther Kind = iota
	// KindNotFound: the object no longer exists.
	KindNotFound
	// KindConflict: the daemon refused because of the object's state — in
	// use, running, already started, name taken.
	KindConflict
	// KindUnavailable: the daemon could not be reached.
	KindUnavailable
	// KindReadOnly: refused locally by read-only mode.
	KindReadOnly
	// KindUnsupported: the daemon does not serve the endpoint asked for.
	KindUnsupported
	// KindDenied: the daemon, or the registry behind it, refused the
	// credential (401 or 403 on a request that is not a state change).
	KindDenied
)

// Error is an error from the daemon (or from reaching it) with its class.
// Its message is the daemon's own text, without client-library prefixes.
type Error struct {
	Kind Kind
	Op   string // what the desk was doing, e.g. "remove container"
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e.Op == "" {
		return e.Msg
	}
	return e.Op + ": " + e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the class of err.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	if errors.Is(err, ErrReadOnly) {
		return KindReadOnly
	}
	return KindOther
}

// IsNotFound reports whether the daemon said the object does not exist.
func IsNotFound(err error) bool { return KindOf(err) == KindNotFound }

// IsConflict reports whether the daemon refused because of the object's state.
func IsConflict(err error) bool { return KindOf(err) == KindConflict }

// IsUnavailable reports whether the daemon could not be reached.
func IsUnavailable(err error) bool { return KindOf(err) == KindUnavailable }

// cleanMessage strips the client library's framing from a daemon message.
func cleanMessage(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "Error response from daemon: ")
	return strings.TrimSpace(msg)
}
