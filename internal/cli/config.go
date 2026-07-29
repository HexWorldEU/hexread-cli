package cli

import (
	"bufio"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// Config is the resolved per-invocation configuration.
type Config struct {
	BaseURL string
	Output  string
	Quiet   bool
	// BaseFromConfigFile is true when BaseURL came from the config file (not a flag/env). A config
	// file is the one base-URL source a user does not re-state on each run, so it is the stealthy
	// redirect vector clientFor surfaces before sending the credential.
	BaseFromConfigFile bool
}

const defaultBaseURL = "https://api.hexread.com/v1"

// resolveConfig applies the precedence flag > env > config file > default.
//
//	base-url: --base-url > HEXREAD_BASE_URL > HEXREAD_API (legacy) > config file > default
//	quiet:    --quiet > config file
//	output:   -o/--output only (an ambient output path from env/config would silently
//	          redirect every command's result, so it is flag-only on purpose)
func resolveConfig(cmd *cobra.Command) (Config, error) {
	file := loadConfigFile()

	base, _ := cmd.Flags().GetString("base-url")
	if base == "" {
		base = os.Getenv("HEXREAD_BASE_URL")
	}
	if base == "" {
		base = os.Getenv("HEXREAD_API") // legacy name, kept for back-compat
	}
	fromConfigFile := false
	if base == "" && file["base-url"] != "" {
		base = file["base-url"]
		fromConfigFile = true
	}
	if base == "" {
		base = defaultBaseURL
	}
	base = strings.TrimRight(base, "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, withExit(exitUsage, fmt.Errorf("invalid base URL %q (need http(s)://host[/path])", base))
	}
	// A plain-http base sends the API key in a cleartext Authorization header on every request.
	// http stays allowed for a local dev server, where there is no wire to sniff, and nowhere else.
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return Config{}, withExit(exitUsage, fmt.Errorf(
			"refusing to send your API key in cleartext to %q - use https:// (http is allowed only for localhost)", base))
	}

	quiet, _ := cmd.Flags().GetBool("quiet")
	if !cmd.Flags().Changed("quiet") && file["quiet"] == "true" {
		quiet = true
	}

	output, _ := cmd.Flags().GetString("output")
	return Config{BaseURL: base, Output: output, Quiet: quiet, BaseFromConfigFile: fromConfigFile}, nil
}

// isLoopbackHost reports whether host names this machine, by name or by address (both IP families).
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// configFilePath returns <os.UserConfigDir>/hexread/config.yaml - ~/.config on Linux,
// ~/Library/Application Support on macOS, %AppData% on Windows.
func configFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "hexread", "config.yaml")
}

// loadConfigFile reads the optional config file: flat `key: value` lines (a deliberate
// YAML subset - no nesting), `#` comment lines, optional single/double quotes around the
// value. Unknown keys are ignored for forward compatibility. Any read/parse problem just
// yields an empty config - the file is optional and must never break the CLI.
func loadConfigFile() map[string]string {
	path := configFilePath()
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if i := strings.Index(v, " #"); i >= 0 { // inline comment
			v = v[:i]
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}
