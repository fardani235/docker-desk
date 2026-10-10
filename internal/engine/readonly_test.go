package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/fardani235/docker-desk/internal/fakeengine"
)

// readMethods are the Engine methods that cannot change anything. Every other
// method must be overridden by the read-only wrapper.
var readMethods = map[string]bool{
	"Ping": true, "Info": true, "Close": true,
	"Containers": true, "Images": true, "Volumes": true, "Networks": true,
	"InspectContainer": true, "InspectImage": true, "InspectVolume": true, "InspectNetwork": true,
	"Events": true, "Logs": true, "Stats": true,
	"ExecInspect": true,
}

// TestReadOnlyCoversEveryMutation calls every non-read method of a read-only
// engine that wraps nothing. An overridden method returns ErrReadOnly; one
// that was forgotten falls through to the nil engine and panics, which fails
// the test. Adding a mutating method to Engine therefore cannot silently
// escape read-only mode.
func TestReadOnlyCoversEveryMutation(t *testing.T) {
	ro := reflect.ValueOf(ReadOnly(nil))
	iface := reflect.TypeOf((*Engine)(nil)).Elem()
	checked := 0
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		if readMethods[m.Name] {
			continue
		}
		checked++
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s is not overridden by the read-only wrapper (it reached the wrapped engine)", m.Name)
				}
			}()
			args := make([]reflect.Value, m.Type.NumIn())
			for j := range args {
				args[j] = reflect.Zero(m.Type.In(j))
			}
			args[0] = reflect.ValueOf(context.Background())
			out := ro.MethodByName(m.Name).Call(args)
			err, _ := out[len(out)-1].Interface().(error)
			if !errors.Is(err, ErrReadOnly) {
				t.Errorf("%s returned %v, want ErrReadOnly", m.Name, err)
			}
		}()
	}
	if checked != 18 {
		t.Errorf("checked %d mutating methods, expected 18 — update readMethods if the interface changed", checked)
	}
}

func TestReadOnlySendsNothing(t *testing.T) {
	srv := fakeengine.New()
	srv.SeedDemo()
	cleanup, err := srv.StartTemp()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	eng, err := New(Options{Host: srv.Host(), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if !IsReadOnly(eng) {
		t.Fatal("engine does not report read-only")
	}
	ctx := ctxT(t)
	id := srv.ContainerID("shop-web-1")
	net := srv.NetworkID("orphan-net")

	// Reads work.
	if cs, err := eng.Containers(ctx); err != nil || len(cs) != 12 {
		t.Fatalf("read in read-only mode: %d %v", len(cs), err)
	}

	try := func(e Engine) []error {
		var errs []error
		add := func(err error) { errs = append(errs, err) }
		add(e.StartContainer(ctx, id))
		add(e.StopContainer(ctx, id))
		add(e.RestartContainer(ctx, id))
		add(e.PauseContainer(ctx, id))
		add(e.UnpauseContainer(ctx, id))
		add(e.KillContainer(ctx, id))
		add(e.RemoveContainer(ctx, id))
		add(e.PullImage(ctx, "alpine", nil))
		_, err := e.RemoveImage(ctx, "python:3.12-slim")
		add(err)
		_, err = e.CreateVolume(ctx, "x")
		add(err)
		add(e.RemoveVolume(ctx, "old-backups"))
		_, err = e.CreateNetwork(ctx, "x")
		add(err)
		add(e.RemoveNetwork(ctx, net))
		add(e.ConnectNetwork(ctx, net, id))
		add(e.DisconnectNetwork(ctx, net, id))
		_, err = e.ExecCreate(ctx, id, []string{"sh"}, 24, 80)
		add(err)
		return errs
	}

	// Layer 1: the wrapper.
	for i, err := range try(eng) {
		if !errors.Is(err, ErrReadOnly) {
			t.Errorf("wrapper, call %d: %v", i, err)
		}
	}
	// Layer 2: bypass the wrapper; the HTTP request hook must still refuse.
	for i, err := range try(eng.(*readOnlyEngine).Engine) {
		if !errors.Is(err, ErrReadOnly) {
			t.Errorf("request hook, call %d: %v", i, err)
		}
	}
	if muts := srv.Mutations(); len(muts) != 0 {
		t.Fatalf("read-only mode let %d mutating request(s) reach the daemon: %v", len(muts), muts)
	}
	if st, _ := srv.ContainerState("shop-web-1"); st != "running" {
		t.Fatalf("container state changed to %q", st)
	}
}
