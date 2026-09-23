package web

import (
	"context"
	"html"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nextleaf/internal/library"
	"nextleaf/internal/picker"
	"nextleaf/internal/series"
)

// namedStub is a stubSource under another name, so two can be combined.
type namedStub struct {
	stubSource
	name string
}

func (s namedStub) Name() string { return s.name }

// finderStub is a catalogue: it resolves series and finds them by ISBN.
type finderStub struct {
	namedStub
	claims map[string][]library.Series
	next   library.Entry
}

func (s finderStub) NextInSeries(context.Context, library.SeriesQuery) (library.Entry, bool, error) {
	return s.next, true, nil
}
func (s finderStub) SeriesByISBN(_ context.Context, isbns []string) (map[string][]library.Series, error) {
	out := map[string][]library.Series{}
	for _, isbn := range isbns {
		if c, ok := s.claims[isbn]; ok {
			out[isbn] = c
		}
	}
	return out, nil
}

func shelfAndCatalogue() library.Source {
	read := library.Entry{
		Book: library.Book{
			Title: "Death's End", Authors: []string{"Cixin Liu"}, ISBNs: []string{"9780765377104"},
			Series: &library.Series{Name: "Three-Body", Position: library.At(3), Source: "grimmory"},
		},
		Status: library.StatusRead, FinishedAt: time.Now().Add(-24 * time.Hour),
	}
	hc := finderStub{
		namedStub: namedStub{name: "hardcover"},
		claims: map[string][]library.Series{"9780765377104": {
			{Name: "Remembrance of Earth's Past", Slug: "remembrance", Position: library.At(3), Source: "hardcover"},
		}},
		next: library.Entry{Book: library.Book{Title: "The Redemption of Time"}},
	}
	gm := namedStub{name: "grimmory", stubSource: stubSource{reads: []library.Entry{read}}}
	return library.Combine(hc, gm)
}

