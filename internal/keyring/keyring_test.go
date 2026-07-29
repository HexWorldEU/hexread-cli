package keyring

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "credential")
	f := NewFile(path)

	if _, err := f.Get(); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := f.Set("hr_live_abc_def"); err != nil {
		t.Fatal(err)
	}
	if got, err := f.Get(); err != nil || got != "hr_live_abc_def" {
		t.Fatalf("get: %v %q", err, got)
	}

	// Not world-readable (0600).
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("credential perms %o, want 0600", perm)
		}
	}

	if err := f.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get(); err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
	// Delete is idempotent.
	if err := f.Delete(); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
}

// TestFileKeyringSetIsAlways0600 - every Set lands on 0600, including a rewrite (rename replaces the
// inode, so this is the mode the new file was created with, not the old file's mode preserved).
func TestFileKeyringSetIsAlways0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	path := filepath.Join(t.TempDir(), "credential")
	f := NewFile(path)
	if err := f.Set("first"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("second"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("perms after rewrite = %v %v, want 0600", info.Mode().Perm(), err)
	}
	if got, _ := f.Get(); got != "second" {
		t.Fatalf("got %q, want the rewritten value", got)
	}
}

// TestDefaultBackendSelection - HEXREAD_KEYRING picks the backend; an unknown value errors
// instead of silently storing the secret somewhere unexpected.
func TestDefaultBackendSelection(t *testing.T) {
	t.Setenv("HEXREAD_KEYRING", "")
	if _, ok := Default().(*File); !ok {
		t.Error("unset → file backend")
	}
	t.Setenv("HEXREAD_KEYRING", "file")
	if _, ok := Default().(*File); !ok {
		t.Error("file → file backend")
	}
	t.Setenv("HEXREAD_KEYRING", "system")
	if _, ok := Default().(System); !ok {
		t.Error("system → system backend")
	}
	t.Setenv("HEXREAD_KEYRING", "bogus")
	if err := Default().Set("x"); err == nil {
		t.Error("bogus backend must refuse to store a secret")
	}
}

// TestDefaultFilePath - the credential path resolves under the config dir, and is never relative:
// that would put the API key in whatever directory the process happened to be in.
func TestDefaultFilePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	if p := DefaultFile().path; !filepath.IsAbs(p) || !strings.Contains(p, "hexread") {
		t.Fatalf("path = %q, want an absolute path under the config dir", p)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "") // windows
	f := DefaultFile()
	if f.path != "" {
		if !filepath.IsAbs(f.path) {
			t.Fatalf("credential path %q is relative", f.path)
		}
		return
	}
	if _, err := f.Get(); err == nil {
		t.Error("Get with no config dir must error, not read a relative path")
	}
	if err := f.Set("hr_live_x"); err == nil {
		t.Error("Set with no config dir must error, not write a relative path")
	}
}
