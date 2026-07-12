// Package hardcover implements library.Source against the Hardcover GraphQL API
// (https://api.hardcover.app/v1/graphql) using only the standard library.
package hardcover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultEndpoint  = "https://api.hardcover.app/v1/graphql"
	defaultUserAgent = "nextleaf (+https://github.com/nextleaf)"
)

// Sentinel errors let callers distinguish auth and throttling failures from
// generic ones. They wrap the HTTP status the API returned.
var (
	ErrUnauthorized = errors.New("hardcover: unauthorized (check token)")
	ErrForbidden    = errors.New("hardcover: forbidden")
	ErrThrottled    = errors.New("hardcover: rate limited")
	ErrServer       = errors.New("hardcover: server error")
)

// Client is a thin transport over the Hardcover GraphQL endpoint.
type Client struct {
	token     string
	endpoint  string
	userAgent string
	http      *http.Client

	userIDMu   sync.Mutex
	userIDOnce bool
	userID     int
}

// Option configures a Client.
type Option func(*Client)

// WithEndpoint overrides the GraphQL endpoint (used in tests).
func WithEndpoint(url string) Option { return func(c *Client) { c.endpoint = url } }

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// New returns a Client authenticating with token. The token is the value shown
// on Hardcover's API settings page; a missing "Bearer " prefix is added.
func New(token string, opts ...Option) *Client {
	c := &Client{
		token:     ensureBearer(token),
		endpoint:  defaultEndpoint,
		userAgent: defaultUserAgent,
		http:      &http.Client{Timeout: 35 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func ensureBearer(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		return token
	}
	return "Bearer " + token
}

// gqlError is one entry in a GraphQL response's errors array.
type gqlError struct {
	Message string `json:"message"`
}

// execute runs a GraphQL query and unmarshals the response's data field into
// out. It maps HTTP status codes to sentinel errors and surfaces GraphQL-level
// errors (which arrive with a 200 status).
func (c *Client) execute(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables,omitempty"`
	}{Query: query, Variables: vars})
	if err != nil {
		return fmt.Errorf("hardcover: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("hardcover: build request: %w", err)
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("hardcover: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := statusError(resp.StatusCode); err != nil {
		return err
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("hardcover: decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("hardcover: graphql error: %s", envelope.Errors[0].Message)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("hardcover: decode data: %w", err)
	}
	return nil
}

func statusError(code int) error {
	switch {
	case code == http.StatusOK:
		return nil
	case code == http.StatusUnauthorized:
		return ErrUnauthorized
	case code == http.StatusForbidden:
		return ErrForbidden
	case code == http.StatusTooManyRequests:
		return ErrThrottled
	case code >= 500:
		return fmt.Errorf("%w (status %d)", ErrServer, code)
	default:
		return fmt.Errorf("hardcover: unexpected status %d", code)
	}
}

// currentUserID returns the authenticated user's id, fetching it once via
// `me { id }` and caching it. Filtering user_books by user_id keeps the status
// queries one level shallower than nesting them under `me`.
func (c *Client) currentUserID(ctx context.Context) (int, error) {
	c.userIDMu.Lock()
	defer c.userIDMu.Unlock()
	if c.userIDOnce {
		return c.userID, nil
	}

	var data struct {
		Me []struct {
			ID int `json:"id"`
		} `json:"me"`
	}
	if err := c.execute(ctx, `query { me { id } }`, nil, &data); err != nil {
		return 0, err
	}
	if len(data.Me) == 0 {
		return 0, errors.New("hardcover: me query returned no user")
	}
	c.userID, c.userIDOnce = data.Me[0].ID, true
	return c.userID, nil
}
