package plan

import (
	"context"
	"sync"

	"github.com/fardani235/docker-desk/internal/engine"
)

// Take reads a fresh snapshot of the engine. Plans are always built from a
// snapshot taken at the moment the operator asks for the action, never from
// whatever the lists on screen happened to show.
func Take(ctx context.Context, eng engine.Engine) (Snapshot, error) {
	var (
		snap Snapshot
		wg   sync.WaitGroup
		mu   sync.Mutex
		fail error
	)
	note := func(err error) {
		if err != nil {
			mu.Lock()
			if fail == nil {
				fail = err
			}
			mu.Unlock()
		}
	}
	wg.Add(4)
	go func() { defer wg.Done(); var err error; snap.Containers, err = eng.Containers(ctx); note(err) }()
	go func() { defer wg.Done(); var err error; snap.Images, err = eng.Images(ctx); note(err) }()
	go func() { defer wg.Done(); var err error; snap.Volumes, err = eng.Volumes(ctx); note(err) }()
	go func() { defer wg.Done(); var err error; snap.Networks, err = eng.Networks(ctx); note(err) }()
	wg.Wait()
	return snap, fail
}
