package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/HexWorldEU/hexread-cli/internal/client"
)

func newConvert() *cobra.Command {
	var asJSON, async bool
	var lang, model, prefer string
	cmd := &cobra.Command{
		Use:   "convert [file|-]",
		Short: "Convert a PDF or image to Markdown",
		Long: "Convert a document to Markdown. Pass a file path, or '-' to read from stdin.\n" +
			"The server picks the sync (inline) or async path by size; large docs stream progress.\n" +
			"With -o/--output the result is written to a file, otherwise to stdout.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, cfg, err := clientFor(cmd)
			if err != nil {
				return err
			}
			src, name, closeFn, err := openInput(args[0])
			if err != nil {
				return withExit(exitUsage, err)
			}
			defer closeFn()

			opts := client.ConvertOptions{
				Filename: name, Lang: lang, Model: model, Prefer: prefer,
				IdempotencyKey: newIdempotencyKey(),
			}
			if async {
				opts.Prefer = "async"
			}

			_, format := outFormat(asJSON)
			body, err := convertOnce(cmd.Context(), cmd, c, src, opts, format, !cfg.Quiet && isTerminalWriter(cmd.ErrOrStderr()))
			if err != nil {
				return err
			}
			return writeResult(cmd, cfg, body)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&asJSON, "json", false, "output the structured JSON result instead of Markdown")
	f.BoolVar(&async, "async", false, "force the async path (return a job, stream progress)")
	f.StringVar(&lang, "lang", "", "OCR language hint")
	f.StringVar(&model, "model", "", "parser to use: auto, mineru-2.5, granite-docling, paddleocr-vl")
	f.StringVar(&prefer, "prefer", "", "force sync|async (else the server decides)")
	return cmd
}

// convertOnce runs one conversion: POST, then for an async job wait (optionally streaming progress)
// and fetch the read-once result. Returns the result bytes (Markdown or JSON per `format`).
func convertOnce(ctx context.Context, cmd *cobra.Command, c *client.Client, src io.Reader, opts client.ConvertOptions, format string, showProgress bool) ([]byte, error) {
	resp, err := c.Convert(ctx, src, opts)
	if err != nil {
		return nil, err
	}
	if resp.Inline != nil {
		return inlineBody(resp, format == "json"), nil
	}
	job := resp.Job
	var onState func(client.JobState)
	if showProgress {
		onState = func(st client.JobState) { printProgress(cmd.ErrOrStderr(), st) }
	}
	st, err := c.JobEvents(ctx, job.JobID, onState)
	if showProgress {
		fmt.Fprintln(cmd.ErrOrStderr())
	}
	if err != nil {
		return nil, err
	}
	switch st.Status {
	case "failed":
		return nil, jobFailedErr(st)
	case "canceled", "cancelled":
		return nil, fmt.Errorf("job %s was canceled before it finished", job.JobID)
	}
	body, _, err := c.JobResult(ctx, job.JobID, format, st.Token)
	if err != nil {
		// The one-time token is spent only on a successful fetch, so hand it back rather than
		// stranding the result behind a transient network failure.
		if st.Token != "" {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"\nThe result was not fetched. It is still waiting - retrieve it with:\n  hexread jobs result %s --token %s\n",
				job.JobID, st.Token)
		}
		return nil, err
	}
	return body, nil
}

// openInput returns the source reader, a multipart part name, and a close func. "-" reads stdin.
func openInput(arg string) (io.Reader, string, func(), error) {
	if arg == "-" {
		return os.Stdin, "stdin", func() {}, nil
	}
	f, err := os.Open(arg)
	if err != nil {
		return nil, "", func() {}, err
	}
	return f, filepath.Base(arg), func() { _ = f.Close() }, nil
}

func printProgress(w io.Writer, st client.JobState) {
	switch st.Status {
	case "warming":
		fmt.Fprintf(w, "\rwarming up…                       ")
	case "queued":
		fmt.Fprintf(w, "\rqueued…                           ")
	case "processing":
		if st.Pages > 0 {
			fmt.Fprintf(w, "\rconverting… page %d of %d        ", st.PagesDone, st.Pages)
		} else {
			fmt.Fprintf(w, "\rconverting…                       ")
		}
	case "completed":
		fmt.Fprintf(w, "\rdone.                             ")
	case "failed":
		fmt.Fprintf(w, "\rfailed.                           ")
	}
}

// inlineBody renders the inline result as JSON or Markdown bytes. The JSON is the server's response
// body unchanged, so it matches the async path instead of dropping fields this client doesn't model.
func inlineBody(resp *client.ConvertResponse, asJSON bool) []byte {
	if asJSON {
		if len(resp.InlineRaw) > 0 {
			return resp.InlineRaw
		}
		b, _ := json.MarshalIndent(resp.Inline, "", "  ")
		return b
	}
	return []byte(resp.Inline.Markdown)
}

// writeResult writes to -o/--output or stdout (appending a newline for terminal Markdown). The
// result can only be fetched once, so a failed -o write falls back to stdout rather than discarding
// the bytes - and still returns the error, so the exit code reports the failure.
func writeResult(cmd *cobra.Command, cfg Config, body []byte) error {
	var writeErr error
	if cfg.Output != "" {
		writeErr = os.WriteFile(cfg.Output, body, 0o644)
		if writeErr == nil {
			return nil
		}
		fmt.Fprintf(cmd.ErrOrStderr(),
			"could not write %s - printing the result instead; it cannot be fetched again\n", cfg.Output)
	}
	out := cmd.OutOrStdout()
	if _, err := out.Write(body); err != nil {
		return err
	}
	if len(body) > 0 && body[len(body)-1] != '\n' {
		_, _ = out.Write([]byte("\n"))
	}
	return writeErr
}

// typeForJobError maps a job-failure error_code (from the SSE `error` event, which carries no
// envelope type) to its error class in HexRead's error catalog, so an async failure exits with
// the same code the sync path would (encrypted_pdf → 7, page_too_large → 8, …).
var typeForJobError = map[string]string{
	"encrypted_pdf":          "validation_error",
	"malformed_document":     "validation_error",
	"too_many_pages":         "validation_error",
	"unsupported_media_type": "validation_error",
	"invalid_request":        "validation_error",
	"file_too_large":         "payload_too_large",
	"page_too_large":         "payload_too_large",
	"document_too_complex":   "payload_too_large",
	"quota_exceeded":         "quota_error",
	"trial_expired":          "quota_error",
	"rate_limited":           "rate_limit_error",
	"job_not_found":          "not_found",
	"concurrency_limit":      "capacity_error",
	"billing_unavailable":    "capacity_error",
	"conversion_failed":      "capacity_error",
}

func jobFailedErr(st client.JobState) error {
	code := st.ErrorCode
	if code == "" {
		code = "conversion_failed"
	}
	typ := typeForJobError[code]
	if typ == "" {
		typ = "capacity_error" // unknown codes default to the transient class
	}
	return &client.APIError{Type: typ, Code: code, Message: "conversion failed: " + code}
}

// newIdempotencyKey mints a random key so a network-retried convert isn't processed twice.
func newIdempotencyKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "cli-" + hex.EncodeToString(b[:])
}

// isTerminalWriter reports whether w is an interactive terminal (for progress display).
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
