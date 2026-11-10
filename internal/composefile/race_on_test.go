//go:build race

package composefile

// mutationStride thins the mutation sweep under the race detector, which
// slows parsing tenfold; the parser has no shared state for it to watch.
const mutationStride = 7
