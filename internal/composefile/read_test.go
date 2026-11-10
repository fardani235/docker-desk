package composefile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestReadRegularFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(p, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 27, 9, 13, 13, 0, time.UTC)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
	b, st, err := ReadInfo(p)
	if err != nil || string(b) != "services: {}\n" {
		t.Fatalf("%q, %v", b, err)
	}
	if !st.ModTime().Equal(when) {
		t.Errorf("write time %v, want %v", st.ModTime(), when)
	}
	// Exactly MaxSize is accepted.
	big := filepath.Join(dir, "max.yaml")
	if err := os.WriteFile(big, bytes.Repeat([]byte("#"), MaxSize), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := Read(big); err != nil || len(b) != MaxSize {
		t.Fatalf("a file of exactly MaxSize: %d bytes, %v", len(b), err)
	}
}

// Each refusal is its own error, named by errors.Is, and none returns bytes.
func TestReadRefusalsAreDistinct(t *testing.T) {
	dir := t.TempDir()
	over := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(over, bytes.Repeat([]byte("#"), MaxSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		path string
		want error
	}{
		{"missing file", filepath.Join(dir, "absent.yaml"), ErrNotFound},
		{"directory", dir, ErrDirectory},
		{"oversized file", over, ErrTooLarge},
		{"relative path", "compose.yaml", ErrRelative},
	}
	if fifo := makeFIFO(t, dir); fifo != "" {
		// Opening a FIFO for reading blocks until a writer comes; Read must
		// refuse it without opening it, so this returns at once.
		cases = append(cases, struct {
			name string
			path string
			want error
		}{"FIFO", fifo, ErrNotRegular})
	}
	all := []error{ErrNotFound, ErrDirectory, ErrNotRegular, ErrTooLarge, ErrUnreadable, ErrRelative}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan struct{})
			var b []byte
			var err error
			go func() { b, err = Read(c.path); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Read blocked")
			}
			if b != nil {
				t.Fatalf("bytes returned with a refusal: %d", len(b))
			}
			var re *ReadError
			if !errors.As(err, &re) || re.Path != c.path {
				t.Fatalf("not a ReadError for the path: %v", err)
			}
			for _, e := range all {
				if errors.Is(err, e) != (e == c.want) {
					t.Errorf("errors.Is(%v, %v) = %v", err, e, errors.Is(err, e))
				}
			}
		})
	}
}

func TestReadUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissions do not stop this user")
	}
	p := filepath.Join(t.TempDir(), "locked.yaml")
	if err := os.WriteFile(p, []byte("services: {}\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("%v, want ErrUnreadable", err)
	}
}
