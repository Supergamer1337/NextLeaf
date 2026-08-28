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
	if body := getBody(t, h, "/view"); !strings.Contains(body, "Book 4") {
		t.Error("the next book in the tracked series was not recommended")
	}
}

func TestParkingASeriesStopsItBeingContinued(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))

	rec := post(t, h, "/series/park", url.Values{"name": {"Mistborn"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /series/park: status = %d, want 200", rec.Code)
	}
	if body := getBody(t, h, "/view"); strings.Contains(body, "Continues Mistborn") {
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

	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("POST /series/drop: status = %d, want 200", rec.Code)
	}
	if body := getBody(t, h, "/view"); strings.Contains(body, "Book 4") {
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

	if body := getBody(t, h, "/view"); !strings.Contains(body, "Stormlight 2") {
		t.Fatal("expected the most recently finished series to be continued")
	}
	if rec := post(t, h, "/series/pin", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("POST /series/pin: status = %d, want 200", rec.Code)
	}
	if body := getBody(t, h, "/view"); !strings.Contains(body, "Mistborn 4") {
		t.Error("the pinned series was not continued first")
	}
}

func TestPanelListsTrackedSeriesAndOffersUndo(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))

	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("POST /series/drop: status = %d, want 200", rec.Code)
	}
	body := getBody(t, h, "/view")
	if !strings.Contains(body, "Mistborn") {
		t.Error("the panel does not list the tracked series")
	}
	if !strings.Contains(body, "/series/clear") {
		t.Error("the panel offers no way to undo a standing decision")
	}
}

// Pinning from the card re-renders the very same card, so the button read as
// doing nothing at all. Pinning lives in the drawer, where filing a series
// under "Reading next" is a change the reader can see.
func TestTheCardOffersParkAndDropButNoPin(t *testing.T) {
	body := getBody(t, ready(t, midSeries(), testStore(t)), "/view")
	deck, _, _ := strings.Cut(body, `id="drawer-toggle"`)
	if strings.Contains(deck, `hx-post="/series/pin"`) {
		t.Error("the card still offers a pin whose effect it cannot show")
	}
	if !strings.Contains(deck, `hx-post="/series/park"`) || !strings.Contains(deck, `hx-post="/series/drop"`) {
		t.Error("park and drop belong on the card")
	}
}

