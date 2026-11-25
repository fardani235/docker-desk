package engine

import (
	"context"
	"io"
)

// ReadOnly wraps an Engine so that every mutating method — and exec, which
// runs a process inside a container — returns ErrReadOnly without calling the
// wrapped engine. Reads and streams pass through.
//
// The embedded interface supplies the read methods; every mutating method of
// Engine is overridden below. TestReadOnlyCoversEveryMutation fails if a new
// mutating method is added to Engine without being overridden here.
func ReadOnly(e Engine) Engine { return &readOnlyEngine{Engine: e} }

// IsReadOnly reports whether e refuses mutations.
func IsReadOnly(e Engine) bool {
	_, ok := e.(*readOnlyEngine)
	return ok
}

type readOnlyEngine struct {
	Engine
}

// CopyToContainer writes into a container: refused. CopyFromContainer only
// reads, and passes through.
func (r *readOnlyEngine) CopyToContainer(context.Context, string, string, io.Reader) error {
	return ErrReadOnly
}

func (r *readOnlyEngine) StartContainer(context.Context, string) error   { return ErrReadOnly }
func (r *readOnlyEngine) StopContainer(context.Context, string) error    { return ErrReadOnly }
func (r *readOnlyEngine) RestartContainer(context.Context, string) error { return ErrReadOnly }
func (r *readOnlyEngine) PauseContainer(context.Context, string) error   { return ErrReadOnly }
func (r *readOnlyEngine) UnpauseContainer(context.Context, string) error { return ErrReadOnly }
func (r *readOnlyEngine) KillContainer(context.Context, string) error    { return ErrReadOnly }
func (r *readOnlyEngine) RemoveContainer(context.Context, string) error  { return ErrReadOnly }

func (r *readOnlyEngine) RenameContainer(context.Context, string, string) error {
	return ErrReadOnly
}

func (r *readOnlyEngine) CreateContainer(context.Context, ContainerCreateOptions) (string, error) {
	return "", ErrReadOnly
}

func (r *readOnlyEngine) PullImage(context.Context, string, func(PullProgress)) error {
	return ErrReadOnly
}

// PushImage is a POST that publishes to a registry: refused before anything
// is sent, the credential included.
func (r *readOnlyEngine) PushImage(context.Context, string, string, func(PushProgress)) (PushResult, error) {
	return PushResult{}, ErrReadOnly
}

func (r *readOnlyEngine) RemoveImage(context.Context, string) ([]ImageDelete, error) {
	return nil, ErrReadOnly
}

// LoadImage adds an image to the daemon's store and can move a tag:
// refused before anything is sent, and without reading a byte of the archive.
func (r *readOnlyEngine) LoadImage(context.Context, io.Reader, Platform, func(LoadProgress)) (LoadResult, error) {
	return LoadResult{}, ErrReadOnly
}

// TagImage writes a local reference, and can move one: refused.
func (r *readOnlyEngine) TagImage(context.Context, string, string) error { return ErrReadOnly }

func (r *readOnlyEngine) CreateVolume(context.Context, string) (Volume, error) {
	return Volume{}, ErrReadOnly
}

func (r *readOnlyEngine) RemoveVolume(context.Context, string) error { return ErrReadOnly }

func (r *readOnlyEngine) CreateNetwork(context.Context, string) (string, error) {
	return "", ErrReadOnly
}

func (r *readOnlyEngine) RemoveNetwork(context.Context, string) error { return ErrReadOnly }

// BuildCachePrune acts on a set the daemon chooses: refused before anything
// is sent.
func (r *readOnlyEngine) BuildCachePrune(context.Context, bool) (BuildCachePruneReport, error) {
	return BuildCachePruneReport{}, ErrReadOnly
}

func (r *readOnlyEngine) ConnectNetwork(context.Context, string, string) error { return ErrReadOnly }

func (r *readOnlyEngine) DisconnectNetwork(context.Context, string, string) error {
	return ErrReadOnly
}

func (r *readOnlyEngine) ExecCreate(context.Context, string, []string, uint, uint) (string, error) {
	return "", ErrReadOnly
}

func (r *readOnlyEngine) ExecAttach(context.Context, string, uint, uint) (ExecConn, error) {
	return nil, ErrReadOnly
}

func (r *readOnlyEngine) ExecResize(context.Context, string, uint, uint) error { return ErrReadOnly }
