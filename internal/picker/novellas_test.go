package picker

import (
	"testing"

	"nextleaf/internal/library"
)

func TestNextOnShelvesOffersANovellaWhenTheyAreIncluded(t *testing.T) {
	toRead := []library.Entry{
		{Book: seriesBook("Novella", "Saga", 3.5)},
		{Book: seriesBook("Vol 4", "Saga", 4)},
	}

	got, ok := NextOnShelves(library.Series{Name: "Saga", Position: library.At(3)}, toRead, Prefs{IncludeNovellas: true})
	if !ok || got.Book.Title != "Novella" {
		t.Errorf("NextOnShelves = (%q, %v), want Novella", got.Book.Title, ok)
	}
}

func TestNextOnShelvesSkipsANovellaWhenTheyAreExcluded(t *testing.T) {
	toRead := []library.Entry{
		{Book: seriesBook("Novella", "Saga", 3.5)},
		{Book: seriesBook("Vol 4", "Saga", 4)},
	}

	got, ok := NextOnShelves(library.Series{Name: "Saga", Position: library.At(3)}, toRead, Prefs{IncludeNovellas: false})
	if !ok || got.Book.Title != "Vol 4" {
		t.Errorf("NextOnShelves = (%q, %v), want Vol 4", got.Book.Title, ok)
	}
}

func TestCollapseSeriesRepresentsASeriesByItsFirstNovelWhenNovellasAreExcluded(t *testing.T) {
	// A series should not enter the variety lottery represented by a 90-page
	// side story when the reader has asked not to be offered those.
	entries := []library.Entry{
		{Book: seriesBook("Novella", "Saga", 0.5)},
		{Book: seriesBook("Vol 1", "Saga", 1)},
	}

	got := collapseSeries(entries, Prefs{IncludeNovellas: false})
	if len(got) != 1 || got[0].Book.Title != "Vol 1" {
		t.Errorf("collapseSeries = %v, want just Vol 1", entryTitles(got))
	}
}

func TestCollapseSeriesKeepsANovellaWhenTheyAreIncluded(t *testing.T) {
	entries := []library.Entry{
		{Book: seriesBook("Novella", "Saga", 0.5)},
		{Book: seriesBook("Vol 1", "Saga", 1)},
	}

	got := collapseSeries(entries, Prefs{IncludeNovellas: true})
	if len(got) != 1 || got[0].Book.Title != "Novella" {
		t.Errorf("collapseSeries = %v, want just Novella", entryTitles(got))
	}
}

func TestNegativeAndZeroPositionsAreRealSlots(t *testing.T) {
	// Series number prequels below their first volume. Those are positions
	// like any other, and must order correctly rather than reading as
	// "no position".
	toRead := []library.Entry{
		{Book: seriesBook("Volume One", "Saga", 1)},
		{Book: seriesBook("Prequel", "Saga", -1)},
		{Book: seriesBook("Prologue", "Saga", 0)},
	}

	// Having read the prequel at -1, the prologue at 0 is what follows.
	got, ok := NextOnShelves(library.Series{Name: "Saga", Position: library.At(-1)}, toRead, Prefs{IncludeNovellas: true})
	if !ok || got.Book.Title != "Prologue" {
		t.Errorf("after the prequel: %q (ok=%v), want Prologue", got.Book.Title, ok)
	}

	// And having read the prologue at 0, volume one follows.
	got, ok = NextOnShelves(library.Series{Name: "Saga", Position: library.At(0)}, toRead, Prefs{IncludeNovellas: true})
	if !ok || got.Book.Title != "Volume One" {
		t.Errorf("after the prologue: %q (ok=%v), want Volume One", got.Book.Title, ok)
	}
}

func TestAnUnplacedAnchorTakesTheEarliestShelvedVolume(t *testing.T) {
	// The Hobbit case: read something in the series that holds no slot, so
	// every numbered volume still lies ahead.
	toRead := []library.Entry{
		{Book: seriesBook("Volume Two", "Saga", 2)},
		{Book: seriesBook("Prequel", "Saga", -1)},
	}

	got, ok := NextOnShelves(library.Series{Name: "Saga"}, toRead, Prefs{IncludeNovellas: true})
	if !ok || got.Book.Title != "Prequel" {
		t.Errorf("NextOnShelves = %q (ok=%v), want the earliest volume", got.Book.Title, ok)
	}
}

func TestAnUnplacedShelvedVolumeIsNeverOfferedAsNext(t *testing.T) {
	// A volume with no slot cannot be shown to come after anything.
	toRead := []library.Entry{
		{Book: unplacedBook("Companion", "Saga")},
		{Book: seriesBook("Volume Two", "Saga", 2)},
	}

	got, ok := NextOnShelves(library.Series{Name: "Saga", Position: library.At(1)}, toRead, Prefs{IncludeNovellas: true})
	if !ok || got.Book.Title != "Volume Two" {
		t.Errorf("NextOnShelves = %q (ok=%v), want Volume Two", got.Book.Title, ok)
	}
}
