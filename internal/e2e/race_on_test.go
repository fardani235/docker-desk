//go:build race

package e2e

// raceEnabled is true when the tests run under the race detector. The
// docker-desk binary under test is built without it, but the harness around
// it — the fake daemon producing the flood, the terminal emulator, the
// polling loop that takes the measurement — is slowed several times over, so
// the latency it measures is no longer the desk's. The figure is then logged
// rather than asserted.
const raceEnabled = true
