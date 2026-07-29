package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRefusesCrossOriginRedirect - a redirect to a different origin must NOT be followed, so the
// Bearer API key is never re-sent to another host (and a same-host scheme downgrade can't leak it).
func TestRefusesCrossOriginRedirect(t *testing.T) {
	var attackerSawAuth string
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerSawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"leaked","tier":"x","via":"apikey"}`))
	}))
	defer attacker.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/v1/auth/me", http.StatusFound)
	}))
	defer origin.Close()

	c := New(origin.URL+"/v1", "hr_live_secret")
	if _, err := c.WhoAmI(context.Background()); err == nil {
		t.Fatal("expected the cross-origin redirect to be refused, got nil error")
	}
	if attackerSawAuth != "" {
		t.Fatalf("API key leaked to the redirect target: %q", attackerSawAuth)
	}
}

func TestWhoAmI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hr_live_good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"type":"authentication_error","code":"invalid_api_key","request_id":"x"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"user-1","tier":"standard","via":"apikey"}`))
	}))
	defer srv.Close()

	id, err := New(srv.URL+"/v1", "hr_live_good").WhoAmI(context.Background())
	if err != nil || id.Sub != "user-1" || id.Tier != "standard" {
		t.Fatalf("whoami: %v %+v", err, id)
	}

	// A 401 returns the typed APIError carrying the envelope's code + status (for the CLI exit map).
	_, err = New(srv.URL+"/v1", "hr_live_bad").WhoAmI(context.Background())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized || ae.Code != "invalid_api_key" {
		t.Fatalf("expected *APIError{401, invalid_api_key}, got %v", err)
	}
	if ae.Type != "authentication_error" {
		t.Fatalf("type = %q, want authentication_error", ae.Type)
	}
}
