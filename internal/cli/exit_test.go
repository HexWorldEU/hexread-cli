package cli

import (
	"errors"
	"testing"

	"github.com/HexWorldEU/hexread-cli/internal/client"
)

// TestExitForType - every error class maps to its frozen exit code.
func TestExitForType(t *testing.T) {
	cases := map[string]int{
		"authentication_error": exitAuth,
		"permission_error":     exitForbidden,
		"quota_error":          exitQuota,
		"rate_limit_error":     exitRateLimit,
		"validation_error":     exitUnprocessable,
		"not_found":            exitUnprocessable,
		"gone":                 exitUnprocessable,
		"conflict":             exitUnprocessable,
		"payload_too_large":    exitTooLarge,
		"capacity_error":       exitCapacity,
		"internal_error":       exitGeneric,
		"":                     exitGeneric,
	}
	for typ, want := range cases {
		if got := exitForType(typ); got != want {
			t.Errorf("exitForType(%q) = %d, want %d", typ, got, want)
		}
	}
}

// TestExitCode - nil→0; an APIError maps by class; a codedError wins; a usage error→2; else generic.
func TestExitCode(t *testing.T) {
	if exitCode(nil) != exitOK {
		t.Error("nil error must be exit 0")
	}
	if got := exitCode(&client.APIError{Type: "quota_error", Status: 402}); got != exitQuota {
		t.Errorf("APIError quota → %d, want %d", got, exitQuota)
	}
	if got := exitCode(withExit(exitPartialBatch, errors.New("3 of 5 failed"))); got != exitPartialBatch {
		t.Errorf("codedError → %d, want %d", got, exitPartialBatch)
	}
	if got := exitCode(errors.New(`unknown command "bogus" for "hexread"`)); got != exitUsage {
		t.Errorf("cobra usage error → %d, want %d", got, exitUsage)
	}
	if got := exitCode(errors.New("network unreachable")); got != exitGeneric {
		t.Errorf("generic error → %d, want %d", got, exitGeneric)
	}
}

// TestCodedErrorUnwrap - a codedError unwraps to its cause so errors.As/Is keep working.
func TestCodedErrorUnwrap(t *testing.T) {
	cause := &client.APIError{Type: "rate_limit_error"}
	wrapped := withExit(exitRateLimit, cause)
	var ae *client.APIError
	if !errors.As(wrapped, &ae) {
		t.Fatal("codedError must unwrap to its cause")
	}
}
