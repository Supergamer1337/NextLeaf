package series

import (
	"context"
	"testing"

	"nextleaf/internal/library"
	"nextleaf/internal/picker"
)

// shelf is a named backend with no catalogue, like Grimmory.
type shelf struct {
	fakeSource
	name string
}

func (s shelf) Name() string { return s.name }

// catalogue is a named backend that can look series up, like Hardcover. It
// records what it was asked.
type catalogue struct {
	fakeSource
	name    string
	next    library.Entry
	found   bool
	asked   []library.SeriesQuery
	byISBN  map[string][]library.Series
	finds   int
	findErr error
}

func (c *catalogue) Name() string { return c.name }
func (c *catalogue) NextInSeries(_ context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	c.asked = append(c.asked, q)
	return c.next, c.found, nil
}
func (c *catalogue) SeriesByISBN(_ context.Context, isbns []string) (map[string][]library.Series, error) {
	c.finds++
	if c.findErr != nil {
		return nil, c.findErr
	}
	out := map[string][]library.Series{}
	for _, isbn := range isbns {
		if claims, ok := c.byISBN[isbn]; ok {
			out[isbn] = claims
		}
	}
	return out, nil
}

func readOn(src, title, seriesName string, pos float64, isbns ...string) library.Entry {
	e := entry(title, seriesName, pos, src)
	e.Book.Authors = []string{"Cixin Liu"}
	e.Book.ISBNs = isbns
	e.Status, e.FinishedAt = library.StatusRead, day0
	e.Sources = []library.SourceRef{{Name: src}}
	return e
}

func twoProviders(t *testing.T, hc *catalogue, gm shelf) *Engine {
	t.Helper()
	hc.name, gm.name = "hardcover", "grimmory"
	e := NewEngine(openStore(t), library.Combine(hc, gm), picker.Prefs{IncludeNovellas: true})
	e.SourceOrder = []string{"hardcover", "grimmory"}
	return e
}

func TestARowNeverAsksAnotherProvidersCatalogue(t *testing.T) {
	// Grimmory's "Remembrance of Earth's Past" is Grimmory's claim. Hardcover
	// using the same name proves nothing, so it is not asked.
	hc := &catalogue{next: library.Entry{Book: library.Book{Title: "The Redemption of Time"}}, found: true}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Death's End", "Remembrance of Earth's Past", 3)}}}

	v, err := twoProviders(t, hc, gm).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hc.asked) != 0 {
		t.Errorf("hardcover was asked about %d series it does not own", len(hc.asked))
	}
	g := groupNamed(t, v, "Remembrance of Earth's Past")
	if g.NextTitle != "" {
		t.Errorf("Next = %q, want nothing beyond the shelf", g.NextTitle)
	}
	if !g.CaughtUp {
		t.Error("with nothing on the shelf and no catalogue to ask, the row is caught up")
	}
}

func TestARowAsksItsOwnProviderByItsOwnIdentifier(t *testing.T) {
	read := readOn("hardcover", "The Last Wish", "The Witcher", 1)
	read.Book.Series.Slug = "the-witcher"
	hc := &catalogue{fakeSource: fakeSource{reads: []library.Entry{read}}, next: library.Entry{Book: library.Book{Title: "Sword of Destiny"}}, found: true}

	v, err := twoProviders(t, hc, shelf{}).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(hc.asked) != 1 || hc.asked[0].Series.Slug != "the-witcher" || hc.asked[0].Series.Source != "hardcover" {
		t.Fatalf("asked = %+v, want one query carrying hardcover's own identifier", hc.asked)
	}
	g := groupNamed(t, v, "The Witcher")
	if g.NextTitle != "Sword of Destiny" || g.CaughtUp {
		t.Errorf("Next = %q, CaughtUp = %v", g.NextTitle, g.CaughtUp)
	}
}

