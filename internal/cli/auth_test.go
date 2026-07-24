package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deviceIdP mocks the IdP's RFC 8628 endpoints: device_authorization hands out codes, token
// approves on the first poll.
func deviceIdP(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device":
			_, _ = io.WriteString(w, `{"device_code":"DC-1","user_code":"WXYZ-1234","verification_uri":"https://idp.example/device","interval":1,"expires_in":300}`)
		case "/token":
			_, _ = io.WriteString(w, `{"access_token":"AT-1","token_type":"Bearer","expires_in":3600}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// TestLoginDeviceFlowStoresExchangedKey - the full `hexread login` chain: device grant → OIDC
// access token → POST /auth/cli/exchange → the minted hr_live KEY (never the raw token) lands in
// the credential store.
func TestLoginDeviceFlowStoresExchangedKey(t *testing.T) {
	idp := deviceIdP(t)
	defer idp.Close()

	var gotToken string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/cli/exchange" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotToken = req.AccessToken
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"key":"hr_live_kid1_secret9999","key_id":"kid1","last4":"9999","scopes":["convert"]}`)
	}))
	defer api.Close()

	t.Setenv("HEXREAD_DEVICE_AUTH_URL", idp.URL+"/device")
	t.Setenv("HEXREAD_TOKEN_URL", idp.URL+"/token")
	t.Setenv("HEXREAD_CLIENT_ID", "cli-client")

	out, code, errOut := runCLI(t, "", "login", "--base-url", api.URL+"/v1")
	if code != 0 || !strings.Contains(out, "Signed in") || !strings.Contains(out, "WXYZ-1234") {
		t.Fatalf("login out=%q code=%d stderr=%q", out, code, errOut)
	}
	if gotToken != "AT-1" {
		t.Fatalf("exchange saw access_token %q, want AT-1", gotToken)
	}
	cred, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "hexread", "credential"))
	if err != nil || string(cred) != "hr_live_kid1_secret9999" {
		t.Fatalf("stored credential = %q, %v - want the MINTED KEY, not the OIDC token", cred, err)
	}
}

// TestWhoamiInvalidKeyMessageBySource - a 401 names the credential's source: the stored-key
// variant hints at re-login, the env-key variant names HEXREAD_API_KEY (re-login would not
// help; the env var takes precedence over the store).
func TestWhoamiInvalidKeyMessageBySource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"authentication_error","code":"invalid_api_key","message":"Invalid or revoked API key."}`)
	}))
	defer srv.Close()

	_, code, errOut := runCLI(t, "hr_live_stored", "whoami", "--base-url", srv.URL+"/v1")
	if code != exitAuth || !strings.Contains(errOut, "stored credential is invalid") {
		t.Fatalf("stored-key whoami: code=%d stderr=%q", code, errOut)
	}

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir) // no credential file anywhere
	t.Setenv("HOME", dir)
	t.Setenv("HEXREAD_KEYRING", "")
	t.Setenv("HEXREAD_API_KEY", "hr_live_env_key")
	root := newRoot()
	root.SetArgs([]string{"whoami", "--base-url", srv.URL + "/v1"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.Execute()
	if exitCode(err) != exitAuth || err == nil || !strings.Contains(err.Error(), "HEXREAD_API_KEY is invalid") {
		t.Fatalf("env-key whoami: err=%v", err)
	}
}

// TestLoginDeviceFlowExchangeRejected - a 401 from the exchange endpoint is a clean auth failure
// (exit 3) and nothing is stored.
func TestLoginDeviceFlowExchangeRejected(t *testing.T) {
	idp := deviceIdP(t)
	defer idp.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"authentication_error","code":"unauthenticated"}`)
	}))
	defer api.Close()

	t.Setenv("HEXREAD_DEVICE_AUTH_URL", idp.URL+"/device")
	t.Setenv("HEXREAD_TOKEN_URL", idp.URL+"/token")
	t.Setenv("HEXREAD_CLIENT_ID", "cli-client")

	_, code, _ := runCLI(t, "", "login", "--base-url", api.URL+"/v1")
	if code != exitAuth {
		t.Fatalf("rejected exchange exit = %d, want %d", code, exitAuth)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "hexread", "credential")); !os.IsNotExist(err) {
		t.Fatal("no credential may be stored when the exchange is rejected")
	}
}
