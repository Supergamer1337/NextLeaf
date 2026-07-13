// Package grimmory implements library.Source against a self-hosted Grimmory
// instance (https://github.com/grimmory-tools/grimmory) using only the
// standard library. Grimmory issues short-lived JWTs via username/password
// login; the client re-logs in transparently when a token lapses.
package grimmory

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

const defaultUserAgent = "nextleaf (+https://github.com/nextleaf)"

// tokenSkew is subtracted from a token's lifetime so we re-login shortly
// before the server would start rejecting it.
const tokenSkew = 30 * time.Second

// Sentinel errors let callers distinguish auth and throttling failures from
// generic ones. They wrap the HTTP status the API returned.
var (
	ErrUnauthorized = errors.New("grimmory: unauthorized (check username/password)")
	ErrForbidden    = errors.New("grimmory: forbidden")
	ErrThrottled    = errors.New("grimmory: rate limited")
	ErrServer       = errors.New("grimmory: server error")
)

// Client is a thin transport over a Grimmory instance's REST API.
type Client struct {
	baseRaw   string // instance root, no trailing slash
	username  string
	password  string
	userAgent string
	http      *http.Client

	tokenMu     sync.Mutex
	accessToken string
	tokenExp    time.Time
	now         func() time.Time // overridable in tests
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// New returns a Client for the Grimmory instance at baseURL, authenticating
// with a local account's username and password. Grimmory has no long-lived API
// keys, so the credentials are kept to re-login whenever the short-lived
// access token expires; tokens live only in memory.
func New(baseURL, username, password string, opts ...Option) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	c := &Client{
		baseRaw:   baseURL,
		username:  username,
		password:  password,
		userAgent: defaultUserAgent,
		http:      &http.Client{Timeout: 35 * time.Second},
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
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
		return fmt.Errorf("grimmory: unexpected status %d", code)
	}
}

// token returns a valid access token, logging in when none is held or the
// held one has expired. Concurrent callers share one login.
func (c *Client) token(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.accessToken != "" && c.now().Before(c.tokenExp) {
		return c.accessToken, nil
	}
	return c.loginLocked(ctx)
}

// invalidateToken drops the held token if it is the one the server just
// rejected, so the next call logs in afresh.
func (c *Client) invalidateToken(rejected string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.accessToken == rejected {
		c.accessToken = ""
	}
}

// loginLocked performs the credential login. Callers must hold tokenMu.
// Grimmory also returns a refresh token, but those rotate on every use and so
// cannot survive a stateless restart — re-login with credentials instead.
func (c *Client) loginLocked(ctx context.Context) (string, error) {
	body, err := json.Marshal(struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{c.username, c.password})
	if err != nil {
		return "", fmt.Errorf("grimmory: marshal login: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseRaw+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("grimmory: build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("grimmory: login request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Grimmory reports invalid credentials as 400 rather than 401.
	if resp.StatusCode == http.StatusBadRequest {
		return "", ErrUnauthorized
	}
	if err := statusError(resp.StatusCode); err != nil {
		return "", err
	}

	var auth struct {
		AccessToken string `json:"accessToken"`
		Expires     int64  `json:"expires"` // seconds until the access token lapses
	}
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		return "", fmt.Errorf("grimmory: decode login response: %w", err)
	}
	if auth.AccessToken == "" {
		return "", errors.New("grimmory: login returned no access token")
	}

	c.accessToken = auth.AccessToken
	c.tokenExp = c.now().Add(time.Duration(auth.Expires)*time.Second - tokenSkew)
	return c.accessToken, nil
}

// getJSON performs an authenticated GET and decodes the response into out.
// A 401 invalidates the token and retries once with a fresh login, since the
// server may revoke tokens before their advertised expiry.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	for attempt := 0; ; attempt++ {
		token, err := c.token(ctx)
		if err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseRaw+path, nil)
		if err != nil {
			return fmt.Errorf("grimmory: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", c.userAgent)

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("grimmory: request: %w", err)
		}

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			_ = resp.Body.Close()
			c.invalidateToken(token)
			continue
		}
		if err := statusError(resp.StatusCode); err != nil {
			_ = resp.Body.Close()
			return err
		}

		err = json.NewDecoder(resp.Body).Decode(out)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("grimmory: decode response: %w", err)
		}
		return nil
	}
}

// book mirrors the fields we use from Grimmory's book JSON. Unset fields are
// omitted by the server (NON_NULL serialization), so absence decodes to zero
// values. Instants are kept as strings and parsed leniently so one malformed
// date cannot fail the whole payload.
type book struct {
	ID             int       `json:"id"`
	Title          string    `json:"title"` // fallback when metadata is absent
	AddedOn        string    `json:"addedOn"`
	PersonalRating float64   `json:"personalRating"`
	ReadStatus     string    `json:"readStatus"` // absent means UNSET
	DateFinished   string    `json:"dateFinished"`
	Metadata       *metadata `json:"metadata"`
}

type metadata struct {
	Title         string   `json:"title"`
	Subtitle      string   `json:"subtitle"`
	PublishedDate string   `json:"publishedDate"` // "2006-01-02"
	SeriesName    string   `json:"seriesName"`
	SeriesNumber  float64  `json:"seriesNumber"`
	PageCount     int      `json:"pageCount"`
	Authors       []string `json:"authors"`
	Categories    []string `json:"categories"`
	Moods         []string `json:"moods"`
	ThumbnailURL  string   `json:"thumbnailUrl"`
	ExternalURL   string   `json:"externalUrl"`
}

// fetchBooks retrieves every book visible to the account, with full metadata
// (the list view strips fields like moods and categories that the picker
// scores on).
func (c *Client) fetchBooks(ctx context.Context) ([]book, error) {
	var books []book
	if err := c.getJSON(ctx, "/api/v1/books?withDescription=false&stripForListView=false", &books); err != nil {
		return nil, err
	}
	return books, nil
}