func TestCardOffersNoDecisionsForASeriesTheReaderHasNotStarted(t *testing.T) {
	src := stubSource{toRead: []library.Entry{seriesEntry("Book 1", "Unstarted", 1)}}
	h := ready(t, src, testStore(t))

	if body := getBody(t, h, "/view"); strings.Contains(body, "/series/park") {
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
	if rec.Code != http.StatusOK {
		t.Errorf("same-origin POST: status = %d, want 200", rec.Code)
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
	body := getBody(t, h, "/view")

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
	body := getBody(t, h, "/view")

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
	if rec := post(t, h, "/series/park", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("POST /series/park: status = %d, want 200", rec.Code)
	}
	body := getBody(t, h, "/view")

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
	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("POST /series/drop: status = %d, want 200", rec.Code)
	}
	body := getBody(t, h, "/view")

	// Finished is still your history; dropped is what you rejected, and the
	// rejected pile belongs at the very bottom.
	fin, dro := strings.Index(body, ">Finished"), strings.Index(body, ">Dropped")
	if fin < 0 || dro < 0 || fin > dro {
		t.Errorf("group order: Finished at %d, Dropped at %d; want Finished above", fin, dro)
	}
}

func TestDrawerRowsOfferPark(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	body := getBody(t, h, "/view")

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
	if body := getBody(t, h, "/view"); strings.Contains(body, `class="switcher-btn"`) {
		t.Error("a switch control is offered with nothing meaningful to switch to")
	}
}

func TestAFinishedSeriesShowsTheCoverOfTheLastBookRead(t *testing.T) {
	src := caughtUpSource()
	src.reads[1].Book.CoverURL = "https://covers.example/stormlight4.jpg"
	h := ready(t, src, testStore(t))

	if body := getBody(t, h, "/view"); !strings.Contains(body, "stormlight4.jpg") {
		t.Error("a finished series shows no cover at all")
	}
}

func TestStaleSourcesAreCalledOutOnThePage(t *testing.T) {
	flaky := &breakableSource{stubSource: midSeries()}
	cached := library.NewCached(flaky, time.Nanosecond)
	h := ready(t, cached, testStore(t))

	getBody(t, h, "/view") // prime the cache
	flaky.broken = true
	time.Sleep(2 * time.Nanosecond)
	body := getBody(t, h, "/view")
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
	body := getBody(t, h, "/view")

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

	// The wheel's candidates are rendered whole by the server — the current
	// identity first, then each alternative — so the script only chooses
	// which one shows, never writes their content.
	if n := strings.Count(body, `class="wheel-item"`); n != 2 {
		t.Errorf("%d wheel items, want both candidates rendered whole", n)
	}
	if !strings.Contains(body, "Currently tracked.") {
		t.Error("the current identity is not a rendered candidate")
	}
	if !strings.Contains(body, `data-to="The Expanse"`) {
		t.Error("the alternative candidate does not say what to switch to")
	}
	if !strings.Contains(body, "Published order.") {
		t.Error("the candidate does not carry its description")
	}
	if !strings.Contains(body, `hx-post="/series/switch"`) {
		t.Error("no form to submit the chosen candidate")
	}
	if strings.Contains(body, "wheel-candidate") {
		t.Error("data-span candidates survived; the server renders candidates whole now")
	}
	// The selection moves between ghost cards above and below the lifted row,
	// each holding a server-rendered peek of every candidate.
	if !strings.Contains(body, `class="wheel-ghost wheel-prev"`) || !strings.Contains(body, `class="wheel-ghost wheel-next"`) {
		t.Error("the wheel has no ghost cards to move between")
	}
	if n := strings.Count(body, `class="wheel-peek"`); n != 4 {
		t.Errorf("%d peeks, want each ghost to hold every candidate", n)
	}
	// The face floats over the row rather than replacing its content, so it
	// wears its own cover; the row underneath is never touched.
	if !strings.Contains(body, `class="wheel-cover"`) {
		t.Error("the face has no cover of its own, so it would have to borrow the row's")
	}
}

func TestSwitchingFromTheDrawerChangesWhichSeriesIsTracked(t *testing.T) {
	h := ready(t, switchSource(), testStore(t))

	form := url.Values{"name": {"The Expanse (Chronological)"}, "to": {"The Expanse"}}
	if rec := post(t, h, "/series/switch", form); rec.Code != http.StatusOK {
		t.Fatalf("POST /series/switch: status = %d, want 200", rec.Code)
	}
	body := getBody(t, h, "/view")
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
	body := getBody(t, h, "/view")
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

	if body := getBody(t, h, "/view"); strings.Contains(body, "read to book 0") {
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

// flashOf returns the notice slot's contents from a response body.
func flashOf(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `<div id="flash"`)
	if i < 0 {
		t.Fatal("the response carries no notice slot")
	}
	end := strings.Index(body[i:], `<div class="deck"`)
	if end < 0 {
		return body[i:]
	}
	return body[i : i+end]
}

// A recorded decision answers with a quiet confirmation carrying its own
// undo, so reversing a slip never means hunting the drawer for the row.
func TestADecisionConfirmsItselfAndOffersUndo(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	flash := flashOf(t, rec.Body.String())
	if !strings.Contains(flash, "Dropped") || !strings.Contains(flash, "Mistborn") {
		t.Errorf("the confirmation does not say what was decided: %q", flash)
	}
	if !strings.Contains(flash, `hx-post="/series/clear"`) || !strings.Contains(flash, "Undrop") {
		t.Errorf("the confirmation offers no undo: %q", flash)
	}
}

// A decision made in the drawer shows its effect right where the reader is
// standing — the row moves to its new group, undo alongside — so no banner
// appears in the page behind it. The drawer's controls say so themselves.
func TestADrawerDecisionRaisesNoBanner(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	rec := post(t, h, "/series/park", url.Values{"name": {"Mistborn"}, "from": {"drawer"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if flash := flashOf(t, rec.Body.String()); strings.Contains(flash, "notice") {
		t.Errorf("a drawer decision still raises a banner: %q", flash)
	}

	body := getBody(t, h, "/view")
	if !strings.Contains(body, `&#34;from&#34;:&#34;drawer&#34;`) {
		t.Error("drawer buttons do not say where they were pressed")
	}
	i := strings.Index(body, `class="wheel-form"`)
	if i >= 0 && !strings.Contains(body[i:], `name="from" value="drawer"`) {
		t.Error("the wheel form does not say it lives in the drawer")
	}
}

// An undo is a decision too, but its confirmation ends the chain: it offers
// no undo of its own.
func TestAClearedDecisionConfirmsWithoutAnotherUndo(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	if rec := post(t, h, "/series/park", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("park: status = %d, want 200", rec.Code)
	}
	rec := post(t, h, "/series/clear", url.Values{"name": {"Mistborn"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: status = %d, want 200", rec.Code)
	}
	flash := flashOf(t, rec.Body.String())
	if !strings.Contains(flash, "Mistborn") {
		t.Errorf("the undo does not confirm itself: %q", flash)
	}
	if strings.Contains(flash, "hx-post") {
		t.Errorf("undoing an undo would loop forever: %q", flash)
	}
}

// htmx buttons carry their own parameters, so a decision needs no form or
// hidden input around it. The one form left is the wheel's, whose "to" value
// the script fills in at confirm time.
func TestDecisionsAreButtonsNotForms(t *testing.T) {
	body := getBody(t, ready(t, midSeries(), testStore(t)), "/view")
	if strings.Contains(body, "<form") {
		t.Error("a decision still travels by form; buttons carry their own hx-vals")
	}
	if !strings.Contains(body, "Mistborn") || !strings.Contains(body, "hx-vals") {
		t.Error("no hx-vals on the decision buttons, so a POST would carry no series name")
	}

	body = getBody(t, ready(t, switchSource(), testStore(t)), "/view")
	if strings.Count(body, "<form") != 1 {
		t.Errorf("%d forms on a page with a wheel, want only the wheel's", strings.Count(body, "<form"))
	}
}

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

func TestADecisionReturnsTheRefreshedDrawer(t *testing.T) {
	// The reader was in the drawer when they decided. Nothing navigates now, so
	// what matters is that the response carries the drawer back for the morph,
	// with the decision already reflected in it.
	h := ready(t, midSeries(), testStore(t))
	rec := post(t, h, "/series/park", url.Values{"name": {"Mistborn"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="drawer-body" hx-swap-oob="innerHTML"`) {
		t.Error("the response carries no drawer body to swap back into the page")
	}
	if !strings.Contains(body, "Parked") {
		t.Error("the parked series is not filed under Parked in the response")
	}
}

func TestDecidingNeverWaitsOnTheCatalogue(t *testing.T) {
	// Recording a statement needs the group, not the next-book lookups; a
	// slow catalogue must not stretch the POST.
	src := &countingSlowResolver{stubSource: midSeries()}
	h := ready(t, src, testStore(t))

	if rec := post(t, h, "/series/park", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if src.calls != 0 {
		t.Errorf("the decision POST made %d catalogue lookups, want 0", src.calls)
	}
}

// countingSlowResolver counts lookups so a test can prove none happened.
type countingSlowResolver struct {
	stubSource
	calls int
}

func (c *countingSlowResolver) NextInSeries(_ context.Context, _ library.SeriesQuery) (library.Entry, bool, error) {
	c.calls++
	return library.Entry{}, false, nil
}

func TestThePinnedGroupIsOpenAndNamed(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	if rec := post(t, h, "/series/pin", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("POST /series/pin: status = %d, want 200", rec.Code)
	}
	body := getBody(t, h, "/view")

	// The pinned series is the one thing the reader explicitly asked for
	// next: at most one exists, so it is a plain section with nothing to
	// fold, and the label says what pinning means.
	i := strings.Index(body, "Reading next — pinned")
	if i < 0 {
		t.Fatal("no pinned group with an explanatory label")
	}
	if _, ok := enclosingDetails(body, ">Reading next — pinned"); ok {
		t.Error("the pinned group is foldable; a group of at most one has nothing to fold")
	}
	if !strings.Contains(body, `class="drawer-group drawer-group--flat"`) {
		t.Error("the pinned group is not rendered as a flat section")
	}
}

func TestRowsWearTheirIdentityBadges(t *testing.T) {
	// The source and position show on the row itself, so two same-named rows
	// are tellable apart without opening anything.
	body := getBody(t, ready(t, switchSource(), testStore(t)), "/view")

	i := strings.Index(body, `class="row-head"`)
	if i < 0 {
		t.Fatal("rows have no identity line")
	}
	row := body[i:min(i+600, len(body))]
	if !strings.Contains(row, ">Hardcover<") {
		t.Errorf("the row does not name its source: %.300s", row)
	}
	if !strings.Contains(body, ">read to book 2<") {
		t.Error("the switcher does not say where the reader stands in each ordering")
	}
}

// midSeriesReading is the same reader, partway through book 3 rather than
// done with it.
func midSeriesReading() stubSource {
	started := seriesEntry("Book 3", "Mistborn", 3)
	started.Status = library.StatusCurrentlyRead
	return stubSource{
		reading: []library.Entry{started},
		toRead:  []library.Entry{seriesEntry("Book 4", "Mistborn", 4)},
	}
}

func TestTheNextLineNumbersTheBookItOffers(t *testing.T) {
	row := drawerRow(t, getBody(t, ready(t, midSeries(), testStore(t)), "/view"))
	if !strings.Contains(row, "Next: Book 4, book 4") {
		t.Errorf("the offered book is not numbered where it is named: %.300s", row)
	}
}

func TestARowSaysNothingAboutWhereTheReaderHasBeen(t *testing.T) {
	// The row offers a book; how far the reader came is the switcher's job.
	row := drawerRow(t, getBody(t, ready(t, midSeriesReading(), testStore(t)), "/view"))
	if strings.Contains(row, "read to") || strings.Contains(row, "reading book") {
		t.Errorf("the row still reports the reader's own place: %.300s", row)
	}
	if !strings.Contains(row, "Next: Book 4, book 4") {
		t.Errorf("the offered book is not numbered: %.300s", row)
	}
}

func TestACaughtUpRowOffersNoNumber(t *testing.T) {
	done := seriesEntry("Book 3", "Mistborn", 3)
	done.Status = library.StatusRead
	done.FinishedAt = time.Now().Add(-24 * time.Hour)
	src := resolverStub{stubSource: stubSource{reads: []library.Entry{done}}}

	row := drawerRow(t, getBody(t, ready(t, src, testStore(t)), "/view"))
	if !strings.Contains(row, "Nothing left to read") {
		t.Errorf("a caught-up row does not say it is done: %.300s", row)
	}
	if strings.Contains(row, "book 3") {
		t.Errorf("a caught-up row numbers a book it is not offering: %.300s", row)
	}
}

func TestANextBookWithoutASlotIsNamedWithoutANumber(t *testing.T) {
	done := seriesEntry("Book 3", "Mistborn", 3)
	done.Status = library.StatusRead
	done.FinishedAt = time.Now().Add(-24 * time.Hour)
	src := resolverStub{
		stubSource: stubSource{reads: []library.Entry{done}},
		next:       library.Entry{Book: library.Book{Title: "The Companion"}},
		found:      true,
	}

	row := drawerRow(t, getBody(t, ready(t, src, testStore(t)), "/view"))
	if !strings.Contains(row, "Next: The Companion<") {
		t.Errorf("an unnumbered offer is not named plainly: %.300s", row)
	}
	if strings.Contains(row, "book 0") {
		t.Errorf("an unnumbered offer claims slot zero: %.300s", row)
	}
}

func TestTheTagsSitBeneathTheTitle(t *testing.T) {
	row := drawerRow(t, getBody(t, ready(t, midSeries(), testStore(t)), "/view"))
	head := row[:strings.Index(row, "</span>")]
	if strings.Contains(head, "row-badge") {
		t.Errorf("the tags share the title's line: %.300s", row)
	}
	tags := strings.Index(row, `class="row-tags"`)
	if tags < 0 || tags < strings.Index(row, "drawer-name") {
		t.Errorf("the tags do not sit on their own line beneath the title: %.300s", row)
	}
}

func TestTheSwitcherStillSaysWhereTheReaderStands(t *testing.T) {
	// The badge now speaks of the next book, so the reader's own place in each
	// ordering lives where orderings are compared: the switcher.
	started := library.Entry{Book: library.Book{
		Title:  "Leviathan Wakes",
		Series: &library.Series{Name: "The Expanse (Chronological)", Position: library.At(1), Source: "hardcover"},
		OtherSeries: []library.Series{
			{Name: "The Expanse", Position: library.At(1), Source: "hardcover", Description: "Published order."},
		},
	}, Status: library.StatusCurrentlyRead}
	src := stubSource{reading: []library.Entry{started}}

	body := getBody(t, ready(t, src, testStore(t)), "/view")
	if !strings.Contains(body, ">reading book 1<") {
		t.Error("the switcher does not say the tracked identity is mid-book")
	}
}

// drawerRow returns the first series row's markup.
func drawerRow(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `class="row-head"`)
	if i < 0 {
		t.Fatal("the drawer holds no series rows")
	}
	return body[i:min(i+600, len(body))]
}

func TestAnOfferAtSlotZeroIsStillNumbered(t *testing.T) {
	done := seriesEntry("Book 1", "Saga", 1)
	done.Status = library.StatusRead
	done.FinishedAt = time.Now().Add(-24 * time.Hour)
	src := stubSource{
		reads:  []library.Entry{done},
		toRead: []library.Entry{seriesEntry("The Prequel", "Saga", 0)},
	}

	row := drawerRow(t, getBody(t, ready(t, src, testStore(t)), "/view"))
	if !strings.Contains(row, "Next: The Prequel, book 0") {
		t.Errorf("a prequel at slot 0 loses its number: %.300s", row)
	}
}

// htmx will not swap a 4xx into the page by default, so a refusal has to say
// where it should land. The status stays honest either way.
func TestARefusedDecisionIsRetargetedAtTheNoticeSlot(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	rec := post(t, h, "/series/park", url.Values{"name": {""}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got := rec.Header().Get("HX-Retarget"); got != "#flash" {
		t.Errorf("HX-Retarget = %q, want #flash", got)
	}
	if got := rec.Header().Get("HX-Reswap"); got != "innerHTML" {
		t.Errorf("HX-Reswap = %q, want innerHTML", got)
	}
	if !strings.Contains(rec.Body.String(), "a series name is required") {
		t.Error("the refusal does not say what went wrong")
	}
}

// Each fold carries a stable key. The reader's open/closed state is theirs,
// not the server's, and the key is what lets it be matched across a morph
// that would otherwise reset every group to the markup's default.
func TestDrawerGroupsCarryAStableFoldKey(t *testing.T) {
	body := getBody(t, ready(t, midSeries(), testStore(t)), "/view")
	if !strings.Contains(body, `data-group="Current"`) {
		t.Error("the Current fold has no stable key to restore its state against")
	}
}

// Switching changes the name a group is tracked under, and the lookahead cache
// is keyed on that name, so the new identity is never already cached. A
// re-render that refuses to spend a lookup hands back a row with nothing next
// and no way to pick it.
func TestSwitchingStillOffersTheNextBook(t *testing.T) {
	src := resolverStub{
		stubSource: switchSource(),
		next:       library.Entry{Book: library.Book{Title: "Leviathan Falls"}},
		found:      true,
	}
	h := ready(t, src, testStore(t))
	form := url.Values{"name": {"The Expanse (Chronological)"}, "to": {"The Expanse"}}
	rec := post(t, h, "/series/switch", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Next: Leviathan Falls") {
		t.Error("the switched group came back with no next book, so there is nothing to pick")
	}
}

// offShelfSeries is a reader partway through a series with nothing of it on the
// shelf, so only the catalogue can say what comes next.
func offShelfSeries() stubSource {
	read := seriesEntry("Book 3", "Mistborn", 3)
	read.Status = library.StatusRead
	read.FinishedAt = time.Now().Add(-24 * time.Hour)
	return stubSource{reads: []library.Entry{read}}
}

// A dropped group is skipped by the request-time enrichment and by the warm
// pass alike, so nothing is ever cached for it. Undropping has to be allowed to
// ask, or the series returns to the drawer with nothing next.
func TestUndroppingStillOffersTheNextBook(t *testing.T) {
	src := resolverStub{
		stubSource: offShelfSeries(),
		next:       library.Entry{Book: library.Book{Title: "The Alloy of Law"}},
		found:      true,
	}
	h := ready(t, src, testStore(t))
	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("drop: status = %d, want %d", rec.Code, http.StatusOK)
	}
	rec := post(t, h, "/series/clear", url.Values{"name": {"Mistborn"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Next: The Alloy of Law") {
		t.Error("the undropped group came back with no next book, so there is nothing to pick")
	}
}

// Whether an undo needs a lookup is the engine's knowledge now; the markup
// carries no marker for the client to hand back.
func TestNoRowCarriesACacheMarker(t *testing.T) {
	src := offShelfSeries()
	h := ready(t, src, testStore(t))
	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("drop: status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := getBody(t, h, "/view")
	if !strings.Contains(body, "Undrop") {
		t.Fatal("no undrop control to inspect")
	}
	if strings.Contains(body, `name="uncached"`) {
		t.Error("a row still asks the client to report cache state the server already knows")
	}
}

// Unpinning and resuming go through the same action as undropping, but their
// groups were enriched all along, so they must not spend a lookup.
func TestResumingSpendsNoLookup(t *testing.T) {
	src := &countingSlowResolver{stubSource: offShelfSeries()}
	h := ready(t, src, testStore(t))
	if rec := post(t, h, "/series/park", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("park: status = %d, want %d", rec.Code, http.StatusOK)
	}
	before := src.calls
	if rec := post(t, h, "/series/clear", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusOK {
		t.Fatalf("clear: status = %d, want %d", rec.Code, http.StatusOK)
	}
	if src.calls != before {
		t.Errorf("resuming made %d catalogue lookups, want none", src.calls-before)
	}
}
