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

	// The row itself is answered by its own provider only. Hardcover's book
	// may be named in the wheel, as the thing a switch would lead to, but
	// never on the row as though Grimmory had offered it.
	for _, line := range nextLines(body) {
		if strings.Contains(line, "The Redemption of Time") {
			t.Errorf("a Grimmory row was answered from Hardcover's catalogue without being asked to: %q", line)
		}
	}
	// The row reads like any other the reader is caught up with; the icon is
	// the only difference.
	if !strings.Contains(section(body, "Finished"), "Three-Body") {
		t.Error("a row with nothing left on its provider belongs under Finished")
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

	if strings.Contains(body, "Checking…") || strings.Contains(body, "drawer-status") {
		t.Error("the drawer claims to be still checking when every answer is in")
	}
	if strings.Contains(body, "drawer=1") {
		t.Error("the drawer keeps polling after it has everything")
	}
}
