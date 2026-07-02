// Package deviceflow implements the OAuth 2.0 Device Authorization Grant (RFC 8628) used
// by `hexread login`: request a device+user code, show the verification URL, then poll the
// token endpoint honoring interval / slow_down / expired / denied.
package deviceflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	DeviceAuthURL string
	TokenURL      string
	ClientID      string
	Scopes        []string
}

type AuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

var (
	ErrDenied  = errors.New("device authorization was denied")
	ErrExpired = errors.New("device code expired before approval")
)

// oauthError is the RFC 6749 §5.2 error body ({"error","error_description"}).
type oauthError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// Start requests a device + user code.
func Start(ctx context.Context, hc *http.Client, cfg Config) (AuthResponse, error) {
	form := url.Values{"client_id": {cfg.ClientID}, "scope": {strings.Join(cfg.Scopes, " ")}}
	res, err := postForm(ctx, hc, cfg.DeviceAuthURL, form)
	if err != nil {
		return AuthResponse{}, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		var oe oauthError
		_ = json.Unmarshal(body, &oe)
		if oe.Error != "" {
			return AuthResponse{}, fmt.Errorf("device authorization failed: %s (%s)", oe.Error, oe.Description)
		}
		return AuthResponse{}, fmt.Errorf("device authorization failed: status %d", res.StatusCode)
	}
	var ar AuthResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return AuthResponse{}, err
	}
	if ar.DeviceCode == "" || ar.UserCode == "" {
		return AuthResponse{}, errors.New("device authorization response is missing the device/user code")
	}
	if ar.Interval <= 0 {
		ar.Interval = 5
	}
	return ar, nil
}

// Poll polls until approval, denial, expiry, or ctx cancellation. sleep is injectable for
// tests; the default sleep aborts early when ctx is done, so Ctrl-C doesn't hang a poll.
func Poll(ctx context.Context, hc *http.Client, cfg Config, deviceCode string, interval int, sleep func(time.Duration)) (Token, error) {
	if sleep == nil {
		sleep = func(d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	if interval <= 0 {
		interval = 5
	}
	for {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return Token{}, ErrExpired
			}
			return Token{}, err
		}
		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {deviceCode},
			"client_id":   {cfg.ClientID},
		}
		res, err := postForm(ctx, hc, cfg.TokenURL, form)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return Token{}, ErrExpired // the expiry deadline fired mid-request
			}
			return Token{}, err
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		res.Body.Close()

		if res.StatusCode == http.StatusOK {
			var t Token
			if err := json.Unmarshal(body, &t); err != nil {
				return Token{}, err
			}
			if t.AccessToken == "" {
				return Token{}, errors.New("token response is missing access_token")
			}
			return t, nil
		}
		var oe oauthError
		_ = json.Unmarshal(body, &oe)
		switch oe.Error {
		case "authorization_pending":
			sleep(time.Duration(interval) * time.Second)
		case "slow_down":
			interval += 5
			sleep(time.Duration(interval) * time.Second)
		case "access_denied":
			return Token{}, ErrDenied
		case "expired_token":
			return Token{}, ErrExpired
		default:
			if oe.Description != "" {
				return Token{}, fmt.Errorf("device token error: %s (%s)", oe.Error, oe.Description)
			}
			return Token{}, fmt.Errorf("device token error: %q (status %d)", oe.Error, res.StatusCode)
		}
	}
}

func postForm(ctx context.Context, hc *http.Client, endpoint string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return hc.Do(req)
}
