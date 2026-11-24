package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fardani235/docker-desk/internal/fakeengine"
	"github.com/fardani235/docker-desk/internal/ptyscreen"
)

// The pointer on a real terminal: SGR reports (1-based cells) written to the
// program's input, as a terminal with mouse reporting on sends them.

const (
	mouseOn  = "\x1b[?1002h"
	mouseSGR = "\x1b[?1006h"
)

// where finds text on the screen at or below row from: its 0-based cell.
func where(screen, text string, from int) (x, y int, ok bool) {
	for i, line := range strings.Split(screen, "\n") {
		if i < from {
			continue
		}
		if j := strings.Index(line, text); j >= 0 {
			return len([]rune(line[:j])), i, true
		}
	}
	return 0, 0, false
}

// click presses and releases the left button on a 0-based cell.
func (d *desk) click(x, y int) {
	d.t.Helper()
	d.keys(fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1), fmt.Sprintf("\x1b[<0;%d;%dm", x+1, y+1))
}

func (d *desk) wheelDown(x, y int) {
	d.t.Helper()
	d.keys(fmt.Sprintf("\x1b[<65;%d;%dM", x+1, y+1))
}

// clickText clicks the first occurrence of text below the header.
func (d *desk) clickText(text string) {
	d.t.Helper()
	x, y, ok := where(d.term.Screen(), text, 2)
	if !ok {
		d.t.Fatalf("%q is not on screen:\n%s", text, d.term.Screen())
	}
	d.click(x, y)
}

// detailsBody is the text of the Details pane's rows, inside its borders.
func detailsBody(screen string) []string {
	lines := strings.Split(screen, "\n")
	var out []string
	for i, l := range lines {
		r := []rune(l)
		j := strings.Index(l, "3 Details")
		if j < 0 {
			continue
		}
		// The pane's left border is the corner before the title.
		left := len([]rune(l[:j])) - 2
		for left > 0 && r[left] != '┌' && r[left] != '┏' {
			left--
		}
		right := left + 1
		for right < len(r) && r[right] != '┐' && r[right] != '┓' {
			right++
		}
		for _, body := range lines[i+1:] {
			br := []rune(body)
			if len(br) <= left || (br[left] != '│' && br[left] != '┃') {
				break
			}
			out = append(out, string(br[left+1:min(right, len(br))]))
		}
		break
	}
	return out
}

func TestMouseClickAndWheel(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	d := launch(t, srv, 40, 140)
	d.see("2 Containers (13)")
	if raw := d.term.Raw(); !strings.Contains(raw, mouseOn) || !strings.Contains(raw, mouseSGR) {
		t.Fatal("the desk did not ask for SGR cell-motion mouse reporting")
	}
	srv.ResetRequests()

	// A container row: highlighted, and Details follows it.
	d.clickText("redis-cache")
	d.see("3 Details: redis-cache", "▸ redis-cache")
	if muts := srv.Mutations(); len(muts) != 0 {
		t.Fatalf("a click sent %v", muts)
	}

	// The wheel over Details (Inspect: longer than the pane): three lines
	// further, focus left on the List.
	d.keys("3", "[", "2")
	d.see("[Inspect]", "◆ 2 Containers")
	if err := d.term.WaitUntil(wait, "the Inspect tab", func(s string) bool { return len(detailsBody(s)) > 10 && strings.TrimSpace(detailsBody(s)[4]) != "" }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	before := detailsBody(d.term.Screen())
	x, y, _ := where(d.term.Screen(), "3 Details", 2)
	d.wheelDown(x, y+3)
	if err := d.term.WaitUntil(wait, "Details to scroll three lines", func(s string) bool {
		b := detailsBody(s)
		return len(b) > 0 && b[0] == before[3]
	}); err != nil {
		t.Fatal(err)
	}
	d.see("◆ 2 Containers")
	d.keys("q")
	d.quit()
}

func TestMouseOff(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	d := launch(t, srv, 40, 140, "--no-mouse")
	d.see("2 Containers (13)", "3 Details:")
	if raw := d.term.Raw(); strings.Contains(raw, mouseOn) || strings.Contains(raw, mouseSGR) {
		t.Fatal("--no-mouse: a mouse-enabling sequence was written")
	}
	_, y, _ := where(d.term.Screen(), "3 Details:", 2)
	title := strings.Split(d.term.Screen(), "\n")[y]
	d.clickText("redis-cache")
	time.Sleep(400 * time.Millisecond)
	if strings.Contains(title, "redis-cache") {
		t.Skip("redis-cache was already highlighted")
	}
	if strings.Contains(d.term.Screen(), "3 Details: redis-cache") {
		t.Fatal("--no-mouse: a click moved the highlight")
	}
	d.keys("q")
	d.quit()
}

// After a container shell, the desk asks for mouse reporting again and a
// click works.
func TestMouseAfterShell(t *testing.T) {
	srv := startFake(t, func(s *fakeengine.Server) { s.SeedDemo() })
	d := launch(t, srv, 40, 140)
	d.see("2 Containers (13)")
	d.keys("2", "/", "s", "h", "o", "p", "-", "a", "p", "i", ptyscreen.Enter)
	d.see("3 Details: shop-api-1")
	d.keys("e")
	d.see("Open a shell")
	d.consent(ptyscreen.Enter)
	d.see(":/#")
	enables := strings.Count(d.term.Raw(), mouseOn)
	d.keys("e", "x", "i", "t", ptyscreen.Enter)
	d.see("Back from the shell in shop-api-1")
	if err := d.term.WaitUntil(wait, "mouse reporting again", func(string) bool {
		return strings.Count(d.term.Raw(), mouseOn) > enables
	}); err != nil {
		t.Fatal(err)
	}
	d.keys(ptyscreen.Esc)
	d.see("2 Containers (13)")
	d.clickText("redis-cache")
	d.see("3 Details: redis-cache")
	d.keys("q")
	d.quit()
}
