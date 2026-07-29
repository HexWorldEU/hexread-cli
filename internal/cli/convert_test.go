package cli

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI runs the root command with args against a temp keyring (XDG_CONFIG_HOME) holding `cred`,
// returning stdout, the exit code, and combined stderr.
func runCLI(t *testing.T, cred string, args ...string) (string, int, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("HEXREAD_KEYRING", "")  // force the file backend
	t.Setenv("HEXREAD_API_KEY", "")  // ambient env creds must not leak into tests
	t.Setenv("HEXREAD_BASE_URL", "") // nor an ambient base URL
	t.Setenv("HEXREAD_API", "")
	if cred != "" {
		if err := os.MkdirAll(filepath.Join(dir, "hexread"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hexread", "credential"), []byte(cred), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errBuf bytes.Buffer
	root := newRoot()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errBuf)
	err := root.Execute()
	if err != nil {
		fmt.Fprintln(&errBuf, "hexread:", err) // mirror Execute()'s error printing
	}
	return out.String(), exitCode(err), errBuf.String()
}

// TestConvertCmdSyncMarkdown - a sync inline result prints Markdown to stdout.
func TestConvertCmdSyncMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"markdown":"# Converted","pages":[],"meta":{"pages":1}}`)
	}))
	defer srv.Close()

	pdf := filepath.Join(t.TempDir(), "doc.pdf")
	_ = os.WriteFile(pdf, []byte("PDF"), 0o644)
	out, code, _ := runCLI(t, "hr_live_k", "convert", pdf, "--base-url", srv.URL+"/v1")
	if code != 0 || strings.TrimSpace(out) != "# Converted" {
		t.Fatalf("out=%q code=%d, want '# Converted' + 0", out, code)
	}
}

// TestConvertCmdJSON - --json prints the server's result body verbatim, so fields this client's
// types do not model survive instead of being dropped by a re-marshal.
func TestConvertCmdJSON(t *testing.T) {
	const body = `{"markdown":"# X","pages":[],"meta":{"pages":1,"sha256":"ab","unmodeled":"kept"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	out, code, _ := runCLI(t, "k", "convert", "-", "--json", "--base-url", srv.URL+"/v1")
	if code != 0 || strings.TrimSpace(out) != body {
		t.Fatalf("json out=%q code=%d, want the server body verbatim", out, code)
	}
	if !strings.Contains(out, `"unmodeled":"kept"`) {
		t.Fatalf("--json dropped an unmodeled field: %q", out)
	}
}

// TestConvertCmdStdinToFile - stdin input + -o writes the result to a file.
func TestConvertCmdStdinToFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"markdown":"# File","pages":[],"meta":{"pages":1}}`)
	}))
	defer srv.Close()
	outFile := filepath.Join(t.TempDir(), "r.md")
	_, code, _ := runCLI(t, "k", "convert", "-", "-o", outFile, "--base-url", srv.URL+"/v1")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	b, _ := os.ReadFile(outFile)
	if string(b) != "# File" {
		t.Fatalf("file = %q, want '# File'", b)
	}
}

// TestConvertCmdNotLoggedIn - no credential → exit 3 (auth).
func TestConvertCmdNotLoggedIn(t *testing.T) {
	if _, code, _ := runCLI(t, "", "convert", "-", "--base-url", "https://x/v1"); code != exitAuth {
		t.Fatalf("no-cred convert exit = %d, want %d", code, exitAuth)
	}
}

// TestConvertCmdEnvAPIKey - HEXREAD_API_KEY authenticates without any stored credential.
func TestConvertCmdEnvAPIKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"markdown":"# Env","pages":[],"meta":{}}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir) // no credential file anywhere
	t.Setenv("HOME", dir)
	t.Setenv("HEXREAD_KEYRING", "")
	t.Setenv("HEXREAD_API_KEY", "hr_live_env_key")
	var out bytes.Buffer
	root := newRoot()
	root.SetArgs([]string{"convert", "-", "--base-url", srv.URL + "/v1"})
	root.SetOut(&out)
	root.SetErr(io.Discard)
	if code := exitCode(root.Execute()); code != 0 || gotAuth != "Bearer hr_live_env_key" {
		t.Fatalf("env-key convert: code=%d auth=%q", code, gotAuth)
	}
}

// TestConvertCmdAsyncFlow - 202 → SSE progress (real wire format: all-string maps keyed by
// "event") → completed with token → fetch the result WITH that token.
func TestConvertCmdAsyncFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/convert"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"job_id":"j1","status":"queued","links":{}}`)
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: page\ndata: {\"event\":\"page\",\"page\":\"1\",\"total\":\"2\"}\n\n")
			_, _ = io.WriteString(w, "event: completed\ndata: {\"event\":\"completed\",\"token\":\"tok-9\",\"result\":\"/v1/jobs/j1/result\",\"pages\":\"2\"}\n\n")
		case strings.HasSuffix(r.URL.Path, "/markdown"):
			if r.URL.Query().Get("token") != "tok-9" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"type":"validation_error","code":"missing_token"}`)
				return
			}
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = io.WriteString(w, "# Async Done")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	out, code, errOut := runCLI(t, "k", "convert", "-", "--async", "--base-url", srv.URL+"/v1")
	if code != 0 || strings.TrimSpace(out) != "# Async Done" {
		t.Fatalf("async out=%q code=%d stderr=%q, want '# Async Done'", out, code, errOut)
	}
}

// TestConvertCmdAsyncFailed - a failed job maps to a non-zero exit via the error code class.
func TestConvertCmdAsyncFailed(t *testing.T) {
	for _, tc := range []struct {
		code string
		exit int
	}{
		{"conversion_failed", exitCapacity},    // capacity_error class → 9
		{"encrypted_pdf", exitUnprocessable},   // validation_error class → 7
		{"page_too_large", exitTooLarge},       // payload_too_large class → 8
		{"quota_exceeded", exitQuota},          // quota_error class → 5
		{"totally_unknown_code", exitCapacity}, // unknown codes default to the transient class
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/convert"):
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, `{"job_id":"j2","status":"queued","links":{}}`)
			case strings.HasSuffix(r.URL.Path, "/events"):
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: error\ndata: {\"event\":\"error\",\"code\":\""+tc.code+"\"}\n\n")
			}
		}))
		_, code, _ := runCLI(t, "k", "convert", "-", "--base-url", srv.URL+"/v1")
		srv.Close()
		if code != tc.exit {
			t.Errorf("failed-job %s exit = %d, want %d", tc.code, code, tc.exit)
		}
	}
}

// TestConvertCmdCanceled - a job canceled mid-stream is reported as a cancel (exit 1, generic),
// NOT a retryable "conversion failed" (exit 9). Cancel arrives as an error event with code
// "cancelled".
func TestConvertCmdCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/convert"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"job_id":"j4","status":"queued","links":{}}`)
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: error\ndata: {\"event\":\"error\",\"code\":\"cancelled\"}\n\n")
		}
	}))
	defer srv.Close()
	_, code, errOut := runCLI(t, "k", "convert", "-", "--base-url", srv.URL+"/v1")
	if code != exitGeneric || !strings.Contains(errOut, "canceled") {
		t.Fatalf("canceled convert: code=%d (want %d) stderr=%q", code, exitGeneric, errOut)
	}
}
