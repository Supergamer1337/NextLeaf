package series

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

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
	// answer overrides next/found per query, for a catalogue whose reply
	// depends on which of its series is asked about.
	answer  func(library.SeriesQuery) (library.Entry, bool)
	nextErr error
	// failing makes only the series it names fail.
	failing map[string]bool
}

func (c *catalogue) Name() string { return c.name }
func (c *catalogue) NextInSeries(_ context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	c.asked = append(c.asked, q)
	if c.nextErr != nil {
		return library.Entry{}, false, c.nextErr
	}
	if c.failing[q.Series.Name] {
		return library.Entry{}, false, errors.New("rate limited")
	}
	if c.answer != nil {
		e, ok := c.answer(q)
		return e, ok, nil
	}
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
	// The offer is only made once hardcover has been asked, and answered with
	// a book: an unchecked offer leads the reader back to "nothing left".
	if len(hc.asked) != 1 || hc.asked[0].Series.Slug != "remembrance" {
		t.Fatalf("asked = %+v, want the offer checked against hardcover's own series", hc.asked)
	}
	if pos, _ := hc.asked[0].Series.Slot(); pos != 3 {
		t.Errorf("checked after position %v, want 3: where the row would stand on hardcover", pos)
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
	// The check and the row's own lookup are the same question, so following
	// the offer costs no second round trip.
	if len(hc.asked) != 1 {
		t.Errorf("asked = %+v, want the checked answer reused after the switch", hc.asked)
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
	}}, next: library.Entry{Book: library.Book{Title: "The Alloy of Law"}}, found: true}
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
	}}, next: library.Entry{Book: library.Book{Title: "The Alloy of Law"}}, found: true}
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

func TestAFollowedSeriesIsFoundAgainWhenTheCacheIsLost(t *testing.T) {
	// The lookup cache is disposable; the reader's switch is not. With the
	// cache gone, a fresh process must find the claim again rather than fall
	// back to the name.
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
	if err := store.PruneCache(ctx, time.Now().AddDate(100, 0, 0)); err != nil {
		t.Fatal(err)
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

func TestNoOfferWhenTheCatalogueHasNothingAfterThePosition(t *testing.T) {
	// Hardcover knows the series and ends it where the reader does. Offering
	// the switch anyway lands them back on the same "nothing left to read",
	// one press later.
	hc := &catalogue{byISBN: map[string][]library.Series{
		"9780765377104": {{Name: "Remembrance of Earth's Past", Slug: "remembrance", Position: library.At(3), Source: "hardcover"}},
	}}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Death's End", "Three-Body", 3, "9780765377104")}}}

	v, err := twoProviders(t, hc, gm).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "Three-Body")
	if g.ContinueOn != nil {
		t.Errorf("ContinueOn = %+v, want no offer: hardcover has nothing after book 3", g.ContinueOn)
	}
	if !g.CaughtUp {
		t.Error("the row is still finished, and says so")
	}
}

func TestAnUnplacedFoundClaimIsNotOffered(t *testing.T) {
	// A claim without a slot cannot be asked what comes after it, so the row
	// would switch into silence. It is not offered.
	hc := &catalogue{
		byISBN: map[string][]library.Series{
			"9780765377104": {{Name: "Remembrance of Earth's Past", Slug: "remembrance", Source: "hardcover"}},
		},
		next: library.Entry{Book: library.Book{Title: "The Redemption of Time"}}, found: true,
	}
	read := readOn("grimmory", "Death's End", "Three-Body", 3, "9780765377104")
	read.Book.Series.Position = nil
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{read}}}

	v, err := twoProviders(t, hc, gm).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, v, "Three-Body"); g.ContinueOn != nil {
		t.Errorf("ContinueOn = %+v, want no offer for an unplaced series", g.ContinueOn)
	}
	if len(hc.asked) != 0 {
		t.Errorf("asked = %+v, want no lookup: there is no position to ask after", hc.asked)
	}
}

func TestAnUncheckedOfferWaitsForBudgetRatherThanBeingMade(t *testing.T) {
	// One lookup is enough to find the series by ISBN but not to check what it
	// offers. The row waits for the warm pass rather than promising a book
	// nobody has confirmed.
	hc := &catalogue{
		byISBN: map[string][]library.Series{
			"9780765377104": {{Name: "Remembrance of Earth's Past", Slug: "remembrance", Position: library.At(3), Source: "hardcover"}},
		},
		next: library.Entry{Book: library.Book{Title: "The Redemption of Time"}}, found: true,
	}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Death's End", "Three-Body", 3, "9780765377104")}}}
	e := twoProviders(t, hc, gm)
	ctx := context.Background()

	v, err := e.viewWithin(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, v, "Three-Body"); g.ContinueOn != nil {
		t.Errorf("ContinueOn = %+v, want no offer before it can be checked", g.ContinueOn)
	}

	v, err = e.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, v, "Three-Body"); g.ContinueOn == nil {
		t.Error("with budget to check it, the offer is made")
	}
}

