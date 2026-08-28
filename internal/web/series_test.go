package web

import (
	"context"
	"errors"
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

// ready builds a fully configured handler over the given source and store.
func ready(t *testing.T, src library.Source, st *series.Store) http.Handler {
	t.Helper()
	engine := series.NewEngine(st, src, picker.Prefs{IncludeNovellas: true})
	return NewHandler(Deps{Source: src, Engine: engine})
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
	read.Status = library.StatusRead
	read.FinishedAt = time.Now().Add(-24 * time.Hour)
	return stubSource{
		reads:  []library.Entry{read},
		toRead: []library.Entry{seriesEntry("Book 4", "Mistborn", 4), {Book: library.Book{Title: "Standalone"}}},
	}
}

func TestSeriesContinuationIsOffered(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	if body := getBody(t, h, "/"); !strings.Contains(body, "Book 4") {
		t.Error("the next book in the tracked series was not recommended")
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
	read := seriesEntry("Book 3", "Mistborn", 3)
	read.Status = library.StatusRead
	src := stubSource{
		reads:  []library.Entry{read},
		toRead: []library.Entry{seriesEntry("Book 4", "Mistborn", 4)},
	}
	h := ready(t, src, testStore(t))

	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/drop: status = %d, want 303", rec.Code)
	}
	if body := getBody(t, h, "/"); strings.Contains(body, "Book 4") {
		t.Error("a dropped series' book still reached the variety pool")
	}
}

func TestPinnedSeriesOutranksAMoreRecentlyFinishedOne(t *testing.T) {
	older := seriesEntry("Mistborn 3", "Mistborn", 3)
	older.Status = library.StatusRead
	older.FinishedAt = time.Now().Add(-72 * time.Hour)
	newer := seriesEntry("Stormlight 1", "Stormlight", 1)
	newer.Status = library.StatusRead
	newer.FinishedAt = time.Now().Add(-1 * time.Hour)
	src := stubSource{
		reads: []library.Entry{newer, older},
		toRead: []library.Entry{
			seriesEntry("Mistborn 4", "Mistborn", 4),
			seriesEntry("Stormlight 2", "Stormlight", 2),
		},
	}
	h := ready(t, src, testStore(t))

	if body := getBody(t, h, "/"); !strings.Contains(body, "Stormlight 2") {
		t.Fatal("expected the most recently finished series to be continued")
	}
	if rec := post(t, h, "/series/pin", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusSeeOther {
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
	if !strings.Contains(body, "/series/clear") {
		t.Error("the panel offers no way to undo a standing decision")
	}
}

func TestCardOffersNoDecisionsForASeriesTheReaderHasNotStarted(t *testing.T) {
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

func TestACrossSiteDecisionPostIsRefused(t *testing.T) {
	st := testStore(t)
	h := ready(t, midSeries(), st)

	req := httptest.NewRequest(http.MethodPost, "/series/drop",
		strings.NewReader(url.Values{"name": {"Mistborn"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: status = %d, want 403", rec.Code)
	}
	sts, err := st.Statements(context.Background())
	if err != nil {
		t.Fatalf("Statements: %v", err)
	}
	if len(sts) != 0 {
		t.Error("a cross-site request recorded a statement")
	}
}

func TestASameOriginDecisionPostStillWorks(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	req := httptest.NewRequest(http.MethodPost, "/series/drop",
		strings.NewReader(url.Values{"name": {"Mistborn"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("same-origin POST: status = %d, want 303", rec.Code)
	}
}

// caughtUpSource has one series with a shelf continuation and one the
// catalogue says is finished.
func caughtUpSource() *resolverStub {
	mist := seriesEntry("Book 3", "Mistborn", 3)
	mist.Status = library.StatusRead
	mist.FinishedAt = time.Now().Add(-24 * time.Hour)
	storm := seriesEntry("Book 4", "Stormlight", 4)
	storm.Status = library.StatusRead
	storm.FinishedAt = time.Now().Add(-48 * time.Hour)
	return &resolverStub{
		stubSource: stubSource{
			reads:  []library.Entry{mist, storm},
			toRead: []library.Entry{seriesEntry("Book 4", "Mistborn", 4)},
		},
		found: false,
	}
}

func TestDrawerFilesACaughtUpSeriesUnderFinished(t *testing.T) {
	h := ready(t, caughtUpSource(), testStore(t))
	body := getBody(t, h, "/")

	finished := section(body, "Finished")
	if !strings.Contains(finished, "Stormlight") {
		t.Errorf("Stormlight is not in the Finished drawer:\n%s", finished)
	}
	if strings.Contains(section(body, "Current"), "Stormlight") {
		t.Error("a caught-up series is still listed as active")
	}
}

func TestOnlyTheCurrentGroupStartsOpen(t *testing.T) {
	h := ready(t, caughtUpSource(), testStore(t))
	body := getBody(t, h, "/")

	block, ok := enclosingDetails(body, ">Finished")
	if !ok {
		t.Fatal("the Finished group is not collapsible")
	}
	if openTag, _, _ := strings.Cut(block, ">"); strings.Contains(openTag, "open") {
		t.Errorf("the Finished group starts open: %q", openTag)
	}
	block, ok = enclosingDetails(body, ">Current")
	if !ok {
		t.Fatal("the Current group is not collapsible")
	}
	if openTag, _, _ := strings.Cut(block, ">"); !strings.Contains(openTag, "open") {
		t.Errorf("the Current group does not start open: %q", openTag)
	}
}

func TestTheParkedGroupStartsCollapsed(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	if rec := post(t, h, "/series/park", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/park: status = %d, want 303", rec.Code)
	}
	body := getBody(t, h, "/")

	block, ok := enclosingDetails(body, ">Parked")
	if !ok {
		t.Fatal("the Parked group is not collapsible")
	}
	if openTag, _, _ := strings.Cut(block, ">"); strings.Contains(openTag, "open") {
		t.Errorf("the Parked group starts open: %q", openTag)
	}
}

func TestFinishedSitsAboveDropped(t *testing.T) {
	h := ready(t, caughtUpSource(), testStore(t))
	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/drop: status = %d, want 303", rec.Code)
	}
	body := getBody(t, h, "/")

	// Finished is still your history; dropped is what you rejected, and the
	// rejected pile belongs at the very bottom.
	fin, dro := strings.Index(body, ">Finished"), strings.Index(body, ">Dropped")
	if fin < 0 || dro < 0 || fin > dro {
		t.Errorf("group order: Finished at %d, Dropped at %d; want Finished above", fin, dro)
	}
}

func TestDrawerRowsOfferPark(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	body := getBody(t, h, "/")

	current := section(body, "Current")
	if !strings.Contains(current, ">Park<") {
		t.Error("a current series' row offers no park")
	}
}

func TestAFoldedRowOffersNoDoNothingSwitch(t *testing.T) {
	// Both backends file the book under the same name; switching between
	// their identical claims changes nothing the reader can see.
	hc := seriesEntry("Vol 8", "Overlord", 8)
	hc.Status = library.StatusRead
	hc.FinishedAt = time.Now().Add(-24 * time.Hour)
	hc.Book.Series.Source = "hardcover"
	gm := seriesEntry("Vol 8", "Overlord", 8)
	gm.Status = library.StatusRead
	gm.FinishedAt = hc.FinishedAt
	gm.Book.Series.Source = "grimmory"
	h := ready(t, stubSource{reads: []library.Entry{hc, gm}}, testStore(t))

	// The CSS class definition is always in the stylesheet; what must be
	// absent is the control itself.
	if body := getBody(t, h, "/"); strings.Contains(body, `class="switcher-btn"`) {
		t.Error("a switch control is offered with nothing meaningful to switch to")
	}
}

func TestAFinishedSeriesShowsTheCoverOfTheLastBookRead(t *testing.T) {
	src := caughtUpSource()
	src.reads[1].Book.CoverURL = "https://covers.example/stormlight4.jpg"
	h := ready(t, src, testStore(t))

	if body := getBody(t, h, "/"); !strings.Contains(body, "stormlight4.jpg") {
		t.Error("a finished series shows no cover at all")
	}
}

func TestStaleSourcesAreCalledOutOnThePage(t *testing.T) {
	flaky := &breakableSource{stubSource: midSeries()}
	cached := library.NewCached(flaky, time.Nanosecond)
	h := ready(t, cached, testStore(t))

	getBody(t, h, "/") // prime the cache
	flaky.broken = true
	time.Sleep(2 * time.Nanosecond)
	body := getBody(t, h, "/")
	if !strings.Contains(body, "couldn’t be reached") {
		t.Error("no notice that the source data may be out of date")
	}
	// The page still works on the stale data.
	if !strings.Contains(body, "Book 4") {
		t.Error("the stale data was not used for a recommendation")
	}
}

// switchSource files one read book under two orderings.
func switchSource() stubSource {
	book := library.Entry{Book: library.Book{
		Title:  "Leviathan Wakes",
		Series: &library.Series{Name: "The Expanse (Chronological)", Position: library.At(2), Source: "hardcover"},
		OtherSeries: []library.Series{
			{Name: "The Expanse", Position: library.At(1), Source: "hardcover", Description: "Published order."},
		},
	}, Status: library.StatusRead}
	book.FinishedAt = time.Now().Add(-24 * time.Hour)
	return stubSource{reads: []library.Entry{book}}
}

func TestTheSwitcherSpinsInPlace(t *testing.T) {
	h := ready(t, switchSource(), testStore(t))
	body := getBody(t, h, "/")

	// The row itself becomes the wheel: an icon-only control, candidate data
	// for the script to cycle through in place, and one form to submit the
	// choice. No expanding menu, no modal.
	if !strings.Contains(body, `class="switcher-btn"`) {
		t.Fatal("no switch control on a row with alternatives")
	}
	if strings.Contains(body, ">Switch series<") {
		t.Error("the switcher still carries permanent text")
	}
	if strings.Contains(body, `<details class="switcher"`) || strings.Contains(body, "switch-modal") {
		t.Error("old switcher markup survived")
	}

	// The wheel's candidates: the current identity first, then each
	// alternative with what distinguishes it.
	i := strings.Index(body, `class="wheel-candidate"`)
	if i < 0 {
		t.Fatal("no wheel candidates for the script to cycle")
	}
	if !strings.Contains(body, `data-name="The Expanse (Chronological)"`) {
		t.Error("the current identity is not a candidate")
	}
	if !strings.Contains(body, `data-to="The Expanse"`) {
		t.Error("the alternative is not a candidate")
	}
	if !strings.Contains(body, "Published order.") {
		t.Error("the candidate does not carry its description")
	}
	if !strings.Contains(body, `action="/series/switch"`) {
		t.Error("no form to submit the chosen candidate")
	}
	// The selection moves between ghost cards above and below the lifted row.
	if !strings.Contains(body, `class="wheel-ghost wheel-prev"`) || !strings.Contains(body, `class="wheel-ghost wheel-next"`) {
		t.Error("the wheel has no ghost cards to move between")
	}
}

func TestSwitchingFromTheDrawerChangesWhichSeriesIsTracked(t *testing.T) {
	h := ready(t, switchSource(), testStore(t))

	form := url.Values{"name": {"The Expanse (Chronological)"}, "to": {"The Expanse"}}
	if rec := post(t, h, "/series/switch", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/switch: status = %d, want 303", rec.Code)
	}
	body := getBody(t, h, "/")
	if !strings.Contains(body, `drawer-name">The Expanse<`) {
		t.Errorf("the drawer does not track the preferred identity:\n%s", section(body, "Current"))
	}
}

func TestSwitchingToASeriesThatIsNotAnAlternativeIsRefused(t *testing.T) {
	h := ready(t, switchSource(), testStore(t))
	form := url.Values{"name": {"The Expanse (Chronological)"}, "to": {"Some Other Saga"}}
	if rec := post(t, h, "/series/switch", form); rec.Code != http.StatusBadRequest {
		t.Errorf("POST /series/switch to a stranger: status = %d, want 400", rec.Code)
	}
}

func TestPositionsRenderAsNumbersNotPointers(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	body := getBody(t, h, "/")
	if strings.Contains(body, "0xc0") {
		t.Error("a position rendered as a pointer address")
	}
}

func TestAnUnplacedSeriesShowsNoPositionRatherThanZero(t *testing.T) {
	hobbit := library.Entry{Book: library.Book{
		Title:  "The Hobbit",
		Series: &library.Series{Name: "The Lord of the Rings"},
	}, Status: library.StatusRead}
	hobbit.FinishedAt = time.Now()
	h := ready(t, stubSource{reads: []library.Entry{hobbit}}, testStore(t))

	if body := getBody(t, h, "/"); strings.Contains(body, "Read to book 0") {
		t.Error("an unplaced series claims to be read to book 0")
	}
}

// breakableSource errors on demand, for staleness tests.
type breakableSource struct {
	stubSource
	broken bool
}

func (s *breakableSource) Name() string { return "grimmory" }
func (s *breakableSource) CurrentlyReading(ctx context.Context) ([]library.Entry, error) {
	if s.broken {
		return nil, errTestDown
	}
	return s.stubSource.CurrentlyReading(ctx)
}
func (s *breakableSource) RecentReads(ctx context.Context, limit int) ([]library.Entry, error) {
	if s.broken {
		return nil, errTestDown
	}
	return s.stubSource.RecentReads(ctx, limit)
}
func (s *breakableSource) ToRead(ctx context.Context) ([]library.Entry, error) {
	if s.broken {
		return nil, errTestDown
	}
	return s.stubSource.ToRead(ctx)
}

var errTestDown = errors.New("dial tcp: connection refused")

// section returns the drawer group whose label is name.
func section(body, name string) string {
	marker := ">" + name
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i:]
	if end := strings.Index(rest, "drawer-group"); end > 0 {
		return rest[:end]
	}
	return rest
}

// enclosingDetails returns the <details> element that contains marker.
func enclosingDetails(body, marker string) (string, bool) {
	i := strings.Index(body, marker)
	if i < 0 {
		return "", false
	}
	start := strings.LastIndex(body[:i], `<details class="drawer-group"`)
	if start < 0 {
		return "", false
	}
	end := strings.Index(body[start:], "</details>")
	if end < 0 {
		return "", false
	}
	return body[start : start+end], true
}
