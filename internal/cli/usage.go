package cli

import (
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
			u, err := c.GetUsage(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				b, _ := json.MarshalIndent(u, "", "  ")
				fmt.Fprintln(out, string(b))
				return nil
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
