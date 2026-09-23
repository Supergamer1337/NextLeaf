package web

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"nextleaf/internal/library"
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
	h := ready(t, shelfAndCatalogue(), testStore(t))
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
	h := ready(t, shelfAndCatalogue(), testStore(t))
	body := getBody(t, h, "/view")

	current := between(body, `data-to=""`, `</div>`)
	if !strings.Contains(current, "Nothing left to read") {
		t.Errorf("the tracked identity does not say it has run out:\n%s", current)
	}
	alt := between(body, `data-to="Remembrance`, `</div>`)
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
	return library.Entry{}, false, context.DeadlineExceeded
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
	// unless it says otherwise. The drawer says so for the whole panel too,
	// since the row may be inside a section the reader has collapsed.
	h := ready(t, waitingLibrary(), testStore(t))
	body := getBody(t, h, "/view")

	if !strings.Contains(body, "Checking…") {
		t.Error("a row waiting on a lookup renders silent, as though it held nothing")
	}
	if !strings.Contains(body, "drawer-status") {
		t.Error("the drawer does not say that answers are still coming")
	}
	// And it refreshes itself: the state changes on its own, so a reader who
	// leaves the page open must not be left with a stale one.
	if !strings.Contains(body, `hx-get="/view?drawer=1"`) {
		t.Error("nothing refreshes the drawer while it is still filling in")
	}
	// But never under a reader spinning a wheel: a swap closes it.
	if !strings.Contains(body, `every 20s [!document.querySelector('.drawer-row.spinning')]`) {
		t.Error("the refresh fires even while a wheel is open, and would close it")
	}
	// Re-inserted every twenty seconds, a live region would be announced
	// every twenty seconds.
	if strings.Contains(between(body, `class="drawer-status"`, `>`), "role=") {
		t.Error("the status line is a live region, re-announced on every refresh")
	}

	// The refresh only touches the drawer: re-rendering the card would deal
	// the reader a different book every twenty seconds.
	drawer := getBody(t, h, "/view?drawer=1")
	if strings.Contains(drawer, "Recommended") || strings.Contains(drawer, `id="deck"`) {
		t.Error("the drawer refresh re-renders the recommendation card")
	}
	if !strings.Contains(drawer, "The Witcher") {
		t.Error("the drawer refresh does not carry the rows")
	}
}

func TestASettledDrawerSaysNothingAndStopsRefreshing(t *testing.T) {
	h := ready(t, midSeries(), testStore(t))
	body := getBody(t, h, "/view")

	if strings.Contains(body, "Checking…") || strings.Contains(body, "drawer-status") || strings.Contains(body, "pending-dot") {
		t.Error("the drawer claims to be still checking when every answer is in")
	}
	if strings.Contains(body, "drawer=1") {
		t.Error("the drawer keeps polling after it has everything")
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
	body := getBody(t, ready(t, twoClaimsOneAnswered(), testStore(t)), "/view")
	row := between(body, `<span class="drawer-name">The Lord of the Rings</span>`, `class="row-tags"`)
	if !strings.Contains(row, "pending-dot") {
		t.Errorf("a row with series still being checked is not marked:\n%s", row)
	}
	if !strings.Contains(body, "Next: The Two Towers") {
		t.Error("the row's own answer should still show")
	}
}

func TestACollapsedSectionSaysItHoldsAnUnsettledSeries(t *testing.T) {
	// Finished starts folded, so a marker on the row alone would be hidden.
	body := getBody(t, ready(t, finishedAndUnchecked(), testStore(t)), "/view")
	summary := between(body, `data-group="Finished"`, `</summary>`)
	if !strings.Contains(summary, "pending-dot") {
		t.Errorf("the folded Finished section does not say it holds a series still being checked:\n%s", summary)
	}
	if strings.Contains(between(body, `data-group="Current"`, `</summary>`), "pending-dot") {
		t.Error("a section with nothing unsettled is marked")
	}
}

func TestAContinuableSeriesIsCountedApartAndCanBeTurnedDown(t *testing.T) {
	h := ready(t, shelfAndCatalogue(), testStore(t))
	body := getBody(t, h, "/view")

	sec := section(body, "Continues elsewhere")
	if !strings.Contains(sec, `<span class="drawer-tally">1</span>`) {
		t.Errorf("the section does not count what could still go on:\n%s", sec)
	}
	if !strings.Contains(body, "Next on Hardcover: The Redemption of Time") {
		t.Error("the row does not say what it would continue with, or where")
	}
	if !strings.Contains(body, `hx-post="/series/stop"`) {
		t.Fatal("there is no way to say no to continuing it")
	}

	// Turning it down files it with the finished ones, for good.
	rec := post(t, h, "/series/stop", url.Values{"name": {"Three-Body"}, "from": {"drawer"}})
	if rec.Code != 200 {
		t.Fatalf("stop: status = %d, body = %s", rec.Code, rec.Body)
	}
	after := rec.Body.String()
	if !strings.Contains(section(after, "Finished"), "Three-Body") {
		t.Error("a stopped series is not filed with the finished ones")
	}
	if strings.Contains(after, "Continues elsewhere") || strings.Contains(after, "row-follow") {
		t.Error("a stopped series is still offered for continuing")
	}
	// And it can be taken back.
	if !strings.Contains(section(after, "Finished"), `hx-post="/series/clear"`) {
		t.Error("a stopped series has no way to be offered again")
	}
	rec = post(t, h, "/series/clear", url.Values{"name": {"Three-Body"}, "from": {"drawer"}})
	if !strings.Contains(section(rec.Body.String(), "Continues elsewhere"), "Three-Body") {
		t.Error("clearing the stop does not bring the offer back")
	}
}
