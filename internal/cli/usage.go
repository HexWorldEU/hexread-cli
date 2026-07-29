package cli

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newUsage() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "usage",
		Short:        "Show your page usage/allowance, concurrency, and API access",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := clientFor(cmd)
			if err != nil {
				return err
			}
			out, closeOut, err := commandOut(cmd)
			if err != nil {
				return err
			}
			defer closeOut()
			if asJSON {
				// Echo the server's exact JSON (pretty-printed), so `--json` carries every field the
				// API returns - re-marshaling the CLI's typed view silently drops any it does not model.
				raw, err := c.GetUsageRaw(cmd.Context())
				if err != nil {
					return err
				}
				var buf bytes.Buffer
				if json.Indent(&buf, raw, "", "  ") != nil {
					buf.Write(raw)
				}
				fmt.Fprintln(out, buf.String())
				return nil
			}
			u, err := c.GetUsage(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "tier:        %s\n", u.Tier)
			fmt.Fprintf(out, "pages:       %d used / %d this period\n", u.Pages.Used, u.Pages.Allowance)
			fmt.Fprintf(out, "remaining:   %d\n", u.Pages.Remaining)
			fmt.Fprintf(out, "concurrency: %d / %d in use\n", u.Concurrency.InUse, u.Concurrency.Limit)
			fmt.Fprintf(out, "API access:  %s\n", yesNo(u.APIAccess))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