func TestAnOrdinaryRenderLeavesAHealthyRowsOtherProvidersAlone(t *testing.T) {
	// The shelf still has the next book, so nothing about this row is urgent.
	// A request must not spend its budget widening the switcher.
	hc := &catalogue{byISBN: map[string][]library.Series{
		"9780441013593": {{Name: "Dune Chronicles", Slug: "dune-chronicles", Position: library.At(1), Source: "hardcover"}},
	}, next: library.Entry{Book: library.Book{Title: "Children of Dune"}}, found: true}
	gm := shelf{fakeSource: fakeSource{
		reads:  []library.Entry{readOn("grimmory", "Dune", "Dune", 1, "9780441013593")},
		toRead: []library.Entry{entry("Dune Messiah", "Dune", 2, "grimmory")},
	}}

	v, err := twoProviders(t, hc, gm).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hc.finds != 0 {
		t.Errorf("finds = %d, want none on a request for a row that is not stuck", hc.finds)
	}
	if g := groupNamed(t, v, "Dune"); len(g.Alternatives) != 0 {
		t.Errorf("Alternatives = %+v, want none until the warm pass has looked", g.Alternatives)
	}
}

func TestTheWarmPassWidensEveryRowsSwitcher(t *testing.T) {
	hc := &catalogue{byISBN: map[string][]library.Series{
		"9780441013593": {{Name: "Dune Chronicles", Slug: "dune-chronicles", Position: library.At(1), Source: "hardcover"}},
	}, next: library.Entry{Book: library.Book{Title: "Children of Dune", Series: &library.Series{Position: library.At(3)}}}, found: true}
	gm := shelf{fakeSource: fakeSource{
		reads:  []library.Entry{readOn("grimmory", "Dune", "Dune", 1, "9780441013593")},
		toRead: []library.Entry{entry("Dune Messiah", "Dune", 2, "grimmory")},
	}}
	e := twoProviders(t, hc, gm)
	ctx := context.Background()

	v, err := e.compute(ctx, 1<<20, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "Dune")
	if len(g.Alternatives) != 1 || g.Alternatives[0].Name != "Dune Chronicles" {
		t.Fatalf("Alternatives = %+v, want hardcover's series offered on a row that is not stuck", g.Alternatives)
	}
	// Knowing where a switch would land is the point: the wheel says what
	// each identity holds next, so the choice is made with both in view.
	if alt := g.Alternatives[0]; alt.NextTitle != "Children of Dune" || alt.NextLabel() != "Next: Children of Dune, book 3" {
		t.Errorf("alternative offers %q (%q), want hardcover's next book", alt.NextTitle, alt.NextLabel())
	}

	// The answers are cached, so an ordinary render afterwards costs nothing.
	before := len(hc.asked)
	if _, err := e.View(ctx); err != nil {
		t.Fatal(err)
	}
	if len(hc.asked) != before {
		t.Errorf("asked %d more times on a render that should read the cache", len(hc.asked)-before)
	}
}

func TestAnIdentityWithNothingLeftSaysSoInTheWheel(t *testing.T) {
	hc := &catalogue{byISBN: map[string][]library.Series{
		"9780441013593": {{Name: "Dune Chronicles", Slug: "dune-chronicles", Position: library.At(1), Source: "hardcover"}},
	}}
	gm := shelf{fakeSource: fakeSource{
		reads:  []library.Entry{readOn("grimmory", "Dune", "Dune", 1, "9780441013593")},
		toRead: []library.Entry{entry("Dune Messiah", "Dune", 2, "grimmory")},
	}}

	v, err := twoProviders(t, hc, gm).compute(context.Background(), 1<<20, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "Dune")
	if len(g.Alternatives) != 1 {
		t.Fatalf("Alternatives = %+v", g.Alternatives)
	}
	if label := g.Alternatives[0].NextLabel(); label != "Nothing left to read" {
		t.Errorf("NextLabel = %q, want the dead end named before it is taken", label)
	}
}

func TestAnIdentityWithNoAnswerComingSaysNothing(t *testing.T) {
	// Neither "nothing left", which would talk the reader out of a series
	// that may have books waiting, nor "checking", which would never end.
	alt := Alternative{Name: "Dune Chronicles", Source: "hardcover"}
	if label := alt.NextLabel(); label != "" {
		t.Errorf("NextLabel = %q, want nothing said", label)
	}
}

func TestTheArrowSkipsACandidateThatLeadsNowhere(t *testing.T) {
	// Hardcover ranks the umbrella first, and it is finished; the trilogy
	// under it is not. The offer follows the one that leads somewhere, on the
	// render the reader is looking at — a row that has run out is exactly the
	// one they need an answer for.
	claim := func(name, slug string, pos float64) library.Series {
		return library.Series{Name: name, Slug: slug, Position: library.At(pos), Source: "hardcover"}
	}
	hc := &catalogue{byISBN: map[string][]library.Series{"9780765350381": {
		claim("The Cosmere", "cosmere", 16), claim("Mistborn", "mistborn", 3),
	}}}
	hc.answer = func(q library.SeriesQuery) (library.Entry, bool) {
		return library.Entry{Book: library.Book{Title: "The Alloy of Law"}}, q.Series.Slug == "mistborn"
	}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "The Hero of Ages", "The Final Empire Books", 3, "9780765350381")}}}
	v, err := twoProviders(t, hc, gm).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "The Final Empire Books")
	if g.ContinueOn == nil || g.ContinueOn.Name != "Mistborn" {
		t.Errorf("ContinueOn = %+v, want the candidate that actually has a book", g.ContinueOn)
	}
}

// manyClaims is a shelf-only row whose books hardcover files under several
// series, only one of which has anything left.
func manyClaims(live string, dead ...string) *catalogue {
	claims := []library.Series{}
	for _, name := range append([]string{live}, dead...) {
		claims = append(claims, library.Series{Name: name, Slug: key(name), Position: library.At(3), Source: "hardcover"})
	}
	hc := &catalogue{byISBN: map[string][]library.Series{"9780000000001": claims}}
	hc.answer = func(q library.SeriesQuery) (library.Entry, bool) {
		return library.Entry{Book: library.Book{Title: "Book Four"}}, q.Series.Slug == key(live)
	}
	return hc
}

