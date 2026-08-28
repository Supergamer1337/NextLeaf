package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
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

// flakySource fails on RecentReads, as a throttled backend would.
type flakySource struct{ stubSource }

func (flakySource) RecentReads(_ context.Context, _ int) ([]library.Entry, error) {
	return nil, errors.New("rate limited")
}

func TestParkIsRefusedWhenTheReadingHistoryCannotBeRead(t *testing.T) {
	st := testStore(t)
	h := ready(t, flakySource{}, st)

	// Anchoring a park to an unknown "newest finish" would record a park that
	// the very next reconcile throws away, so it must not be recorded at all.
	rec := post(t, h, "/series/park", url.Values{"name": {"Mistborn"}})
	if rec.Code == http.StatusSeeOther {
		t.Fatal("park was accepted despite an unreadable reading history")
	}

	tracked, ok, err := st.Get(context.Background(), "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok && tracked.Decision == series.Parked {
		t.Error("a park was recorded that could not be anchored")
	}
}

// omnibusResolver resolves an off-shelf next book that the catalogue files
// under a differently named omnibus series, as Hardcover's sub-series do.
type omnibusResolver struct{ stubSource }

func (omnibusResolver) NextInSeries(_ context.Context, _ library.SeriesQuery) (library.Entry, bool, error) {
	e := seriesEntry("Book 4", "The Mistborn Saga: The Original Trilogy", 4)
	return e, true, nil
}

func TestDecisionsAreRecordedAgainstTheTrackedSeriesNotTheBooksOwnLabel(t *testing.T) {
	read := seriesEntry("Book 3", "Mistborn", 3)
	read.FinishedAt = time.Now().Add(-24 * time.Hour)
	src := omnibusResolver{stubSource: stubSource{reads: []library.Entry{read}}}

	st := testStore(t)
	h := ready(t, src, st)
	body := getBody(t, h, "/")

	if !strings.Contains(body, "Book 4") {
		t.Fatalf("expected the resolved continuation:\n%s", body)
	}
	// Parking from this card must park Mistborn, the series being continued,
	// not the omnibus the volume happens to be labelled with.
	if !strings.Contains(body, `value="Mistborn"`) {
		t.Errorf("decision forms should carry the tracked series name:\n%s", formValues(body))
	}

	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/drop: status = %d, want 303", rec.Code)
	}
	tracked, ok, err := st.Get(context.Background(), "Mistborn")
	if err != nil || !ok {
		t.Fatalf("Get = (%v, %v)", ok, err)
	}
	if tracked.Decision != series.Dropped {
		t.Errorf("Decision = %v, want Dropped", tracked.Decision)
	}
}

// manySeries reports a reader tracked in many series, none with a book shelved.
func manySeries(n int) stubSource {
	var reads []library.Entry
	for i := 0; i < n; i++ {
		e := seriesEntry("Book", "Series "+strconv.Itoa(i), 1)
		e.FinishedAt = time.Now().Add(-time.Duration(i) * time.Hour)
		reads = append(reads, e)
	}
	return stubSource{reads: reads, toRead: []library.Entry{{Book: library.Book{Title: "Standalone"}}}}
}

// countingResolver records how many next-in-series lookups a page load makes.
type countingResolver struct {
	stubSource
	calls int
	err   error
}

func (c *countingResolver) NextInSeries(_ context.Context, _ library.SeriesQuery) (library.Entry, bool, error) {
	c.calls++
	return library.Entry{}, false, c.err
}

func TestAPageLoadDoesNotAskAboutEverySeriesTheReaderHasEverFinished(t *testing.T) {
	src := &countingResolver{stubSource: manySeries(40)}
	h := ready(t, src, testStore(t))

	if body := getBody(t, h, "/"); !strings.Contains(body, "Standalone") {
		t.Error("no recommendation was served")
	}
	if src.calls > maxSeriesLookups {
		t.Errorf("made %d lookups for one page load, want at most %d", src.calls, maxSeriesLookups)
	}
}

func TestAThrottledResolverStillYieldsARecommendation(t *testing.T) {
	// One backend refusing must degrade to a variety pick, not blank the page.
	src := &countingResolver{stubSource: manySeries(3), err: errors.New("rate limited")}
	h := ready(t, src, testStore(t))

	body := getBody(t, h, "/")
	if !strings.Contains(body, "Standalone") {
		t.Errorf("a throttled resolver blanked the page instead of falling back:\n%s", body)
	}
}

// formValues extracts the decision forms' hidden values for failure messages.
func formValues(body string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, `name="name"`) {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return strings.Join(out, "\n")
}

