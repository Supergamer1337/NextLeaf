package picker

import (
	"math/rand"
	"strings"
	"testing"

	"nextleaf/internal/library"
)

func book(title string, genres, authors []string) library.Book {
	return library.Book{Title: title, Genres: genres, Authors: authors}
}

func seriesBook(title, series string, pos float64) library.Book {
	return library.Book{Title: title, Series: &library.Series{Name: series, Position: pos}}
}

func TestPickEmptyCandidates(t *testing.T) {
	if _, ok := Pick(rand.New(rand.NewSource(1)), nil, nil, nil); ok {
		t.Error("Pick with no candidates should return ok == false")
	}
}

func TestPickSingleCandidateAlwaysChosen(t *testing.T) {
	cands := []library.Entry{{Book: book("Only", []string{"Sci-Fi"}, nil)}}
	rec, ok := Pick(rand.New(rand.NewSource(7)), cands, nil, nil)
	if !ok || rec.Entry.Book.Title != "Only" {
		t.Errorf("Pick = (%q, %v), want Only/true", rec.Entry.Book.Title, ok)
	}
}

func TestPickReproducibleForSeed(t *testing.T) {
	recent := []library.Entry{{Book: book("Recent", []string{"Fantasy"}, nil)}}
	cands := []library.Entry{
		{Book: book("A", []string{"History"}, nil)},
		{Book: book("B", []string{"Romance"}, nil)},
		{Book: book("C", []string{"Fantasy"}, nil)},
	}
	first, _ := Pick(rand.New(rand.NewSource(99)), cands, recent, nil)
	second, _ := Pick(rand.New(rand.NewSource(99)), cands, recent, nil)
	if first.Entry.Book.Title != second.Entry.Book.Title {
		t.Errorf("same seed gave %q then %q", first.Entry.Book.Title, second.Entry.Book.Title)
	}
}

func TestPickHandlesBooksWithoutMetadata(t *testing.T) {
	cands := []library.Entry{
		{Book: book("Bare", nil, nil)},
		{Book: book("AlsoBare", nil, nil)},
	}
	if _, ok := Pick(rand.New(rand.NewSource(3)), cands, nil, nil); !ok {
		t.Error("Pick should handle candidates with no metadata")
	}
}

func TestPickFavoursNovelGenreAcrossDraws(t *testing.T) {
	recent := []library.Entry{{Book: book("Recent", []string{"Fantasy"}, nil)}}
	cands := []library.Entry{
		{Book: book("Repeat", []string{"Fantasy"}, nil)},
		{Book: book("Novel", []string{"History"}, nil)},
	}
	rng := rand.New(rand.NewSource(42))
	novelWins := 0
	const draws = 1000
	for i := 0; i < draws; i++ {
		rec, _ := Pick(rng, cands, recent, nil)
		if rec.Entry.Book.Title == "Novel" {
			novelWins++
		}
	}
	if novelWins <= draws/2 {
		t.Errorf("novel-genre book won %d/%d draws, want a clear majority", novelWins, draws)
	}
}

func TestPickExposesProsAndCons(t *testing.T) {
	// A dominant recent genre makes a same-genre single candidate carry a con.
	recent := []library.Entry{
		{Book: book("R1", []string{"Fantasy"}, nil)},
		{Book: book("R2", []string{"Fantasy"}, nil)},
		{Book: book("R3", []string{"Fantasy"}, nil)},
	}
	conCand := []library.Entry{{Book: book("Fantasy Again", []string{"Fantasy"}, nil)}}
	rec, _ := Pick(rand.New(rand.NewSource(1)), conCand, recent, nil)
	if len(rec.Cons) == 0 || len(rec.Pros) != 0 {
		t.Errorf("dominant-genre repeat should be a con-only pick, got pros=%v cons=%v", rec.Pros, rec.Cons)
	}

	proCand := []library.Entry{{Book: book("Fresh Genre", []string{"History"}, nil)}}
	rec, _ = Pick(rand.New(rand.NewSource(1)), proCand, recent, nil)
	if len(rec.Pros) == 0 || len(rec.Cons) != 0 {
		t.Errorf("novel-genre pick should be a pro-only pick, got pros=%v cons=%v", rec.Pros, rec.Cons)
	}
}

func TestActiveSeriesPrefersMostRecentFinish(t *testing.T) {
	recent := []library.Entry{
		{Book: seriesBook("Book 2", "Broken Earth", 2), Rating: 5},
		{Book: seriesBook("Prev", "Old Series", 1)},
	}
	got, ok := ActiveSeries(nil, recent)
	if !ok || got.Book.Series.Name != "Broken Earth" || got.Book.Series.Position != 2 || got.Rating != 5 {
		t.Errorf("ActiveSeries = (%+v, %v), want Broken Earth #2 rated 5", got.Book.Series, ok)
	}
}

func TestActiveSeriesFallsBackToReading(t *testing.T) {
	reading := []library.Entry{{Book: seriesBook("In Progress", "Mistborn", 1)}}
	recent := []library.Entry{{Book: book("Standalone", nil, nil)}}
	got, ok := ActiveSeries(reading, recent)
	if !ok || got.Book.Series.Name != "Mistborn" {
		t.Errorf("ActiveSeries = (%+v, %v), want Mistborn", got.Book.Series, ok)
	}
}

func TestActiveSeriesNoneInProgress(t *testing.T) {
	if _, ok := ActiveSeries(nil, []library.Entry{{Book: book("Solo", nil, nil)}}); ok {
		t.Error("ActiveSeries should be false when nothing is in a series")
	}
}

func TestNextOnShelvesFindsEarliestLaterBook(t *testing.T) {
	toRead := []library.Entry{
		{Book: seriesBook("Book 4", "Broken Earth", 4)},
		{Book: seriesBook("Book 3", "Broken Earth", 3)},
		{Book: seriesBook("Other", "Different", 1)},
	}
	got, ok := NextOnShelves(library.Series{Name: "Broken Earth", Position: 2}, toRead)
	if !ok || got.Book.Title != "Book 3" {
		t.Errorf("NextOnShelves = (%q, %v), want Book 3", got.Book.Title, ok)
	}
}

func TestNextOnShelvesAbsent(t *testing.T) {
	toRead := []library.Entry{{Book: seriesBook("Book 1", "Broken Earth", 1)}}
	if _, ok := NextOnShelves(library.Series{Name: "Broken Earth", Position: 2}, toRead); ok {
		t.Error("NextOnShelves should be false when no later book is on the shelf")
	}
}

func TestContinueSeriesReasonWithRating(t *testing.T) {
	rec := ContinueSeries(seriesBook("The Obelisk Gate", "The Broken Earth", 2), 4.5)
	if len(rec.Pros) != 1 {
		t.Fatalf("want one pro, got %v", rec.Pros)
	}
	for _, want := range []string{"The Broken Earth", "book 2", "4.5"} {
		if !strings.Contains(rec.Pros[0], want) {
			t.Errorf("continuation pro %q missing %q", rec.Pros[0], want)
		}
	}
}

func TestContinueSeriesOmitsRatingWhenUnrated(t *testing.T) {
	rec := ContinueSeries(seriesBook("The Obelisk Gate", "The Broken Earth", 2), 0)
	if strings.Contains(rec.Pros[0], "★") {
		t.Errorf("unrated series should not mention a rating: %q", rec.Pros[0])
	}
}