func TestARequestChecksTheCandidateTheOfferWouldTakeFirst(t *testing.T) {
	// The alternatives are sorted by name for display; spending the budget in
	// that order would leave the one the offer prefers unasked, and the row
	// showing no way on at all.
	hc := manyClaims("Zzz Saga", "Alpha", "Beta", "Gamma")
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Book Three", "Zzz Saga", 3, "9780000000001")}}}

	v, err := twoProviders(t, hc, gm).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "Zzz Saga")
	if g.ContinueOn == nil || g.ContinueOn.Name != "Zzz Saga" {
		t.Errorf("ContinueOn = %+v, want the preferred candidate offered", g.ContinueOn)
	}
	// Asking stops at the first candidate that leads somewhere: the arrow is
	// decided, and the rest is wheel detail the warm pass can fill in.
	if len(hc.asked) != 1 || hc.asked[0].Series.Slug != "zzz saga" {
		t.Errorf("asked = %+v, want one lookup, spent on the preferred candidate", hc.asked)
	}
}

func TestOneStuckRowCannotStarveTheRowsBelowIt(t *testing.T) {
	// Every row's own next book is looked up before any row's candidates, so
	// a stuck row sorted first cannot spend the request and leave the rows
	// under it silent.
	hc := manyClaims("Zzz Saga", "Alpha", "Beta", "Gamma")
	hc.fakeSource = fakeSource{reads: []library.Entry{readOn("hardcover", "The Last Wish", "The Witcher", 1)}}
	hc.answer = func(q library.SeriesQuery) (library.Entry, bool) {
		return library.Entry{Book: library.Book{Title: "Sword of Destiny"}}, q.Series.Name == "The Witcher" || q.Series.Slug == "zzz saga"
	}
	stuck := readOn("grimmory", "Book Three", "Zzz Saga", 3, "9780000000001")
	stuck.FinishedAt = day2 // sorts above the hardcover row
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{stuck}}}

	v, err := twoProviders(t, hc, gm).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, v, "The Witcher"); g.NextTitle != "Sword of Destiny" {
		t.Errorf("The Witcher: Next = %q, CaughtUp = %v; the row below was starved of its own lookup", g.NextTitle, g.CaughtUp)
	}
}

func TestAFailingCatalogueIsStillChargedToTheBudget(t *testing.T) {
	// A failure is a round trip like any other. Left uncharged, a backend
	// that is down would be asked once per candidate on a single render.
	hc := manyClaims("Zzz Saga", "Alpha", "Beta", "Gamma", "Delta", "Epsilon")
	hc.nextErr = errors.New("rate limited")
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Book Three", "Zzz Saga", 3, "9780000000001")}}}

	if _, err := twoProviders(t, hc, gm).View(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(hc.asked) > maxRequestLookups {
		t.Errorf("asked %d times on one request, want the failures charged to the budget of %d", len(hc.asked), maxRequestLookups)
	}
}

func TestADroppedSeriesIsLookedUpByNobody(t *testing.T) {
	hc := manyClaims("Zzz Saga")
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Book Three", "Zzz Saga", 3, "9780000000001")}}}
	e := twoProviders(t, hc, gm)
	ctx := context.Background()
	if _, err := e.Decide(ctx, "drop", "Zzz Saga", ""); err != nil {
		t.Fatal(err)
	}

	if _, err := e.compute(ctx, 1<<20, 0, true); err != nil {
		t.Fatal(err)
	}
	if len(hc.asked) != 0 || hc.finds != 0 {
		t.Errorf("asked = %d, finds = %d; a dropped series offers nothing, so nothing is looked up for it", len(hc.asked), hc.finds)
	}
}

func TestATwinWithNoCatalogueSaysNothingInTheWheel(t *testing.T) {
	// Grimmory's row is "caught up" only because nobody could ask it. Passing
	// that on as the twin's answer would label a switch a dead end when it
	// leads to a book.
	gmRead := readOn("grimmory", "Dune", "Dune", 1)
	hcRead := readOn("hardcover", "Dune Messiah", "Dune", 2)
	hc := &catalogue{
		fakeSource: fakeSource{
			reads:  []library.Entry{hcRead},
			toRead: []library.Entry{entry("Children of Dune", "Dune", 3, "hardcover")},
		},
	}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{gmRead}}}

	v, err := twoProviders(t, hc, gm).compute(context.Background(), 1<<20, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range v.Groups {
		for _, alt := range g.Alternatives {
			if alt.Source == "grimmory" && alt.NextLabel() != "" {
				t.Errorf("%q's grimmory twin says %q, but grimmory has no catalogue to say it with", g.Name, alt.NextLabel())
			}
		}
	}
}

