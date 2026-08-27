package hardcover

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
      "title": "Secret History", "editions": [{"id": 1}],
      "slug": "secret-history",
      "release_year": 2016,
      "release_date": "2016-01-26",
      "book_series": [{"position": 3.5, "featured": true, "series": {"name": "Mistborn", "slug": "mistborn", "is_completed": false}}]
    }
  },
  {
    "position": 4,
    "book": {
      "title": "The Alloy of Law", "editions": [{"id": 1}],
      "slug": "the-alloy-of-law",
      "release_year": 2011,
      "release_date": "2011-11-08",
      "book_series": [{"position": 4, "featured": true, "series": {"name": "Mistborn", "slug": "mistborn", "is_completed": false}}]
    }
  },
  {
    "position": 5,
    "book": {
      "title": "The Lost Metal", "editions": [{"id": 1}],
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
      "title": "The Lost Metal", "editions": [{"id": 1}],
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

// splitEditions is what Hardcover really returns for a reader who has finished
// book 1 of The Wheel of Time: the two halves of that same book at .1 and .2,
// then the actual next novel, then a novella.
const splitEditions = `{"data":{"book_series":[
  {"position": 1.1, "book": {"title": "The Eye of the World, Part 1 of 2", "editions": [{"id": 1}], "release_year": 1990,
    "book_series": [{"position": 1.1, "featured": true, "series": {"name": "The Wheel of Time"}}]}},
  {"position": 1.2, "book": {"title": "The Eye of the World, Part 2 of 2", "editions": [{"id": 1}], "release_year": 1990,
    "book_series": [{"position": 1.2, "featured": true, "series": {"name": "The Wheel of Time"}}]}},
  {"position": 1.5, "book": {"title": "A Proper Novella", "editions": [{"id": 1}], "editions": [{"id": 1}], "release_year": 1998,
    "book_series": [{"position": 1.5, "featured": true, "series": {"name": "The Wheel of Time"}}]}},
  {"position": 2, "book": {"title": "The Great Hunt", "editions": [{"id": 1}], "editions": [{"id": 1}], "release_year": 1990,
    "book_series": [{"position": 2, "featured": true, "series": {"name": "The Wheel of Time"}}]}}
]}}`

func TestNextInSeriesSkipsHalvesOfABookAlreadyRead(t *testing.T) {
	srv := candidateServer(t, splitEditions)
	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))

	// Book 1 split into parts is not a next read, whatever the novella
	// preference says; only a genuine half-position volume is.
	q := library.SeriesQuery{Series: library.Series{Name: "The Wheel of Time", Position: 1}, IncludeNovellas: true}
	entry, found, err := c.NextInSeries(context.Background(), q)
	if err != nil || !found {
		t.Fatalf("NextInSeries = (_, %v, %v), want found", found, err)
	}
	if entry.Book.Title != "A Proper Novella" {
		t.Errorf("Title = %q, want the novella at 1.5, not a split edition", entry.Book.Title)
	}
}

func TestNextInSeriesSkipsSplitEditionsAndNovellasTogether(t *testing.T) {
	srv := candidateServer(t, splitEditions)
	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))

	q := library.SeriesQuery{Series: library.Series{Name: "The Wheel of Time", Position: 1}, IncludeNovellas: false}
	entry, found, err := c.NextInSeries(context.Background(), q)
	if err != nil || !found {
		t.Fatalf("NextInSeries = (_, %v, %v), want found", found, err)
	}
	if entry.Book.Title != "The Great Hunt" {
		t.Errorf("Title = %q, want the next novel", entry.Book.Title)
	}
}

func TestNextInSeriesAsksOnlyForTheCanonicalBookAtEachPosition(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)
		gotQuery = req.Query
		_, _ = io.WriteString(w, splitEditions)
	}))
	defer srv.Close()

	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))
	q := library.SeriesQuery{Series: library.Series{Name: "The Wheel of Time", Position: 1}}
	if _, _, err := c.NextInSeries(context.Background(), q); err != nil {
		t.Fatalf("NextInSeries: %v", err)
	}

	// Translations share a position with the original and are not featured;
	// without this the reader gets offered a book in a language they can't read.
	if !strings.Contains(gotQuery, "featured: {_eq: true}") {
		t.Errorf("query does not restrict to the featured book at each position:\n%s", gotQuery)
	}
}

// translations is what Hardcover really returns for one position of a popular
// series: the same book in six languages, plus an omnibus.
const translations = `{"data":{"book_series":[
  {"position": 3, "book": {"title": "Furtuna de Onix", "release_year": 2025,
    "book_series": [{"position": 3, "featured": true, "series": {"name": "The Empyrean"}}]}},
  {"position": 3, "book": {"title": "Myrskynsilma", "release_year": 2025,
    "book_series": [{"position": 3, "featured": true, "series": {"name": "The Empyrean"}}]}},
  {"position": 3, "book": {"title": "The Empyrean Bundle", "editions": [{"id": 1}], "release_year": 2025, "compilation": true,
    "default_physical_edition": {"language": {"code3": "eng"}},
    "book_series": [{"position": 3, "featured": true, "series": {"name": "The Empyrean"}}]}},
  {"position": 3, "book": {"title": "Onyx Storm", "editions": [{"id": 1}], "editions": [{"id": 1}], "release_year": 2025,
    "default_physical_edition": {"language": {"code3": "eng"}},
    "book_series": [{"position": 3, "featured": true, "series": {"name": "The Empyrean"}}]}}
]}}`

