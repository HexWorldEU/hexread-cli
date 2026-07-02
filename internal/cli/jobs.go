package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func newJobs() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "jobs",
		Short:        "Inspect, fetch, or cancel async conversion jobs",
		SilenceUsage: true,
	}
	cmd.AddCommand(newJobsStatus(), newJobsResult(), newJobsCancel())
	return cmd
}

func newJobsStatus() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "status <job_id>",
		Short:        "Print a job's status",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := clientFor(cmd)
			if err != nil {
				return err
			}
			st, err := c.GetJob(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				b, _ := json.MarshalIndent(st, "", "  ")
				fmt.Fprintln(cmd.OutOrStdout(), string(b))
				return nil
			}
			status := st.Status
			if st.ErrorCode != "" {
				status += " (" + st.ErrorCode + ")" // surface the reason, e.g. failed (cancelled)
			}
			if st.Pages > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  (%d/%d pages)\n", st.JobID, status, st.PagesDone, st.Pages)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s\n", st.JobID, status)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func newJobsResult() *cobra.Command {
	var asJSON bool
	var token string
	cmd := &cobra.Command{
		Use:   "result <job_id>",
		Short: "Fetch a job's result (read-once)",
		Long: "Fetch a job's read-once result. Requires the one-time --token delivered in the SSE\n" +
			"`completed` event or the webhook payload; the result can be retrieved exactly once.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, cfg, err := clientFor(cmd)
			if err != nil {
				return err
			}
			if token == "" {
				return withExit(exitUsage, errors.New("a --token is required (delivered via SSE or webhook)"))
			}
			_, format := outFormat(asJSON)
			body, _, err := c.JobResult(cmd.Context(), args[0], format, token)
			if err != nil {
				return err
			}
			return writeResult(cmd, cfg, body)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "fetch the structured JSON result")
	cmd.Flags().StringVar(&token, "token", "", "one-time download token (from the SSE completed event or webhook)")
	return cmd
}

func newJobsCancel() *cobra.Command {
	return &cobra.Command{
		Use:          "cancel <job_id>",
		Short:        "Cancel a queued or in-flight job",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := clientFor(cmd)
			if err != nil {
				return err
			}
			if err := c.CancelJob(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Canceled %s.\n", args[0])
			return nil
		},
	}
}
