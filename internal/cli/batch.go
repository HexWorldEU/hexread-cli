package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/HexWorldEU/hexread-cli/internal/client"
)

func newBatch() *cobra.Command {
	var concurrency int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "batch <glob>... -o <dir>",
		Short: "Convert many files (globs) into an output directory",
		Long: "Convert every file matched by the given globs, writing <dir>/<name>.md (or .json)\n" +
			"per file. Concurrency is clamped to your tier's limit. Exit 10 if any file fails.",
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, cfg, err := clientFor(cmd)
			if err != nil {
				return err
			}
			if cfg.Output == "" {
				return withExit(exitUsage, errors.New("batch needs an output directory: -o <dir>"))
			}
			files, err := expandGlobs(args)
			if err != nil {
				return withExit(exitUsage, err)
			}
			if len(files) == 0 {
				return withExit(exitUsage, errors.New("no files matched"))
			}
			ext, format := outFormat(asJSON)
			// Distinct inputs mapping to one output path would silently clobber each other -
			// refuse up front rather than losing results.
			outs := map[string]string{}
			for _, f := range files {
				out := outputPath(cfg.Output, f, ext)
				if prev, dup := outs[out]; dup {
					return withExit(exitUsage, fmt.Errorf(
						"output collision: %q and %q both write %s - rename inputs or run separate batches", prev, f, out))
				}
				outs[out] = f
			}
			if err := os.MkdirAll(cfg.Output, 0o755); err != nil {
				return err
			}

			workers, err := clampConcurrency(cmd.Context(), c, concurrency)
			if err != nil {
				return err
			}
			results := runBatch(cmd, c, files, cfg.Output, ext, format, workers)
			failed := printBatchTable(cmd, results)

			if failed > 0 {
				return withExit(exitPartialBatch, fmt.Errorf("%d of %d file(s) failed", failed, len(results)))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&concurrency, "concurrency", 0, "parallel conversions (0 = your account limit; clamped to it)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "write the structured JSON result per file")
	return cmd
}

type batchResult struct {
	file string
	out  string
	err  error
}

// outputPath maps an input file to its result path in dir: <dir>/<base-without-ext>.<ext>.
func outputPath(dir, in, ext string) string {
	base := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))
	return filepath.Join(dir, base+"."+ext)
}

// clampConcurrency limits the requested parallelism to the account's concurrency limit (from
// /usage), defaulting to that limit. An auth/permission rejection is returned (the whole batch
// would fail file-by-file anyway); other /usage errors just degrade to sequential.
func clampConcurrency(ctx context.Context, c *client.Client, want int) (int, error) {
	limit := 1
	u, err := c.GetUsage(ctx)
	switch {
	case err == nil:
		if u.Concurrency.Limit > 0 {
			limit = u.Concurrency.Limit
		}
	default:
		var ae *client.APIError
		if errors.As(err, &ae) && (ae.Type == "authentication_error" || ae.Type == "permission_error") {
			return 0, err
		}
	}
	if want <= 0 || want > limit {
		return limit, nil
	}
	return want, nil
}

// runBatch converts files through a fixed worker pool, preserving input order in the results.
// On context cancellation (Ctrl-C) remaining files are marked canceled instead of converted.
func runBatch(cmd *cobra.Command, c *client.Client, files []string, outDir, ext, format string, workers int) []batchResult {
	ctx := cmd.Context()
	results := make([]batchResult, len(files))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = convertFileTo(ctx, cmd, c, files[i], outputPath(outDir, files[i], ext), format)
			}
		}()
	}
	for i := range files {
		select {
		case <-ctx.Done():
			results[i] = batchResult{file: files[i], err: ctx.Err()}
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

// convertFileTo converts one file and writes the result to `out` (no progress; batch shows a table).
func convertFileTo(ctx context.Context, cmd *cobra.Command, c *client.Client, path, out, format string) batchResult {
	src, name, closeFn, err := openInput(path)
	if err != nil {
		return batchResult{file: path, err: err}
	}
	defer closeFn()
	opts := client.ConvertOptions{Filename: name, IdempotencyKey: newIdempotencyKey()}
	body, err := convertOnce(ctx, cmd, c, src, opts, format, false)
	if err != nil {
		return batchResult{file: path, err: err}
	}
	if err := os.WriteFile(out, body, 0o644); err != nil {
		return batchResult{file: path, err: err}
	}
	return batchResult{file: path, out: out}
}

func printBatchTable(cmd *cobra.Command, results []batchResult) (failed int) {
	tw := tabwriter.NewWriter(cmd.ErrOrStderr(), 0, 2, 2, ' ', 0)
	for _, r := range results {
		if r.err != nil {
			failed++
			fmt.Fprintf(tw, "FAIL\t%s\t%s\n", r.file, errMessage(r.err))
		} else {
			fmt.Fprintf(tw, "ok\t%s\t→ %s\n", r.file, r.out)
		}
	}
	_ = tw.Flush()
	return failed
}

func errMessage(err error) string {
	var ae *client.APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return err.Error()
}

// expandGlobs resolves the glob args to a sorted, de-duplicated file list.
func expandGlobs(globs []string) ([]string, error) {
	seen := map[string]bool{}
	var files []string
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil {
			return nil, fmt.Errorf("bad pattern %q: %w", g, err)
		}
		if matches == nil { // a literal path with no glob meta-characters
			if _, statErr := os.Stat(g); statErr == nil {
				matches = []string{g}
			}
		}
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && !info.IsDir() && !seen[m] {
				seen[m] = true
				files = append(files, m)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}
