//go:build !unix

package composefile

import "testing"

func makeFIFO(*testing.T, string) string { return "" }