func TestAFinishedShelfOffersTheCatalogue(t *testing.T) {
	h := warmed(t, shelfAndCatalogue(), testStore(t))
	body := getBody(t, h, "/view")

	// Hardcover's book may be named on the row, but only as Hardcover's: never
	// as though Grimmory had offered it.
	for _, line := range nextLines(body) {
		if strings.Contains(line, "The Redemption of Time") && !strings.Contains(line, "on Hardcover") {
			t.Errorf("a Grimmory row presents Hardcover's book as its own: %q", line)
		}
	}
	// Finished on its own provider, but not finished: it has a section of its
	// own, so what could still go on is counted apart from what is done.
	if strings.Contains(section(body, "Finished"), "Three-Body") {
		t.Error("a row another provider carries on past is filed with the finished ones")
	}
	if !strings.Contains(section(body, "Continues elsewhere"), "Three-Body") {
		t.Error("a row another provider carries on past has no section of its own")
	}
	// An icon in the badge row, not a sentence: its label says where it leads.
	if !strings.Contains(body, `aria-label="Continue on Hardcover as “Remembrance of Earth&#39;s Past”"`) {
		t.Error("the row does not offer the series it was found under")
	}
	if strings.Contains(body, "Can be continued on") {
		t.Error("the offer is written out inline again")
	}

	rec := post(t, h, "/series/switch", url.Values{"name": {"Three-Body"}, "to": {"Remembrance of Earth's Past"}, "from": {"drawer"}})
	if rec.Code != 200 {
		t.Fatalf("switch: status = %d, body = %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "The Redemption of Time") {
		t.Error("after following the series on Hardcover, its catalogue should answer")
	}
}

func TestTheWheelNamesWhatEachIdentityHoldsNext(t *testing.T) {
	// Choosing where to continue and choosing how to track are one gesture,
	// so each candidate says what it would leave the reader with.
	h := warmed(t, shelfAndCatalogue(), testStore(t))
	body := getBody(t, h, "/view")

	current := between(body, `class="wheel-item" data-to=""`, `</div>`)
	if !strings.Contains(current, "Nothing left to read") {
		t.Errorf("the tracked identity does not say it has run out:\n%s", current)
	}
	alt := between(body, `class="wheel-item" data-to="Remembrance`, `</div>`)
	if !strings.Contains(alt, "Next: The Redemption of Time") {
		t.Errorf("the alternative does not say what it offers:\n%s", alt)
	}
}

// between returns the slice of s from the first occurrence of from up to the
// next occurrence of to.
func between(s, from, to string) string {
	i := strings.Index(s, from)
	if i < 0 {
		return ""
	}
	rest := s[i:]
	if j := strings.Index(rest, to); j >= 0 {
		return rest[:j]
	}
	return rest
}

// nextLines returns every row's "what is next" line, the wheel's excluded.
func nextLines(body string) []string {
	var out []string
	for _, part := range strings.Split(body, `<span class="drawer-next">`)[1:] {
		out = append(out, part[:strings.Index(part, "</span>")])
	}
	return out
}

// slowCat answers nothing in time: every row is left waiting.
type slowCat struct {
	namedStub
	claims map[string][]library.Series
}

func (s slowCat) NextInSeries(ctx context.Context, _ library.SeriesQuery) (library.Entry, bool, error) {
	<-ctx.Done()
	return library.Entry{}, false, ctx.Err()
}
func (s slowCat) SeriesByISBN(_ context.Context, isbns []string) (map[string][]library.Series, error) {
	out := map[string][]library.Series{}
	for _, isbn := range isbns {
		if c, ok := s.claims[isbn]; ok {
			out[isbn] = c
		}
	}
	return out, nil
}

func waitingLibrary() library.Source {
	read := library.Entry{
		Book: library.Book{
			Title: "The Last Wish", Authors: []string{"Andrzej Sapkowski"},
			Series: &library.Series{Name: "The Witcher", Position: library.At(1), Source: "hardcover"},
		},
		Status: library.StatusRead, FinishedAt: time.Now().Add(-24 * time.Hour),
	}
	return slowCat{namedStub: namedStub{name: "hardcover", stubSource: stubSource{reads: []library.Entry{read}}}}
}

func TestTheDrawerSaysWhenAnswersAreStillComing(t *testing.T) {
	// A row with no answer yet renders the same as one with nothing in it,
	// unless it says otherwise.
	st := testStore(t)
	engine := series.NewEngine(st, waitingLibrary(), picker.Prefs{IncludeNovellas: true})
	h := NewHandler(Deps{Source: waitingLibrary(), Engine: engine})
	body := getBody(t, h, "/view")

	if !strings.Contains(body, "Checking…") {
		t.Error("a row waiting on a lookup renders silent, as though it held nothing")
	}
	status := between(body, `id="drawer-status"`, `</span>`)
	if !strings.Contains(status, "Checking") {
		t.Errorf("the drawer does not say answers are still coming:\n%s", status)
	}
	// It lives in the drawer's header, so appearing and going cannot push
	// the rows about.
	if strings.Contains(between(body, `id="drawer-body"`, `class="drawer-group`), "drawer-status") {
		t.Error("the status sits in the list, where it shifts the rows when it comes and goes")
	}
	if !strings.Contains(between(body, `id="drawer-toggle"`, `</a>`), `class="pending-dot"`) {
		t.Error("with the drawer closed, nothing says answers are still coming")
	}
	// It says which generation it shows, and that it is waiting, for the
	// page's one listener to ask from.
	if !strings.Contains(status, `data-gen="`) || !strings.Contains(status, "data-waiting") {
		t.Errorf("the status does not say what the listener should ask from:\n%s", status)
	}
	// Re-inserted on every refresh, a live region would be announced every
	// time.
	if strings.Contains(status, "role=") {
		t.Error("the status is a live region, re-announced on every refresh")
	}
	// And the background pass is asked to come round for what is missing.
	select {
	case <-engine.Nudged():
	default:
		t.Error("a render with answers still to come did not nudge the background pass")
	}

	// The refresh only touches the drawer: re-rendering the card would deal
	// the reader a different book every time.
	drawer := getBody(t, h, "/view?drawer=1")
	if strings.Contains(drawer, "Recommended") || strings.Contains(drawer, `id="deck"`) {
		t.Error("the drawer refresh re-renders the recommendation card")
	}
	if !strings.Contains(drawer, "The Witcher") || !strings.Contains(drawer, `id="drawer-status"`) {
		t.Error("the drawer refresh does not carry the rows and their status")
	}
}

func TestASettledDrawerSaysNothingButKeepsListening(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	body := getBody(t, h, "/view")

	if strings.Contains(body, "Checking") || strings.Contains(body, `class="pending-dot"`) {
		t.Error("the drawer claims to be still checking when every answer is in")
	}
	// Nothing had been outstanding, so there is nothing to announce as done.
	if strings.Contains(body, "Up to date") {
		t.Error("an ordinary render announces it is up to date")
	}
	// It still listens, quietly: the background pass may change what it shows.
	status := between(body, `id="drawer-status"`, `</span>`)
	if !strings.Contains(status, `data-gen="`) || strings.Contains(status, "data-waiting") {
		t.Errorf("a settled drawer should listen quietly for changes:\n%s", status)
	}
}

func TestTheRefreshAnswersTheMomentThereIsSomethingNew(t *testing.T) {
	st := testStore(t)
	engine := series.NewEngine(st, waitingLibrary(), picker.Prefs{IncludeNovellas: true})
	h := NewHandler(Deps{Source: waitingLibrary(), Engine: engine, Wait: 300 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := engine.View(ctx); err != nil { // an ask ends
		t.Fatal(err)
	}
	gen, _ := engine.Changes()

	// Nothing new since this generation: it waits, then answers anyway.
	start := time.Now()
	getBody(t, h, "/view?drawer=1&since="+strconv.FormatUint(gen, 10))
	if waited := time.Since(start); waited < 250*time.Millisecond {
		t.Errorf("answered after %v with nothing new to show", waited)
	}

	// Something landed after the generation the drawer last saw: at once.
	start = time.Now()
	getBody(t, h, "/view?drawer=1&since="+strconv.FormatUint(gen-1, 10))
	if waited := time.Since(start); waited > 150*time.Millisecond {
		t.Errorf("waited %v with a change already waiting", waited)
	}
}

func TestADrawerThatSettlesSaysSoAtOnce(t *testing.T) {
	// A drawer that was waiting says so when its last answer lands; one that
	// was merely listening has nothing to announce.
	h := ready(t, midSeries(), testStore(t))
	status := between(getBody(t, h, "/view?drawer=1&waiting=1"), `id="drawer-status"`, `</span>`)
	if !strings.Contains(status, "Up to date") {
		t.Errorf("a drawer that has just settled does not say so:\n%s", status)
	}
	if strings.Contains(between(getBody(t, h, "/view?drawer=1"), `id="drawer-status"`, `</span>`), "Up to date") {
		t.Error("a drawer that was never waiting announces it is up to date")
	}
}

func TestTheArrowOpensTheSwitcherRatherThanSwitching(t *testing.T) {
	// The arrow suggests where to continue; the reader sees what that holds,
	// and what else there is, before the row follows anything.
	body := getBody(t, warmed(t, shelfAndCatalogue(), testStore(t)), "/view")
	arrow := between(body, `class="row-follow"`, `>`)
	if strings.Contains(arrow, "hx-post") {
		t.Errorf("the arrow switches without showing where it leads:\n%s", arrow)
	}
	if !strings.Contains(arrow, `data-to="Remembrance of Earth&#39;s Past"`) {
		t.Errorf("the arrow does not say which series to open the switcher on:\n%s", arrow)
	}
	// Opened from the arrow, the wheel answers "where does this continue?",
	// so it cycles only the identities that do; the switcher button still
	// shows them all.
	cont := between(body, `class="wheel-item" data-to="Remembrance`, `>`)
	if !strings.Contains(cont, "data-continues") {
		t.Errorf("an identity with a next book is not marked as continuing:\n%s", cont)
	}
	if strings.Contains(between(body, `class="wheel-item" data-to=""`, `>`), "data-continues") {
		t.Error("the series the reader has run out of is marked as continuing")
	}
}

// twoClaimsOneAnswered is a Hardcover row whose own next book is answered on
// the first render, while its other series waits for the warm pass.
func twoClaimsOneAnswered() library.Source {
	read := library.Entry{
		Book: library.Book{
			Title: "The Fellowship of the Ring", Authors: []string{"J.R.R. Tolkien"},
			Series:      &library.Series{Name: "The Lord of the Rings", Slug: "lotr", Position: library.At(1), Source: "hardcover"},
			OtherSeries: []library.Series{{Name: "Middle Earth", Slug: "middle-earth", Position: library.At(2), Source: "hardcover"}},
		},
		Status: library.StatusRead, FinishedAt: time.Now().Add(-24 * time.Hour),
	}
	return finderStub{
		namedStub: namedStub{name: "hardcover", stubSource: stubSource{reads: []library.Entry{read}}},
		next:      library.Entry{Book: library.Book{Title: "The Two Towers"}},
	}
}

// finishedAndUnchecked is a finished Grimmory row whose Hardcover counterpart
// cannot be asked yet: it sits in the collapsed Finished section, unsettled.
func finishedAndUnchecked() library.Source {
	read := library.Entry{
		Book: library.Book{
			Title: "Death's End", Authors: []string{"Cixin Liu"}, ISBNs: []string{"9780765377104"},
			Series: &library.Series{Name: "Three-Body", Position: library.At(3), Source: "grimmory"},
		},
		Status: library.StatusRead, FinishedAt: time.Now().Add(-24 * time.Hour),
	}
	hc := slowCat{
		namedStub: namedStub{name: "hardcover"},
		claims: map[string][]library.Series{"9780765377104": {
			{Name: "Remembrance of Earth's Past", Slug: "remembrance", Position: library.At(3), Source: "hardcover"},
		}},
	}
	return library.Combine(hc, namedStub{name: "grimmory", stubSource: stubSource{reads: []library.Entry{read}}})
}

func TestEachUnsettledSeriesIsMarked(t *testing.T) {
	// The row's own next book is known, so its line reads as settled. Only
	// the marker says its other series are still being checked.
	body := getBody(t, warmed(t, twoClaimsOneAnswered(), testStore(t)), "/view")
	row := between(body, `<span class="drawer-name">The Lord of the Rings</span>`, `class="row-tags"`)
	if !strings.Contains(row, `class="pending-dot"`) {
		t.Errorf("a row with series still being checked is not marked:\n%s", row)
	}
	if !strings.Contains(body, "Next: The Two Towers") {
		t.Error("the row's own answer should still show")
	}
}

func TestACollapsedSectionSaysItHoldsAnUnsettledSeries(t *testing.T) {
	// Finished starts folded, so a marker on the row alone would be hidden.
	// Warmed until its catalogue is found, but not answered: the lookup is
	// still out when the pass is cut short.
	src := finishedAndUnchecked()
	engine := series.NewEngine(testStore(t), src, picker.Prefs{IncludeNovellas: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := engine.View(ctx); err != nil {
		t.Fatal(err)
	}
	body := getBody(t, NewHandler(Deps{Source: src, Engine: engine}), "/view")
	summary := between(body, `data-group="Finished"`, `</summary>`)
	if !strings.Contains(summary, `class="pending-dot"`) {
		t.Errorf("the folded Finished section does not say it holds a series still being checked:\n%s", summary)
	}
	if strings.Contains(between(body, `data-group="Current"`, `</summary>`), `class="pending-dot"`) {
		t.Error("a section with nothing unsettled is marked")
	}
}

func TestAContinuableSeriesIsCountedApartAndCanBeKeptToItsOwnSeries(t *testing.T) {
	h := warmed(t, shelfAndCatalogue(), testStore(t))
	body := getBody(t, h, "/view")

	sec := section(body, "Continues elsewhere")
	if !strings.Contains(sec, `<span class="drawer-tally">1</span>`) {
		t.Errorf("the section does not count what could still go on:\n%s", sec)
	}
	if !strings.Contains(body, "Next on Hardcover: The Redemption of Time") {
		t.Error("the row does not say what it would continue with, or where")
	}
	if !strings.Contains(body, `hx-post="/series/keep"`) || !strings.Contains(body, "Keep this series") {
		t.Fatal("there is no way to keep to this series and turn the others down")
	}

	// Keeping it files it with the finished ones, for good.
	rec := post(t, h, "/series/keep", url.Values{"name": {"Three-Body"}, "from": {"drawer"}})
	if rec.Code != 200 {
		t.Fatalf("keep: status = %d, body = %s", rec.Code, rec.Body)
	}
	after := rec.Body.String()
	if !strings.Contains(section(after, "Finished"), "Three-Body") {
		t.Error("a kept series is not filed with the finished ones")
	}
	if strings.Contains(after, "Continues elsewhere") || strings.Contains(after, "row-follow") {
		t.Error("a kept series is still offered for continuing elsewhere")
	}
	// And it can be taken back.
	fin := section(after, "Finished")
	if !strings.Contains(fin, `hx-post="/series/unkeep"`) || !strings.Contains(fin, "Suggest others") {
		t.Error("a kept series has no way to have the others suggested again")
	}
	rec = post(t, h, "/series/unkeep", url.Values{"name": {"Three-Body"}, "from": {"drawer"}})
	if !strings.Contains(section(rec.Body.String(), "Continues elsewhere"), "Three-Body") {
		t.Error("clearing the keep does not bring the offer back")
	}
}

// countingCat is a catalogue that counts every question put to it.
type countingCat struct {
	finderStub
	asked *int32
}

func (c countingCat) NextInSeries(ctx context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	atomic.AddInt32(c.asked, 1)
	return c.finderStub.NextInSeries(ctx, q)
}
func (c countingCat) SeriesByISBN(ctx context.Context, isbns []string) (map[string][]library.Series, error) {
	atomic.AddInt32(c.asked, 1)
	return c.finderStub.SeriesByISBN(ctx, isbns)
}

func TestAPageLoadAsksTheCatalogueNothing(t *testing.T) {
	// The background pass does the asking; a reader opening the page never
	// waits on the catalogue. What is not known yet says so, and is fetched.
	var asked int32
	hc := countingCat{finderStub: finderStub{
		namedStub: namedStub{name: "hardcover"},
		claims: map[string][]library.Series{"9780765377104": {
			{Name: "Remembrance of Earth's Past", Slug: "remembrance", Position: library.At(3), Source: "hardcover"},
		}},
		next: library.Entry{Book: library.Book{Title: "The Redemption of Time"}},
	}, asked: &asked}
	gm := namedStub{name: "grimmory", stubSource: stubSource{reads: []library.Entry{{
		Book: library.Book{
			Title: "Death's End", Authors: []string{"Cixin Liu"}, ISBNs: []string{"9780765377104"},
			Series: &library.Series{Name: "Three-Body", Position: library.At(3), Source: "grimmory"},
		},
		Status: library.StatusRead, FinishedAt: time.Now().Add(-24 * time.Hour),
	}}}}
	lib := library.Combine(hc, gm)
	engine := series.NewEngine(testStore(t), lib, picker.Prefs{IncludeNovellas: true})
	h := NewHandler(Deps{Source: lib, Engine: engine})

	getBody(t, h, "/view")
	if n := atomic.LoadInt32(&asked); n != 0 {
		t.Errorf("a page load asked the catalogue %d times", n)
	}
	select {
	case <-engine.Nudged():
	default:
		t.Error("what the page could not show yet was not handed to the background pass")
	}
}

// changingLibrary is a reader's library that can change between refreshes,
// and hold a fetch until released.
type changingLibrary struct {
	mu     sync.Mutex
	toRead []library.Entry
	hold   chan struct{}
}

func (c *changingLibrary) Name() string { return "hardcover" }
func (c *changingLibrary) CurrentlyReading(context.Context) ([]library.Entry, error) {
	return nil, nil
}
func (c *changingLibrary) RecentReads(context.Context, int) ([]library.Entry, error) {
	return nil, nil
}
func (c *changingLibrary) ToRead(context.Context) ([]library.Entry, error) {
	c.mu.Lock()
	hold := c.hold
	c.mu.Unlock()
	if hold != nil {
		<-hold
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]library.Entry(nil), c.toRead...), nil
}

func TestABookAddedToTheListShowsUpWithoutWaitingForThePage(t *testing.T) {
	// The page paints at once from what is held. The library is refreshed
	// behind it, and when that brings something new — a book just added to the
	// list — the page follows up with it straight away.
	lib := &changingLibrary{}
	src := library.NewCached(lib, time.Hour)
	engine := series.NewEngine(testStore(t), src, picker.Prefs{IncludeNovellas: true})
	<-engine.RefreshLibrary() // what the background pass had fetched
	h := NewHandler(Deps{Source: src, Engine: engine, LoadFresh: time.Nanosecond})

	release := make(chan struct{})
	lib.mu.Lock()
	lib.toRead = []library.Entry{{Book: library.Book{Title: "Piranesi", Authors: []string{"Susanna Clarke"}}, Status: library.StatusWantToRead}}
	lib.hold = release
	lib.mu.Unlock()

	body := getBody(t, h, "/view")
	if strings.Contains(body, "Piranesi") {
		t.Fatal("the page waited for the refresh instead of painting what was held")
	}
	follow := between(body, `class="card-follow"`, `>`)
	if !strings.Contains(follow, "/view?after=") {
		t.Fatalf("a page painted from an old library does not follow up:\n%s", follow)
	}
	path := follow[strings.Index(follow, "/view?after="):]
	path = path[:strings.Index(path, `"`)]

	close(release)
	after := getBody(t, h, path)
	if !strings.Contains(after, "Piranesi") {
		t.Error("the follow-up does not bring in the book just added")
	}

	// With nothing new since, a follow-up leaves the page alone.
	_, gen := engine.Library()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/view?after="+strconv.FormatUint(gen, 10), nil))
	if rec.Code != 204 {
		t.Errorf("a follow-up with nothing new answered %d, want 204", rec.Code)
	}
}

func TestAFollowUpLeavesTheCardOnScreen(t *testing.T) {
	// The follow-up redraws the page with what the refresh brought. The card
	// the reader is already looking at stays, rather than being dealt afresh.
	lib := &changingLibrary{}
	for _, title := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		lib.toRead = append(lib.toRead, library.Entry{Book: library.Book{Title: title}, Status: library.StatusWantToRead})
	}
	src := library.NewCached(lib, time.Hour)
	engine := series.NewEngine(testStore(t), src, picker.Prefs{})
	<-engine.RefreshLibrary()
	h := NewHandler(Deps{Source: src, Engine: engine, LoadFresh: time.Nanosecond})

	release := make(chan struct{})
	lib.mu.Lock()
	lib.toRead = append(lib.toRead, library.Entry{Book: library.Book{Title: "Piranesi"}, Status: library.StatusWantToRead})
	lib.hold = release
	lib.mu.Unlock()
	body := getBody(t, h, "/view")
	shown := between(body, `class="rec-title">`, `<`)
	follow := between(body, `class="card-follow"`, `>`)
	path := html.UnescapeString(follow[strings.Index(follow, "/view?after="):])
	path = path[:strings.Index(path, `"`)]
	close(release)

	for i := 0; i < 10; i++ {
		after := getBody(t, h, path)
		if got := between(after, `class="rec-title">`, `<`); got != shown {
			t.Fatalf("the follow-up dealt %q over %q", got, shown)
		}
	}
}

func TestARerolledCardIsNeverFollowedUp(t *testing.T) {
	// A follow-up redraws the card; after "Not now" that would undo the reroll.
	lib := &changingLibrary{}
	src := library.NewCached(lib, time.Hour)
	engine := series.NewEngine(testStore(t), src, picker.Prefs{})
	<-engine.RefreshLibrary()
	h := NewHandler(Deps{Source: src, Engine: engine, LoadFresh: time.Nanosecond})
	if body := getBody(t, h, "/view?another=1"); strings.Contains(body, "card-follow") {
		t.Error("a rerolled card is set to be redrawn")
	}
}

func TestTheDrawerListenerLivesWhereNoRedrawReaches(t *testing.T) {
	// A listener living on something a redraw replaces is left running by
	// every redraw: five rerolls held six requests open, a browser's whole
	// allowance for one site. The page's one listener sits where no swap
	// reaches, and replaces its own request rather than adding another. This
	// pins that structure; the markup cannot show the requests themselves.
	h := ready(t, midSeries(), testStore(t))
	shell := getBody(t, h, "/")
	listener := between(shell, `id="drawer-listen"`, `>`)
	if listener == "" || strings.Count(shell, `id="drawer-listen"`) != 1 {
		t.Fatalf("the page has no single drawer listener:\n%s", listener)
	}
	if !strings.Contains(listener, `hx-sync="this:replace"`) {
		t.Errorf("the listener can hold two requests open at once:\n%s", listener)
	}
	if strings.Contains(between(shell, `id="drawer-body"`, `</div>`), "drawer-listen") {
		t.Error("the listener sits inside the drawer body, which every refresh replaces")
	}
	for _, path := range []string{"/view", "/view?drawer=1"} {
		body := getBody(t, h, path)
		if strings.Contains(body, "drawer-listen") || strings.Contains(between(body, `id="drawer-status"`, `>`), "hx-get") {
			t.Errorf("%s brings a request of its own with it, which would run beside the page's", path)
		}
	}
}

func TestAFollowUpNeverCancelsTheReadersOwnRequest(t *testing.T) {
	// A follow-up is the page catching up on its own. If the reader has a
	// request in flight — a reroll, a decision — the follow-up gives way:
	// that request's answer is drawn from the refreshed library anyway. And
	// it can be asked for again, for when it landed under an open wheel.
	lib := &changingLibrary{}
	src := library.NewCached(lib, time.Hour)
	engine := series.NewEngine(testStore(t), src, picker.Prefs{})
	<-engine.RefreshLibrary()
	h := NewHandler(Deps{Source: src, Engine: engine, LoadFresh: time.Nanosecond})
	follow := between(getBody(t, h, "/view"), `class="card-follow"`, `>`)
	if !strings.Contains(follow, `hx-sync="#app:drop"`) {
		t.Errorf("a follow-up can cancel the reader's own request:\n%s", follow)
	}
	if !strings.Contains(follow, `hx-trigger="load, follow"`) {
		t.Errorf("a follow-up held under an open wheel cannot be asked for again:\n%s", follow)
	}
}

func TestAWaitingRefreshEndsWhenItsRequestDoes(t *testing.T) {
	// A reader who closes the tab ends the request's context. A refresh still
	// waiting for a change must end with it, not hold a goroutine for the
	// rest of its wait.
	engine := series.NewEngine(testStore(t), midSeries(), picker.Prefs{})
	h := NewHandler(Deps{Source: midSeries(), Engine: engine, Wait: time.Minute})
	gen, _ := engine.Changes()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/view?drawer=1&since="+strconv.FormatUint(gen, 10), nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { h.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a waiting refresh outlived its request")
	}
}

func TestAWaitingRefreshAnswersAsSoonAsTheServerDrains(t *testing.T) {
	// Shutdown lets requests finish, but a refresh waits up to twenty seconds
	// for a change: every restart with a tab open overran its grace period
	// and failed. Draining ends the wait, and the answer goes out at once.
	engine := series.NewEngine(testStore(t), midSeries(), picker.Prefs{})
	draining := make(chan struct{})
	h := NewHandler(Deps{Source: midSeries(), Engine: engine, Wait: time.Minute, Draining: draining})
	gen, _ := engine.Changes()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/view?drawer=1&since="+strconv.FormatUint(gen, 10), nil))
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	close(draining)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a waiting refresh held a draining server open")
	}
	if rec.Code != 200 {
		t.Errorf("status = %d, want the drawer answered as it stands", rec.Code)
	}
}

