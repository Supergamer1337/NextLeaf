package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nextleaf/internal/library"
)

// stubSource is a library.Source with canned results for handler tests.
type stubSource struct {
	reading   []library.Entry
	reads     []library.Entry
	toRead    []library.Entry
	readsErr  error
	toReadErr error
}

func (s stubSource) Name() string { return "stub" }
func (s stubSource) CurrentlyReading(_ context.Context) ([]library.Entry, error) {
	return s.reading, nil
}
func (s stubSource) RecentReads(_ context.Context, _ int) ([]library.Entry, error) {
	return s.reads, s.readsErr
}
func (s stubSource) ToRead(_ context.Context) ([]library.Entry, error) {
	return s.toRead, s.toReadErr
}

// resolverStub adds the optional SeriesResolver capability to stubSource.
type resolverStub struct {
	stubSource
	next  library.Book
	found bool
}

func (s resolverStub) NextInSeries(_ context.Context, _ library.Series) (library.Book, bool, error) {
	return s.next, s.found, nil
}

func seriesEntry(title, series string, pos float64) library.Entry {
	return library.Entry{Book: library.Book{Title: title, Series: &library.Series{Name: series, Position: pos}}}
}

func get(t *testing.T, src library.Source, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	NewHandler(src).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want %d", target, rec.Code, http.StatusOK)
	}
	return rec
}

func TestHealthcheck(t *testing.T) {
	rec := get(t, nil, "/healthcheck")
	if got := strings.TrimSpace(rec.Body.String()); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	NewHandler(nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (root pattern must not be a catch-all)", rec.Code, http.StatusNotFound)
	}
}

func TestSelectorUnconfigured(t *testing.T) {
	rec := get(t, nil, "/")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, "HARDCOVER_TOKEN") {
		t.Errorf("unconfigured page should mention HARDCOVER_TOKEN:\n%s", body)
	}
}

func TestSelectorSourceError(t *testing.T) {
	src := stubSource{toReadErr: errors.New("boom")}
	rec := get(t, src, "/")
	if body := rec.Body.String(); !strings.Contains(body, "boom") {
		t.Errorf("error page should surface the failure:\n%s", body)
	}
}

func TestSelectorEmptyList(t *testing.T) {
	rec := get(t, stubSource{}, "/")
	if body := rec.Body.String(); !strings.Contains(body, "Want-to-Read list is empty") {
		t.Errorf("empty page should invite adding books:\n%s", body)
	}
}

func TestSelectorContinuesSeriesFromShelf(t *testing.T) {
	src := stubSource{
		reads:  []library.Entry{seriesEntry("The Fifth Season", "The Broken Earth", 1)},
		toRead: []library.Entry{seriesEntry("The Obelisk Gate", "The Broken Earth", 2)},
	}
	body := get(t, src, "/").Body.String()
	for _, want := range []string{"The Obelisk Gate", "The Broken Earth", "Continues"} {
		if !strings.Contains(body, want) {
			t.Errorf("series continuation should mention %q:\n%s", want, body)
		}
	}
}

func TestSelectorResolvesSeriesOffShelf(t *testing.T) {
	// The next series book is on no shelf; the resolver supplies it.
	src := resolverStub{
		stubSource: stubSource{reads: []library.Entry{seriesEntry("The Fifth Season", "The Broken Earth", 1)}},
		next:       library.Book{Title: "The Obelisk Gate", Series: &library.Series{Name: "The Broken Earth", Position: 2}},
		found:      true,
	}
	body := get(t, src, "/").Body.String()
	if !strings.Contains(body, "The Obelisk Gate") {
		t.Errorf("off-shelf next book should be recommended:\n%s", body)
	}
}

func TestSelectorShowsFavourReasons(t *testing.T) {
	// A novel-genre TBR pick renders a capitalized "In favour" reason.
	src := stubSource{
		reads:  []library.Entry{{Book: library.Book{Title: "Recent", Genres: []string{"Fantasy"}}}},
		toRead: []library.Entry{{Book: library.Book{Title: "TBR Pick", Genres: []string{"History"}}}},
	}
	body := get(t, src, "/?another=1").Body.String()

	if !strings.Contains(body, "In favour") {
		t.Errorf("a pick with pros should show an 'In favour' section:\n%s", body)
	}
	if !strings.Contains(body, "Brings in History") {
		t.Errorf("pro should name the fresh genre:\n%s", body)
	}
	if strings.Contains(body, "Trade-offs") {
		t.Errorf("a pro-only pick should not show a Trade-offs section:\n%s", body)
	}
}

func TestSelectorShowsTradeOffs(t *testing.T) {
	// A dominant recent genre makes a same-genre pick carry a trade-off.
	fantasy := func(title string) library.Entry {
		return library.Entry{Book: library.Book{Title: title, Genres: []string{"Fantasy"}}}
	}
	src := stubSource{
		reads:  []library.Entry{fantasy("R1"), fantasy("R2"), fantasy("R3")},
		toRead: []library.Entry{fantasy("TBR Fantasy")},
	}
	body := get(t, src, "/?another=1").Body.String()

	if !strings.Contains(body, "Trade-offs") {
		t.Errorf("a pick with cons should show a 'Trade-offs' section:\n%s", body)
	}
	if !strings.Contains(body, "Leans into Fantasy") {
		t.Errorf("trade-off reason should be present:\n%s", body)
	}
}

func TestSelectorSkipsDislikedSeries(t *testing.T) {
	// The most recent series book was rated low, so we shouldn't push its sequel.
	src := stubSource{
		reads:  []library.Entry{{Book: library.Book{Title: "Meh 1", Series: &library.Series{Name: "Meh", Position: 1}}, Rating: 1}},
		toRead: []library.Entry{{Book: library.Book{Title: "Better Pick"}}, {Book: library.Book{Title: "Meh 2", Series: &library.Series{Name: "Meh", Position: 2}}}},
	}
	body := get(t, src, "/").Body.String()
	if strings.Contains(body, "Continues") {
		t.Errorf("a series rated below the gate should not be continued:\n%s", body)
	}
}

func TestSelectorSkipsUnknownSeriesPosition(t *testing.T) {
	// The anchor's series position is unknown (0), so "next" is undefined —
	// don't guess a continuation.
	src := stubSource{
		reads:  []library.Entry{{Book: library.Book{Title: "Anchor", Series: &library.Series{Name: "S"}}}},
		toRead: []library.Entry{{Book: library.Book{Title: "S Book Two", Series: &library.Series{Name: "S", Position: 2}}}},
	}
	body := get(t, src, "/").Body.String()
	if strings.Contains(body, "Continues") {
		t.Errorf("an unknown anchor position should not continue a series:\n%s", body)
	}
}

func TestSelectorRerollUsesVariety(t *testing.T) {
	// A series is active, but ?another=1 must skip it and pick from the TBR.
	src := stubSource{
		reads:  []library.Entry{seriesEntry("The Fifth Season", "The Broken Earth", 1)},
		toRead: []library.Entry{{Book: library.Book{Title: "A Standalone Pick"}}},
	}
	body := get(t, src, "/?another=1").Body.String()
	if !strings.Contains(body, "A Standalone Pick") {
		t.Errorf("reroll should pick from the TBR:\n%s", body)
	}
	if strings.Contains(body, "Continues") {
		t.Errorf("reroll should not offer a series continuation:\n%s", body)
	}
}