func TestAnIdentityIsAskedAboutAtItsOwnNumbering(t *testing.T) {
	// The umbrella numbers this reader at 15 and the sub-series at 3. Asking
	// the sub-series what follows book 15 walks off the end of it, and the
	// wheel would call a live series finished.
	sub := library.Series{Name: "Subseries", Slug: "sub", Position: library.At(3), Source: "hardcover"}
	umbrella := library.Series{Name: "Umbrella", Slug: "umbrella", Position: library.At(15), Source: "hardcover"}
	placed := readOn("grimmory", "Book Three", "Shelf Series", 3, "9780000000001")
	unplaced := readOn("grimmory", "Side Story", "Shelf Series", 4, "9780000000002")
	hc := &catalogue{byISBN: map[string][]library.Series{
		"9780000000001": {sub, umbrella},
		"9780000000002": {umbrella},
	}}
	hc.answer = func(q library.SeriesQuery) (library.Entry, bool) {
		after, _ := q.Series.Slot()
		return library.Entry{Book: library.Book{Title: "Book Four"}}, q.Series.Slug == "sub" && after == 3
	}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{placed, unplaced}}}

	v, err := twoProviders(t, hc, gm).compute(context.Background(), 1<<20, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "Shelf Series")
	for _, alt := range g.Alternatives {
		if alt.Name == "Subseries" && alt.NextTitle != "Book Four" {
			t.Errorf("Subseries offers %q; it was asked at a slot borrowed from another ordering: %+v", alt.NextTitle, hc.asked)
		}
	}
}

func TestARateLimitedCatalogueDoesNotSlowEveryRender(t *testing.T) {
	// The pass that fills the cache is paced to stay under the backend's
	// limit, but if it is tripped anyway, the render after it must not go
	// back to the backend for every candidate all over again.
	hc := manyClaims("Zzz Saga", "Alpha", "Beta")
	hc.nextErr = errors.New("rate limited")
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Book Three", "Zzz Saga", 3, "9780000000001")}}}
	e := twoProviders(t, hc, gm)
	ctx := context.Background()

	if _, err := e.View(ctx); err != nil {
		t.Fatal(err)
	}
	after := len(hc.asked)
	if after == 0 {
		t.Fatal("the first render asked nothing")
	}
	if _, err := e.View(ctx); err != nil {
		t.Fatal(err)
	}
	if len(hc.asked) != after {
		t.Errorf("asked %d more times on the next render; a failure just answered is held", len(hc.asked)-after)
	}
}

func TestARowWaitingOnALookupSaysSoRatherThanNothing(t *testing.T) {
	// Out of budget is not the same as out of books. A row that renders
	// silent looks settled, and the reader has no way to tell that an answer
	// is still coming.
	hc := &catalogue{next: library.Entry{Book: library.Book{Title: "Sword of Destiny"}}, found: true}
	var reads []library.Entry
	for _, name := range []string{"A", "B", "C", "D", "E", "F"} {
		reads = append(reads, readOn("hardcover", "Book "+name, "Series "+name, 1))
	}
	hc.fakeSource = fakeSource{reads: reads}
	e := twoProviders(t, hc, shelf{})
	ctx := context.Background()

	v, err := e.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waiting := 0
	for _, g := range v.Groups {
		if g.NextTitle == "" && !g.CaughtUp {
			if !g.NextPending {
				t.Errorf("%q has no answer and does not say one is coming", g.Name)
			}
			if g.NextLabel() != "Checking…" {
				t.Errorf("%q label = %q, want it to say it is still checking", g.Name, g.NextLabel())
			}
			waiting++
		}
	}
	if waiting == 0 {
		t.Fatal("every row was answered; the budget was not the constraint")
	}

	// And once the answers are in, nothing claims to be waiting.
	if _, err := e.compute(ctx, 1<<20, 0, true); err != nil {
		t.Fatal(err)
	}
	v, err = e.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range v.Groups {
		if g.NextPending {
			t.Errorf("%q still says it is checking after every answer is in", g.Name)
		}
	}
}

func TestAnIdentityNobodyCanAskIsNotWaitingForever(t *testing.T) {
	// An identity with no slot to ask after, or on a provider with no
	// catalogue, has no answer coming. Marking it as waiting would leave the
	// drawer saying it is still checking for as long as the app runs.
	hc := &catalogue{
		byISBN: map[string][]library.Series{"9780000000001": {{Name: "Unplaced Saga", Source: "hardcover"}}},
		next:   library.Entry{Book: library.Book{Title: "x"}}, found: true,
	}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Dune", "Dune", 1, "9780000000001")}}}

	v, err := twoProviders(t, hc, gm).compute(context.Background(), 1<<20, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "Dune")
	if g.NextPending {
		t.Error("a row on a provider with no catalogue has no answer coming")
	}
	for _, alt := range g.Alternatives {
		if alt.Pending {
			t.Errorf("%q says it is still being checked, but it has no slot to ask after", alt.Name)
		}
	}
}

func TestAnUncheckedIdentitySaysItIsStillBeingChecked(t *testing.T) {
	hc := manyClaims("Zzz Saga", "Alpha", "Beta")
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Book Three", "Zzz Saga", 3, "9780000000001")}}}
	e := twoProviders(t, hc, gm)
	ctx := context.Background()

	v, err := e.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "Zzz Saga")
	for _, alt := range g.Alternatives {
		if alt.Checked {
			continue
		}
		if !alt.Pending || alt.NextLabel() != "Checking…" {
			t.Errorf("%q: Pending = %v, label = %q; an identity waiting on the warm pass says so", alt.Name, alt.Pending, alt.NextLabel())
		}
	}

	if _, err := e.compute(ctx, 1<<20, 0, true); err != nil {
		t.Fatal(err)
	}
	v, err = e.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, alt := range groupNamed(t, v, "Zzz Saga").Alternatives {
		if alt.Pending {
			t.Errorf("%q still says it is being checked after the warm pass", alt.Name)
		}
	}
}