func TestAWaitingFollowUpAnswersAsSoonAsTheServerDrains(t *testing.T) {
	// A follow-up waits on the library refresh behind its page, which may be
	// slow; a draining server must not wait for it.
	lib := &changingLibrary{}
	src := library.NewCached(lib, time.Hour)
	engine := series.NewEngine(testStore(t), src, picker.Prefs{})
	<-engine.RefreshLibrary()
	release := make(chan struct{})
	defer close(release)
	lib.mu.Lock()
	lib.hold = release
	lib.mu.Unlock()
	engine.RefreshLibrary() // held: the follow-up has something to wait on

	draining := make(chan struct{})
	h := NewHandler(Deps{Source: src, Engine: engine, Wait: time.Minute, Draining: draining})
	_, gen := engine.Library()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/view?after="+strconv.FormatUint(gen, 10), nil))
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	close(draining)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a waiting follow-up held a draining server open")
	}
}

func TestNothingListensWithoutSeriesTracking(t *testing.T) {
	// With no series tracking there is nothing for the drawer to hear, and a
	// listener would only ask, every few seconds, to be told so.
	body := getBody(t, NewHandler(Deps{Source: midSeries()}), "/view")
	if status := between(body, `id="drawer-status"`, `>`); strings.Contains(status, "data-gen") {
		t.Errorf("the status gives the listener a generation to ask from:\n%s", status)
	}
}
