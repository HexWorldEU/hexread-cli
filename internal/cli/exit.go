package cli

import (
	"errors"
	"strings"

	"github.com/HexWorldEU/hexread-cli/internal/client"
)

// Frozen process exit codes - a scriptable contract; each error-envelope class maps to one
// exit. Never renumber.
const (
	exitOK            = 0
	exitGeneric       = 1
	exitUsage         = 2
	exitAuth          = 3
	exitForbidden     = 4
	exitQuota         = 5
	exitRateLimit     = 6
	exitUnprocessable = 7
	exitTooLarge      = 8
	exitCapacity      = 9
	exitPartialBatch  = 10
	// exitInterrupted follows the shell convention 128+SIGINT for a Ctrl-C'd run; it is
	// additive to (not part of) the 0-10 API-class contract above.
	exitInterrupted = 130
)

// exitForType maps an error-envelope "type" to its exit code. The exit follows the error CLASS,
// not the HTTP status (e.g. an authentication_error envelope can ride a 502 and still exits 3).
// The authoritative mapping is HexRead's published error catalog (see each error's doc_url).
func exitForType(t string) int {
	switch t {
	case "authentication_error":
		return exitAuth
	case "permission_error":
		return exitForbidden
	case "quota_error":
		return exitQuota
	case "rate_limit_error":
		return exitRateLimit
	case "validation_error", "not_found", "gone", "conflict":
		return exitUnprocessable
	case "payload_too_large":
		return exitTooLarge
	case "capacity_error":
		return exitCapacity
	default:
		return exitGeneric
	}
}

// codedError lets a command request an explicit exit code (e.g. usage errors → 2, partial batch → 10).
type codedError struct {
	code int
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func withExit(code int, err error) error {
	if err == nil {
		return nil
	}
	return &codedError{code: code, err: err}
}

// exitCode resolves a command error to a process exit code: an explicit codedError wins, else an
// API error maps by class, else generic.
func exitCode(err error) int {
	if err == nil {
		return exitOK
	}
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.code
	}
	var ae *client.APIError
	if errors.As(err, &ae) {
		return exitForType(ae.Type)
	}
	if isUsageError(err) {
		return exitUsage
	}
	return exitGeneric
}

// isUsageError detects Cobra's command-resolution / argument-count errors, which it returns from
// Execute before any RunE (so they can't carry a codedError), so they exit 2 like flag errors.
// The patterns are Cobra's exact message prefixes; API errors never reach this check because
// exitCode matches *APIError first.
func isUsageError(err error) bool {
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand", "accepts ",
		"requires at least ", "requires exactly ", "required flag"} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}
