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
    "owned": true,
    "date_added": "2022-01-15",
    "last_read_date": "2024-03-20",
    "book": {
      "title": "The Fifth Season",
      "subtitle": "",
      "description": "A world ends in ash.",
      "slug": "the-fifth-season",
      "release_year": 2015,
      "pages": 512,
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
	wantRef := library.SourceRef{Name: "hardcover", URL: "https://hardcover.app/books/the-fifth-season"}
	if len(e.Sources) != 1 || e.Sources[0] != wantRef {
		t.Errorf("Sources = %v, want [%v]", e.Sources, wantRef)
	}
	if !e.Available {
		t.Error("Available = false, want true for an owned book")
	}
	if !strings.Contains(api.lastQuery, "owned") {
		t.Error("query does not request the owned field")
	}
	if b := e.Book; b.Description != "A world ends in ash." {
		t.Errorf("Description = %q, want mapped", b.Description)
	}
	if !strings.Contains(api.lastQuery, "description") {
		t.Error("query does not request the description field")
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
	if b.PageCount != 512 {
		t.Errorf("PageCount = %d, want 512", b.PageCount)
	}
	if len(b.Moods) != 1 || b.Moods[0] != "Dark" {
		t.Errorf("Moods = %v, want [Dark] (casing normalized)", b.Moods)
	}
	if b.Nonfiction != nil {
		t.Errorf("Nonfiction = %v, want nil (no fiction/nonfiction tag)", *b.Nonfiction)
	}
}

func TestClassifyMode(t *testing.T) {
	cases := []struct {
		genres []string
		want   *bool
	}{
		{[]string{"History", "Nonfiction"}, boolp(true)},
		{[]string{"Science", "Non-Fiction"}, boolp(true)},
		{[]string{"Fantasy", "Fiction"}, boolp(false)},
		{[]string{"Fantasy", "Adventure"}, nil},
		{nil, nil},
	}
	for _, tc := range cases {
		got := classifyMode(tc.genres)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("classifyMode(%v) = %v, want nil", tc.genres, *got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("classifyMode(%v) = %v, want %v", tc.genres, got, *tc.want)
		}
	}
}

func boolp(b bool) *bool { return &b }

func TestCleanGenres(t *testing.T) {
	got := cleanGenres([]string{"political science", "General", "Fantasy", "Fiction", "Science Fiction", "genre fiction", "LGBT"})
	want := []string{"Political Science", "Fantasy", "Science Fiction", "LGBT"}
	if len(got) != len(want) {
		t.Fatalf("cleanGenres = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cleanGenres[%d] = %q, want %q", i, got[i], want[i])
		}
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

const nextInSeriesResponse = `{"data":{"book_series":[
  {
    "book": {
      "title": "The Obelisk Gate",
      "subtitle": "",
      "slug": "the-obelisk-gate",
      "release_year": 2016,
      "cached_tags": {"Genre": [{"tag": "Fantasy"}]},
      "image": {"url": "https://covers.example/obelisk.jpg"},
      "contributions": [{"author": {"name": "N. K. Jemisin"}}],
      "book_series": [{"position": 2, "featured": true, "series": {"name": "The Broken Earth"}}]
    }
  }
]}}`

func TestNextInSeriesReturnsNextBook(t *testing.T) {
	var gotQuery string
	var gotVars map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		gotQuery, gotVars = req.Query, req.Variables
		_, _ = io.WriteString(w, nextInSeriesResponse)
	}))
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL))
	book, found, err := c.NextInSeries(context.Background(), library.Series{Name: "The Broken Earth", Position: 1})
	if err != nil {
		t.Fatalf("NextInSeries: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if book.Title != "The Obelisk Gate" {
		t.Errorf("Title = %q, want The Obelisk Gate", book.Title)
	}
	if book.URL != "https://hardcover.app/books/the-obelisk-gate" {
		t.Errorf("URL = %q", book.URL)
	}
	if book.Series == nil || book.Series.Position != 2 {
		t.Errorf("Series = %+v, want position 2", book.Series)
	}
	// It must query the next position (>) of the named series, not by user.
	if !strings.Contains(gotQuery, "position: {_gt: $after}") {
		t.Errorf("query should fetch the next position:\n%s", gotQuery)
	}
	if got := gotVars["name"]; got != "The Broken Earth" {
		t.Errorf("name var = %v, want The Broken Earth", got)
	}
	if got := gotVars["after"]; got != float64(1) {
		t.Errorf("after var = %v, want 1", got)
	}
}

func TestNextInSeriesNoLaterBook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"book_series":[]}}`)
	}))
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL))
	_, found, err := c.NextInSeries(context.Background(), library.Series{Name: "Ended", Position: 9})
	if err != nil {
		t.Fatalf("NextInSeries: %v", err)
	}
	if found {
		t.Error("found = true, want false for a series with no later book")
	}
}

func TestNextInSeriesSkipsQueryWhenUnresolvable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("NextInSeries should not hit the API without a name and position")
	}))
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL))
	cases := []library.Series{
		{Position: 1},   // missing name
		{Name: "Known"}, // unknown position (0)
	}
	for _, s := range cases {
		if _, found, err := c.NextInSeries(context.Background(), s); err != nil || found {
			t.Errorf("NextInSeries(%+v) = (found %v, err %v), want (false, nil)", s, found, err)
		}
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
