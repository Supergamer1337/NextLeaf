package series

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nextleaf/internal/library"
	"nextleaf/internal/picker"
)

// fakeSource is a Source with fixed lists, optionally a SeriesResolver.
type fakeSource struct {
	reads   []library.Entry
	reading []library.Entry
	toRead  []library.Entry
}

func (s fakeSource) Name() string { return "fake" }
func (s fakeSource) CurrentlyReading(_ context.Context) ([]library.Entry, error) {
	return s.reading, nil
}
func (s fakeSource) RecentReads(_ context.Context, limit int) ([]library.Entry, error) {
	if limit > 0 && limit < len(s.reads) {
		return s.reads[:limit], nil
	}
	return s.reads, nil
}
func (s fakeSource) ToRead(_ context.Context) ([]library.Entry, error) { return s.toRead, nil }

type resolvingSource struct {
	fakeSource
	next  library.Entry
	found bool
	err   error
	calls int
}

func (s *resolvingSource) NextInSeries(_ context.Context, _ library.SeriesQuery) (library.Entry, bool, error) {
	s.calls++
	return s.next, s.found, s.err
}

func testEngine(t *testing.T, src library.Source) *Engine {
	t.Helper()
	return NewEngine(openStore(t), src, picker.Prefs{IncludeNovellas: true})
}

func TestRecommendContinuesTheHighestRankedSeries(t *testing.T) {
	src := fakeSource{
		reads:  []library.Entry{read("Book 3", "Mistborn", 3, day0)},
		toRead: []library.Entry{tbr("Book 4", "Mistborn", 4, day0), tbr("Standalone", "", 0, day0)},
	}
	rec, _, err := testEngine(t, src).Recommend(context.Background(), false)
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if !rec.OK || rec.Rec.Entry.Book.Title != "Book 4" {
		t.Errorf("recommended %q, want the series continuation", rec.Rec.Entry.Book.Title)
	}
	if !rec.Decidable || rec.Group != "Mistborn" {
		t.Errorf("Decidable=%v Group=%q, want decisions offered against Mistborn", rec.Decidable, rec.Group)
	}
}

func TestParkingSkipsOneTurn(t *testing.T) {
	src := fakeSource{
		reads: []library.Entry{read("Book 3", "Mistborn", 3, day0)},
		toRead: []library.Entry{
			tbr("Book 4", "Mistborn", 4, day0),
			{Book: library.Book{Title: "Standalone"}, Status: library.StatusWantToRead},
		},
	}
	e := testEngine(t, src)
	ctx := context.Background()
	if err := e.Decide(ctx, "park", "Mistborn", ""); err != nil {
		t.Fatalf("Decide park: %v", err)
	}
	rec, _, err := e.Recommend(ctx, false)
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	// The book may still surface as a variety pick — a park skips the
	// continuation, it does not hide the books — so what must be absent is
	// the continuation itself.
	for _, pro := range rec.Rec.Pros {
		if strings.Contains(pro, "Continues") {
			t.Errorf("a parked series was still continued: %q", pro)
		}
	}
}

func TestDroppingWithholdsTheBooksFromVarietyToo(t *testing.T) {
	src := fakeSource{
		reads:  []library.Entry{read("Book 3", "Mistborn", 3, day0)},
		toRead: []library.Entry{tbr("Book 4", "Mistborn", 4, day0)},
	}
	e := testEngine(t, src)
	ctx := context.Background()
	if err := e.Decide(ctx, "drop", "Mistborn", ""); err != nil {
		t.Fatalf("Decide drop: %v", err)
	}
	rec, _, err := e.Recommend(ctx, true)
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if rec.OK {
		t.Errorf("recommended %q from a dropped series' books", rec.Rec.Entry.Book.Title)
	}
}

func TestTheCatalogueAnswersWhenTheShelfCannot(t *testing.T) {
	next := entry("The Alloy of Law", "Mistborn", 4, "hardcover")
	src := &resolvingSource{
		fakeSource: fakeSource{reads: []library.Entry{read("Book 3", "Mistborn", 3, day0)}},
		next:       next, found: true,
	}
	rec, _, err := testEngine(t, src).Recommend(context.Background(), false)
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if !rec.OK || rec.Rec.Entry.Book.Title != "The Alloy of Law" {
		t.Errorf("recommended %q, want the catalogue's answer", rec.Rec.Entry.Book.Title)
	}
}

