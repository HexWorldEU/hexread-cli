// Package keyring stores the CLI credential. Default is a 0600 file (headless/CI-safe,
// never world-readable); the OS keychain (go-keyring) is opt-in via HEXREAD_KEYRING=system.
package keyring

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gk "github.com/zalando/go-keyring"
)

const (
	service = "hexread"
	account = "default"
)

var ErrNotFound = errors.New("keyring: no stored credential")

type Keyring interface {
	Set(secret string) error
	Get() (string, error)
	Delete() error
}

// System uses the OS keychain (macOS Keychain / Windows Credential Manager / Linux Secret Service).
type System struct{}

func (System) Set(s string) error { return gk.Set(service, account, s) }

func (System) Get() (string, error) {
	v, err := gk.Get(service, account)
	if errors.Is(err, gk.ErrNotFound) {
		return "", ErrNotFound
	}
	return strings.TrimSpace(v), err
}

func (System) Delete() error {
	err := gk.Delete(service, account)
	if errors.Is(err, gk.ErrNotFound) {
		return nil
	}
	return err
}

// File is a 0600 credential file.
type File struct{ path string }

func NewFile(path string) *File { return &File{path: path} }

func DefaultFile() *File {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return &File{path: filepath.Join(dir, "hexread", "credential")}
}

// Set writes the credential atomically (temp file + rename) so a crash can't leave a truncated
// credential, and never widens the permissions of an existing file.
func (f *File) Set(s string) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(s); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}

func (f *File) Get() (string, error) {
	b, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", ErrNotFound
	}
	return s, nil
}

func (f *File) Delete() error {
	err := os.Remove(f.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Default selects the backend from HEXREAD_KEYRING: "file" (or unset) → the 0600 credential
// file, "system" → the OS keychain. An unknown value returns an erroring stub rather than
// silently storing the secret somewhere the user didn't choose.
func Default() Keyring {
	switch v := os.Getenv("HEXREAD_KEYRING"); v {
	case "", "file":
		return DefaultFile()
	case "system":
		return System{}
	default:
		return invalid{v}
	}
}

type invalid struct{ v string }

func (i invalid) err() error {
	return fmt.Errorf("invalid HEXREAD_KEYRING=%q (use \"file\" or \"system\")", i.v)
}
func (i invalid) Set(string) error     { return i.err() }
func (i invalid) Get() (string, error) { return "", i.err() }
func (i invalid) Delete() error        { return i.err() }
