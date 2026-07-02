// Package cli is the Cobra command tree for the `hexread` binary - a pure HTTP API client over the
// public /v1 contract. Configuration precedence is flags > env (HEXREAD_*) > config file > default;
// the credential comes from HEXREAD_API_KEY, --key/--key-stdin, or the credential store. Every
// command maps a failed API call to the frozen process exit code (3=auth … 9=capacity,
// 10=partial-batch) via the error envelope.
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// Execute builds the command tree, runs it, and exits with the contract-mapped code.
// SIGINT/SIGTERM cancel the command's context for a clean shutdown (in-flight requests
// abort, watch drains); a second signal falls through to default handling and kills.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); stop() }()

	err := newRoot().ExecuteContext(ctx)
	if err == nil {
		return
	}
	// A termination signal arrived and the command failed: report "interrupted" no matter how
	// the cancellation surfaced (net/http wraps it as the signal cause, not context.Canceled).
	if ctx.Err() != nil {
		os.Exit(exitInterrupted) // the shell already shows ^C; no message needed
	}
	fmt.Fprintln(os.Stderr, "hexread:", err)
	os.Exit(exitCode(err))
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "hexread",
		Short: "Convert PDFs and images to clean Markdown (HexRead)",
		Long: "hexread - a pure API client for HexRead. Convert documents, manage jobs and keys,\n" +
			"and check usage from the command line.",
		// We print errors + choose the exit code ourselves; Cobra should not duplicate them.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Flag parse errors are usage errors → exit 2.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return withExit(exitUsage, err) })
	pf := root.PersistentFlags()
	pf.String("base-url", "", "API base URL (env HEXREAD_BASE_URL; default "+defaultBaseURL+")")
	pf.StringP("output", "o", "", "write output to a file (or directory for batch/watch) instead of stdout")
	pf.Bool("quiet", false, "suppress progress and non-essential output")

	root.AddCommand(newConvert(), newBatch(), newWatch(), newJobs(), newKeys(), newUsage(),
		newLogin(), newWhoami(), newLogout(), newVersion())
	return root
}