func TestAnUnnumberedClaimIsNotAskedAtAnotherOrderingsSlot(t *testing.T) {
	// Hardcover files one of the reader's books under "Sub" without a slot,
	// and the other not at all. Nothing places the reader in Sub, so asking
	// after the umbrella's book 15 would answer a question about a place they
	// have never been.
	sub := library.Series{Name: "Sub", Slug: "sub", Source: "hardcover"}
	umbrella := library.Series{Name: "Umbrella", Slug: "umbrella", Position: library.At(15), Source: "hardcover"}
	hc := &catalogue{
		byISBN: map[string][]library.Series{
			"9780000000001": {sub, umbrella},
			"9780000000002": {umbrella},
		},
		next: library.Entry{Book: library.Book{Title: "Somewhere"}}, found: true,
	}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{
		readOn("grimmory", "Book One", "Shelf Series", 1, "9780000000001"),
		readOn("grimmory", "Book Two", "Shelf Series", 2, "9780000000002"),
	}}}

	v, err := twoProviders(t, hc, gm).compute(context.Background(), 1<<20, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range hc.asked {
		if q.Series.Slug == "sub" {
			t.Errorf("Sub was asked after slot %v, a number from another ordering", *q.Series.Position)
		}
	}
	for _, alt := range groupNamed(t, v, "Shelf Series").Alternatives {
		if alt.Name == "Sub" && (alt.NextLabel() != "" || alt.Pending) {
			t.Errorf("Sub says %q, pending %v; with no slot to ask after, no answer is coming", alt.NextLabel(), alt.Pending)
		}
	}
}

func TestATwinStillBeingCheckedSaysSo(t *testing.T) {
	// The twin's own row is waiting on its catalogue; the wheel entry pointing
	// at it is waiting on the same answer.
	hcRead := readOn("hardcover", "The Last Wish", "The Witcher", 1)
	gmRead := readOn("grimmory", "Sword of Destiny", "The Witcher", 2)
	hc := &catalogue{fakeSource: fakeSource{reads: []library.Entry{hcRead}}, nextErr: errors.New("rate limited")}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{gmRead}}}

	v, err := twoProviders(t, hc, gm).View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range v.Groups {
		if g.Source != "grimmory" {
			continue
		}
		for _, alt := range g.Alternatives {
			if alt.Source == "hardcover" && alt.NextLabel() != "Checking…" {
				t.Errorf("the hardcover twin says %q while its own row is still checking", alt.NextLabel())
			}
		}
	}
}

func TestAHeldFailureCostsNothing(t *testing.T) {
	// Four series fail and are held for a minute. Reading a held failure back
	// is not a round trip; charging it as one would spend every later render
	// on them and leave the fifth series waiting for the whole minute.
	var reads []library.Entry
	failing := map[string]bool{}
	for _, name := range []string{"A", "B", "C", "D"} {
		reads = append(reads, readOn("hardcover", "Book "+name, "Series "+name, 1))
		failing["Series "+name] = true
	}
	late := readOn("hardcover", "Book E", "Series E", 1)
	late.FinishedAt = day0.AddDate(0, 0, -1) // sorts last
	reads = append(reads, late)
	hc := &catalogue{fakeSource: fakeSource{reads: reads}, failing: failing,
		next: library.Entry{Book: library.Book{Title: "Book Two"}}, found: true}
	e := twoProviders(t, hc, shelf{})
	ctx := context.Background()

	if _, err := e.View(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := e.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, v, "Series E"); g.NextTitle != "Book Two" {
		t.Errorf("Series E: Next = %q, pending %v; the held failures spent its lookup", g.NextTitle, g.NextPending)
	}
}

// continuable is a finished Grimmory trilogy that Hardcover carries on past.
func continuable(t *testing.T) (*Engine, *catalogue, shelf) {
	t.Helper()
	hc := &catalogue{
		byISBN: map[string][]library.Series{"9780765377104": {
			{Name: "Remembrance of Earth's Past", Slug: "remembrance", Position: library.At(3), Source: "hardcover"},
		}},
		next: library.Entry{Book: library.Book{Title: "The Redemption of Time"}}, found: true,
	}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Death's End", "Three-Body", 3, "9780765377104")}}}
	return twoProviders(t, hc, gm), hc, gm
}

