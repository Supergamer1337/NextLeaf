package series

import (
	"testing"
	"time"

	"nextleaf/internal/library"
	"nextleaf/internal/picker"
)

var (
	day0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	day1 = day0.AddDate(0, 0, 1)
	day2 = day0.AddDate(0, 0, 2)
)

func entry(title, seriesName string, pos float64, src string) library.Entry {
	return library.Entry{Book: library.Book{
		Title:  title,
		Series: &library.Series{Name: seriesName, Position: library.At(pos), Source: src},
	}}
}

func read(title, seriesName string, pos float64, at time.Time) library.Entry {
	e := entry(title, seriesName, pos, "hardcover")
	e.Status = library.StatusRead
	e.FinishedAt = at
	return e
}

func tbr(title, seriesName string, pos float64, added time.Time) library.Entry {
	e := entry(title, seriesName, pos, "hardcover")
	e.Status = library.StatusWantToRead
	e.DateAdded = added
	return e
}

func groupNamed(t *testing.T, v View, name string) Group {
	t.Helper()
	for _, g := range v.Groups {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("no group named %q in %v", name, groupNames(v))
	return Group{}
}

func groupNames(v View) []string {
	out := make([]string, len(v.Groups))
	for i, g := range v.Groups {
		out[i] = g.Name
	}
	return out
}

func TestReadingIntoASeriesTracksIt(t *testing.T) {
	v := Compute(Input{Reads: []library.Entry{read("Book 3", "Mistborn", 3, day0)}})
	g := groupNamed(t, v, "Mistborn")
	if g.Position == nil || *g.Position != 3 {
		t.Errorf("Position = %v, want 3", g.Position)
	}
	if g.Decision != Active {
		t.Errorf("Decision = %v, want Active", g.Decision)
	}
}

func TestABookOnlyOnTheShelfTracksNothing(t *testing.T) {
	v := Compute(Input{ToRead: []library.Entry{tbr("Book 1", "Unstarted", 1, day0)}})
	if len(v.Groups) != 0 {
		t.Errorf("groups = %v, want none for an unstarted series", groupNames(v))
	}
}

func TestNextIsTheEarliestUnreadNotTheSlotAfterTheFurthest(t *testing.T) {
	// The reader is at book 5; a prequel published at 0.7 and a skipped book 2
	// are both behind them, and both are simply unread.
	v := Compute(Input{
		Reads: []library.Entry{read("Book 5", "Saga", 5, day0), read("Book 1", "Saga", 1, day0)},
		ToRead: []library.Entry{
			tbr("Book 6", "Saga", 6, day0),
			tbr("The Prequel", "Saga", 0.7, day0),
			tbr("Book 2", "Saga", 2, day0),
		},
		Prefs: picker.Prefs{IncludeNovellas: true},
	})
	g := groupNamed(t, v, "Saga")
	if g.NextTitle != "The Prequel" {
		t.Errorf("NextTitle = %q, want the earliest unread volume", g.NextTitle)
	}
}

func TestSlotZeroIsARealPositionAndUnplacedIsNot(t *testing.T) {
	hobbit := library.Entry{Book: library.Book{
		Title:  "The Hobbit",
		Series: &library.Series{Name: "Middle Earth", Source: "grimmory"},
	}, Status: library.StatusRead}
	hobbit.FinishedAt = day0
	v := Compute(Input{
		Reads:  []library.Entry{hobbit, read("Prologue", "Saga", 0, day0)},
		ToRead: []library.Entry{tbr("Volume One", "Saga", 1, day0)},
		Prefs:  picker.Prefs{IncludeNovellas: true},
	})
	if g := groupNamed(t, v, "Middle Earth"); g.Position != nil {
		t.Errorf("an unplaced read gave Position %v, want nil", g.Position)
	}
	g := groupNamed(t, v, "Saga")
	if g.Position == nil || *g.Position != 0 {
		t.Errorf("Position = %v, want the real slot 0", g.Position)
	}
	if g.NextTitle != "Volume One" {
		t.Errorf("NextTitle = %q, want Volume One", g.NextTitle)
	}
}

func TestAnUnplacedAnchorStillContinuesFromTheShelf(t *testing.T) {
	hobbit := library.Entry{Book: library.Book{
		Title:  "The Hobbit",
		Series: &library.Series{Name: "The Lord of the Rings", Source: "grimmory"},
	}, Status: library.StatusRead}
	hobbit.FinishedAt = day0
	// All from Grimmory: a shelved book continues a group only when one of its
	// own claims matches the group's identity — names never join across sources.
	towers := entry("The Two Towers", "The Lord of the Rings", 2, "grimmory")
	towers.Status = library.StatusWantToRead
	fellowship := entry("The Fellowship of the Ring", "The Lord of the Rings", 1, "grimmory")
	fellowship.Status = library.StatusWantToRead
	v := Compute(Input{
		Reads:  []library.Entry{hobbit},
		ToRead: []library.Entry{towers, fellowship},
		Prefs:  picker.Prefs{IncludeNovellas: true},
	})
	g := groupNamed(t, v, "The Lord of the Rings")
	if g.NextTitle != "The Fellowship of the Ring" {
		t.Errorf("NextTitle = %q, want the earliest shelved volume", g.NextTitle)
	}
}

func TestABookReadOnTwoSourcesIsOneGroup(t *testing.T) {
	hc := read("Vol. 8", "Overlord", 8, day0)
	gm := entry("Vol. 8", "Overlord", 8, "grimmory")
	gm.Status = library.StatusRead
	gm.FinishedAt = day0
	v := Compute(Input{Reads: []library.Entry{hc, gm}, SourceOrder: []string{"hardcover", "grimmory"}})
	if len(v.Groups) != 1 {
		t.Fatalf("groups = %v, want the same book on two backends to be one series", groupNames(v))
	}
	// The other backend's identical claim is not offered as an alternative:
	// switching to it would change nothing the reader can see.
	if len(v.Groups[0].Alternatives) != 0 {
		t.Errorf("Alternatives = %+v, want none for an identical claim", v.Groups[0].Alternatives)
	}
}

func TestParkIsSpentByFinishingOneMoreBook(t *testing.T) {
	reads := []library.Entry{read("Book 3", "Mistborn", 3, day0)}
	park := Statement{Kind: KindPark, MadeAt: day0, ParkCount: 1, Anchors: []string{library.BookKey(reads[0])}}

	// Nothing new finished: the park stands, however often the view recomputes.
	v := Compute(Input{Reads: reads, Statements: []Statement{park}})
	if g := groupNamed(t, v, "Mistborn"); g.Decision != Parked {
		t.Errorf("Decision = %v with nothing newly finished, want Parked", g.Decision)
	}

	// One book finished elsewhere is the one instance a park costs.
	v = Compute(Input{
		Reads:      append(reads, read("Other", "Elsewhere", 1, day1)),
		Statements: []Statement{park},
	})
	if g := groupNamed(t, v, "Mistborn"); g.Decision != Active {
		t.Errorf("Decision = %v after finishing another book, want Active", g.Decision)
	}
}

func TestDropIsUndoneOnlyByAShelfAdditionAfterTheDrop(t *testing.T) {
	reads := []library.Entry{read("Book 3", "Mistborn", 3, day0)}
	drop := Statement{Kind: KindDrop, MadeAt: day1, Anchors: []string{library.BookKey(reads[0])}}

	stale := tbr("Book 4", "Mistborn", 4, day0) // was already shelved
	v := Compute(Input{Reads: reads, ToRead: []library.Entry{stale}, Statements: []Statement{drop}})
	if g := groupNamed(t, v, "Mistborn"); g.Decision != Dropped {
		t.Errorf("Decision = %v with only a pre-existing shelf copy, want Dropped", g.Decision)
	}

	fresh := tbr("Book 7", "Mistborn", 7, day2) // added after the drop
	v = Compute(Input{Reads: reads, ToRead: []library.Entry{fresh}, Statements: []Statement{drop}})
	if g := groupNamed(t, v, "Mistborn"); g.Decision != Active {
		t.Errorf("Decision = %v after adding a book back, want Active", g.Decision)
	}
}

func TestPinIsSpentByReadingThePinnedBook(t *testing.T) {
	book3 := read("Book 3", "Mistborn", 3, day0)
	book4 := entry("Book 4", "Mistborn", 4, "hardcover")
	pin := Statement{Kind: KindPin, MadeAt: day0, PinnedBook: library.BookKey(book4),
		Anchors: []string{library.BookKey(book3)}}

	v := Compute(Input{Reads: []library.Entry{book3}, Statements: []Statement{pin}})
	if g := groupNamed(t, v, "Mistborn"); g.Decision != Pinned {
		t.Fatalf("Decision = %v right after pinning, want Pinned", g.Decision)
	}

	started := book4
	started.Status = library.StatusCurrentlyRead
	v = Compute(Input{Reads: []library.Entry{book3}, Reading: []library.Entry{started}, Statements: []Statement{pin}})
	if g := groupNamed(t, v, "Mistborn"); g.Decision != Active {
		t.Errorf("Decision = %v after starting the pinned book, want Active", g.Decision)
	}
}

func TestOnlyTheLatestPinStands(t *testing.T) {
	a := read("Mist 3", "Mistborn", 3, day0)
	b := read("Storm 1", "Stormlight", 1, day0)
	v := Compute(Input{Reads: []library.Entry{a, b}, Statements: []Statement{
		{ID: 1, Kind: KindPin, MadeAt: day0, Anchors: []string{library.BookKey(a)}, PinnedBook: "x"},
		{ID: 2, Kind: KindPin, MadeAt: day1, Anchors: []string{library.BookKey(b)}, PinnedBook: "y"},
	}})
	if g := groupNamed(t, v, "Mistborn"); g.Decision != Active {
		t.Errorf("Mistborn = %v after pinning another series, want Active", g.Decision)
	}
	if g := groupNamed(t, v, "Stormlight"); g.Decision != Pinned {
		t.Errorf("Stormlight = %v, want Pinned", g.Decision)
	}
}

func TestClearReturnsASeriesToActive(t *testing.T) {
	reads := []library.Entry{read("Book 3", "Mistborn", 3, day0)}
	k := library.BookKey(reads[0])
	v := Compute(Input{Reads: reads, Statements: []Statement{
		{ID: 1, Kind: KindDrop, MadeAt: day0, Anchors: []string{k}},
		{ID: 2, Kind: KindClear, MadeAt: day1, Anchors: []string{k}},
	}})
	if g := groupNamed(t, v, "Mistborn"); g.Decision != Active {
		t.Errorf("Decision = %v after clearing, want Active", g.Decision)
	}
}

func TestAStatementSurvivesTheSeriesBeingRenamed(t *testing.T) {
	// The provider renames the series; the anchor book is the same book, so
	// the drop still applies. Names are labels, not identity.
	before := read("Book 3", "Mistborn", 3, day0)
	drop := Statement{Kind: KindDrop, MadeAt: day1, Anchors: []string{library.BookKey(before)}}
	after := read("Book 3", "The Mistborn Saga (New Provider)", 3, day0)

	v := Compute(Input{Reads: []library.Entry{after}, Statements: []Statement{drop}})
	if g := groupNamed(t, v, "The Mistborn Saga (New Provider)"); g.Decision != Dropped {
		t.Errorf("Decision = %v after a provider rename, want the drop to survive", g.Decision)
	}
}

func TestANameOnlyStatementFromTheOldSchemaStillApplies(t *testing.T) {
	// Statements migrated from the stored-state schema have no anchors, only
	// the name they were made under.
	v := Compute(Input{
		Reads:      []library.Entry{read("Book 3", "Mistborn", 3, day0)},
		Statements: []Statement{{Kind: KindDrop, MadeAt: day1, Name: "Mistborn"}},
	})
	if g := groupNamed(t, v, "Mistborn"); g.Decision != Dropped {
		t.Errorf("Decision = %v, want the name-matched drop to apply", g.Decision)
	}
}

func TestPreferringAnAlternativeRefilesTheSeries(t *testing.T) {
	book := library.Entry{Book: library.Book{
		Title:  "Leviathan Wakes",
		Series: &library.Series{Name: "The Expanse (Chronological)", Position: library.At(2), Source: "hardcover"},
		OtherSeries: []library.Series{
			{Name: "The Expanse", Position: library.At(1), Source: "hardcover"},
		},
	}, Status: library.StatusRead}
	book.FinishedAt = day0
	prefer := Statement{Kind: KindPrefer, MadeAt: day1,
		PrefSource: "hardcover", PrefName: "The Expanse",
		Anchors: []string{library.BookKey(book)}}

	v := Compute(Input{Reads: []library.Entry{book}, Statements: []Statement{prefer}})
	if len(v.Groups) != 1 {
		t.Fatalf("groups = %v, want one", groupNames(v))
	}
	g := v.Groups[0]
	if g.Name != "The Expanse" {
		t.Errorf("Name = %q, want the preferred identity", g.Name)
	}
	// Each ordering numbers the book differently; the position follows.
	if g.Position == nil || *g.Position != 1 {
		t.Errorf("Position = %v, want 1, the slot in the chosen ordering", g.Position)
	}
}

func TestADecisionSurvivesSwitching(t *testing.T) {
	book := library.Entry{Book: library.Book{
		Title:       "Leviathan Wakes",
		Series:      &library.Series{Name: "Chrono", Position: library.At(2), Source: "hardcover"},
		OtherSeries: []library.Series{{Name: "Published", Position: library.At(1), Source: "hardcover"}},
	}, Status: library.StatusRead}
	book.FinishedAt = day0
	k := library.BookKey(book)

	v := Compute(Input{Reads: []library.Entry{book}, Statements: []Statement{
		{ID: 1, Kind: KindDrop, MadeAt: day0, Anchors: []string{k}},
		{ID: 2, Kind: KindPrefer, MadeAt: day1, PrefSource: "hardcover", PrefName: "Published", Anchors: []string{k}},
	}})
	g := groupNamed(t, v, "Published")
	if g.Decision != Dropped {
		t.Errorf("Decision = %v after switching, want the drop to follow the books", g.Decision)
	}
}

func TestPinnedOutranksAMoreRecentFinish(t *testing.T) {
	older := read("Mist 3", "Mistborn", 3, day0)
	newer := read("Storm 1", "Stormlight", 1, day2)
	v := Compute(Input{Reads: []library.Entry{newer, older}, Statements: []Statement{
		{Kind: KindPin, MadeAt: day1, Anchors: []string{library.BookKey(older)}, PinnedBook: "x"},
	}})
	if v.Groups[0].Name != "Mistborn" {
		t.Errorf("first group = %q, want the pinned series first", v.Groups[0].Name)
	}
}

func TestGroupsOrderByMostRecentFinish(t *testing.T) {
	v := Compute(Input{Reads: []library.Entry{
		read("Mist 3", "Mistborn", 3, day0),
		read("Storm 1", "Stormlight", 1, day2),
	}})
	if v.Groups[0].Name != "Stormlight" {
		t.Errorf("first group = %q, want the most recently finished", v.Groups[0].Name)
	}
}

func TestTheCoverIsTheFurthestBookRead(t *testing.T) {
	b3 := read("Book 3", "Saga", 3, day0)
	b3.Book.CoverURL = "https://covers.example/three.jpg"
	b1 := read("Book 1", "Saga", 1, day2) // reread later, but earlier in the series
	b1.Book.CoverURL = "https://covers.example/one.jpg"
	v := Compute(Input{Reads: []library.Entry{b1, b3}})
	if g := groupNamed(t, v, "Saga"); g.CoverURL != "https://covers.example/three.jpg" {
		t.Errorf("CoverURL = %q, want the furthest book's", g.CoverURL)
	}
}

func TestNovellasAreSkippedWhenExcluded(t *testing.T) {
	v := Compute(Input{
		Reads:  []library.Entry{read("Book 3", "Saga", 3, day0)},
		ToRead: []library.Entry{tbr("Novella", "Saga", 3.5, day0), tbr("Book 4", "Saga", 4, day0)},
		Prefs:  picker.Prefs{IncludeNovellas: false},
	})
	if g := groupNamed(t, v, "Saga"); g.NextTitle != "Book 4" {
		t.Errorf("NextTitle = %q, want the novella skipped", g.NextTitle)
	}
}

func TestPrimaryClaimFollowsSourceOrderNotFetchOrder(t *testing.T) {
	// The same series read on both backends, with the copies arriving in
	// inconsistent order per book (finish timestamps differ across sources).
	// Without a stable tiebreak the books split across two groups, each
	// believing the other's books unread.
	hc1 := read("Vol 1", "Overlord", 1, day0)
	gm1 := entry("Vol 1", "Overlord", 1, "grimmory")
	gm1.Status, gm1.FinishedAt = library.StatusRead, day0
	hc2 := read("Vol 2", "Overlord", 2, day1)
	gm2 := entry("Vol 2", "Overlord", 2, "grimmory")
	gm2.Status, gm2.FinishedAt = library.StatusRead, day1

	v := Compute(Input{
		// gm2 arrives before hc2: fetch order must not decide identity.
		Reads:       []library.Entry{gm2, hc2, hc1, gm1},
		SourceOrder: []string{"hardcover", "grimmory"},
	})
	if len(v.Groups) != 1 {
		t.Fatalf("groups = %v, want one series however the copies arrived", groupNames(v))
	}
	if g := v.Groups[0]; g.Source != "hardcover" {
		t.Errorf("primary source = %q, want the first configured source", g.Source)
	}
}

func TestSplitReadsDoNotInventANextBookTheReaderFinished(t *testing.T) {
	// Book 2 read on hardcover only, but grimmory still shelves its copy as
	// unread: the merged book is read, so nothing is offered.
	hc2 := read("Vol 2", "Overlord", 2, day1)
	gmShelf := entry("Vol 2", "Overlord", 2, "grimmory")
	gmShelf.Status = library.StatusWantToRead

	v := Compute(Input{
		Reads:       []library.Entry{hc2},
		ToRead:      []library.Entry{gmShelf},
		SourceOrder: []string{"hardcover", "grimmory"},
		Prefs:       picker.Prefs{IncludeNovellas: true},
	})
	if g := groupNamed(t, v, "Overlord"); g.NextTitle != "" {
		t.Errorf("NextTitle = %q, want nothing: the reader already read it", g.NextTitle)
	}
}

func TestSameNamedGroupsOnTwoBackendsOfferEachOtherForFolding(t *testing.T) {
	// The copies did not join (title variants defeat the book key), so each
	// backend's reads form their own group. NextLeaf must not fuse them on the
	// name — but it should offer the fold for the reader to confirm.
	hc := read("Overlord, Vol. 8", "Overlord", 8, day0)
	gm := entry("Overlord (Light Novel), Vol. 8", "Overlord", 8, "grimmory")
	gm.Status, gm.FinishedAt = library.StatusRead, day0

	v := Compute(Input{Reads: []library.Entry{hc, gm}, SourceOrder: []string{"hardcover", "grimmory"}})
	if len(v.Groups) != 2 {
		t.Fatalf("groups = %v, want two until the reader folds them", groupNames(v))
	}
	for _, g := range v.Groups {
		found := false
		for _, alt := range g.Alternatives {
			if key(alt.Name) == key("Overlord") && alt.Source != g.Source {
				found = true
			}
		}
		if !found {
			t.Errorf("group (%s, %s) does not offer its same-named twin as a switch target", g.Source, g.Name)
		}
	}
}

func TestFoldingSameNamedGroupsMergesTheirReadSets(t *testing.T) {
	hc := read("Overlord, Vol. 8", "Overlord", 8, day0)
	gm := entry("Overlord (Light Novel), Vol. 8", "Overlord", 8, "grimmory")
	gm.Status, gm.FinishedAt = library.StatusRead, day0
	// Grimmory also shelves its unread-looking copy of the book hardcover
	// says was read; after folding, it must not be offered.
	gmShelf := entry("Overlord (Light Novel), Vol. 8", "Overlord", 8, "grimmory")
	gmShelf.Status = library.StatusWantToRead

	prefer := Statement{Kind: KindPrefer, MadeAt: day1,
		PrefSource: "hardcover", PrefName: "Overlord",
		Anchors: []string{library.BookKey(gm)}}
	v := Compute(Input{
		Reads:       []library.Entry{hc, gm},
		ToRead:      []library.Entry{gmShelf},
		Statements:  []Statement{prefer},
		SourceOrder: []string{"hardcover", "grimmory"},
		Prefs:       picker.Prefs{IncludeNovellas: true},
	})
	if len(v.Groups) != 1 {
		t.Fatalf("groups = %v, want the fold to make one series", groupNames(v))
	}
	if g := v.Groups[0]; g.NextTitle != "" {
		t.Errorf("NextTitle = %q, want nothing: the folded read-set covers it", g.NextTitle)
	}
}

func TestASharedBookJoinsBothBackendsSameNamedSeries(t *testing.T) {
	// Vol 8 is held by both backends (one merged book carrying both claims);
	// vol 5 was read only on grimmory. The shared book is positive evidence
	// the two same-named identities are one series, so vol 5's group joins it.
	shared := library.Entry{Book: library.Book{
		Title:       "Vol 8",
		Series:      &library.Series{Name: "Overlord", Position: library.At(8), Source: "hardcover"},
		OtherSeries: []library.Series{{Name: "Overlord", Position: library.At(8), Source: "grimmory"}},
	}, Status: library.StatusRead}
	shared.FinishedAt = day0
	gmOnly := entry("Vol 5", "Overlord", 5, "grimmory")
	gmOnly.Status, gmOnly.FinishedAt = library.StatusRead, day0

	v := Compute(Input{Reads: []library.Entry{shared, gmOnly}, SourceOrder: []string{"hardcover", "grimmory"}})
	if len(v.Groups) != 1 {
		t.Fatalf("groups = %v, want one: a shared book proves the identities equal", groupNames(v))
	}
	if v.Groups[0].Position == nil || *v.Groups[0].Position != 8 {
		t.Errorf("Position = %v, want 8 across the joined read-set", v.Groups[0].Position)
	}
}

func TestASharedBookDoesNotJoinDifferentlyNamedSeries(t *testing.T) {
	// One book in a franchise and its sub-series: real, distinct series.
	shared := library.Entry{Book: library.Book{
		Title:       "Wool",
		Series:      &library.Series{Name: "Silo", Position: library.At(1), Source: "hardcover"},
		OtherSeries: []library.Series{{Name: "Wool", Position: library.At(1), Source: "hardcover"}},
	}, Status: library.StatusRead}
	shared.FinishedAt = day0
	other := entry("Shift", "Wool", 2, "hardcover")
	other.Status, other.FinishedAt = library.StatusRead, day0

	v := Compute(Input{Reads: []library.Entry{shared, other}, SourceOrder: []string{"hardcover"}})
	if len(v.Groups) != 2 {
		t.Errorf("groups = %v, want Silo and Wool kept apart", groupNames(v))
	}
}

func TestDisagreeingPositionsBlockTheAutoJoin(t *testing.T) {
	// Same name across backends but the shared book sits at different slots:
	// the numbering schemes differ, so fusing would corrupt the read-set.
	shared := library.Entry{Book: library.Book{
		Title:       "Crossroads",
		Series:      &library.Series{Name: "The Witcher", Position: library.At(0.1), Source: "hardcover"},
		OtherSeries: []library.Series{{Name: "The Witcher", Position: library.At(9), Source: "grimmory"}},
	}, Status: library.StatusRead}
	shared.FinishedAt = day0
	gmOnly := entry("Book 5", "The Witcher", 5, "grimmory")
	gmOnly.Status, gmOnly.FinishedAt = library.StatusRead, day0

	v := Compute(Input{Reads: []library.Entry{shared, gmOnly}, SourceOrder: []string{"hardcover", "grimmory"}})
	if len(v.Groups) != 2 {
		t.Errorf("groups = %v, want the disagreement kept visible for the reader to fold", groupNames(v))
	}
}

func TestAStatementWithDeadAnchorsFallsBackToItsName(t *testing.T) {
	// The book-key scheme changed underneath a stored statement, so none of
	// its anchors exist any more. Its name still says what it was about.
	reads := []library.Entry{read("Book 3", "Mistborn", 3, day0)}
	drop := Statement{Kind: KindDrop, MadeAt: day1, Name: "Mistborn",
		Anchors: []string{"stale-key-from-an-older-scheme"}}

	v := Compute(Input{Reads: reads, Statements: []Statement{drop}})
	if g := groupNamed(t, v, "Mistborn"); g.Decision != Dropped {
		t.Errorf("Decision = %v, want the drop to survive via its name", g.Decision)
	}
}

func TestEachAlternativeCarriesItsOwnProspectiveCover(t *testing.T) {
	// Two orderings put different books furthest, so each identity has its
	// own face: the wheel must show what the row would look like if picked.
	a := library.Entry{Book: library.Book{
		Title: "Book A", CoverURL: "https://covers.example/a.jpg",
		Series:      &library.Series{Name: "Chrono", Position: library.At(2), Source: "hardcover"},
		OtherSeries: []library.Series{{Name: "Published", Position: library.At(1), Source: "hardcover"}},
	}, Status: library.StatusRead}
	a.FinishedAt = day0
	b := library.Entry{Book: library.Book{
		Title: "Book B", CoverURL: "https://covers.example/b.jpg",
		Series:      &library.Series{Name: "Chrono", Position: library.At(1), Source: "hardcover"},
		OtherSeries: []library.Series{{Name: "Published", Position: library.At(2), Source: "hardcover"}},
	}, Status: library.StatusRead}
	b.FinishedAt = day0

	v := Compute(Input{Reads: []library.Entry{a, b}, SourceOrder: []string{"hardcover"}})
	g := groupNamed(t, v, "Chrono")
	if g.CoverURL != "https://covers.example/a.jpg" {
		t.Errorf("group cover = %q, want chrono's furthest (Book A)", g.CoverURL)
	}
	if len(g.Alternatives) != 1 {
		t.Fatalf("alternatives = %+v, want Published", g.Alternatives)
	}
	if got := g.Alternatives[0].CoverURL; got != "https://covers.example/b.jpg" {
		t.Errorf("alternative cover = %q, want published's furthest (Book B)", got)
	}
}

func TestAnAlternativeWithoutACoverFallsBackToTheRowsNotALowerVolumes(t *testing.T) {
	// The furthest book in the alternative ordering has no cover. Showing a
	// lower volume's cover would mislabel the preview; fall back to the row's.
	a := library.Entry{Book: library.Book{
		Title: "Book A", CoverURL: "https://covers.example/a.jpg",
		Series:      &library.Series{Name: "Chrono", Position: library.At(2), Source: "hardcover"},
		OtherSeries: []library.Series{{Name: "Published", Position: library.At(1), Source: "hardcover"}},
	}, Status: library.StatusRead}
	a.FinishedAt = day0
	b := library.Entry{Book: library.Book{
		Title:       "Book B",
		Series:      &library.Series{Name: "Chrono", Position: library.At(1), Source: "hardcover"},
		OtherSeries: []library.Series{{Name: "Published", Position: library.At(2), Source: "hardcover"}},
	}, Status: library.StatusRead}
	b.FinishedAt = day0
	c := library.Entry{Book: library.Book{
		Title: "Book C", CoverURL: "https://covers.example/c.jpg",
		Series: &library.Series{Name: "Chrono", Position: library.At(3), Source: "hardcover"},
	}, Status: library.StatusRead}
	c.FinishedAt = day0

	v := Compute(Input{Reads: []library.Entry{a, b, c}, SourceOrder: []string{"hardcover"}})
	g := groupNamed(t, v, "Chrono")
	if len(g.Alternatives) != 1 {
		t.Fatalf("alternatives = %+v, want Published", g.Alternatives)
	}
	if got := g.Alternatives[0].CoverURL; got != "https://covers.example/c.jpg" {
		t.Errorf("alternative cover = %q, want the row's fallback, not Book A's", got)
	}
}
