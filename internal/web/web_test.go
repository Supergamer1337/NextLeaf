package web

import (
	"context"
	"errors"
	"io"
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
	next  library.Entry
	found bool
}

func (s resolverStub) NextInSeries(_ context.Context, _ library.SeriesQuery) (library.Entry, bool, error) {
	return s.next, s.found, nil
}

func seriesEntry(title, series string, pos float64) library.Entry {
	return library.Entry{Book: library.Book{Title: title, Series: &library.Series{Name: series, Position: library.At(pos)}}}
}

// get exercises a fully configured app: a source, a series store, and a
// history import that has already finished.
func get(t *testing.T, src library.Source, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	ready(t, src, testStore(t)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want %d", target, rec.Code, http.StatusOK)
	}
	return rec
}

// coverStub adds the optional CoverProvider capability to stubSource.
type coverStub struct {
	stubSource
	lastID string
	body   string
	ct     string
	err    error
}

func (s *coverStub) Name() string { return "grimmory" }
func (s *coverStub) CoverImage(_ context.Context, id string) (io.ReadCloser, string, error) {
	s.lastID = id
	if s.err != nil {
		return nil, "", s.err
	}
	body, ct := s.body, s.ct
	if body == "" {
		body, ct = "jpeg-bytes", "image/jpeg"
	}
	return io.NopCloser(strings.NewReader(body)), ct, nil
}

func TestCoverRouteStreamsImage(t *testing.T) {
	src := &coverStub{}
	rec := get(t, src, "/cover/grimmory/7")

	if body := rec.Body.String(); body != "jpeg-bytes" {
		t.Errorf("body = %q, want the provider's bytes", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("Cache-Control = %q, want a max-age so browsers cache covers", cc)
	}
	if src.lastID != "7" {
		t.Errorf("provider saw id %q, want 7", src.lastID)
	}
}

func TestCoverRouteSniffsMislabeledImages(t *testing.T) {
	// Grimmory labels cover bytes application/json; only trust image/* types
	// and let the response writer sniff the rest.
	jpegMagic := "\xff\xd8\xff\xe0\x00\x10JFIFrest-of-image"
	src := &coverStub{body: jpegMagic, ct: "application/json"}
	rec := get(t, src, "/cover/grimmory/7")

	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want sniffed image/jpeg", ct)
	}
}

func TestCoverRouteNotFoundCases(t *testing.T) {
	cases := map[string]struct {
		src    library.Source
		target string
	}{
		"unknown source name":  {&coverStub{}, "/cover/nope/7"},
		"source has no covers": {stubSource{}, "/cover/stub/7"},
		"unconfigured app":     {nil, "/cover/grimmory/7"},
		"provider error":       {&coverStub{err: errors.New("boom")}, "/cover/grimmory/7"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			rec := httptest.NewRecorder()
			NewHandler(Deps{Source: tc.src}).ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
		})
	}
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
	NewHandler(Deps{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (root pattern must not be a catch-all)", rec.Code, http.StatusNotFound)
	}
}

func TestSelectorUnconfigured(t *testing.T) {
	rec := get(t, nil, "/view")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, "HARDCOVER_TOKEN") {
		t.Errorf("unconfigured page should mention HARDCOVER_TOKEN:\n%s", body)
	}
}

func TestSelectorSourceError(t *testing.T) {
	src := stubSource{toReadErr: errors.New("boom")}
	rec := get(t, src, "/view")
	if body := rec.Body.String(); !strings.Contains(body, "boom") {
		t.Errorf("error page should surface the failure:\n%s", body)
	}
}

func TestSelectorEmptyList(t *testing.T) {
	rec := get(t, stubSource{}, "/view")
	if body := rec.Body.String(); !strings.Contains(body, "Want-to-Read list is empty") {
		t.Errorf("empty page should invite adding books:\n%s", body)
	}
}

func TestSelectorContinuesSeriesFromShelf(t *testing.T) {
	src := stubSource{
		reads:  []library.Entry{seriesEntry("The Fifth Season", "The Broken Earth", 1)},
		toRead: []library.Entry{seriesEntry("The Obelisk Gate", "The Broken Earth", 2)},
	}
	body := get(t, src, "/view").Body.String()
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
		next:       library.Entry{Book: library.Book{Title: "The Obelisk Gate", Series: &library.Series{Name: "The Broken Earth", Position: library.At(2)}}},
		found:      true,
	}
	body := get(t, src, "/view").Body.String()
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
	body := get(t, src, "/view?another=1").Body.String()

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
	body := get(t, src, "/view?another=1").Body.String()

	if !strings.Contains(body, "Trade-offs") {
		t.Errorf("a pick with cons should show a 'Trade-offs' section:\n%s", body)
	}
	if !strings.Contains(body, "Leans into Fantasy") {
		t.Errorf("trade-off reason should be present:\n%s", body)
	}
}