// caughtUp is a reader tracked in two series, one of which has nothing left.
func caughtUpStore(t *testing.T) *series.Store {
	t.Helper()
	st := testStore(t)
	ctx := context.Background()
	reads := []library.Entry{seriesEntry("Book 3", "Mistborn", 3), seriesEntry("Book 4", "Stormlight", 4)}
	if err := st.Reconcile(ctx, series.Snapshot{Reads: reads}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	next := library.Entry{Book: library.Book{
		Title:    "The Alloy of Law",
		Series:   &library.Series{Name: "Mistborn", Position: library.At(4)},
		CoverURL: "https://covers.example/alloy.jpg",
	}}
	if err := st.SetNext(ctx, "Mistborn", 3, next, true, time.Now()); err != nil {
		t.Fatalf("SetNext: %v", err)
	}
	if err := st.SetNext(ctx, "Stormlight", 4, library.Entry{}, false, time.Now()); err != nil {
		t.Fatalf("SetNext: %v", err)
	}
	return st
}

func TestDrawerFilesACaughtUpSeriesUnderFinished(t *testing.T) {
	h := ready(t, stubSource{}, caughtUpStore(t))
	body := getBody(t, h, "/")

	finished := section(body, "Finished")
	if !strings.Contains(finished, "Stormlight") {
		t.Errorf("Stormlight is not in the Finished drawer:\n%s", finished)
	}
	if strings.Contains(section(body, "Active"), "Stormlight") {
		t.Error("a caught-up series is still listed as active")
	}
}

func TestDrawerShowsTheNextBookAndItsCover(t *testing.T) {
	h := ready(t, stubSource{}, caughtUpStore(t))
	body := getBody(t, h, "/")

	if !strings.Contains(body, "The Alloy of Law") {
		t.Error("the drawer does not name the next book")
	}
	if !strings.Contains(body, "https://covers.example/alloy.jpg") {
		t.Error("the drawer does not show the next book's cover")
	}
}

func TestDrawerRowsOfferDropAndPick(t *testing.T) {
	st := caughtUpStore(t)
	h := ready(t, stubSource{}, st)
	body := getBody(t, h, "/")

	if strings.Count(body, `action="/series/drop"`) < 2 {
		t.Error("drawer rows do not offer Drop")
	}

	// Picking from a row pins the series to the book the drawer is showing.
	form := url.Values{"name": {"Mistborn"}, "position": {"4"}}
	if rec := post(t, h, "/series/pin", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/pin: status = %d, want 303", rec.Code)
	}
	tracked, _, err := st.Get(context.Background(), "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.Decision != series.Pinned || tracked.PinnedPosition != 4 {
		t.Errorf("Decision = %v at %v, want Pinned at book 4", tracked.Decision, tracked.PinnedPosition)
	}
}

// section returns the drawer group whose label is name, for readable failures.
func section(body, name string) string {
	// The label may be followed by a tally span rather than closing at once.
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

func TestADroppedRowDoesNotAlsoOfferToPickIt(t *testing.T) {
	st := caughtUpStore(t)
	h := ready(t, stubSource{}, st)
	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/drop: status = %d, want 303", rec.Code)
	}

	// Offering "Pick this" next to "Undrop" asks the reader to hold two
	// contradictory intentions; undropping is the way back.
	dropped := section(getBody(t, h, "/"), "Dropped")
	if strings.Contains(dropped, "Pick this") {
		t.Errorf("a dropped series still offers Pick this:\n%s", dropped)
	}
	if !strings.Contains(dropped, "Undrop") {
		t.Errorf("a dropped series offers no way back:\n%s", dropped)
	}
}

func TestACrossSiteDecisionPostIsRefused(t *testing.T) {
	st := caughtUpStore(t)
	h := ready(t, stubSource{}, st)

	// NextLeaf has no login, so a page anywhere on the web could otherwise
	// drop a series through the reader's own browser.
	req := httptest.NewRequest(http.MethodPost, "/series/drop",
		strings.NewReader(url.Values{"name": {"Mistborn"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: status = %d, want 403", rec.Code)
	}
	tracked, _, err := st.Get(context.Background(), "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.Decision == series.Dropped {
		t.Error("a cross-site request recorded a decision")
	}
}

func TestASameOriginDecisionPostStillWorks(t *testing.T) {
	h := ready(t, stubSource{}, caughtUpStore(t))

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

func TestADecisionPostWithoutFetchMetadataStillWorks(t *testing.T) {
	// curl and older browsers send no Sec-Fetch-Site; absence is not evidence
	// of a cross-site request.
	h := ready(t, stubSource{}, caughtUpStore(t))

	if rec := post(t, h, "/series/drop", url.Values{"name": {"Mistborn"}}); rec.Code != http.StatusSeeOther {
		t.Errorf("plain POST: status = %d, want 303", rec.Code)
	}
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

func TestTheFinishedGroupStartsCollapsed(t *testing.T) {
	h := ready(t, stubSource{}, caughtUpStore(t))
	body := getBody(t, h, "/")

	// Finished is a record of what is done, so it should stay out of the way
	// until asked for.
	block, ok := enclosingDetails(body, ">Finished")
	if !ok {
		t.Fatal("the Finished group is not collapsible")
	}
	if openTag, _, _ := strings.Cut(block, ">"); strings.Contains(openTag, "open") {
		t.Errorf("the Finished group starts open: %q", openTag)
	}
	if !strings.Contains(block, "Stormlight") {
		t.Error("the caught-up series is not inside the collapsed group")
	}
}

func TestTheGroupsWorthActingOnAreNotCollapsed(t *testing.T) {
	h := ready(t, stubSource{}, caughtUpStore(t))
	body := getBody(t, h, "/")

	if _, ok := enclosingDetails(body, ">Active"); ok {
		t.Error("Active is hidden behind a fold; only Finished should be")
	}
}

func TestAFinishedSeriesShowsTheCoverOfTheLastBookRead(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	read := seriesEntry("Book 4", "Stormlight", 4)
	read.Book.CoverURL = "https://covers.example/stormlight4.jpg"
	if err := st.Reconcile(ctx, series.Snapshot{Reads: []library.Entry{read}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.SetNext(ctx, "Stormlight", 4, library.Entry{}, false, time.Now()); err != nil {
		t.Fatalf("SetNext: %v", err)
	}

	// There is no next book to picture, so the series wears the last one read
	// rather than a blank square.
	if body := getBody(t, ready(t, stubSource{}, st), "/"); !strings.Contains(body, "stormlight4.jpg") {
		t.Error("a finished series shows no cover at all")
	}
}

func TestASeriesWithNoKnownPositionShowsACover(t *testing.T) {
	st := testStore(t)
	read := seriesEntry("The Fellowship", "The Lord of the Rings", 0)
	read.Book.CoverURL = "https://covers.example/fellowship.jpg"
	if err := st.Reconcile(context.Background(), series.Snapshot{Reads: []library.Entry{read}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Without a position there is never a next book, so this row would
	// otherwise stay blank forever.
	if body := getBody(t, ready(t, stubSource{}, st), "/"); !strings.Contains(body, "fellowship.jpg") {
		t.Error("a series with no known position shows no cover at all")
	}
}

func TestTheNextBooksCoverWinsOverTheSeriesOwn(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	read := seriesEntry("Book 3", "Mistborn", 3)
	read.Book.CoverURL = "https://covers.example/read.jpg"
	if err := st.Reconcile(ctx, series.Snapshot{Reads: []library.Entry{read}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	next := library.Entry{Book: library.Book{
		Title:    "The Alloy of Law",
		Series:   &library.Series{Name: "Mistborn", Position: library.At(4)},
		CoverURL: "https://covers.example/next.jpg",
	}}
	if err := st.SetNext(ctx, "Mistborn", 3, next, true, time.Now()); err != nil {
		t.Fatalf("SetNext: %v", err)
	}

	body := getBody(t, ready(t, stubSource{}, st), "/")
	if !strings.Contains(body, "next.jpg") {
		t.Error("the next book's cover is not shown")
	}
	if strings.Contains(body, "read.jpg") {
		t.Error("the already-read cover is shown alongside the next book")
	}
}

func TestAnUnplacedAnchorStillContinuesFromTheShelf(t *testing.T) {
	// The Hobbit belongs to The Lord of the Rings without a number of its own,
	// yet all three volumes sit on the shelf: the earliest is plainly next.
	hobbit := library.Entry{Book: library.Book{
		Title:  "The Hobbit",
		Series: &library.Series{Name: "The Lord of the Rings"},
	}}
	hobbit.FinishedAt = time.Now().Add(-24 * time.Hour)
	src := stubSource{
		reads: []library.Entry{hobbit},
		toRead: []library.Entry{
			seriesEntry("The Two Towers", "The Lord of the Rings", 2),
			seriesEntry("The Fellowship of the Ring", "The Lord of the Rings", 1),
		},
	}

	body := getBody(t, ready(t, src, testStore(t)), "/")
	if !strings.Contains(body, "The Fellowship of the Ring") {
		t.Errorf("an unplaced anchor blocked a continuation its shelf could answer:\n%s", body)
	}
}

func TestAnUnplacedSeriesIsNotAskedOfTheCatalogue(t *testing.T) {
	// Asking "what follows nothing" returns the series' first volume, which
	// would nag a reader whose source merely lost the position.
	hobbit := library.Entry{Book: library.Book{
		Title:  "The Hobbit",
		Series: &library.Series{Name: "The Lord of the Rings"},
	}}
	hobbit.FinishedAt = time.Now().Add(-24 * time.Hour)
	src := &countingResolver{stubSource: stubSource{
		reads:  []library.Entry{hobbit},
		toRead: []library.Entry{{Book: library.Book{Title: "Standalone"}}},
	}}

	getBody(t, ready(t, src, testStore(t)), "/")
	if src.calls != 0 {
		t.Errorf("asked the catalogue about an unplaced series %d times, want 0", src.calls)
	}
}

func TestPositionsRenderAsNumbersNotPointers(t *testing.T) {
	st := testStore(t)
	read := seriesEntry("Book 3", "Mistborn", 3)
	read.FinishedAt = time.Now().Add(-24 * time.Hour)
	src := stubSource{
		reads:  []library.Entry{read},
		toRead: []library.Entry{seriesEntry("Book 4", "Mistborn", 4)},
	}

	body := getBody(t, ready(t, src, st), "/")
	// A pointer rendered raw would show an address like 0xc000123456.
	if strings.Contains(body, "0xc0") {
		t.Error("a position rendered as a pointer address")
	}
	if !strings.Contains(body, "Book 4") {
		t.Fatal("no continuation to check")
	}
	// The pin form must carry a usable number for the store to record.
	if !strings.Contains(body, `name="position" value="4"`) {
		i := strings.Index(body, `name="position"`)
		t.Errorf("pin form position is not a plain number: %q", body[i:min(i+60, len(body))])
	}
}

func TestAnUnplacedSeriesShowsNoPositionRatherThanZero(t *testing.T) {
	st := testStore(t)
	read := library.Entry{Book: library.Book{Title: "The Hobbit", Series: &library.Series{Name: "The Lord of the Rings"}}}
	if err := st.Reconcile(context.Background(), series.Snapshot{Reads: []library.Entry{read}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	body := getBody(t, ready(t, stubSource{}, st), "/")
	if strings.Contains(body, "Read to book 0") {
		t.Error("an unplaced series claims to be read to book 0")
	}
}

// expanseStore tracks one franchise that has an alternative ordering.
func expanseStore(t *testing.T) *series.Store {
	t.Helper()
	st := testStore(t)
	e := library.Entry{Book: library.Book{
		Title:       "Leviathan Wakes",
		Series:      &library.Series{Name: "The Expanse (Chronological)", Position: library.At(2)},
		OtherSeries: []library.Series{{Name: "The Expanse", Position: library.At(1)}},
	}}
	if err := st.Reconcile(context.Background(), series.Snapshot{Reads: []library.Entry{e}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return st
}

func TestTheDrawerOffersTheAlternativeOrdering(t *testing.T) {
	body := getBody(t, ready(t, stubSource{}, expanseStore(t)), "/")

	if !strings.Contains(body, `action="/series/switch"`) {
		t.Error("the drawer offers no way to change which series is tracked")
	}
	// The alternative has to be named, or the reader cannot tell what they
	// would be switching to.
	if !strings.Contains(body, "The Expanse<") && !strings.Contains(body, ">The Expanse") {
		t.Errorf("the alternative is not named:\n%s", section(body, "Active"))
	}
	// The form must say which series is being switched, and to what.
	i := strings.Index(body, `action="/series/switch"`)
	form := body[i:min(i+400, len(body))]
	if !strings.Contains(form, `name="name" value="The Expanse (Chronological)"`) {
		t.Errorf("the switch form does not name the series being switched:\n%s", form)
	}
	if !strings.Contains(form, `name="to" value="The Expanse"`) {
		t.Errorf("the switch form does not name the target:\n%s", form)
	}
}

func TestASeriesWithNoAlternativeOffersNoSwitch(t *testing.T) {
	body := getBody(t, ready(t, stubSource{}, caughtUpStore(t)), "/")

	if strings.Contains(body, `action="/series/switch"`) {
		t.Error("offered a switch for series that belong to no other series")
	}
}

func TestSwitchingFromTheDrawerChangesWhichSeriesIsTracked(t *testing.T) {
	st := expanseStore(t)
	h := ready(t, stubSource{}, st)

	form := url.Values{"name": {"The Expanse (Chronological)"}, "to": {"The Expanse"}}
	if rec := post(t, h, "/series/switch", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /series/switch: status = %d, want 303", rec.Code)
	}

	tracked, err := st.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tracked) != 1 || tracked[0].Name != "The Expanse" {
		names := make([]string, len(tracked))
		for i, tr := range tracked {
			names[i] = tr.Name
		}
		t.Errorf("tracked = %v, want only The Expanse", names)
	}
}

func TestSwitchingToASeriesThatIsNotAnAlternativeIsRefused(t *testing.T) {
	st := expanseStore(t)
	h := ready(t, stubSource{}, st)

	// Only the series the books actually belong to are switchable; anything
	// else would invent a series the reader has never read.
	form := url.Values{"name": {"The Expanse (Chronological)"}, "to": {"Some Other Saga"}}
	if rec := post(t, h, "/series/switch", form); rec.Code != http.StatusBadRequest {
		t.Errorf("POST /series/switch to a stranger: status = %d, want 400", rec.Code)
	}
}

// manyAlternatives tracks one series filed under three others, from two
// backends, one of them with a blurb.
func manyAlternativesStore(t *testing.T) *series.Store {
	t.Helper()
	st := testStore(t)
	e := library.Entry{Book: library.Book{
		Title:  "Leviathan Wakes",
		Series: &library.Series{Name: "The Expanse (Chronological)", Position: library.At(2), Source: "hardcover"},
		OtherSeries: []library.Series{
			{Name: "The Expanse", Position: library.At(1), Source: "hardcover", Description: "Published order."},
			{Name: "Expanse Shorts", Position: library.At(9), Source: "hardcover"},
			{Name: "The Expanse Saga", Position: library.At(1), Source: "grimmory"},
		},
	}}
	if err := st.Reconcile(context.Background(), series.Snapshot{Reads: []library.Entry{e}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return st
}

func TestTheSwitcherOpensAnOverlayHoldingEveryAlternative(t *testing.T) {
	body := getBody(t, ready(t, stubSource{}, manyAlternativesStore(t)), "/")

	// The toggle points at an overlay that only :target reveals, so the row
	// carries one small control however many series the books belong to.
	i := strings.Index(body, `class="switcher-toggle" href="#`)
	if i < 0 {
		t.Fatal("no switch control on a row with alternatives")
	}
	rest := body[i+len(`class="switcher-toggle" href="#`):]
	id, _, _ := strings.Cut(rest, `"`)
	if id == "" {
		t.Fatal("the switch control points nowhere")
	}

	j := strings.Index(body, `id="`+id+`"`)
	if j < 0 {
		t.Fatalf("no overlay with id %q for the control to open", id)
	}
	overlay := body[j:]
	if end := strings.Index(overlay, "</div>\n        </div>"); end > 0 {
		overlay = overlay[:end]
	}
	for _, want := range []string{"The Expanse", "Expanse Shorts", "The Expanse Saga"} {
		if !strings.Contains(overlay, `value="`+want+`"`) {
			t.Errorf("the overlay does not offer %q", want)
		}
	}
	// It must be dismissable without choosing anything.
	if !strings.Contains(overlay, "switch-backdrop") {
		t.Error("the overlay cannot be dismissed")
	}
}

func TestEachRowsOverlayHasItsOwnAddress(t *testing.T) {
	// Two rows sharing an id would open each other's overlay.
	body := getBody(t, ready(t, stubSource{}, manyAlternativesStore(t)), "/")

	seen := map[string]bool{}
	for _, chunk := range strings.Split(body, `class="switch-modal" id="`)[1:] {
		id, _, _ := strings.Cut(chunk, `"`)
		if seen[id] {
			t.Errorf("overlay id %q is used twice", id)
		}
		seen[id] = true
	}
}

func TestTheSwitcherNamesTheSourceAndBlurbThatTellAlternativesApart(t *testing.T) {
	body := getBody(t, ready(t, stubSource{}, manyAlternativesStore(t)), "/")

	// Two orderings of one franchise are indistinguishable by name alone.
	if !strings.Contains(body, "Published order.") {
		t.Error("the switcher does not show an alternative's description")
	}
	if !strings.Contains(body, ">Grimmory<") {
		t.Error("the switcher does not say which backend an alternative comes from")
	}
}
