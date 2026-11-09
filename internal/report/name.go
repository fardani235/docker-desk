package report

import (
	"strings"
	"time"
)

// DefaultPath is the path the export dialog is prefilled with:
// ~/docker-desk-<kind>-<object>-<YYYYMMDD>.<ext>, with the object reduced to
// letters, digits, dots and dashes (python:3.12-slim → python-3.12-slim).
// The ~ is expanded only when the path is confirmed (ExpandPath).
func DefaultPath(kind, object string, f Format, day time.Time) string {
	return "~/docker-desk-" + kind + "-" + Slug(object) + "-" + day.Format("20060102") + "." + f.Ext()
}

// Slug reduces s to what is safe in a file name.
func Slug(s string) string {
	s = strings.TrimPrefix(strings.ToLower(s), "sha256:")
	var b strings.Builder
	dash := false
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.'
		if ok {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-.")
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-.")
	}
	if out == "" {
		return "object"
	}
	return out
}

// SwapExt replaces a path's format extension with f's, when it ends in one
// of the formats' extensions; otherwise the path is left as typed.
func SwapExt(path string, f Format) string {
	// The longer extensions first: "x.spdx.json" ends in ".json" too.
	for _, g := range []Format{SPDX, CycloneDX, JSON, Text} {
		if strings.HasSuffix(path, "."+g.Ext()) {
			return strings.TrimSuffix(path, "."+g.Ext()) + "." + f.Ext()
		}
	}
	return path
}