func TestKeepingASeriesEndsTheOfferToContinueElsewhere(t *testing.T) {
	// The reader tracks the trilogy's own ordering because that is the one
	// they mean to follow. Another provider's longer ordering is an offer
	// they can turn down for good.
	e, hc, _ := continuable(t)
	ctx := context.Background()
	v, err := e.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if groupNamed(t, v, "Three-Body").ContinueOn == nil {
		t.Fatal("no offer to turn down")
	}

	if _, err := e.Decide(ctx, "keep", "Three-Body", ""); err != nil {
		t.Fatalf("keep: %v", err)
	}
	asked := len(hc.asked)
	v, err = e.compute(ctx, 1<<20, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	g := groupNamed(t, v, "Three-Body")
	if !g.Kept || g.ContinueOn != nil || !g.CaughtUp {
		t.Errorf("Kept = %v, ContinueOn = %+v, CaughtUp = %v; want a finished row with no offer", g.Kept, g.ContinueOn, g.CaughtUp)
	}
	if g.Pending() {
		t.Error("a row that has turned its continuations down is not waiting on them")
	}
	if len(hc.asked) != asked {
		t.Errorf("asked %d more times about continuations the reader turned down", len(hc.asked)-asked)
	}
}

func TestEndingAKeepOffersAgain(t *testing.T) {
	e, _, _ := continuable(t)
	ctx := context.Background()
	if _, err := e.View(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Decide(ctx, "keep", "Three-Body", ""); err != nil {
		t.Fatal(err)
	}
	uncached, err := e.Decide(ctx, "unkeep", "Three-Body", "")
	if err != nil {
		t.Fatal(err)
	}
	if !uncached {
		t.Error("ending a keep revives lookups the engine has been skipping, so the re-render needs a budget")
	}
	v, err := e.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, v, "Three-Body"); g.Kept || g.ContinueOn == nil {
		t.Errorf("Kept = %v, ContinueOn = %+v; want the offer back", g.Kept, g.ContinueOn)
	}
}

func TestAKeptSeriesStillCarriesOnByItself(t *testing.T) {
	// Keeping to this ordering says nothing against its own next book: when
	// one turns up on the shelf, it is offered, and the keep still stands.
	later := tbr("Three-Body 4", "Three-Body", 4, day2)
	later.Book.Series.Source = "grimmory"
	e, _, _ := continuable(t)
	e.now = func() time.Time { return day1 }
	if _, err := e.Decide(context.Background(), "keep", "Three-Body", ""); err != nil {
		t.Fatal(err)
	}
	v := Compute(Input{
		Reads:       []library.Entry{readOn("grimmory", "Death's End", "Three-Body", 3, "9780765377104")},
		ToRead:      []library.Entry{later},
		Statements:  mustStatements(t, e),
		SourceOrder: e.SourceOrder,
	})
	g := groupNamed(t, v, "Three-Body")
	if !g.Kept {
		t.Error("a book of the kept series is no reason to drop the keep")
	}
	if g.NextTitle != "Three-Body 4" {
		t.Errorf("Next = %q: the kept series should still carry on by itself", g.NextTitle)
	}
}

func mustStatements(t *testing.T, e *Engine) []Statement {
	t.Helper()
	st, err := e.store.Statements(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestTheViewSaysWhenItHasMovedOn(t *testing.T) {
	// A drawer waiting on answers is told the moment one lands, rather than
	// finding out on a timer.
	e, _, _ := continuable(t)
	ctx := context.Background()
	gen, changed := e.Changes()

	if _, err := e.View(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("answers landed, but nothing waiting on them was told")
	}
	if now, _ := e.Changes(); now <= gen {
		t.Errorf("generation = %d, want it past %d", now, gen)
	}

	// Reading answers back changes nothing a render would show.
	_, changed = e.Changes()
	if _, err := e.View(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
		t.Error("a render served from the cache claimed the view had moved on")
	default:
	}
}

func TestANudgedEngineFetchesPromptlyAndTellsWhenDone(t *testing.T) {
	// A render with answers still to come nudges the background pass, which
	// fetches them and says so, without waiting for the next daily run.
	e, hc, _ := continuable(t)
	e.pace, e.retryGap = 0, 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, changed := e.Changes()
	go e.Run(ctx, 24*time.Hour)
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("the first pass never ran")
	}
	for { // let the first pass finish
		_, c := e.Changes()
		select {
		case <-c:
			continue
		case <-time.After(200 * time.Millisecond):
		}
		break
	}

	before := len(hc.asked)
	e.Nudge()
	e.Nudge() // nudges coalesce
	_, changed = e.Changes()
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("a nudge did not bring a pass round")
	}
	if len(hc.asked) != before {
		t.Errorf("the nudged pass asked %d questions it already had answers to", len(hc.asked)-before)
	}
}

func TestAMergedRowIsNamedTheSameWhicheverBookWasReadLast(t *testing.T) {
	// Both providers file these books under "The Saga", and a book they share
	// sits at the same slot in both, so the rows are one. Which name it wears
	// must not hang on which book the reader happened to finish last.
	shared := read("Book One", "The Saga", 1, day0)
	sharedGm := entry("Book One", "The Saga", 1, "grimmory")
	sharedGm.Status, sharedGm.FinishedAt = library.StatusRead, day0
	onlyGm := entry("Book Two", "The Saga", 2, "grimmory")
	onlyGm.Status = library.StatusRead

	names := map[string]bool{}
	for _, last := range []time.Time{day1, day0.AddDate(0, 0, -1)} {
		onlyGm.FinishedAt = last
		v := Compute(Input{Reads: []library.Entry{shared, sharedGm, onlyGm}, SourceOrder: []string{"hardcover", "grimmory"}})
		if len(v.Groups) != 1 {
			t.Fatalf("groups = %v, want one row", groupNames(v))
		}
		names[v.Groups[0].Source+":"+v.Groups[0].Name] = true
	}
	if len(names) != 1 || !names["hardcover:The Saga"] {
		t.Errorf("identities = %v, want one, from the source ranked first", names)
	}
}

func TestARestartAsksNothingItAlreadyKnows(t *testing.T) {
	// Answers and ISBN matches outlive the process, so a restart shows the
	// drawer as it was and asks the catalogue nothing it has an answer to.
	store := openStore(t)
	ctx := context.Background()
	newCatalogue := func() *catalogue {
		return &catalogue{
			name: "hardcover",
			byISBN: map[string][]library.Series{"9780765377104": {
				{Name: "Remembrance of Earth's Past", Slug: "remembrance", Position: library.At(3), Source: "hardcover"},
			}},
			next: library.Entry{Book: library.Book{Title: "The Redemption of Time"}}, found: true,
		}
	}
	gm := shelf{name: "grimmory", fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Death's End", "Three-Body", 3, "9780765377104")}}}

	first := NewEngine(store, library.Combine(newCatalogue(), gm), picker.Prefs{IncludeNovellas: true})
	first.SourceOrder = []string{"hardcover", "grimmory"}
	if _, err := first.compute(ctx, 1<<20, 0, true); err != nil {
		t.Fatal(err)
	}

	hc := newCatalogue()
	restarted := NewEngine(store, library.Combine(hc, gm), picker.Prefs{IncludeNovellas: true})
	restarted.SourceOrder = first.SourceOrder
	v, err := restarted.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hc.asked) != 0 || hc.finds != 0 {
		t.Errorf("asked = %d, finds = %d after a restart; every answer was already known", len(hc.asked), hc.finds)
	}
	g := groupNamed(t, v, "Three-Body")
	if g.ContinueOn == nil || g.ContinueOn.NextTitle != "The Redemption of Time" {
		t.Errorf("ContinueOn = %+v, want the offer back without asking", g.ContinueOn)
	}
	if g.Pending() {
		t.Error("a row whose answers were all kept says it is still checking")
	}
}

func TestAnAnswerDueARecheckIsShownWhileItIsRechecked(t *testing.T) {
	// A day-old answer is almost certainly still right. The reader sees it at
	// once; the background pass re-checks it, and a request does not spend
	// its lookups on something it can already show.
	read := readOn("hardcover", "The Last Wish", "The Witcher", 1)
	hc := &catalogue{fakeSource: fakeSource{reads: []library.Entry{read}}, next: library.Entry{Book: library.Book{Title: "Sword of Destiny"}}, found: true}
	e := twoProviders(t, hc, shelf{})
	ctx := context.Background()
	now := day0
	e.now, e.lookahead.now = func() time.Time { return now }, func() time.Time { return now }
	if _, err := e.View(ctx); err != nil {
		t.Fatal(err)
	}

	now = day0.AddDate(0, 0, 2)
	asked := len(hc.asked)
	v, err := e.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, v, "The Witcher"); g.NextTitle != "Sword of Destiny" || g.NextPending {
		t.Errorf("Next = %q, pending %v; want the last answer shown while it is due a re-check", g.NextTitle, g.NextPending)
	}
	if len(hc.asked) != asked {
		t.Error("a request spent a lookup re-checking an answer it could already show")
	}

	hc.nextErr = errors.New("rate limited")
	if v, err = e.compute(ctx, 1<<20, 0, true); err != nil {
		t.Fatal(err)
	}
	if len(hc.asked) != asked+1 {
		t.Errorf("the background pass asked %d times, want the due answer re-checked once", len(hc.asked)-asked)
	}
	if g := groupNamed(t, v, "The Witcher"); g.NextTitle != "Sword of Destiny" || g.NextPending {
		t.Errorf("Next = %q, pending %v; a failed re-check must leave the last answer standing", g.NextTitle, g.NextPending)
	}
}

