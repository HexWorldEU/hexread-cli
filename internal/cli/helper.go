package cli

import (
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/HexWorldEU/hexread-cli/internal/client"
	"github.com/HexWorldEU/hexread-cli/internal/keyring"
)

// clientFor resolves the config + credential into an authenticated client. The credential
// comes from HEXREAD_API_KEY (headless/CI - nothing touches disk) or else the credential
// store; no credential is an auth-coded error (exit 3).
func clientFor(cmd *cobra.Command) (*client.Client, Config, error) {
	cfg, err := resolveConfig(cmd)
	if err != nil {
		return nil, Config{}, err
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