func TestNextInSeriesPrefersTheEnglishEditionOverTranslations(t *testing.T) {
	srv := candidateServer(t, translations)
	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))

	// Every translation shares the position and is flagged featured, so only
	// the edition's language tells them apart.
	q := library.SeriesQuery{Series: library.Series{Name: "The Empyrean", Position: 2}, IncludeNovellas: true}
	entry, found, err := c.NextInSeries(context.Background(), q)
	if err != nil || !found {
		t.Fatalf("NextInSeries = (_, %v, %v), want found", found, err)
	}
	if entry.Book.Title != "Onyx Storm" {
		t.Errorf("Title = %q, want Onyx Storm rather than a translation or a bundle", entry.Book.Title)
	}
}

// omnibusOnly offers a compilation and a single novel at the same position.
const omnibusOnly = `{"data":{"book_series":[
  {"position": 2, "book": {"title": "Dune Messiah & Children of Dune", "editions": [{"id": 1}], "release_year": 1969, "compilation": true,
    "default_physical_edition": {"language": {"code3": "eng"}},
    "book_series": [{"position": 2, "featured": true, "series": {"name": "Dune"}}]}},
  {"position": 2, "book": {"title": "Dune Messiah", "editions": [{"id": 1}], "editions": [{"id": 1}], "release_year": 1969,
    "default_physical_edition": {"language": {"code3": "eng"}},
    "book_series": [{"position": 2, "featured": true, "series": {"name": "Dune"}}]}}
]}}`

func TestNextInSeriesPrefersASingleNovelOverAnOmnibus(t *testing.T) {
	srv := candidateServer(t, omnibusOnly)
	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))

	q := library.SeriesQuery{Series: library.Series{Name: "Dune", Position: 1}, IncludeNovellas: true}
	entry, found, err := c.NextInSeries(context.Background(), q)
	if err != nil || !found {
		t.Fatalf("NextInSeries = (_, %v, %v), want found", found, err)
	}
	if entry.Book.Title != "Dune Messiah" {
		t.Errorf("Title = %q, want the single novel, not the bundle", entry.Book.Title)
	}
}

// noEnglishEdition mirrors Hardcover's "Middle Earth" series, where position 2
// holds only translations and an audiobook: the English novel is filed under a
// different series entirely, so there is no right answer at that position.
const noEnglishEdition = `{"data":{"book_series":[
  {"position": 2, "book": {"title": "Две крепости", "release_year": 1954,
    "book_series": [{"position": 2, "featured": true, "series": {"name": "Middle Earth"}}]}},
  {"position": 2, "book": {"title": "Les deux tours", "release_year": 1954,
    "book_series": [{"position": 2, "featured": true, "series": {"name": "Middle Earth"}}]}},
  {"position": 3, "book": {"title": "The Return of the King", "release_year": 1955,
    "editions": [{"id": 9}],
    "book_series": [{"position": 3, "featured": true, "series": {"name": "Middle Earth"}}]}}
]}}`

func TestNextInSeriesPassesOverAPositionWithNoEditionItCanRead(t *testing.T) {
	srv := candidateServer(t, noEnglishEdition)
	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))

	q := library.SeriesQuery{Series: library.Series{Name: "Middle Earth", Position: 1}, IncludeNovellas: true}
	entry, found, err := c.NextInSeries(context.Background(), q)
	if err != nil || !found {
		t.Fatalf("NextInSeries = (_, %v, %v), want found", found, err)
	}
	if entry.Book.Title != "The Return of the King" {
		t.Errorf("Title = %q, want the next position that has a readable edition", entry.Book.Title)
	}
}

func TestNextInSeriesOffersNothingWhenNoPositionHasAReadableEdition(t *testing.T) {
	const translationsOnly = `{"data":{"book_series":[
	  {"position": 2, "book": {"title": "Две крепости", "release_year": 1954,
	    "book_series": [{"position": 2, "featured": true, "series": {"name": "Middle Earth"}}]}}
	]}}`
	srv := candidateServer(t, translationsOnly)
	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))

	q := library.SeriesQuery{Series: library.Series{Name: "Middle Earth", Position: 1}, IncludeNovellas: true}
	_, found, err := c.NextInSeries(context.Background(), q)
	if err != nil {
		t.Fatalf("NextInSeries: %v", err)
	}
	if found {
		t.Error("found = true; offering a book the reader cannot read is worse than offering none")
	}
}

func TestNextInSeriesSkipsAnUnnamedAnnouncement(t *testing.T) {
	const untitled = `{"data":{"book_series":[
	  {"position": 4, "book": {"title": "", "editions": [{"id": 1}],
	    "book_series": [{"position": 4, "featured": true, "series": {"name": "The Empyrean"}}]}},
	  {"position": 5, "book": {"title": "A Real Book", "release_year": 2020, "editions": [{"id": 2}],
	    "book_series": [{"position": 5, "featured": true, "series": {"name": "The Empyrean"}}]}}
	]}}`
	srv := candidateServer(t, untitled)
	c := New("tok", WithEndpoint(srv.URL), withClock(on2026))

	q := library.SeriesQuery{Series: library.Series{Name: "The Empyrean", Position: 3}, IncludeNovellas: true}
	entry, found, err := c.NextInSeries(context.Background(), q)
	if err != nil || !found {
		t.Fatalf("NextInSeries = (_, %v, %v), want found", found, err)
	}
	if entry.Book.Title != "A Real Book" {
		t.Errorf("Title = %q, want the announcement skipped", entry.Book.Title)
	}
}