// refreshingShelf counts the passes that refreshed it ahead of need.
type refreshingShelf struct {
	shelf
	refreshed *int32
}

func (r refreshingShelf) Refresh(context.Context) (bool, error) {
	atomic.AddInt32(r.refreshed, 1)
	return false, nil
}

func TestEachPassRefreshesTheLibraryFirst(t *testing.T) {
	// A page load reads what the last pass fetched, so it is the pass, not the
	// reader, that waits on the backend.
	var refreshed int32
	gm := refreshingShelf{shelf: shelf{name: "grimmory"}, refreshed: &refreshed}
	e := NewEngine(openStore(t), library.Combine(&catalogue{name: "hardcover"}, gm), picker.Prefs{})
	e.pace = 0
	e.Warm(context.Background())
	if atomic.LoadInt32(&refreshed) != 1 {
		t.Errorf("refreshed %d times, want once per pass", refreshed)
	}
}

func TestANudgeRightAfterAScheduledPassIsNotKeptWaiting(t *testing.T) {
	// The gap between passes only stops renders nudging in a loop; the first
	// nudge after a scheduled pass is answered at once.
	e, _, _ := continuable(t)
	e.pace, e.retryGap = 0, time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, changed := e.Changes()
	go e.Run(ctx, time.Hour)
	passEnd := func(within time.Duration) bool {
		deadline := time.After(within)
		for {
			_, c := e.Changes()
			select {
			case <-c:
			case <-time.After(150 * time.Millisecond):
				return true // quiet: the pass is over
			case <-deadline:
				return false
			}
		}
	}
	<-changed
	passEnd(5 * time.Second)

	_, changed = e.Changes()
	e.Nudge()
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("a nudge right after a scheduled pass was kept waiting")
	}
	passEnd(5 * time.Second)

	_, changed = e.Changes()
	e.Nudge()
	select {
	case <-changed:
		t.Error("a second nudge straight after the first was not spaced out")
	case <-time.After(500 * time.Millisecond):
	}
}

// changingShelf is a library whose refreshes report a change while changes is
// above zero, and which can hold a refresh until released.
type changingShelf struct {
	shelf
	refreshed *int32
	changes   *int32
	hold      chan struct{}
}

func (c changingShelf) Refresh(context.Context) (bool, error) {
	atomic.AddInt32(c.refreshed, 1)
	if c.hold != nil {
		<-c.hold
	}
	return atomic.AddInt32(c.changes, -1) >= 0, nil
}