func TestTheSameSeriesOnAnotherProviderIsAChoice(t *testing.T) {
	// Which provider a series follows decides who is asked what comes next,
	// so the other backend's claim is a real alternative even under one name.
	hc := read("Vol. 8", "Overlord", 8, day0)
	gm := entry("Vol. 8", "Overlord", 8, "grimmory")
	gm.Status, gm.FinishedAt = library.StatusRead, day0
	v := Compute(Input{Reads: []library.Entry{hc, gm}, SourceOrder: []string{"hardcover", "grimmory"}})
	if len(v.Groups) != 1 {
		t.Fatalf("groups = %v, want one row", groupNames(v))
	}
	alts := v.Groups[0].Alternatives
	if len(alts) != 1 || alts[0].Source != "grimmory" || alts[0].Name != "Overlord" {
		t.Errorf("Alternatives = %+v, want grimmory's claim offered", alts)
	}
}

func TestAShelfOnlyRowFindsItsSeriesOnTheCatalogueByISBN(t *testing.T) {
	hcClaim := library.Series{Name: "Remembrance of Earth's Past", Slug: "remembrance", Position: library.At(3), Source: "hardcover"}
	hc := &catalogue{
		byISBN: map[string][]library.Series{"9780765377104": {hcClaim}},
		next:   library.Entry{Book: library.Book{Title: "The Redemption of Time"}}, found: true,
	}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Death's End", "Three-Body", 3, "9780765377104")}}}
	e := twoProviders(t, hc, gm)
	ctx := context.Background()

	v, err := e.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "Three-Body")
	if g.Source != "grimmory" {
		t.Fatalf("Source = %q: a found claim must never take the row over by itself", g.Source)
	}
	if g.ContinueOn == nil || g.ContinueOn.Source != "hardcover" || g.ContinueOn.Name != "Remembrance of Earth's Past" {
		t.Fatalf("ContinueOn = %+v, want hardcover's series offered", g.ContinueOn)
	}
	if len(hc.asked) != 0 {
		t.Error("offering the switch must not already ask hardcover what comes next")
	}

	// Taking the offer is an ordinary switch, and the row now follows hardcover.
	if _, err := e.Decide(ctx, "switch", "Three-Body", g.ContinueOn.Name); err != nil {
		t.Fatalf("switch: %v", err)
	}
	v, err = e.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g = groupNamed(t, v, "Remembrance of Earth's Past")
	if g.Source != "hardcover" || g.NextTitle != "The Redemption of Time" || g.ContinueOn != nil {
		t.Errorf("after the switch: Source = %q, Next = %q, ContinueOn = %+v", g.Source, g.NextTitle, g.ContinueOn)
	}
	if len(hc.asked) != 1 || hc.asked[0].Series.Slug != "remembrance" {
		t.Errorf("asked = %+v, want hardcover asked about its own series", hc.asked)
	}
	if hc.finds != 1 {
		t.Errorf("finds = %d, want the ISBN answer reused", hc.finds)
	}
}

func TestNoOfferWithoutAMatchOrWhileTheShelfStillHasBooks(t *testing.T) {
	hc := &catalogue{byISBN: map[string][]library.Series{}}
	gm := shelf{fakeSource: fakeSource{
		reads:  []library.Entry{readOn("grimmory", "Dune", "Dune", 1, "9780441013593"), readOn("grimmory", "Obscure", "Obscure Saga", 1, "9780000000002")},
		toRead: []library.Entry{entry("Dune Messiah", "Dune", 2, "grimmory")},
	}}
	v, err := twoProviders(t, hc, gm).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, v, "Dune"); g.ContinueOn != nil || g.CaughtUp {
		t.Errorf("Dune: ContinueOn = %+v, CaughtUp = %v; the shelf still has the next book", g.ContinueOn, g.CaughtUp)
	}
	if g := groupNamed(t, v, "Obscure Saga"); g.ContinueOn != nil || !g.CaughtUp {
		t.Errorf("Obscure Saga: ContinueOn = %+v, CaughtUp = %v; nothing was found to offer", g.ContinueOn, g.CaughtUp)
	}
	if hc.finds != 1 {
		t.Errorf("finds = %d, want only the row that ran out looked up", hc.finds)
	}
}