func TestAFailedLookupDegradesToVarietyNotAnError(t *testing.T) {
	src := &resolvingSource{
		fakeSource: fakeSource{
			reads:  []library.Entry{read("Book 3", "Mistborn", 3, day0)},
			toRead: []library.Entry{{Book: library.Book{Title: "Standalone"}, Status: library.StatusWantToRead}},
		},
		err: errors.New("rate limited"),
	}
	rec, _, err := testEngine(t, src).Recommend(context.Background(), false)
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if !rec.OK || rec.Rec.Entry.Book.Title != "Standalone" {
		t.Errorf("recommended %q, want the variety fallback", rec.Rec.Entry.Book.Title)
	}
}

func TestANoAnswerFilesTheGroupAsCaughtUp(t *testing.T) {
	src := &resolvingSource{
		fakeSource: fakeSource{reads: []library.Entry{read("Book 3", "Mistborn", 3, day0)}},
		found:      false,
	}
	_, v, err := testEngine(t, src).Recommend(context.Background(), false)
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if g := groupNamed(t, v, "Mistborn"); !g.CaughtUp {
		t.Error("CaughtUp = false after the catalogue said nothing is left")
	}
}

func TestAnUnplacedGroupNeverAsksTheCatalogue(t *testing.T) {
	hobbit := library.Entry{Book: library.Book{
		Title:  "The Hobbit",
		Series: &library.Series{Name: "The Lord of the Rings", Source: "grimmory"},
	}, Status: library.StatusRead}
	hobbit.FinishedAt = day0
	src := &resolvingSource{fakeSource: fakeSource{reads: []library.Entry{hobbit}}}

	if _, _, err := testEngine(t, src).Recommend(context.Background(), false); err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if src.calls != 0 {
		t.Errorf("asked the catalogue %d times about an unplaced series, want 0", src.calls)
	}
}

func TestSwitchRecordsAPreferenceAndTheViewFollows(t *testing.T) {
	book := library.Entry{Book: library.Book{
		Title:       "Leviathan Wakes",
		Series:      &library.Series{Name: "Chrono", Position: library.At(2), Source: "hardcover"},
		OtherSeries: []library.Series{{Name: "Published", Position: library.At(1), Source: "hardcover"}},
	}, Status: library.StatusRead}
	book.FinishedAt = day0
	src := fakeSource{reads: []library.Entry{book}}
	e := testEngine(t, src)
	ctx := context.Background()

	if err := e.Decide(ctx, "switch", "Chrono", "Published"); err != nil {
		t.Fatalf("Decide switch: %v", err)
	}
	v, err := e.View(ctx)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if len(v.Groups) != 1 || v.Groups[0].Name != "Published" {
		t.Errorf("groups = %v, want only Published", groupNames(v))
	}

	// And back again: preferences are fully reversible.
	if err := e.Decide(ctx, "switch", "Published", "Chrono"); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	v, err = e.View(ctx)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if len(v.Groups) != 1 || v.Groups[0].Name != "Chrono" {
		t.Errorf("groups = %v, want only Chrono again", groupNames(v))
	}
}

func TestSwitchToAStrangerIsRefused(t *testing.T) {
	src := fakeSource{reads: []library.Entry{read("Book 3", "Mistborn", 3, day0)}}
	err := testEngine(t, src).Decide(context.Background(), "switch", "Mistborn", "Some Other Saga")
	if !errors.Is(err, ErrNotAnAlternative) {
		t.Errorf("err = %v, want ErrNotAnAlternative", err)
	}
}

func TestDecidingOnAnUnknownSeriesIsRefused(t *testing.T) {
	err := testEngine(t, fakeSource{}).Decide(context.Background(), "park", "Nothing", "")
	if !errors.Is(err, ErrUnknownSeries) {
		t.Errorf("err = %v, want ErrUnknownSeries", err)
	}
}
