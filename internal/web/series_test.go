package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nextleaf/internal/library"
	"nextleaf/internal/picker"
	"nextleaf/internal/series"
)

func testStore(t *testing.T) *series.Store {
	t.Helper()
	st, err := series.Open(filepath.Join(t.TempDir(), "nextleaf.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ready builds a handler whose history import has already finished.
func ready(t *testing.T, src library.Source, st *series.Store) http.Handler {
	t.Helper()
	return NewHandler(Deps{
		Source:   src,
		Store:    st,
		Backfill: series.NewBackfill(st, nil),
		Prefs:    picker.Prefs{IncludeNovellas: true},
	})
}

func getBody(t *testing.T, h http.Handler, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", target, rec.Code)
	}
	return rec.Body.String()
}

func post(t *testing.T, h http.Handler, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// midSeries is a reader three books into Mistborn, with book 4 on the shelf.
func midSeries() stubSource {
	read := seriesEntry("Book 3", "Mistborn", 3)
	read.FinishedAt = time.Now().Add(-24 * time.Hour)
	return stubSource{
		reads:  []library.Entry{read},
		toRead: []library.Entry{seriesEntry("Book 4", "Mistborn", 4), {Book: library.Book{Title: "Standalone"}}},
	}
}

func TestSeriesContinuationIsOfferedOnceHistoryIsImported(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))

	if body := getBody(t, h, "/"); !strings.Contains(body, "Book 4") {
		t.Error("the next book in the tracked series was not recommended")
	}
}

func TestContinuationsWaitForTheHistoryImport(t *testing.T) {
	st := testStore(t)
	// A provider that has not been run yet leaves the backfill unfinished.
	pending := series.NewBackfill(st, []library.HistoryProvider{historyStub{}})
	h := NewHandler(Deps{Source: midSeries(), Store: st, Backfill: pending, Prefs: picker.Prefs{IncludeNovellas: true}})

	body := getBody(t, h, "/")
	if !strings.Contains(body, "Series tracking is still importing") {
		t.Error("no banner explaining why series tracking is unavailable")
	}
	// The app stays useful: a variety pick is still served.
	if !strings.Contains(body, "Standalone") && !strings.Contains(body, "Book 4") {
		t.Error("no recommendation at all was served during the import")
	}
}

func TestParkingASeriesStopsItBeingContinued(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))

	rec := post(t, h, "/series/park", url.Values{"name": {"Mistborn"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/park: status = %d, want 303", rec.Code)
	}

	if body := getBody(t, h, "/"); strings.Contains(body, "Continues Mistborn") {
		t.Error("a parked series was still offered as a continuation")
	}
}

func TestDroppingASeriesAlsoWithholdsItsBooksFromVarietyPicks(t *testing.T) {
	src := stubSource{
		reads:  []library.Entry{seriesEntry("Book 3", "Mistborn", 3)},
		toRead: []library.Entry{seriesEntry("Book 4", "Mistborn", 4)},
	}
	h := ready(t, src, testStore(t))

	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/drop: status = %d, want 303", rec.Code)
	}

	// The only book left belongs to the dropped series, so there is nothing
	// to recommend at all.
	if body := getBody(t, h, "/"); strings.Contains(body, "Book 4") {
		t.Error("a dropped series' book still reached the variety pool")
	}
}

func TestPinnedSeriesOutranksAMoreRecentlyFinishedOne(t *testing.T) {
	older := seriesEntry("Mistborn 3", "Mistborn", 3)
	older.FinishedAt = time.Now().Add(-72 * time.Hour)
	newer := seriesEntry("Stormlight 1", "Stormlight", 1)
	newer.FinishedAt = time.Now().Add(-1 * time.Hour)
	src := stubSource{
		reads: []library.Entry{newer, older},
		toRead: []library.Entry{
			seriesEntry("Mistborn 4", "Mistborn", 4),
			seriesEntry("Stormlight 2", "Stormlight", 2),
		},
	}
	h := ready(t, src, testStore(t))

	// Without a pin, the more recent finish wins.
	if body := getBody(t, h, "/"); !strings.Contains(body, "Stormlight 2") {
		t.Fatal("expected the most recently finished series to be continued")
	}

	form := url.Values{"name": {"Mistborn"}, "position": {"4"}}
	if rec := post(t, h, "/series/pin", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/pin: status = %d, want 303", rec.Code)
	}
	if body := getBody(t, h, "/"); !strings.Contains(body, "Mistborn 4") {
		t.Error("the pinned series was not continued first")
	}
}

func TestPanelListsTrackedSeriesAndOffersUndo(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))

	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/drop: status = %d, want 303", rec.Code)
	}

	body := getBody(t, h, "/")
	if !strings.Contains(body, "Mistborn") {
		t.Error("the panel does not list the tracked series")
	}
	// Undoing a pin or a drop has nowhere else to live.
	if !strings.Contains(body, "/series/clear") {
		t.Error("the panel offers no way to undo a standing decision")
	}
}

func TestCardOffersNoDecisionsForASeriesTheReaderHasNotStarted(t *testing.T) {
	// Book 1 of an untouched series is a variety pick, not a tracked series;
	// removing it from the reading list is how you say no to it.
	src := stubSource{toRead: []library.Entry{seriesEntry("Book 1", "Unstarted", 1)}}
	h := ready(t, src, testStore(t))

	if body := getBody(t, h, "/"); strings.Contains(body, "/series/park") {
		t.Error("decision buttons were offered for a series the reader has never read")
	}
}

func TestUnknownSeriesActionIsRejected(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))

	if rec := post(t, h, "/series/incinerate", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusNotFound {
		t.Errorf("POST /series/incinerate: status = %d, want 404", rec.Code)
	}
}

func TestSeriesActionWithoutANameIsRejected(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))

	if rec := post(t, h, "/series/park", url.Values{}); rec.Code != http.StatusBadRequest {
		t.Errorf("POST /series/park without a name: status = %d, want 400", rec.Code)
	}
}

// historyStub is a HistoryProvider used only to leave a backfill unfinished.
type historyStub struct{}

func (historyStub) Name() string { return "stub" }
func (historyStub) ReadHistory(_ context.Context) ([]library.Entry, error) {
	return nil, nil
}
