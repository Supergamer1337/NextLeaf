package grimmory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fake is a minimal Grimmory instance: a login endpoint issuing sequential
// tokens ("tok1", "tok2", ...) for the credentials user/pass, and a books
// endpoint whose behaviour each test chooses.
type fake struct {
	logins  atomic.Int32
	expires int // login response "expires" in seconds; 0 means 7200
	books   func(r *http.Request, logins int32) (status int, body string)
}

// acceptLatest returns a books handler that only honours the most recently
// issued token and replies with body.
func acceptLatest(body string) func(*http.Request, int32) (int, string) {
	return func(r *http.Request, logins int32) (int, string) {
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer tok%d", logins) {
			return http.StatusUnauthorized, ""
		}
		return http.StatusOK, body
	}
}

func (f *fake) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var creds struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
			t.Errorf("login: decode body: %v", err)
		}
		if creds.Username != "user" || creds.Password != "pass" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		n := f.logins.Add(1)
		expires := f.expires
		if expires == 0 {
			expires = 7200
		}
		_, _ = fmt.Fprintf(w, `{"accessToken":"tok%d","refreshToken":"unused","expires":%d}`, n, expires)
	})
	mux.HandleFunc("GET /api/v1/books", func(w http.ResponseWriter, r *http.Request) {
		status, body := f.books(r, f.logins.Load())
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLoginAndRequestShape(t *testing.T) {
	var gotUA, gotQuery string
	f := &fake{}
	f.books = func(r *http.Request, logins int32) (int, string) {
		gotUA = r.Header.Get("User-Agent")
		gotQuery = r.URL.RawQuery
		return acceptLatest(`[{"id":1,"title":"Dune"}]`)(r, logins)
	}
	srv := f.server(t)

	c := New(srv.URL, "user", "pass", WithUserAgent("nextleaf-test"))
	books, err := c.fetchBooks(context.Background())
	if err != nil {
		t.Fatalf("fetchBooks: %v", err)
	}
	if len(books) != 1 || books[0].Title != "Dune" {
		t.Errorf("books = %+v, want one book titled Dune", books)
	}
	if gotUA != "nextleaf-test" {
		t.Errorf("User-Agent = %q, want nextleaf-test", gotUA)
	}
	want := "withDescription=true&stripForListView=false"
	if gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
}

func TestLoginFailure(t *testing.T) {
	t.Run("bad credentials", func(t *testing.T) {
		f := &fake{books: acceptLatest(`[]`)}
		srv := f.server(t)

		c := New(srv.URL, "user", "wrong")
		if _, err := c.fetchBooks(context.Background()); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("err = %v, want ErrUnauthorized", err)
		}
	})

	// Grimmory reports invalid credentials as 400, not 401.
	t.Run("bad credentials as 400", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"Invalid credentials","status":400}`)
		}))
		t.Cleanup(srv.Close)

		c := New(srv.URL, "user", "pass")
		if _, err := c.fetchBooks(context.Background()); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("err = %v, want ErrUnauthorized", err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		c := New(srv.URL, "user", "pass")
		if _, err := c.fetchBooks(context.Background()); !errors.Is(err, ErrServer) {
			t.Errorf("err = %v, want ErrServer", err)
		}
	})
}

func TestTokenReuse(t *testing.T) {
	f := &fake{books: acceptLatest(`[]`)}
	srv := f.server(t)

	c := New(srv.URL, "user", "pass")
	for i := 0; i < 2; i++ {
		if _, err := c.fetchBooks(context.Background()); err != nil {
			t.Fatalf("fetchBooks #%d: %v", i+1, err)
		}
	}
	if got := f.logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1 (token should be reused)", got)
	}
}

func TestTokenExpiryRelogin(t *testing.T) {
	f := &fake{expires: 60, books: acceptLatest(`[]`)}
	srv := f.server(t)

	base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	current := base
	c := New(srv.URL, "user", "pass")
	c.now = func() time.Time { return current }

	if _, err := c.fetchBooks(context.Background()); err != nil {
		t.Fatalf("fetchBooks #1: %v", err)
	}
	current = base.Add(2 * time.Minute) // past the 60s token lifetime
	if _, err := c.fetchBooks(context.Background()); err != nil {
		t.Fatalf("fetchBooks #2: %v", err)
	}
	if got := f.logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2 (expired token should trigger re-login)", got)
	}
}

func TestBooks401RetryOnce(t *testing.T) {
	t.Run("recovers with fresh token", func(t *testing.T) {
		f := &fake{}
		f.books = func(r *http.Request, _ int32) (int, string) {
			// Only the second token works, as if tok1 were revoked server-side.
			if r.Header.Get("Authorization") != "Bearer tok2" {
				return http.StatusUnauthorized, ""
			}
			return http.StatusOK, `[]`
		}
		srv := f.server(t)

		c := New(srv.URL, "user", "pass")
		if _, err := c.fetchBooks(context.Background()); err != nil {
			t.Fatalf("fetchBooks: %v", err)
		}
		if got := f.logins.Load(); got != 2 {
			t.Errorf("logins = %d, want 2 (one re-login after 401)", got)
		}
	})

	t.Run("gives up after one retry", func(t *testing.T) {
		f := &fake{}
		f.books = func(*http.Request, int32) (int, string) {
			return http.StatusUnauthorized, ""
		}
		srv := f.server(t)

		c := New(srv.URL, "user", "pass")
		if _, err := c.fetchBooks(context.Background()); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("err = %v, want ErrUnauthorized", err)
		}
		if got := f.logins.Load(); got != 2 {
			t.Errorf("logins = %d, want 2 (no retry loop)", got)
		}
	})
}

func TestBooksStatusErrors(t *testing.T) {
	cases := []struct {
		code int
		want error
	}{
		{http.StatusForbidden, ErrForbidden},
		{http.StatusTooManyRequests, ErrThrottled},
		{http.StatusInternalServerError, ErrServer},
	}
	for _, tc := range cases {
		f := &fake{}
		f.books = func(*http.Request, int32) (int, string) { return tc.code, "" }
		srv := f.server(t)

		c := New(srv.URL, "user", "pass")
		_, err := c.fetchBooks(context.Background())
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: err = %v, want %v", tc.code, err, tc.want)
		}
	}
}

func TestCoverImageAuthedFetch(t *testing.T) {
	var logins atomic.Int32
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
			n := logins.Add(1)
			_, _ = fmt.Fprintf(w, `{"accessToken":"tok%d","refreshToken":"unused","expires":7200}`, n)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/media/book/7/thumbnail":
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = io.WriteString(w, "jpeg-bytes")
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "user", "pass")
	body, ct, err := c.CoverImage(context.Background(), "7")
	if err != nil {
		t.Fatalf("CoverImage: %v", err)
	}
	defer func() { _ = body.Close() }()

	data, _ := io.ReadAll(body)
	if string(data) != "jpeg-bytes" || ct != "image/jpeg" {
		t.Errorf("CoverImage = (%q, %q), want (jpeg-bytes, image/jpeg)", data, ct)
	}
	if gotAuth != "Bearer tok1" {
		t.Errorf("Authorization = %q, want Bearer tok1", gotAuth)
	}
}

func TestCoverImage401RetryOnce(t *testing.T) {
	var logins atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/login" {
			n := logins.Add(1)
			_, _ = fmt.Fprintf(w, `{"accessToken":"tok%d","refreshToken":"unused","expires":7200}`, n)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, "png")
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "user", "pass")
	body, _, err := c.CoverImage(context.Background(), "7")
	if err != nil {
		t.Fatalf("CoverImage: %v", err)
	}
	_ = body.Close()
	if got := logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2 (one re-login after 401)", got)
	}
}

func TestCoverImageRejectsBadID(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL, "user", "pass")
	for _, id := range []string{"", "7abc", "../7", "7/cover"} {
		if _, _, err := c.CoverImage(context.Background(), id); err == nil {
			t.Errorf("CoverImage(%q): want error for a non-numeric id", id)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("server saw %d requests, want 0 (ids rejected before HTTP)", got)
	}
}

func TestFetchBooksDecoding(t *testing.T) {
	// One fully populated book and one minimal book, mirroring Grimmory's
	// NON_NULL serialization where unset fields are omitted entirely.
	const body = `[
		{
			"id": 7,
			"title": "Top-Level Title",
			"addedOn": "2025-03-01T10:00:00Z",
			"personalRating": 4,
			"readStatus": "READ",
			"dateFinished": "2025-06-15T20:30:00Z",
			"metadata": {
				"title": "Hyperion",
				"subtitle": "A Cantos",
				"publishedDate": "1989-05-26",
				"seriesName": "Hyperion Cantos",
				"seriesNumber": 1,
				"pageCount": 482,
				"authors": ["Dan Simmons"],
				"categories": ["Science Fiction"],
				"moods": ["dark"],
				"thumbnailUrl": "/api/v1/media/book/7/thumbnail",
				"externalUrl": "https://example.com/hyperion"
			}
		},
		{"id": 8, "title": "Bare Book"}
	]`
	f := &fake{books: acceptLatest(body)}
	srv := f.server(t)

	c := New(srv.URL, "user", "pass")
	books, err := c.fetchBooks(context.Background())
	if err != nil {
		t.Fatalf("fetchBooks: %v", err)
	}
	if len(books) != 2 {
		t.Fatalf("len(books) = %d, want 2", len(books))
	}

	full := books[0]
	if full.ID != 7 || full.ReadStatus != "READ" || full.PersonalRating != 4 {
		t.Errorf("full book = %+v, want id 7, READ, rating 4", full)
	}
	if full.AddedOn != "2025-03-01T10:00:00Z" || full.DateFinished != "2025-06-15T20:30:00Z" {
		t.Errorf("dates = %q / %q, want raw RFC3339 strings", full.AddedOn, full.DateFinished)
	}
	m := full.Metadata
	if m == nil {
		t.Fatal("full book metadata = nil, want populated")
	}
	if m.Title != "Hyperion" || m.SeriesName != "Hyperion Cantos" || m.SeriesNumber != 1 ||
		m.PageCount != 482 || len(m.Authors) != 1 || len(m.Categories) != 1 || len(m.Moods) != 1 ||
		m.PublishedDate != "1989-05-26" || m.ThumbnailURL == "" || m.ExternalURL == "" {
		t.Errorf("metadata = %+v, missing expected fields", m)
	}

	bare := books[1]
	if bare.ReadStatus != "" || bare.Metadata != nil || bare.Title != "Bare Book" {
		t.Errorf("bare book = %+v, want empty status, nil metadata, top-level title", bare)
	}
}