func TestSelectorShowsShelfBadgeAndSources(t *testing.T) {
	src := stubSource{
		toRead: []library.Entry{{
			Book:      library.Book{Title: "Shelf Book"},
			Sources:   []library.SourceRef{{Name: "grimmory"}},
			Available: true,
		}},
	}
	body := get(t, src, "/view?another=1").Body.String()

	if !strings.Contains(body, "On your shelf") {
		t.Errorf("an available book should carry the shelf badge:\n%s", body)
	}
	if !strings.Contains(body, "Grimmory") {
		t.Errorf("the source name should be shown, capitalized:\n%s", body)
	}
}

func TestSelectorShowsSourcesWithoutBadge(t *testing.T) {
	src := stubSource{
		toRead: []library.Entry{{
			Book:    library.Book{Title: "Wishlist Book"},
			Sources: []library.SourceRef{{Name: "hardcover"}},
		}},
	}
	body := get(t, src, "/view?another=1").Body.String()

	if !strings.Contains(body, "Hardcover") {
		t.Errorf("the source name should be shown:\n%s", body)
	}
	if strings.Contains(body, "On your shelf") {
		t.Errorf("a non-available book must not claim to be on the shelf:\n%s", body)
	}
}

func TestSelectorJoinsMergedSources(t *testing.T) {
	src := stubSource{
		toRead: []library.Entry{{
			Book:      library.Book{Title: "Both Places"},
			Sources:   []library.SourceRef{{Name: "grimmory"}, {Name: "hardcover"}},
			Available: true,
		}},
	}
	body := get(t, src, "/view?another=1").Body.String()

	if !strings.Contains(body, "Grimmory · Hardcover") {
		t.Errorf("merged sources should be joined:\n%s", body)
	}
}

func TestSelectorShowsDescription(t *testing.T) {
	src := stubSource{
		toRead: []library.Entry{{Book: library.Book{Title: "Pick", Description: "A world ends in ash."}}},
	}
	body := get(t, src, "/view?another=1").Body.String()
	if !strings.Contains(body, "A world ends in ash.") || !strings.Contains(body, `class="rec-desc"`) {
		t.Errorf("description should render below the card:\n%s", body)
	}
}

func TestSelectorOmitsEmptyDescription(t *testing.T) {
	src := stubSource{
		toRead: []library.Entry{{Book: library.Book{Title: "Pick"}}},
	}
	body := get(t, src, "/view?another=1").Body.String()
	if strings.Contains(body, `class="rec-desc"`) {
		t.Errorf("no description element should render when there is none:\n%s", body)
	}
}

func TestSelectorHasNoViewDetailsLink(t *testing.T) {
	// Source chips are the canonical links now; the redundant button is gone.
	src := stubSource{
		toRead: []library.Entry{{
			Book:    library.Book{Title: "Chipped", URL: "http://gm.local/book/7"},
			Sources: []library.SourceRef{{Name: "grimmory", URL: "http://gm.local/book/7"}},
		}},
	}
	body := get(t, src, "/view?another=1").Body.String()
	if strings.Contains(body, "View details") {
		t.Errorf("View details should no longer render:\n%s", body)
	}
}

func TestSelectorSourceChipLinksToSource(t *testing.T) {
	src := stubSource{
		toRead: []library.Entry{{
			Book:    library.Book{Title: "Linked Book"},
			Sources: []library.SourceRef{{Name: "grimmory", URL: "http://gm.local/book/7"}},
		}},
	}
	body := get(t, src, "/view?another=1").Body.String()

	if !strings.Contains(body, `href="http://gm.local/book/7"`) {
		t.Errorf("chip should link to the source's book page:\n%s", body)
	}
	if !strings.Contains(body, `class="rec-source" href="http://gm.local/book/7" target="_blank" rel="noopener">Grimmory</a>`) {
		t.Errorf("chip link should open safely in a new tab:\n%s", body)
	}
}

func TestSelectorSourceWithoutURLIsPlainText(t *testing.T) {
	src := stubSource{
		toRead: []library.Entry{{
			Book:    library.Book{Title: "Unlinked Book"},
			Sources: []library.SourceRef{{Name: "hardcover"}},
		}},
	}
	body := get(t, src, "/view?another=1").Body.String()

	if !strings.Contains(body, "Hardcover") {
		t.Errorf("source name should still render:\n%s", body)
	}
	if strings.Contains(body, `<a class="rec-source"`) {
		t.Errorf("a URL-less source must not render as a link:\n%s", body)
	}
}

func TestSelectorResolverPickShowsSourceWithoutBadge(t *testing.T) {
	// The off-shelf resolver book is on none of the user's lists, but the
	// resolving source still gets credited — and linked.
	src := resolverStub{
		stubSource: stubSource{
			reads: []library.Entry{seriesEntry("Book One", "Saga", 1)},
		},
		next: library.Entry{
			Book:    library.Book{Title: "Off-Shelf Two", Series: &library.Series{Name: "Saga", Position: library.At(2)}},
			Sources: []library.SourceRef{{Name: "hardcover", URL: "https://hardcover.app/books/two"}},
		},
		found: true,
	}
	body := get(t, src, "/view").Body.String()

	if !strings.Contains(body, `href="https://hardcover.app/books/two"`) || !strings.Contains(body, "Hardcover") {
		t.Errorf("the resolving source should show as a linked chip:\n%s", body)
	}
	if strings.Contains(body, "On your shelf") {
		t.Errorf("an off-shelf book must not claim to be on the shelf:\n%s", body)
	}
}