func TestAZeroBudgetFindsNothingNew(t *testing.T) {
	hc := &catalogue{byISBN: map[string][]library.Series{}}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Death's End", "Three-Body", 3, "9780765377104")}}}
	if _, _, err := twoProviders(t, hc, gm).RecommendWithin(context.Background(), false, 0); err != nil {
		t.Fatal(err)
	}
	if hc.finds != 0 {
		t.Errorf("finds = %d, want none on a render that may not wait on the catalogue", hc.finds)
	}
}

func TestTheOfferPrefersTheSeriesOfTheSameName(t *testing.T) {
	// Hardcover files a Mistborn book under the whole Cosmere too, and ranks
	// the bigger series first. The reader is following the trilogy.
	claim := func(name string, pos float64) library.Series {
		return library.Series{Name: name, Position: library.At(pos), Source: "hardcover"}
	}
	hc := &catalogue{byISBN: map[string][]library.Series{"9780765350381": {
		claim("The Cosmere", 16), claim("Mistborn: The Original Trilogy", 3),
	}}}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "The Hero of Ages", "Mistborn: The Original Trilogy", 3, "9780765350381")}}}

	v, err := twoProviders(t, hc, gm).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "Mistborn: The Original Trilogy")
	if g.ContinueOn == nil || g.ContinueOn.Name != "Mistborn: The Original Trilogy" {
		t.Errorf("ContinueOn = %+v, want the same-named series", g.ContinueOn)
	}
}

func TestWithoutASameNamedSeriesTheOfferFollowsTheCataloguesRanking(t *testing.T) {
	claim := func(name string, pos float64) library.Series {
		return library.Series{Name: name, Position: library.At(pos), Source: "hardcover"}
	}
	hc := &catalogue{byISBN: map[string][]library.Series{"9780765350381": {
		claim("Mistborn", 3), claim("Cosmere", 16), // ranked, not alphabetical
	}}}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "The Hero of Ages", "The Final Empire Books", 3, "9780765350381")}}}

	v, err := twoProviders(t, hc, gm).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "The Final Empire Books")
	if g.ContinueOn == nil || g.ContinueOn.Name != "Mistborn" {
		t.Errorf("ContinueOn = %+v, want the catalogue's first choice", g.ContinueOn)
	}
}

func TestAFollowedSeriesIsFoundAgainAfterARestart(t *testing.T) {
	// What was found by ISBN is not stored; the reader's switch is. A fresh
	// process must find the claim again rather than fall back to the name.
	hcClaim := library.Series{Name: "Remembrance of Earth's Past", Slug: "remembrance", Position: library.At(3), Source: "hardcover"}
	newCatalogue := func() *catalogue {
		return &catalogue{
			name:   "hardcover",
			byISBN: map[string][]library.Series{"9780765377104": {hcClaim}},
			next:   library.Entry{Book: library.Book{Title: "The Redemption of Time"}}, found: true,
		}
	}
	gm := shelf{name: "grimmory", fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Death's End", "Three-Body", 7, "9780765377104")}}}
	store := openStore(t)
	ctx := context.Background()

	first := NewEngine(store, library.Combine(newCatalogue(), gm), picker.Prefs{})
	if _, err := first.View(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Decide(ctx, "switch", "Three-Body", hcClaim.Name); err != nil {
		t.Fatalf("switch: %v", err)
	}

	hc := newCatalogue()
	v, err := NewEngine(store, library.Combine(hc, gm), picker.Prefs{}).View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, hcClaim.Name)
	if g.NextTitle != "The Redemption of Time" {
		t.Errorf("Next = %q", g.NextTitle)
	}
	if len(hc.asked) != 1 || hc.asked[0].Series.Slug != "remembrance" {
		t.Fatalf("asked = %+v, want hardcover's own identifier", hc.asked)
	}
	if pos, _ := hc.asked[0].Series.Slot(); pos != 3 {
		t.Errorf("asked after position %v, want 3: hardcover's numbering, not grimmory's 7", pos)
	}
}
