package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/HexWorldEU/hexread-cli/internal/client"
)

func newKeys() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "keys",
		Short:        "Manage API keys",
		SilenceUsage: true,
		RunE:         parentRunE,
	}
	cmd.AddCommand(newKeysList(), newKeysCreate(), newKeysRotate(), newKeysRevoke())
	return cmd
}

func newKeysList() *cobra.Command {
	return &cobra.Command{
		Use:          "list",
		Short:        "List your API keys (masked)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := clientFor(cmd)
			if err != nil {
				return err
			}
			keys, err := c.ListKeys(cmd.Context())
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No API keys.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "KEY ID\tMASKED\tSCOPES\tSTATUS")
			for _, k := range keys {
				status := "active"
				if k.RevokedAt != "" {
					status = "revoked"
				}
				// Mirrors the real key shape hr_live_<key_id>_<secret>, with the secret elided
				// down to its last 4 characters.
				fmt.Fprintf(tw, "%s\thr_live_%s_…%s\t%s\t%s\n", k.KeyID, k.KeyID, k.Last4, strings.Join(k.Scopes, ","), status)
			}
			return tw.Flush()
		},
	}
}

func newKeysCreate() *cobra.Command {
	return &cobra.Command{
		Use:          "create",
		Short:        "Create an API key (the full secret is shown ONCE)",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := clientFor(cmd)
			if err != nil {
				return err
			}
			k, err := c.CreateKey(cmd.Context(), newIdempotencyKey())
			if err != nil {
				return err
			}
			printNewKey(cmd, k)
			return nil
		},
	}
}

func newKeysRotate() *cobra.Command {
	return &cobra.Command{
		Use:          "rotate <key_id>",
		Short:        "Rotate a key - revokes the old, returns a new secret once",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := clientFor(cmd)
			if err != nil {
				return err
			}
			k, err := c.RotateKey(cmd.Context(), args[0], newIdempotencyKey())
			if err != nil {
				return err
			}
			printNewKey(cmd, k)
			return nil
		},
	}
}

func newKeysRevoke() *cobra.Command {
	return &cobra.Command{
		Use:          "revoke <key_id>",
		Short:        "Revoke an API key",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := clientFor(cmd)
			if err != nil {
				return err
			}
			if err := c.RevokeKey(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Revoked %s.\n", args[0])
			return nil
		},
	}
}

// printNewKey prints the one-time warning on stderr and the bare secret on stdout, so
// `hexread keys create | pbcopy` captures only the secret.
func printNewKey(cmd *cobra.Command, k client.APIKeyCreated) {
	fmt.Fprintln(cmd.ErrOrStderr(), "Copy your key now - it won't be shown again:")
	fmt.Fprintln(cmd.OutOrStdout(), k.Key)
}