func TestALibraryRefreshIsSharedAndCountsOnlyChanges(t *testing.T) {
	var refreshed, changes int32 = 0, 1
	gm := changingShelf{shelf: shelf{name: "grimmory"}, refreshed: &refreshed, changes: &changes, hold: make(chan struct{})}
	e := NewEngine(openStore(t), library.Combine(&catalogue{name: "hardcover"}, gm), picker.Prefs{})

	first, second := e.RefreshLibrary(), e.RefreshLibrary()
	close(gm.hold)
	<-first
	<-second
	if atomic.LoadInt32(&refreshed) != 1 {
		t.Errorf("refreshed %d times, want two callers sharing one refresh", refreshed)
	}
	at, gen := e.Library()
	if at.IsZero() || gen != 1 {
		t.Errorf("Library = (%v, %d), want a refresh time and one change", at, gen)
	}

	<-e.RefreshLibrary() // says nothing new
	if _, again := e.Library(); again != gen {
		t.Errorf("generation moved to %d on a refresh that changed nothing", again)
	}
}

func TestARowNotYetLookedUpByISBNSaysItIsStillChecking(t *testing.T) {
	// A finished row learns where it might continue from an ISBN lookup. Until
	// that has happened, "nothing left" is not yet the whole answer.
	hc := &catalogue{byISBN: map[string][]library.Series{}}
	gm := shelf{fakeSource: fakeSource{reads: []library.Entry{readOn("grimmory", "Death's End", "Three-Body", 3, "9780765377104")}}}
	e := twoProviders(t, hc, gm)
	ctx := context.Background()

	v, err := e.viewWithin(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, v, "Three-Body"); !g.Pending() {
		t.Error("a row never looked up by ISBN reads as settled")
	}

	// Looked up, and nothing found: that is an answer.
	if _, err := e.View(ctx); err != nil {
		t.Fatal(err)
	}
	if v, err = e.viewWithin(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if g := groupNamed(t, v, "Three-Body"); g.Pending() {
		t.Error("a row whose lookup found nothing still says it is checking")
	}
}

func TestAKeepOutlivesEveryOtherDecisionAndItsUndo(t *testing.T) {
	// Keeping to a series is its own standing fact. Dropping and undropping
	// it, or a park that is spent, says nothing about which ordering the
	// reader follows; only "Suggest others" ends a keep.
	for _, then := range []string{"drop", "park", "pin"} {
		t.Run(then, func(t *testing.T) {
			e, _, _ := continuable(t)
			ctx := context.Background()
			if _, err := e.View(ctx); err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"keep", then, "clear"} {
				if _, err := e.Decide(ctx, action, "Three-Body", ""); err != nil {
					t.Fatalf("%s: %v", action, err)
				}
			}
			v, err := e.View(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if g := groupNamed(t, v, "Three-Body"); !g.Kept || g.ContinueOn != nil {
				t.Errorf("after keep, %s and its undo: Kept = %v, ContinueOn = %+v; the keep was lost", then, g.Kept, g.ContinueOn)
			}

			if _, err := e.Decide(ctx, "unkeep", "Three-Body", ""); err != nil {
				t.Fatal(err)
			}
			if v, err = e.View(ctx); err != nil {
				t.Fatal(err)
			}
			if g := groupNamed(t, v, "Three-Body"); g.Kept || g.ContinueOn == nil {
				t.Errorf("after Suggest others: Kept = %v, ContinueOn = %+v; want the offer back", g.Kept, g.ContinueOn)
			}
		})
	}
}

func TestADecisionTellsWhoeverIsListening(t *testing.T) {
	// Another tab's drawer, or this one's, must hear of a decision at once,
	// not at its next timeout.
	e, _, _ := continuable(t)
	ctx := context.Background()
	if _, err := e.View(ctx); err != nil {
		t.Fatal(err)
	}
	gen, changed := e.Changes()
	if _, err := e.Decide(ctx, "park", "Three-Body", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Error("a decision was recorded without telling anyone listening")
	}
	if now, _ := e.Changes(); now <= gen {
		t.Errorf("generation = %d, want it past %d", now, gen)
	}
}

func TestAKeptRowFollowingAFoundSeriesIsFoundAgain(t *testing.T) {
	// Keeping turns down other orderings; it does not stop the row from
	// following the one it switched to. With the ISBN match lost, that series
	// must be found again rather than asked for by name alone.
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
	for _, d := range [][2]string{{"switch", hcClaim.Name}, {"keep", ""}} {
		name := "Three-Body"
		if d[0] == "keep" {
			name = hcClaim.Name
		}
		if _, err := first.Decide(ctx, d[0], name, d[1]); err != nil {
			t.Fatalf("%s: %v", d[0], err)
		}
	}
	if err := store.PruneCache(ctx, time.Now().AddDate(100, 0, 0)); err != nil {
		t.Fatal(err)
	}

	hc := newCatalogue()
	if _, err := NewEngine(store, library.Combine(hc, gm), picker.Prefs{}).View(ctx); err != nil {
		t.Fatal(err)
	}
	if hc.finds == 0 {
		t.Error("a kept row following a found series never looked it up again")
	}
	if len(hc.asked) == 0 || hc.asked[0].Series.Slug != "remembrance" {
		t.Errorf("asked = %+v, want hardcover's own identifier, not the name alone", hc.asked)
	}
}

func TestAKeptRowLooksForNoOtherOrderings(t *testing.T) {
	// The reader has turned other orderings down: looking them up by ISBN
	// would only spend the catalogue's patience on answers nobody is shown.
	e, hc, _ := continuable(t)
	ctx := context.Background()
	if _, err := e.Decide(ctx, "keep", "Three-Body", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.compute(ctx, 1<<20, 0, true); err != nil {
		t.Fatal(err)
	}
	if hc.finds != 0 {
		t.Errorf("a kept row was looked up by ISBN %d times", hc.finds)
	}
}
