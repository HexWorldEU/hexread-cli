package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/HexWorldEU/hexread-cli/internal/client"
)

func mockConvertServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"markdown":"# Watched","pages":[],"meta":{}}`)
	}))
}

func newWatchRunner(t *testing.T, srv *httptest.Server, outDir string) *watchRunner {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return &watchRunner{cmd: cmd, c: client.New(srv.URL+"/v1", "k"), outDir: outDir, ext: "md",
		format: "markdown", convCtx: context.Background(), processed: map[string]time.Time{}}
}

// TestWatchSkipsOwnOutput - when the output dir IS the watched dir, a file that would convert
// onto itself (x.md → x.md) is skipped. Regression: converting own outputs re-triggered the
// watcher in an infinite loop.
func TestWatchSkipsOwnOutput(t *testing.T) {
	srv := mockConvertServer(t)
	defer srv.Close()
	dir := t.TempDir()
	wr := newWatchRunner(t, srv, dir) // outDir == watched dir

	md := filepath.Join(dir, "notes.md")
	_ = os.WriteFile(md, []byte("# already markdown"), 0o644)
	wr.maybeConvert(md)
	wr.wg.Wait()
	if _, tracked := wr.processed[md]; tracked {
		t.Fatal("a file that is its own output must not be converted")
	}
	if b, _ := os.ReadFile(md); string(b) != "# already markdown" {
		t.Fatal("the input file was overwritten")
	}

	// A pdf in the same dir still converts normally.
	pdf := filepath.Join(dir, "doc.pdf")
	_ = os.WriteFile(pdf, []byte("pdf"), 0o644)
	wr.maybeConvert(pdf)
	wr.wg.Wait()
	if b, err := os.ReadFile(filepath.Join(dir, "doc.md")); err != nil || string(b) != "# Watched" {
		t.Fatalf("pdf in-place convert: %q %v", b, err)
	}
}

// TestWatchMaybeConvertIdempotent - a file is converted once; an unchanged re-event is skipped; a
// newer mtime reprocesses.
func TestWatchMaybeConvertIdempotent(t *testing.T) {
	srv := mockConvertServer(t)
	defer srv.Close()
	out := t.TempDir()
	wr := newWatchRunner(t, srv, out)

	in := filepath.Join(t.TempDir(), "doc.pdf")
	_ = os.WriteFile(in, []byte("v1"), 0o644)

	wr.maybeConvert(in)
	wr.wg.Wait()
	if b, err := os.ReadFile(filepath.Join(out, "doc.md")); err != nil || string(b) != "# Watched" {
		t.Fatalf("first convert: %q %v", b, err)
	}
	firstMtime := wr.processed[in]

	// Unchanged → skipped (processed mtime unchanged, no new work).
	wr.maybeConvert(in)
	wr.wg.Wait()
	if wr.processed[in] != firstMtime {
		t.Fatal("unchanged file was reprocessed")
	}

	// Touch newer → reprocessed.
	newer := firstMtime.Add(2 * time.Second)
	_ = os.Chtimes(in, newer, newer)
	wr.maybeConvert(in)
	wr.wg.Wait()
	if !wr.processed[in].After(firstMtime) {
		t.Fatal("a modified file should be reprocessed")
	}
}

// TestWatchRunConvertsNewFile - the fsnotify loop converts a file created in the watched dir, then
// stops cleanly on context cancel.
func TestWatchRunConvertsNewFile(t *testing.T) {
	srv := mockConvertServer(t)
	defer srv.Close()
	watchDir, outDir := t.TempDir(), t.TempDir()
	wr := newWatchRunner(t, srv, outDir)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- wr.run(ctx, watchDir) }()

	time.Sleep(50 * time.Millisecond) // let the watcher register
	_ = os.WriteFile(filepath.Join(watchDir, "new.pdf"), []byte("hi"), 0o644)

	deadline := time.After(5 * time.Second)
	target := filepath.Join(outDir, "new.md")
	for {
		if _, err := os.Stat(target); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("watched file was not converted within the timeout")
		case <-time.After(50 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watch did not stop on cancel")
	}
}
