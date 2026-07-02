// Package client is the minimal HexRead API client used by the CLI.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HexWorldEU/hexread-cli/internal/version"
)

// APIError is a decoded HexRead error envelope. Type drives the CLI exit code (see the cli package);
// Code/Message/DocURL are the stable error contract (DocURL resolves to the public docs).
type APIError struct {
	Status    int    `json:"-"`
	Type      string `json:"type"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	DocURL    string `json:"doc_url"`
	RequestID string `json:"request_id"`
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("request failed (%d)", e.Status)
}

// errorFrom reads the canonical error envelope off a non-2xx response. When the body is not an
// envelope (a proxy/CDN error page), it synthesizes one, deriving the error class from the HTTP
// status so exit codes stay right even for responses that never reached the HexRead backend.
func errorFrom(res *http.Response) *APIError {
	var e APIError
	_ = json.NewDecoder(io.LimitReader(res.Body, 8<<10)).Decode(&e)
	e.Status = res.StatusCode
	if e.Type == "" {
		e.Type = typeForStatus(res.StatusCode)
	}
	if e.Code == "" {
		e.Code = fmt.Sprintf("http_%d", res.StatusCode)
	}
	if e.Message == "" {
		e.Message = fmt.Sprintf("request failed with status %d", res.StatusCode)
	}
	return &e
}

// typeForStatus maps an HTTP status to the envelope class it unambiguously implies;
// ambiguous statuses (5xx) map to internal_error.
func typeForStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "quota_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusGone:
		return "gone"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return "validation_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "internal_error"
	}
}

// Client talks to the HexRead /v1 API. Two HTTP clients on purpose: API carries an overall
// timeout for the small control-plane JSON calls; Stream has none - uploads, SSE streams, and
// result downloads run for as long as the request context allows.
type Client struct {
	BaseURL string
	Key     string
	API     *http.Client
	Stream  *http.Client
}

func New(baseURL, key string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Key:     key,
		API:     &http.Client{Timeout: 60 * time.Second},
		Stream:  &http.Client{}, // dial/TLS bounds come from http.DefaultTransport
	}
}

// newRequest builds a request with the auth + client-identification headers every call sends.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("User-Agent", "hexread-cli/"+version.Version)
	return req, nil
}

// Identity is the response of GET /v1/auth/me.
type Identity struct {
	Sub  string `json:"sub"`
	Tier string `json:"tier"`
	Via  string `json:"via"`
}

func (c *Client) WhoAmI(ctx context.Context) (Identity, error) {
	var id Identity
	err := c.getJSON(ctx, "/auth/me", &id)
	return id, err
}
