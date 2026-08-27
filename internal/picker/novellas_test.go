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

	got, ok := NextOnShelves(library.Series{Name: "Saga", Position: 3}, toRead, Prefs{IncludeNovellas: true})
	if !ok || got.Book.Title != "Novella" {
		t.Errorf("NextOnShelves = (%q, %v), want Novella", got.Book.Title, ok)
	}
}

func TestNextOnShelvesSkipsANovellaWhenTheyAreExcluded(t *testing.T) {
	toRead := []library.Entry{
		{Book: seriesBook("Novella", "Saga", 3.5)},
		{Book: seriesBook("Vol 4", "Saga", 4)},
	}

	got, ok := NextOnShelves(library.Series{Name: "Saga", Position: 3}, toRead, Prefs{IncludeNovellas: false})
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
