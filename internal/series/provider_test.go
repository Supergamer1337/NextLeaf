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
	// answer overrides next/found per query, for a catalogue whose reply
	// depends on which of its series is asked about.
	answer func(library.SeriesQuery) (library.Entry, bool)
}

func (c *catalogue) Name() string { return c.name }
func (c *catalogue) NextInSeries(_ context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	c.asked = append(c.asked, q)
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

func TestAnUncheckedIdentitySaysNothingAtAll(t *testing.T) {
	// Silence, not "nothing left": nobody has asked yet, and a wheel that
	// guesses would talk a reader out of a series that has books waiting.
	alt := Alternative{Name: "Dune Chronicles", Source: "hardcover"}
	if label := alt.NextLabel(); label != "" {
		t.Errorf("NextLabel = %q, want nothing said for an identity nobody has asked about", label)
	}
}

func TestTheArrowSkipsACandidateThatLeadsNowhere(t *testing.T) {
	// Hardcover ranks the umbrella first, and it is finished; the trilogy
	// under it is not. The offer follows the one that leads somewhere.
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
