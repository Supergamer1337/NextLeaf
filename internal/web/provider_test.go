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

	if strings.Contains(body, "The Redemption of Time") {
		t.Error("a Grimmory row was answered from Hardcover's catalogue without being asked to")
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
