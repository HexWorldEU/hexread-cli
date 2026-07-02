package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func testCmd() *cobra.Command {
	c := &cobra.Command{Use: "t"}
	c.Flags().String("base-url", "", "")
	c.Flags().StringP("output", "o", "", "")
	c.Flags().Bool("quiet", false, "")
	return c
}

func mustResolve(t *testing.T, cmd *cobra.Command) Config {
	t.Helper()
	cfg, err := resolveConfig(cmd)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	return cfg
}

// TestConfigPrecedence - flags > env (HEXREAD_*) > config file > default.
func TestConfigPrecedence(t *testing.T) {
	// Isolate config discovery to a temp dir with a config file setting base-url.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir) // macOS UserConfigDir fallback
	if err := os.MkdirAll(filepath.Join(dir, "hexread"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, "# comment\nbase-url: https://file.example/v1\n")

	t.Run("default when nothing set", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no config file here
		if c := mustResolve(t, testCmd()); c.BaseURL != defaultBaseURL {
			t.Errorf("base = %q, want default", c.BaseURL)
		}
	})

	t.Run("config file beats default", func(t *testing.T) {
		if c := mustResolve(t, testCmd()); c.BaseURL != "https://file.example/v1" {
			t.Errorf("base = %q, want the config-file value", c.BaseURL)
		}
	})

	t.Run("env beats config file", func(t *testing.T) {
		t.Setenv("HEXREAD_BASE_URL", "https://env.example/v1")
		if c := mustResolve(t, testCmd()); c.BaseURL != "https://env.example/v1" {
			t.Errorf("base = %q, want the env value", c.BaseURL)
		}
	})

	t.Run("flag beats env", func(t *testing.T) {
		t.Setenv("HEXREAD_BASE_URL", "https://env.example/v1")
		cmd := testCmd()
		_ = cmd.Flags().Set("base-url", "https://flag.example/v1")
		if c := mustResolve(t, cmd); c.BaseURL != "https://flag.example/v1" {
			t.Errorf("base = %q, want the flag value", c.BaseURL)
		}
	})

	t.Run("trailing slash trimmed", func(t *testing.T) {
		cmd := testCmd()
		_ = cmd.Flags().Set("base-url", "https://x.example/v1/")
		if c := mustResolve(t, cmd); c.BaseURL != "https://x.example/v1" {
			t.Errorf("base = %q, want trailing slash trimmed", c.BaseURL)
		}
	})

	t.Run("HEXREAD_API back-compat", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("HEXREAD_API", "https://legacy.example/v1")
		if c := mustResolve(t, testCmd()); c.BaseURL != "https://legacy.example/v1" {
			t.Errorf("base = %q, want the legacy HEXREAD_API value", c.BaseURL)
		}
	})

	t.Run("quoted value + quiet from config file", func(t *testing.T) {
		d := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", d)
		if err := os.MkdirAll(filepath.Join(d, "hexread"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeConfig(t, d, "base-url: \"https://quoted.example/v1\"\nquiet: true\n")
		c := mustResolve(t, testCmd())
		if c.BaseURL != "https://quoted.example/v1" || !c.Quiet {
			t.Errorf("cfg = %+v, want quoted URL + quiet", c)
		}
	})

	t.Run("invalid base URL is a usage error", func(t *testing.T) {
		cmd := testCmd()
		_ = cmd.Flags().Set("base-url", "ftp://nope")
		if _, err := resolveConfig(cmd); err == nil || exitCode(err) != exitUsage {
			t.Errorf("invalid scheme: err=%v exit=%d, want usage error (2)", err, exitCode(err))
		}
	})
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "hexread", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