func TestSelectorContinuationKeepsProvenance(t *testing.T) {
	next := seriesEntry("Book Two", "Saga", 2)
	next.Sources = []library.SourceRef{{Name: "grimmory"}}
	next.Available = true
	src := stubSource{
		reads:  []library.Entry{seriesEntry("Book One", "Saga", 1)},
		toRead: []library.Entry{next},
	}
	body := get(t, src, "/view").Body.String()

	if !strings.Contains(body, "Continues") {
		t.Fatalf("expected a series continuation:\n%s", body)
	}
	if !strings.Contains(body, "On your shelf") || !strings.Contains(body, "Grimmory") {
		t.Errorf("an on-shelf continuation should keep badge and source:\n%s", body)
	}
}

func TestSelectorRerollUsesVariety(t *testing.T) {
	// A series is active, but ?another=1 must skip it and pick from the TBR.
	src := stubSource{
		reads:  []library.Entry{seriesEntry("The Fifth Season", "The Broken Earth", 1)},
		toRead: []library.Entry{{Book: library.Book{Title: "A Standalone Pick"}}},
	}
	body := get(t, src, "/view?another=1").Body.String()
	if !strings.Contains(body, "A Standalone Pick") {
		t.Errorf("reroll should pick from the TBR:\n%s", body)
	}
	if strings.Contains(body, "Continues") {
		t.Errorf("reroll should not offer a series continuation:\n%s", body)
	}
}

// countingSource records whether a handler touched the library at all.
type countingSource struct {
	stubSource
	reads int
}

func (c *countingSource) ToRead(ctx context.Context) ([]library.Entry, error) {
	c.reads++
	return c.stubSource.ToRead(ctx)
}

func (c *countingSource) RecentReads(ctx context.Context, n int) ([]library.Entry, error) {
	c.reads++
	return c.stubSource.RecentReads(ctx, n)
}

// The shell is the same document for everyone. It reads no source, so it can
// neither be slow nor fail, whatever state the library is in.
func TestShellIsConstantAndReadsNoSource(t *testing.T) {
	src := &countingSource{stubSource: midSeries()}
	h := ready(t, src, testStore(t))

	first, second := getBody(t, h, "/"), getBody(t, h, "/")
	if first != second {
		t.Error("the shell differs between requests, so it is not constant")
	}
	if src.reads > 0 {
		t.Errorf("the shell read the source %d times, want 0", src.reads)
	}
	// The drawer shell is part of the page, but nothing that depends on the
	// library may be: no card, and no series rows.
	if strings.Contains(first, `class="rec-title"`) || strings.Contains(first, `class="drawer-row"`) {
		t.Error("the shell carries card or series content, which belongs to /view")
	}
	if !strings.Contains(first, `<div class="drawer-body" id="drawer-body"></div>`) {
		t.Error("the shell has no empty drawer body for the fragment to fill")
	}
	if !strings.Contains(first, `hx-get="/view"`) {
		t.Error("the shell never fetches the view, so the card would never arrive")
	}
}

// The shell owns the only skeleton, shown before the first card. The fragment
// must not carry one: after the first load the card stays on screen and is
// morphed in place, never blanked while a request is in flight.
func TestOnlyTheShellCarriesASkeleton(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	if shell := getBody(t, h, "/"); !strings.Contains(shell, `class="waiting waiting--initial"`) {
		t.Error("the shell has no skeleton to show before the first card arrives")
	}
	if frag := getBody(t, h, "/view"); strings.Contains(frag, `class="waiting`) {
		t.Error("the fragment carries a skeleton, so a swap would blank the card instead of morphing it")
	}
}

// The app is essentially one verb, so declining a variety pick deserves a
// key: the button listens for "n" anywhere on the page, declared in the
// markup itself.
func TestNotNowListensForAKey(t *testing.T) {
	src := stubSource{toRead: []library.Entry{{Book: library.Book{Title: "A Standalone Pick"}}}}
	body := getBody(t, ready(t, src, testStore(t)), "/view")
	if !strings.Contains(body, "keyup[key=='n'] from:body") {
		t.Error("the reroll button has no keyboard trigger")
	}
}

// The fragment is document-free: it is swapped into a page that already has a
// head and a body.
func TestViewIsAFragmentNotAPage(t *testing.T) {
	body := getBody(t, ready(t, midSeries(), testStore(t)), "/view")
	for _, tag := range []string{"<html", "<head", "<body", "<!DOCTYPE"} {
		if strings.Contains(body, tag) {
			t.Errorf("the fragment contains %s, so it would nest a document in the page", tag)
		}
	}
}

// The notice slot comes back empty from every successful render, so a refusal
// clears itself as soon as something works.
func TestTheNoticeSlotIsEmptyOnASuccessfulRender(t *testing.T) {
	body := getBody(t, ready(t, midSeries(), testStore(t)), "/view")
	if !strings.Contains(body, `<div id="flash"></div>`) {
		t.Error("the notice slot is not empty, so an earlier refusal would persist")
	}
}
