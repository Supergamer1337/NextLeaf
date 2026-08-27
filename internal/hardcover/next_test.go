package hardcover

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nextleaf/internal/library"
)

// seriesCandidates holds book 3.5 (a released novella), book 4 (released) and
// book 5 (announced but not out yet).
const seriesCandidates = `{"data":{"book_series":[
  {
    "position": 3.5,
    "book": {
      "title": "Secret History",
      "slug": "secret-history",
      "release_year": 2016,
      "release_date": "2016-01-26",
      "book_series": [{"position": 3.5, "featured": true, "series": {"name": "Mistborn", "slug": "mistborn", "is_completed": false}}]
    }
  },
  {
    "position": 4,
    "book": {
      "title": "The Alloy of Law",
      "slug": "the-alloy-of-law",
      "release_year": 2011,
      "release_date": "2011-11-08",
      "book_series": [{"position": 4, "featured": true, "series": {"name": "Mistborn", "slug": "mistborn", "is_completed": false}}]
    }
  },
  {
    "position": 5,
    "book": {
      "title": "The Lost Metal",
      "slug": "the-lost-metal",
      "release_year": 2099,
      "release_date": "2099-11-15",
      "book_series": [{"position": 5, "featured": true, "series": {"name": "Mistborn", "slug": "mistborn", "is_completed": false}}]
    }
  }
]}}`

// candidateServer replies with body to every request.
func candidateServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// unreleasedOnly is what the API returns past book 4: just the announced book 5.
const unreleasedOnly = `{"data":{"book_series":[
  {
    "position": 5,
    "book": {
      "title": "The Lost Metal",
      "slug": "the-lost-metal",
      "release_year": 2099,
      "release_date": "2099-11-15",
      "book_series": [{"position": 5, "featured": true, "series": {"name": "Mistborn", "slug": "mistborn", "is_completed": false}}]
    }
  }
]}}`

// on2026 is a clock fixed after book 4's release but well before book 5's.
func on2026() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }

func TestNextInSeriesOffersANovellaWhenTheyAreIncluded(t *testing.T) {
	srv := candidateServer(t, seriesCandidates)
	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))

	q := library.SeriesQuery{Series: library.Series{Name: "Mistborn", Position: 3}, IncludeNovellas: true}
	entry, found, err := c.NextInSeries(context.Background(), q)
	if err != nil || !found {
		t.Fatalf("NextInSeries = (_, %v, %v), want found", found, err)
	}
	if entry.Book.Title != "Secret History" {
		t.Errorf("Title = %q, want the novella at 3.5", entry.Book.Title)
	}
}

func TestNextInSeriesSkipsANovellaWhenTheyAreExcluded(t *testing.T) {
	srv := candidateServer(t, seriesCandidates)
	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))

	q := library.SeriesQuery{Series: library.Series{Name: "Mistborn", Position: 3}, IncludeNovellas: false}
	entry, found, err := c.NextInSeries(context.Background(), q)
	if err != nil || !found {
		t.Fatalf("NextInSeries = (_, %v, %v), want found", found, err)
	}
	if entry.Book.Title != "The Alloy of Law" {
		t.Errorf("Title = %q, want the next whole-numbered book", entry.Book.Title)
	}
}

func TestNextInSeriesWithholdsABookThatIsNotOutYet(t *testing.T) {
	srv := candidateServer(t, unreleasedOnly)
	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))

	// Past book 4, only the unreleased book 5 remains: recommending it would
	// send the reader after a book they cannot buy.
	q := library.SeriesQuery{Series: library.Series{Name: "Mistborn", Position: 4}, IncludeNovellas: true}
	_, found, err := c.NextInSeries(context.Background(), q)
	if err != nil {
		t.Fatalf("NextInSeries: %v", err)
	}
	if found {
		t.Error("found = true, want false: the only later book is unreleased")
	}
}

func TestNextInSeriesCarriesSeriesIdentityAndReleaseDate(t *testing.T) {
	srv := candidateServer(t, seriesCandidates)
	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))

	q := library.SeriesQuery{Series: library.Series{Name: "Mistborn", Position: 3}, IncludeNovellas: true}
	entry, found, err := c.NextInSeries(context.Background(), q)
	if err != nil || !found {
		t.Fatalf("NextInSeries = (_, %v, %v), want found", found, err)
	}
	if got := entry.Book.Series.Slug; got != "mistborn" {
		t.Errorf("Series.Slug = %q, want mistborn", got)
	}
	if entry.Book.Series.Completed {
		t.Error("Series.Completed = true, want false")
	}
	if got := entry.Book.ReleaseDate.Format("2006-01-02"); got != "2016-01-26" {
		t.Errorf("ReleaseDate = %q, want 2016-01-26", got)
	}
}

func TestReadHistoryReturnsEveryFinishedBook(t *testing.T) {
	api := &fakeAPI{}
	srv := api.server(t)
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL))
	history, err := c.ReadHistory(context.Background())
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("got %d entries, want 1", len(history))
	}
	// An unlimited query is what makes it a history rather than a window.
	if _, ok := api.lastVars["limit"]; ok {
		t.Error("ReadHistory sent a limit; it must ask for the whole history")
	}
}
