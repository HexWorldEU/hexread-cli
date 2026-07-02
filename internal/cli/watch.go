package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/HexWorldEU/hexread-cli/internal/client"
)

// debounceDelay waits for a file's writes to settle (editors/copies write in chunks) before
// converting it.
const debounceDelay = 400 * time.Millisecond

func newWatch() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "watch <dir> -o <dir>",
		Short: "Watch a directory and convert new/changed files as they appear",
		Long: "Watch a directory (non-recursive); whenever a file is created or modified (and has\n" +
			"settled), convert it to <out>/<name>.md (or .json). Unchanged files are skipped.\n" +
			"Press Ctrl-C to stop (in-flight conversions drain first; Ctrl-C again kills).",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, cfg, err := clientFor(cmd)
			if err != nil {
				return err
			}
			if cfg.Output == "" {
				return withExit(exitUsage, errors.New("watch needs an output directory: -o <dir>"))
			}
			if info, err := os.Stat(args[0]); err != nil || !info.IsDir() {
				return withExit(exitUsage, fmt.Errorf("%q is not a directory", args[0]))
			}
			if err := os.MkdirAll(cfg.Output, 0o755); err != nil {
				return err
			}
			ext, format := outFormat(asJSON)
			wr := &watchRunner{
				cmd: cmd, c: c, outDir: cfg.Output, ext: ext, format: format,
				// In-flight conversions must survive the Ctrl-C that stops the watch loop,
				// so they run on a detached context.
				convCtx:   context.WithoutCancel(cmd.Context()),
				processed: map[string]time.Time{},
			}
			return wr.run(cmd.Context(), args[0])
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "write the structured JSON result per file")
	return cmd
}

// watchRunner converts files dropped into a watched directory, de-duplicating by mtime.
type watchRunner struct {
	cmd     *cobra.Command
	c       *client.Client
	outDir  string
	ext     string
	format  string
	convCtx context.Context

	mu        sync.Mutex
	stopping  bool                 // set before the final drain; blocks new conversions
	processed map[string]time.Time // path → last-converted mtime
	wg        sync.WaitGroup       // in-flight conversions (drained on stop)
}

func (wr *watchRunner) run(ctx context.Context, dir string) error {
	cmd := wr.cmd
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	if err := w.Add(dir); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Watching %s → %s (Ctrl-C to stop)…\n", dir, wr.outDir)

	timers := map[string]*time.Timer{}
	defer func() {
		for _, t := range timers {
			t.Stop()
		}
		// Block conversions that already fired their timer, then drain the in-flight ones.
		// The flag flips under mu before Wait, and maybeConvert only wg.Add()s under the
		// same mu while !stopping - so no Add can race the Wait.
		wr.mu.Lock()
		wr.stopping = true
		wr.mu.Unlock()
		wr.wg.Wait()
		fmt.Fprintln(cmd.ErrOrStderr(), "\nStopped.")
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}
			path := ev.Name
			if t := timers[path]; t != nil {
				t.Stop()
			}
			timers[path] = time.AfterFunc(debounceDelay, func() { wr.maybeConvert(path) })
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "watch error:", err)
		}
	}
}

// maybeConvert converts a settled file unless the runner is draining, the file is a directory,
// it is unchanged since its last conversion, or it IS its own output (converting <dir>/x.md
// onto itself would re-trigger the watcher forever).
func (wr *watchRunner) maybeConvert(path string) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	out := outputPath(wr.outDir, path, wr.ext)
	if samePath(out, path) {
		return
	}
	wr.mu.Lock()
	if wr.stopping {
		wr.mu.Unlock()
		return
	}
	if last, ok := wr.processed[path]; ok && !info.ModTime().After(last) {
		wr.mu.Unlock()
		return // unchanged since last conversion - idempotent
	}
	wr.processed[path] = info.ModTime()
	wr.wg.Add(1)
	wr.mu.Unlock()

	go func() {
		defer wr.wg.Done()
		res := convertFileTo(wr.convCtx, wr.cmd, wr.c, path, out, wr.format)
		if res.err != nil {
			fmt.Fprintf(wr.cmd.ErrOrStderr(), "FAIL  %s  %s\n", filepath.Base(path), errMessage(res.err))
			wr.mu.Lock()
			delete(wr.processed, path) // allow a retry on the next change
			wr.mu.Unlock()
			return
		}
		fmt.Fprintf(wr.cmd.ErrOrStderr(), "ok    %s → %s\n", filepath.Base(path), out)
	}()
}

// samePath reports whether two paths name the same file, comparing absolute cleaned forms.
func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && aa == bb
}
