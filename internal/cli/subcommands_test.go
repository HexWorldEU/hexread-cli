package cli

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJobsStatus - `jobs status <id>` prints the status line.
func TestJobsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"job_id":"j1","status":"processing","pages":5,"pages_done":2}`)
	}))
	defer srv.Close()
	out, code, _ := runCLI(t, "k", "jobs", "status", "j1", "--base-url", srv.URL+"/v1")
	if code != 0 || !strings.Contains(out, "j1") || !strings.Contains(out, "2/5") {
		t.Fatalf("status out=%q code=%d", out, code)
	}
}

// TestJobsCancel - `jobs cancel <id>` calls DELETE and confirms.
func TestJobsCancel(t *testing.T) {
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	out, code, _ := runCLI(t, "k", "jobs", "cancel", "j7", "--base-url", srv.URL+"/v1")
	if code != 0 || method != http.MethodDelete || !strings.Contains(out, "Canceled j7") {
		t.Fatalf("cancel out=%q code=%d method=%s", out, code, method)
	}
}

// TestKeysCreate - `keys create` prints the secret to stdout (warning to stderr).
func TestKeysCreate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"key":"hr_live_abc_secret","key_id":"abc","last4":"cret","scopes":["convert"]}`)
	}))
	defer srv.Close()
	out, code, errOut := runCLI(t, "k", "keys", "create", "--base-url", srv.URL+"/v1")
	if code != 0 || strings.TrimSpace(out) != "hr_live_abc_secret" {
		t.Fatalf("create out=%q code=%d", out, code)
	}
	if !strings.Contains(errOut, "won't be shown again") {
		t.Fatalf("expected the one-time warning on stderr, got %q", errOut)
	}
}

// TestKeysList - `keys list` renders a table; revoked keys are marked.
func TestKeysList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"keys":[{"key_id":"a","last4":"1234","scopes":["convert"],"created_at":"t"},{"key_id":"b","last4":"5678","scopes":[],"created_at":"t","revoked_at":"t2"}]}`)
	}))
	defer srv.Close()
	out, code, _ := runCLI(t, "k", "keys", "list", "--base-url", srv.URL+"/v1")
	if code != 0 || !strings.Contains(out, "hr_live_a_…1234") || !strings.Contains(out, "revoked") {
		t.Fatalf("list out=%q code=%d", out, code)
	}
}

// TestUsage - `usage` prints the tier + page usage/allowance.
func TestUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tier":"standard","anonymous":false,"pages":{"used":100,"reserved":0,"remaining":4900,"allowance":5000},"source":"period","period":"2026-07","resets_at":"2026-08-01T00:00:00Z","concurrency":{"in_use":0,"limit":3},"api_access":true}`)
	}))
	defer srv.Close()
	out, code, _ := runCLI(t, "k", "usage", "--base-url", srv.URL+"/v1")
	if code != 0 || !strings.Contains(out, "tier:") || !strings.Contains(out, "100 used / 5000 this period") || !strings.Contains(out, "remaining:   4900") {
		t.Fatalf("usage out=%q code=%d", out, code)
	}
}

// TestBatchPartialFailureExit10 - batch converts a directory of files; one fails → exit 10, and the
// successful files are written to the output dir.
func TestBatchPartialFailureExit10(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/usage") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"tier":"standard","pages":{},"concurrency":{"in_use":0,"limit":3},"api_access":true}`)
			return
		}
		// convert: fail any file whose bytes contain "BAD".
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		bad := false
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if p.FormName() == "file" {
				b, _ := io.ReadAll(p)
				bad = strings.Contains(string(b), "BAD")
			}
		}
		if bad {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_, _ = io.WriteString(w, `{"type":"payload_too_large","code":"file_too_large"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"markdown":"# OK","pages":[],"meta":{"pages":1}}`)
	}))
	defer srv.Close()

	in := t.TempDir()
	for name, body := range map[string]string{"a.pdf": "OK", "b.pdf": "OK", "c.pdf": "BAD"} {
		_ = os.WriteFile(filepath.Join(in, name), []byte(body), 0o644)
	}
	outDir := t.TempDir()
	_, code, errOut := runCLI(t, "k", "batch", filepath.Join(in, "*.pdf"), "-o", outDir, "--base-url", srv.URL+"/v1")

	if code != exitPartialBatch {
		t.Fatalf("batch exit = %d, want %d (partial); stderr=%s", code, exitPartialBatch, errOut)
	}
	// The two good files are written; the bad one is not.
	for _, ok := range []string{"a.md", "b.md"} {
		if b, err := os.ReadFile(filepath.Join(outDir, ok)); err != nil || string(b) != "# OK" {
			t.Errorf("%s = %q, %v, want '# OK'", ok, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "c.md")); !os.IsNotExist(err) {
		t.Error("the failed file must not be written")
	}
	if !strings.Contains(errOut, "file_too_large") {
		t.Errorf("batch table should show the failure code, got %q", errOut)
	}
}

// TestBatchOutputCollision - two inputs mapping to the same output file are refused up front
// (exit 2) instead of silently clobbering one result with the other.
func TestBatchOutputCollision(t *testing.T) {
	in := t.TempDir()
	for _, name := range []string{"x.pdf", "x.png"} { // both → x.md
		_ = os.WriteFile(filepath.Join(in, name), []byte("data"), 0o644)
	}
	_, code, errOut := runCLI(t, "k", "batch", filepath.Join(in, "*"), "-o", t.TempDir(),
		"--base-url", "http://127.0.0.1:0/v1")
	if code != exitUsage || !strings.Contains(errOut, "output collision") {
		t.Fatalf("collision exit = %d stderr=%q, want %d + collision message", code, errOut, exitUsage)
	}
}

// TestBatchAllSucceedExit0 - when every file converts, exit 0.
func TestBatchAllSucceedExit0(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/usage") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"concurrency":{"limit":2}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"markdown":"# OK","pages":[],"meta":{}}`)
	}))
	defer srv.Close()
	in := t.TempDir()
	_ = os.WriteFile(filepath.Join(in, "x.pdf"), []byte("x"), 0o644)
	_, code, _ := runCLI(t, "k", "batch", filepath.Join(in, "*.pdf"), "-o", t.TempDir(), "--base-url", srv.URL+"/v1")
	if code != 0 {
		t.Fatalf("all-success batch exit = %d, want 0", code)
	}
}
