package hardcover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"nextleaf/internal/library"
)

const readsResponse = `{"data":{"user_books":[
  {
    "status_id": 3,
    "rating": 4.5,
    "date_added": "2022-01-15",
    "last_read_date": "2024-03-20",
    "book": {
      "title": "The Fifth Season",
      "subtitle": "",
      "slug": "the-fifth-season",
      "release_year": 2015,
      "cached_tags": {"Genre": [{"tag": "Fantasy"}, {"tag": "Science Fiction"}], "Mood": [{"tag": "dark"}]},
      "image": {"url": "https://covers.example/fifth.jpg"},
      "contributions": [{"author": {"name": "N. K. Jemisin"}}],
      "book_series": [
        {"position": 1, "featured": true, "series": {"name": "The Broken Earth"}},
        {"position": 3, "featured": false, "series": {"name": "Ignored Series"}}
      ]
    }
  }
]}}`

// fakeAPI dispatches on query contents: `me { id }` vs the user_books query.
// It records how many times each was called and captures request metadata.
type fakeAPI struct {
	meCalls    int64
	booksCalls int64
	lastVars   map[string]any
	lastQuery  string
}

func (f *fakeAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		f.lastQuery = req.Query
		f.lastVars = req.Variables

		switch {
		case strings.Contains(req.Query, "me { id }"):
			atomic.AddInt64(&f.meCalls, 1)
			_, _ = io.WriteString(w, `{"data":{"me":[{"id":42}]}}`)
		case strings.Contains(req.Query, "user_books"):
			atomic.AddInt64(&f.booksCalls, 1)
			_, _ = io.WriteString(w, readsResponse)
		default:
			t.Errorf("unexpected query: %s", req.Query)
		}
	}))
}

func TestRecentReadsMapsData(t *testing.T) {
	api := &fakeAPI{}
	srv := api.server(t)
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL))
	entries, err := c.RecentReads(context.Background(), 5)
	if err != nil {
		t.Fatalf("RecentReads: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	e := entries[0]
	if e.Status != library.StatusRead {
		t.Errorf("Status = %d, want %d", e.Status, library.StatusRead)
	}
	if e.Rating != 4.5 {
		t.Errorf("Rating = %v, want 4.5", e.Rating)
	}
	if got := e.DateAdded.Format("2006-01-02"); got != "2022-01-15" {
		t.Errorf("DateAdded = %q, want 2022-01-15", got)
	}
	if got := e.FinishedAt.Format("2006-01-02"); got != "2024-03-20" {
		t.Errorf("FinishedAt = %q, want 2024-03-20", got)
	}

	b := e.Book
	if b.Title != "The Fifth Season" {
		t.Errorf("Title = %q", b.Title)
	}
	if b.ReleaseYear != 2015 {
		t.Errorf("ReleaseYear = %d, want 2015", b.ReleaseYear)
	}
	if b.CoverURL != "https://covers.example/fifth.jpg" {
		t.Errorf("CoverURL = %q", b.CoverURL)
	}
	if b.URL != "https://hardcover.app/books/the-fifth-season" {
		t.Errorf("URL = %q", b.URL)
	}
	if len(b.Authors) != 1 || b.Authors[0] != "N. K. Jemisin" {
		t.Errorf("Authors = %v", b.Authors)
	}
	if len(b.Genres) != 2 || b.Genres[0] != "Fantasy" || b.Genres[1] != "Science Fiction" {
		t.Errorf("Genres = %v, want [Fantasy, Science Fiction]", b.Genres)
	}
	if b.Series == nil || b.Series.Name != "The Broken Earth" || b.Series.Position != 1 {
		t.Errorf("Series = %+v, want The Broken Earth #1 (featured)", b.Series)
	}
}

func TestRecentReadsSendsStatusAndLimit(t *testing.T) {
	api := &fakeAPI{}
	srv := api.server(t)
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL))
	if _, err := c.RecentReads(context.Background(), 7); err != nil {
		t.Fatalf("RecentReads: %v", err)
	}

	if !strings.Contains(api.lastQuery, "last_read_date: desc") {
		t.Errorf("query missing read ordering:\n%s", api.lastQuery)
	}
	if got := api.lastVars["status"]; got != float64(library.StatusRead) {
		t.Errorf("status var = %v, want %d", got, library.StatusRead)
	}
	if got := api.lastVars["limit"]; got != float64(7) {
		t.Errorf("limit var = %v, want 7", got)
	}
	if got := api.lastVars["userID"]; got != float64(42) {
		t.Errorf("userID var = %v, want 42", got)
	}
}

func TestRecentReadsOrdersNullsLast(t *testing.T) {
	api := &fakeAPI{}
	srv := api.server(t)
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL))
	if _, err := c.RecentReads(context.Background(), 5); err != nil {
		t.Fatalf("RecentReads: %v", err)
	}
	// Undated reads must sort last, not masquerade as the most recent.
	if !strings.Contains(api.lastQuery, "last_read_date: desc_nulls_last") {
		t.Errorf("query should order reads desc_nulls_last:\n%s", api.lastQuery)
	}
}

func TestCurrentlyReadingOrdersByUpdated(t *testing.T) {
	api := &fakeAPI{}
	srv := api.server(t)
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL))
	if _, err := c.CurrentlyReading(context.Background()); err != nil {
		t.Fatalf("CurrentlyReading: %v", err)
	}

	if !strings.Contains(api.lastQuery, "updated_at: desc") {
		t.Errorf("query missing currently-reading ordering:\n%s", api.lastQuery)
	}
	if got := api.lastVars["status"]; got != float64(library.StatusCurrentlyRead) {
		t.Errorf("status var = %v, want %d", got, library.StatusCurrentlyRead)
	}
	if _, ok := api.lastVars["limit"]; ok {
		t.Errorf("CurrentlyReading must not send a limit, got %v", api.lastVars["limit"])
	}
}

func TestToReadOrdersByDateAddedNoLimit(t *testing.T) {
	api := &fakeAPI{}
	srv := api.server(t)
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL))
	if _, err := c.ToRead(context.Background()); err != nil {
		t.Fatalf("ToRead: %v", err)
	}

	if !strings.Contains(api.lastQuery, "date_added: asc") {
		t.Errorf("query missing TBR ordering:\n%s", api.lastQuery)
	}
	if got := api.lastVars["status"]; got != float64(library.StatusWantToRead) {
		t.Errorf("status var = %v, want %d", got, library.StatusWantToRead)
	}
	if _, ok := api.lastVars["limit"]; ok {
		t.Errorf("ToRead must not send a limit, got %v", api.lastVars["limit"])
	}
}

func TestUserIDFetchedOnce(t *testing.T) {
	api := &fakeAPI{}
	srv := api.server(t)
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL))
	if _, err := c.RecentReads(context.Background(), 5); err != nil {
		t.Fatalf("RecentReads: %v", err)
	}
	if _, err := c.ToRead(context.Background()); err != nil {
		t.Fatalf("ToRead: %v", err)
	}

	if got := atomic.LoadInt64(&api.meCalls); got != 1 {
		t.Errorf("me query called %d times, want 1 (should be cached)", got)
	}
	if got := atomic.LoadInt64(&api.booksCalls); got != 2 {
		t.Errorf("user_books called %d times, want 2", got)
	}
}
