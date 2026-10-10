//go:build race

package ui

// raceEnabled is true when the tests run under the race detector, which
// slows stream ingestion by an order of magnitude. Throughput figures are
// only asserted without it; every bound is asserted either way.
const raceEnabled = true
