package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// loadOp is how a load is named in its errors.
const loadOp = "load image"

// LoadProgress is one message of the daemon's load stream, as it sent it.
// Every string is the daemon's own and untrusted: escape it before drawing.
type LoadProgress struct {
	Status string // "Loading layer", …
	ID     string // the layer the status is about, when the daemon names one
	// Current and Total are bytes, when the daemon reports them (else 0).
	Current, Total int64
	// Text is a plain stream line ("Loaded image: …"), without its line end.
	Text string
}

// LoadResult is what the daemon said it loaded, in its own final lines. An
// empty result is not a failure — a daemon may load without naming anything
// — and a filled one is not proof: the caller inspects the daemon to learn
// what the reference names now.
type LoadResult struct {
	Refs []string // "Loaded image: ref" lines
	IDs  []string // "Loaded image ID: sha256:…" lines
}

const (
	// maxLoadLine bounds one message of the load stream. The daemon's
	// messages are a few hundred bytes; a longer one is not believed.
	maxLoadLine = 1 << 20
	// maxLoadNames bounds the references and IDs a load result keeps.
	maxLoadNames = 64
	// maxLoadText bounds the text of one message that is passed on.
	maxLoadText = 4 << 10
)

// loadMessage is one JSON message of the load stream.
type loadMessage struct {
	Stream   string `json:"stream"`
	Status   string `json:"status"`
	ID       string `json:"id"`
	Progress *struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
	Detail *struct {
		Message string `json:"message"`
	} `json:"errorDetail"`
	Error string `json:"error"`
}

func (m *mobyEngine) LoadImage(ctx context.Context, content io.Reader, platform Platform, progress func(LoadProgress)) (LoadResult, error) {
	if content == nil {
		msg := "internal error: a load without an archive; nothing was sent"
		return LoadResult{}, &Error{Kind: KindOther, Op: loadOp, Msg: msg, Err: errors.New(msg)}
	}
	var opts []client.ImageLoadOption
	// The platform selects one variant of a multi-platform archive. A
	// daemon older than the parameter loads the one variant the archive
	// carries, and the library would refuse to send the request at all.
	if platform.OS != "" && platform.Architecture != "" && !apiOlder(m.cli.ClientVersion(), platformAPI) {
		opts = append(opts, client.ImageLoadWithPlatforms(ocispec.Platform{
			OS: platform.OS, Architecture: platform.Architecture, Variant: platform.Variant,
		}))
	}
	resp, err := m.cli.ImageLoad(ctx, content, opts...)
	if err != nil {
		return LoadResult{}, m.wrap(loadOp, err)
	}
	defer resp.Close()
	res, err := decodeLoad(resp, progress)
	// A cancelled context can end the stream as a clean EOF, or as a
	// broken read: either way it is a cancellation and nothing is claimed.
	if cerr := ctx.Err(); cerr != nil {
		return LoadResult{}, cerr
	}
	if err != nil {
		return LoadResult{}, m.wrap(loadOp, err)
	}
	return res, nil
}

// decodeLoad reads the daemon's answer to a load: JSON messages, one per
// line. A message that carries an error ends the load as that error. A
// line that is not JSON is passed on as text, not treated as a failure: the
// daemon's own words are worth more than a guess at their shape.
func decodeLoad(r io.Reader, progress func(LoadProgress)) (LoadResult, error) {
	var res LoadResult
	br := bufio.NewReaderSize(r, 32<<10)
	for {
		line, err := readLoadLine(br)
		if len(line) > 0 {
			p, lerr := parseLoadLine(line)
			if lerr != nil {
				return LoadResult{}, lerr
			}
			for _, text := range []string{p.Text, p.Status} {
				if ref, ok := strings.CutPrefix(text, "Loaded image: "); ok && ref != "" && len(res.Refs) < maxLoadNames {
					res.Refs = append(res.Refs, ref)
				}
				if id, ok := strings.CutPrefix(text, "Loaded image ID: "); ok && id != "" && len(res.IDs) < maxLoadNames {
					res.IDs = append(res.IDs, id)
				}
			}
			if progress != nil && (p != LoadProgress{}) {
				progress(p)
			}
		}
		switch {
		case err == io.EOF:
			return res, nil
		case err != nil:
			return LoadResult{}, err
		}
	}
}

// errLoadLine is a load message longer than maxLoadLine. It is made anew
// each time: an *Error is completed by whoever returns it.
func errLoadLine() error {
	return &Error{Kind: KindOther, Op: loadOp,
		Msg: "the daemon sent a load message larger than 1 MiB: the stream was closed, and whether the image was loaded is not known",
		Err: errors.New("load message too large")}
}

// readLoadLine reads one line, without its line end, of at most maxLoadLine
// bytes. io.EOF comes with the last, unterminated line.
func readLoadLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := br.ReadSlice('\n')
		if len(line)+len(part) > maxLoadLine {
			return nil, errLoadLine()
		}
		line = append(line, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		return bytes.TrimSpace(line), err
	}
}

func parseLoadLine(line []byte) (LoadProgress, error) {
	var m loadMessage
	if line[0] != '{' || json.Unmarshal(line, &m) != nil {
		return LoadProgress{Text: clip(string(line))}, nil
	}
	if msg := m.Error; msg != "" || m.Detail != nil {
		if m.Detail != nil && m.Detail.Message != "" {
			msg = m.Detail.Message
		}
		if msg = strings.TrimSpace(msg); msg == "" {
			msg = "the daemon reported an error without a message"
		}
		msg = clip(msg)
		return LoadProgress{}, &Error{Kind: KindOther, Op: loadOp, Msg: msg, Err: errors.New(msg)}
	}
	p := LoadProgress{Status: clip(m.Status), ID: clip(m.ID), Text: clip(strings.TrimSpace(m.Stream))}
	if m.Progress != nil && m.Progress.Total > 0 && m.Progress.Current >= 0 {
		p.Current, p.Total = min(m.Progress.Current, m.Progress.Total), m.Progress.Total
	}
	return p, nil
}

// clip bounds a string of the daemon's that is passed on.
func clip(s string) string {
	if len(s) > maxLoadText {
		return strings.ToValidUTF8(s[:maxLoadText], "") + "…"
	}
	return s
}
