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

func TestCollapseSeries(t *testing.T) {
	entries := []library.Entry{
		{Book: seriesBook("Vol 5", "Saga", 5)},
		{Book: book("Standalone", nil, nil)},
		{Book: seriesBook("Vol 3", "Saga", 3)},
		{Book: seriesBook("Unknown Pos", "Saga", 0)},
		{Book: seriesBook("Other 1", "other saga", 1)},
		{Book: seriesBook("Other 2", "OTHER SAGA", 2)},
	}

	got := collapseSeries(entries)

	// Saga is one candidate (its earliest volume), position-0 and standalones
	// pass through, and grouping is case-insensitive. Original order is kept.
	want := []string{"Vol 3", "Standalone", "Unknown Pos", "Other 1"}
	if len(got) != len(want) {
		t.Fatalf("titles = %v, want %v", entryTitles(got), want)
	}
	for i, w := range want {
		if got[i].Book.Title != w {
			t.Fatalf("titles = %v, want %v", entryTitles(got), want)
		}
	}
}

func TestCollapseSeriesNoSeries(t *testing.T) {
	entries := []library.Entry{
		{Book: book("A", nil, nil)},
		{Book: book("B", nil, nil)},
	}
	if got := collapseSeries(entries); len(got) != 2 {
		t.Errorf("collapse of series-free pool changed it: %v", entryTitles(got))
	}
}

func TestPickNeverReturnsLaterVolume(t *testing.T) {
	cands := []library.Entry{
		{Book: seriesBook("Vol 5", "Saga", 5)},
		{Book: seriesBook("Vol 3", "Saga", 3)},
	}
	for seed := int64(0); seed < 50; seed++ {
		rec, ok := Pick(rand.New(rand.NewSource(seed)), cands, nil, nil)
		if !ok || rec.Entry.Book.Title != "Vol 3" {
			t.Fatalf("seed %d picked %q, want the earliest unread volume", seed, rec.Entry.Book.Title)
		}
	}
}

func TestPickSeriesCompetesAsOneCandidate(t *testing.T) {
	// A four-volume series must not out-draw an equally scored standalone.
	cands := []library.Entry{
		{Book: seriesBook("Vol 1", "Saga", 1)},
		{Book: seriesBook("Vol 2", "Saga", 2)},
		{Book: seriesBook("Vol 3", "Saga", 3)},
		{Book: seriesBook("Vol 4", "Saga", 4)},
		{Book: book("Standalone", nil, nil)},
	}
	rng := rand.New(rand.NewSource(42))
	seriesWins := 0
	const draws = 1000
	for i := 0; i < draws; i++ {
		rec, _ := Pick(rng, cands, nil, nil)
		if rec.Entry.Book.Title == "Vol 1" {
			seriesWins++
		}
	}
	// One ticket each: expect ~500, tolerate sampling noise; 5 tickets vs 1
	// would give ~800, well outside the band.
	if seriesWins < 400 || seriesWins > 600 {
		t.Errorf("series won %d/%d draws, want roughly half (one ticket)", seriesWins, draws)
	}
}

func entryTitles(entries []library.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Book.Title
	}
	return out
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
	rec := ContinueSeries(library.Entry{Book: seriesBook("The Obelisk Gate", "The Broken Earth", 2)}, 4.5)
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
	rec := ContinueSeries(library.Entry{Book: seriesBook("The Obelisk Gate", "The Broken Earth", 2)}, 0)
	if strings.Contains(rec.Pros[0], "★") {
		t.Errorf("unrated series should not mention a rating: %q", rec.Pros[0])
	}
}

func TestContinueSeriesKeepsEntryProvenance(t *testing.T) {
	e := library.Entry{
		Book:      seriesBook("The Obelisk Gate", "The Broken Earth", 2),
		Sources:   []library.SourceRef{{Name: "grimmory"}},
		Available: true,
	}
	rec := ContinueSeries(e, 0)
	if len(rec.Entry.Sources) != 1 || rec.Entry.Sources[0].Name != "grimmory" {
		t.Errorf("Sources = %v, want [grimmory]", rec.Entry.Sources)
	}
	if !rec.Entry.Available {
		t.Error("Available = false, want the on-shelf entry's flag kept")
	}
	if rec.Entry.Status != library.StatusWantToRead {
		t.Errorf("Status = %v, want StatusWantToRead", rec.Entry.Status)
	}
}
