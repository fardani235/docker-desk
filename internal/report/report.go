// Package report writes what the desk has already computed — an image's
// findings and packages, the Space account — as evidence files
// (docs/DECISIONS.md, X1–X16).
//
// The serializers here are pure: they take the plain structs of this
// package, which hold only what a file may carry, and return bytes. They
// import nothing of the desk's, so nothing here can reach the daemon, the
// network or a secret's value. The views and the command line collect the
// structs (package report/collect) and call them; no view serializes.
//
// Every serializer orders what it is given itself — packages by name then
// version, findings by group then layer, rule and path, everything else by
// a key of its own — so the order a caller happens to hold, or a map's,
// never reaches a file. The same input is the same bytes, every time.
//
// No document carries a timestamp, a host path, a user name, or an ID that
// is not needed to say which object it describes. Where a format demands a
// time (SPDX's creationInfo.created), it is a fixed one, and says so.
package report

import (
	"fmt"
	"strings"
)

// Format is a file format an export can be written in.
type Format string

const (
	JSON      Format = "json"      // the desk's own schema: everything computed
	SPDX      Format = "spdx"      // SPDX 2.3 JSON, packages only
	CycloneDX Format = "cyclonedx" // CycloneDX 1.5 JSON, packages only
	Text      Format = "text"      // findings by group, one line each; Space: totals and groups
)

// Formats lists every format, in the order a dialog offers them.
var Formats = []Format{JSON, SPDX, CycloneDX, Text}

// ParseFormat reads a format's name.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(strings.TrimSpace(s))); f {
	case JSON, SPDX, CycloneDX, Text:
		return f, nil
	case "cdx":
		return CycloneDX, nil
	case "txt":
		return Text, nil
	}
	return "", fmt.Errorf("unknown report format %q (json, spdx, cyclonedx or text)", s)
}

// Ext is the file extension a format is prefilled with.
func (f Format) Ext() string {
	switch f {
	case SPDX:
		return "spdx.json"
	case CycloneDX:
		return "cdx.json"
	case Text:
		return "txt"
	}
	return "json"
}

// Describe is the format in words, for a dialog.
func (f Format) Describe(space bool) string {
	switch f {
	case SPDX:
		return "SPDX 2.3 JSON — packages only"
	case CycloneDX:
		return "CycloneDX 1.5 JSON — packages only"
	case Text:
		if space {
			return "text — totals and shared groups, one line each"
		}
		return "text — findings by group, one line each"
	}
	return "JSON — everything computed, in the desk's own schema"
}

// DescribeFor is the format in words for the document a dialog writes.
func (f Format) DescribeFor(d Document) string {
	if d.Files != nil {
		if f == Text {
			return "text — one path per line, tab-separated, names escaped"
		}
		return "JSON — every path with its type, size, mode and layer, names escaped"
	}
	return f.Describe(d.Space != nil)
}

// Document is what one export describes: an image, the Space account, or
// an image's file listing. Exactly one is set.
type Document struct {
	Image *Image
	Space *Space
	Files *Files
}

// Kind is the document's kind, as the prefilled file name says it.
func (d Document) Kind() string {
	switch {
	case d.Space != nil:
		return "space"
	case d.Files != nil:
		return "files"
	}
	return "findings"
}

// FormatsFor lists the formats the document can be written in.
func FormatsFor(d Document) []Format {
	if d.Space != nil || d.Files != nil {
		return []Format{JSON, Text}
	}
	return Formats
}

// Render serializes the document in the format.
func Render(f Format, d Document) ([]byte, error) {
	set := 0
	for _, x := range []bool{d.Image != nil, d.Space != nil, d.Files != nil} {
		if x {
			set++
		}
	}
	switch {
	case set != 1:
		return nil, fmt.Errorf("an export describes exactly one image, the Space account or a file listing")
	case d.Files != nil:
		switch f {
		case JSON:
			return FilesJSON(*d.Files)
		case Text:
			return FilesText(*d.Files), nil
		}
		return nil, fmt.Errorf("%s holds packages only; a file listing is written as json or text", f)
	case d.Space != nil:
		switch f {
		case JSON:
			return SpaceJSON(*d.Space)
		case Text:
			return SpaceText(*d.Space), nil
		}
		return nil, fmt.Errorf("%s holds packages only; the Space account is written as json or text", f)
	}
	switch f {
	case JSON:
		return ImageJSON(*d.Image)
	case SPDX:
		return ImageSPDX(*d.Image)
	case CycloneDX:
		return ImageCycloneDX(*d.Image)
	case Text:
		return ImageText(*d.Image), nil
	}
	return nil, fmt.Errorf("unknown report format %q", f)
}

// Tool names the program that wrote a document.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func toolOf(version string) Tool {
	if version == "" {
		version = "dev"
	}
	return Tool{Name: "docker-desk", Version: version}
}
