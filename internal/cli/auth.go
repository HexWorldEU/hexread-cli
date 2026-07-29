package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/HexWorldEU/hexread-cli/internal/client"
	"github.com/HexWorldEU/hexread-cli/internal/deviceflow"
	"github.com/HexWorldEU/hexread-cli/internal/keyring"
)

func newLogin() *cobra.Command {
	var key string
	var keyStdin, noValidate bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in via browser, or store an API key",
		Long: "Sign in to HexRead. With no flags, this starts an OAuth device grant: it prints a\n" +
			"URL and one-time code for you to open in a browser, then stores the resulting API\n" +
			"key. Headless setups pass an existing key instead (create one in your HexRead\n" +
			"dashboard); prefer --key-stdin so the secret never appears in shell history or\n" +
			"process lists. A passed key is validated against the API unless --no-validate is given.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig(cmd)
			if err != nil {
				return err
			}
			kr := keyring.Default()

			if keyStdin {
				key, err = readKeyStdin(cmd)
				if err != nil {
					return err
				}
				if key == "" {
					return withExit(exitUsage, errors.New("--key-stdin was given but stdin carried no key"))
				}
			}
			if key != "" {
				return storeKey(cmd, cfg, kr, key, noValidate)
			}
			return deviceLogin(cmd, cfg, kr)
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "API key to store (hr_live_…); prefer --key-stdin")
	cmd.Flags().BoolVar(&keyStdin, "key-stdin", false, "read the API key from stdin (headless)")
	cmd.Flags().BoolVar(&noValidate, "no-validate", false, "store the key without validating it against the API")
	return cmd
}

// readKeyStdin reads the API key from stdin. On a terminal it disables echo so a pasted secret is not
// shown; a piped/redirected stdin (CI) is read as a plain bounded stream.
func readKeyStdin(cmd *cobra.Command) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(cmd.ErrOrStderr(), "Paste your API key (input hidden): ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<10))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func storeKey(cmd *cobra.Command, cfg Config, kr keyring.Keyring, key string, noValidate bool) error {
	if noValidate {
		if err := kr.Set(key); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Stored key (unvalidated).")
		return nil
	}
	id, err := client.New(cfg.BaseURL, key).WhoAmI(cmd.Context())
	if err != nil {
		var ae *client.APIError
		if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
			return withExit(exitAuth, errors.New("that API key is invalid"))
		}
		return err
	}
	if err := kr.Set(key); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s (%s).\n", id.Sub, id.Tier)
	return nil
}

// deviceLogin runs the OAuth device grant, then trades the resulting OIDC access token for an
// hr_live API key at POST /v1/auth/cli/exchange and stores THE KEY (never the raw OIDC token).
// Requires the device endpoints to be configured (env vars or build-time defaults).
func deviceLogin(cmd *cobra.Command, cfg Config, kr keyring.Keyring) error {
	dcfg, ok := deviceConfig()
	if !ok {
		return withExit(exitUsage, errors.New(
			"browser sign-in isn't available yet - create an API key in your HexRead dashboard\n"+
				"and run:  hexread login --key-stdin"))
	}
	ar, err := deviceflow.Start(cmd.Context(), deviceHTTPClient(), dcfg)
	if err != nil {
		return err
	}
	// Poll for as long as the device code is valid (the server's expires_in, else 10 min).
	expiry := 10 * time.Minute
	if ar.ExpiresIn > 0 {
		expiry = time.Duration(ar.ExpiresIn) * time.Second
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), expiry)
	defer cancel()

	uri := ar.VerificationURIComplete
	if uri == "" {
		uri = ar.VerificationURI
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "To sign in, open:\n\n  %s\n\nand enter the code: %s\n\nWaiting for approval…\n", uri, ar.UserCode)
	tok, err := deviceflow.Poll(ctx, deviceHTTPClient(), dcfg, ar.DeviceCode, ar.Interval, nil)
	switch {
	case errors.Is(err, deviceflow.ErrDenied):
		return withExit(exitAuth, errors.New("authorization was denied"))
	case errors.Is(err, deviceflow.ErrExpired):
		return withExit(exitAuth, errors.New("the code expired before approval; run `hexread login` again"))
	case err != nil:
		return err
	}
	created, err := client.New(cfg.BaseURL, "").ExchangeOIDCToken(cmd.Context(), tok.AccessToken)
	if err != nil {
		return exchangeError(err)
	}
	if err := kr.Set(created.Key); err != nil {
		return err
	}
	fmt.Fprintf(out, "Signed in - API key hr_live_%s_…%s stored.\n", created.KeyID, created.Last4)
	return nil
}

// exchangeError maps a failed token exchange to an actionable message and the right exit code.
// Beyond a rejected token, an unverified email and a plan without API access both fail the
// exchange with their own code; each gets a clear next step. Anything else is returned unchanged.
func exchangeError(err error) error {
	var ae *client.APIError
	if !errors.As(err, &ae) {
		return err // transport/other error - reported as-is
	}
	switch {
	case ae.Code == "signup_required":
		return withExit(exitForbidden, errors.New(
			"your email address isn't verified yet - verify it, then run `hexread login` again"))
	case ae.Code == "api_not_available_on_tier":
		return withExit(exitForbidden, errors.New(
			"your HexRead plan doesn't include API or CLI access - subscribe to a paid plan, then run `hexread login` again"))
	case ae.Status == http.StatusUnauthorized || ae.Code == "unauthenticated":
		return withExit(exitAuth, errors.New("the sign-in was not accepted - run `hexread login` again"))
	}
	return err
}

func newWhoami() *cobra.Command {
	return &cobra.Command{
		Use:          "whoami",
		Short:        "Print the signed-in identity",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, err := clientFor(cmd)
			if err != nil {
				return err
			}
			id, err := c.WhoAmI(cmd.Context())
			if err != nil {
				var ae *client.APIError
				if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
					if strings.TrimSpace(os.Getenv("HEXREAD_API_KEY")) != "" {
						return withExit(exitAuth, errors.New("HEXREAD_API_KEY is invalid or revoked"))
					}
					return withExit(exitAuth, errors.New("stored credential is invalid - run `hexread login`"))
				}
				return err
			}
			out, closeOut, err := commandOut(cmd)
			if err != nil {
				return err
			}
			defer closeOut()
			fmt.Fprintf(out, "%s (%s)\n", id.Sub, id.Tier)
			return nil
		},
	}
}

func newLogout() *cobra.Command {
	return &cobra.Command{
		Use:          "logout",
		Short:        "Remove the stored credential",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := keyring.Default().Delete(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Logged out.")
			return nil
		},
	}
}

// Device-grant endpoint defaults, injectable at build time
// (-ldflags -X …cli.defaultDeviceAuthURL=…); env vars override them.
var (
	defaultDeviceAuthURL = ""
	defaultTokenURL      = ""
	defaultClientID      = ""
)

func deviceConfig() (deviceflow.Config, bool) {
	pick := func(env, def string) string {
		if v := os.Getenv(env); v != "" {
			return v
		}
		return def
	}
	da := pick("HEXREAD_DEVICE_AUTH_URL", defaultDeviceAuthURL)
	tk := pick("HEXREAD_TOKEN_URL", defaultTokenURL)
	cid := pick("HEXREAD_CLIENT_ID", defaultClientID)
	if da == "" || tk == "" || cid == "" {
		return deviceflow.Config{}, false
	}
	return deviceflow.Config{
		DeviceAuthURL: da, TokenURL: tk, ClientID: cid,
		Scopes: []string{"openid", "profile", "email", "offline_access"},
	}, true
}

func deviceHTTPClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }
