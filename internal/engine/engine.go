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
// method takes the daemon identifier of exactly one object; none of them can
// force, cascade or act on a set.
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

	// ---- mutations ------------------------------------------------------
	StartContainer(ctx context.Context, id string) error
	// StopContainer asks the container to stop with its configured signal and
	// grace period; the daemon kills it when the grace period expires.
	StopContainer(ctx context.Context, id string) error
	RestartContainer(ctx context.Context, id string) error
	PauseContainer(ctx context.Context, id string) error
	UnpauseContainer(ctx context.Context, id string) error
	// KillContainer sends SIGKILL.
	KillContainer(ctx context.Context, id string) error
	// RemoveContainer removes a container that is not running. Its anonymous
	// volumes are kept. A running container is refused by the daemon.
	RemoveContainer(ctx context.Context, id string) error

	// PullImage pulls exactly one reference (":latest" when no tag or digest
	// is given, never all tags) and reports aggregated progress.
	PullImage(ctx context.Context, ref string, progress func(PullProgress)) error
	// RemoveImage removes a reference (untagging) or an image ID. An image in
	// use is refused by the daemon.
	RemoveImage(ctx context.Context, ref string) ([]ImageDelete, error)

	CreateVolume(ctx context.Context, name string) (Volume, error)
	RemoveVolume(ctx context.Context, name string) error

	// CreateNetwork creates a bridge network and returns its ID.
	CreateNetwork(ctx context.Context, name string) (string, error)
	RemoveNetwork(ctx context.Context, id string) error
	ConnectNetwork(ctx context.Context, networkID, containerID string) error
	DisconnectNetwork(ctx context.Context, networkID, containerID string) error

	// ---- streams (block until ctx is cancelled or the stream ends) -------
	// Events delivers engine events from since (zero means "from now").
	Events(ctx context.Context, since time.Time, fn func(Event)) error
	// Logs writes the container's combined stdout and stderr to w, starting
	// with the last tail lines, and keeps writing while follow is set.
	Logs(ctx context.Context, id string, tail int, follow bool, w io.Writer) error
	// Stats delivers roughly one raw sample per second.
	Stats(ctx context.Context, id string, fn func(RawStats)) error

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
