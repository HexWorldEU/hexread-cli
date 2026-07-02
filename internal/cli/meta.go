package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/HexWorldEU/hexread-cli/internal/version"
)

func newVersion() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "version",
		Short:        "Print the CLI version",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Get("hexread-cli")
			out := cmd.OutOrStdout()
			if asJSON {
				b, _ := json.MarshalIndent(info, "", "  ")
				fmt.Fprintln(out, string(b))
				return nil
			}
			fmt.Fprintf(out, "hexread %s (commit %s, built %s)\n", info.Version, info.Commit, info.Date)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}
