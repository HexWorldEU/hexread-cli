package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/HexWorldEU/hexread-cli/internal/version"
)

// GetJob returns a job's current status (metadata only).
func (c *Client) GetJob(ctx context.Context, jobID string) (JobState, error) {
	var st JobState
	err := c.getJSON(ctx, "/jobs/"+jobID, &st)
	return st, err
}

// CancelJob cancels a queued or in-flight job.
func (c *Client) CancelJob(ctx context.Context, jobID string) error {
	return c.do(ctx, http.MethodDelete, "/jobs/"+jobID, nil, nil)
}

type APIKey struct {
	KeyID     string   `json:"key_id"`
	Last4     string   `json:"last4"`
	Scopes    []string `json:"scopes"`
	Tier      string   `json:"tier,omitempty"`
	CreatedAt string   `json:"created_at"`
	RevokedAt string   `json:"revoked_at,omitempty"`
}

type APIKeyList struct {
	Keys []APIKey `json:"keys"`
}

// APIKeyCreated is a freshly minted key - the full secret is returned ONCE.
type APIKeyCreated struct {
	Key       string   `json:"key"`
	KeyID     string   `json:"key_id"`
	Last4     string   `json:"last4"`
	Scopes    []string `json:"scopes"`
	CreatedAt string   `json:"created_at,omitempty"`
}

func (c *Client) ListKeys(ctx context.Context) ([]APIKey, error) {
	var list APIKeyList
	err := c.getJSON(ctx, "/keys", &list)
	return list.Keys, err
}

func (c *Client) CreateKey(ctx context.Context, idempotencyKey string) (APIKeyCreated, error) {
	return c.mintKey(ctx, "/keys", idempotencyKey)
}

func (c *Client) RotateKey(ctx context.Context, keyID, idempotencyKey string) (APIKeyCreated, error) {
	return c.mintKey(ctx, "/keys/"+keyID+"/rotate", idempotencyKey)
}

func (c *Client) RevokeKey(ctx context.Context, keyID string) error {
	return c.do(ctx, http.MethodDelete, "/keys/"+keyID, nil, nil)
}

func (c *Client) mintKey(ctx context.Context, path, idempotencyKey string) (APIKeyCreated, error) {
	hdr := map[string]string{}
	if idempotencyKey != "" {
		hdr["Idempotency-Key"] = idempotencyKey
	}
	var out APIKeyCreated
	err := c.doJSON(ctx, http.MethodPost, path, []byte("{}"), hdr, &out)
	return out, err
}

// ExchangeOIDCToken trades a device-grant OIDC access token for a freshly minted hr_live API key
// (POST /v1/auth/cli/exchange; the full secret is returned ONCE). Deliberately unauthenticated -
// the token in the body IS the credential, so no Authorization header is sent.
func (c *Client) ExchangeOIDCToken(ctx context.Context, accessToken string) (APIKeyCreated, error) {
	body, err := json.Marshal(map[string]string{"access_token": accessToken})
	if err != nil {
		return APIKeyCreated{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/auth/cli/exchange", bytes.NewReader(body))
	if err != nil {
		return APIKeyCreated{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "hexread-cli/"+version.Version)
	res, err := c.API.Do(req)
	if err != nil {
		return APIKeyCreated{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return APIKeyCreated{}, errorFrom(res)
	}
	var out APIKeyCreated
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return APIKeyCreated{}, err
	}
	return out, nil
}

type UsageMeters struct {
	Tier      string `json:"tier"`
	Anonymous bool   `json:"anonymous"`
	// Pages usage for the current allowance window: Allowance is the cap, Used is how many pages
	// were converted, and Remaining is what's left. Metering is flat - 1 physical page = 1 page.
	Pages struct {
		Used      int `json:"used"`
		Reserved  int `json:"reserved"`
		Remaining int `json:"remaining"`
		Allowance int `json:"allowance"`
	} `json:"pages"`
	Source      string `json:"source"`    // "trial" | "period"
	Period      string `json:"period"`    // "YYYY-MM"
	ResetsAt    string `json:"resets_at"` // RFC3339
	Concurrency struct {
		InUse int `json:"in_use"`
		Limit int `json:"limit"`
	} `json:"concurrency"`
	APIAccess bool `json:"api_access"`
}

func (c *Client) GetUsage(ctx context.Context) (UsageMeters, error) {
	var u UsageMeters
	err := c.getJSON(ctx, "/usage", &u)
	return u, err
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, nil, out)
}

// do issues a request and discards the body, returning a typed *APIError on non-2xx.
func (c *Client) do(ctx context.Context, method, path string, body []byte, hdr map[string]string) error {
	return c.doJSON(ctx, method, path, body, hdr, nil)
}

// doJSON issues a control-plane request and (when out != nil) decodes a JSON body;
// non-2xx → *APIError.
func (c *Client) doJSON(ctx context.Context, method, path string, body []byte, hdr map[string]string, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := c.newRequest(ctx, method, path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.API.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errorFrom(res)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}
