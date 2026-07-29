package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/HexWorldEU/hexread-cli/internal/client"
	"github.com/HexWorldEU/hexread-cli/internal/keyring"
)

// commandOut returns where a read command should print: the -o/--output file (0600) if set, else
// stdout. The returned close func closes the file (a no-op for stdout).
func commandOut(cmd *cobra.Command) (io.Writer, func() error, error) {
	noop := func() error { return nil }
	cfg, err := resolveConfig(cmd)
	if err != nil {
		return nil, noop, err
	}
	if cfg.Output == "" {
		return cmd.OutOrStdout(), noop, nil
	}
	f, err := os.OpenFile(cfg.Output, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, outputFileMode)
	if err != nil {
		return nil, noop, withExit(exitGeneric, err)
	}
	return f, f.Close, nil
}

// outputFileMode keeps written results owner-only (0600): the output is document-derived text.
const outputFileMode = 0o600

// parentRunE is the RunE for a command that only groups subcommands (jobs, keys): a bare invocation
// prints help, a mistyped subcommand is a usage error (exit 2) rather than a silent exit 0.
func parentRunE(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	return withExit(exitUsage, fmt.Errorf("unknown subcommand %q for %q", args[0], cmd.CommandPath()))
}

// clientFor resolves the config + credential into an authenticated client. The credential
// comes from HEXREAD_API_KEY (headless/CI - nothing touches disk) or else the credential
// store; no credential is an auth-coded error (exit 3).
func clientFor(cmd *cobra.Command) (*client.Client, Config, error) {
	cfg, err := resolveConfig(cmd)
	if err != nil {
		return nil, Config{}, err
	}
	// A config-file base URL is not re-stated per run, so surface it before sending the credential
	// (a flag/env base is the user's explicit choice; --quiet suppresses the note).
	if cfg.BaseFromConfigFile && cfg.BaseURL != defaultBaseURL && !cfg.Quiet {
		fmt.Fprintf(cmd.ErrOrStderr(), "hexread: sending credential to %s (from config file)\n", cfg.BaseURL)
	}
	key := strings.TrimSpace(os.Getenv("HEXREAD_API_KEY"))
	if key == "" {
		key, err = keyring.Default().Get()
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, cfg, withExit(exitAuth,
				errors.New("not logged in - run `hexread login --key-stdin` or set HEXREAD_API_KEY"))
		}
		if err != nil {
			return nil, cfg, err
		}
	}
	return client.New(cfg.BaseURL, key), cfg, nil
}

// outFormat maps the --json flag to the output file extension + result format name.
func outFormat(asJSON bool) (ext, format string) {
	if asJSON {
		return "json", "json"
	}
	return "md", "markdown"
}
