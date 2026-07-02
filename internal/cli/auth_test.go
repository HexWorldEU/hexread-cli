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
