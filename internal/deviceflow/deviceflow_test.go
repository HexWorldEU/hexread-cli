package deviceflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStartAndPoll_PendingThenSuccess(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/device":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"device_code":"DC","user_code":"WXYZ-1234","verification_uri":"https://id/device","interval":1,"expires_in":300}`))
		case "/token":
			polls++
			if polls < 3 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"AT","id_token":"ID","token_type":"Bearer","expires_in":3600}`))
		}
	}))
	defer srv.Close()

	cfg := Config{DeviceAuthURL: srv.URL + "/device", TokenURL: srv.URL + "/token", ClientID: "cli", Scopes: []string{"openid"}}
	ar, err := Start(context.Background(), srv.Client(), cfg)
	if err != nil || ar.DeviceCode != "DC" || ar.UserCode != "WXYZ-1234" {
		t.Fatalf("start: %v %+v", err, ar)
	}

	slept := 0
	tok, err := Poll(context.Background(), srv.Client(), cfg, ar.DeviceCode, ar.Interval, func(time.Duration) { slept++ })
	if err != nil || tok.AccessToken != "AT" {
		t.Fatalf("poll: %v %+v", err, tok)
	}
	if polls != 3 || slept != 2 {
		t.Fatalf("expected 3 polls + 2 sleeps, got polls=%d slept=%d", polls, slept)
	}
}

func TestPoll_SlowDownIncreasesInterval(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		polls++
		if polls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"AT"}`))
	}))
	defer srv.Close()
	cfg := Config{TokenURL: srv.URL, ClientID: "cli"}

	var durs []time.Duration
	_, err := Poll(context.Background(), srv.Client(), cfg, "DC", 5, func(d time.Duration) { durs = append(durs, d) })
	if err != nil {
		t.Fatal(err)
	}
	if len(durs) != 1 || durs[0] != 10*time.Second { // 5 + 5 on slow_down
		t.Fatalf("slow_down should raise interval to 10s, got %v", durs)
	}
}

func TestPoll_DeniedAndExpired(t *testing.T) {
	for _, tc := range []struct {
		errStr string
		want   error
	}{{"access_denied", ErrDenied}, {"expired_token", ErrExpired}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"` + tc.errStr + `"}`))
		}))
		cfg := Config{TokenURL: srv.URL, ClientID: "cli"}
		if _, err := Poll(context.Background(), srv.Client(), cfg, "DC", 1, func(time.Duration) {}); err != tc.want {
			t.Fatalf("%s: got %v want %v", tc.errStr, err, tc.want)
		}
		srv.Close()
	}
}
