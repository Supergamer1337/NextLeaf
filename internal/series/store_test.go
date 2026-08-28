package series

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nextleaf/internal/library"
)

// readEntry is a finished book at a position in a series.
func readEntry(name string, pos float64) library.Entry {
	return library.Entry{
		Book:   library.Book{Title: name + " " + formatPos(pos), Series: &library.Series{Name: name, Position: pos}},
		Status: library.StatusRead,
	}
}

func openStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "nextleaf.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestReconcileTracksSeriesFromReads(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 3)}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tracked) != 1 {
		t.Fatalf("got %d tracked series, want 1", len(tracked))
	}
	if tracked[0].Name != "Mistborn" {
		t.Errorf("Name = %q, want %q", tracked[0].Name, "Mistborn")
	}
	if tracked[0].Position != 3 {
		t.Errorf("Position = %v, want 3", tracked[0].Position)
	}
	if tracked[0].Decision != Active {
		t.Errorf("Decision = %v, want Active", tracked[0].Decision)
	}
}

func TestReconcileKeepsFurthestPositionRead(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	// Book 5 read long ago, book 2 revisited since: furthest wins, not latest.
	snap := Snapshot{Reads: []library.Entry{readEntry("Mistborn", 2), readEntry("Mistborn", 5)}}
	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tracked) != 1 {
		t.Fatalf("got %d tracked series, want 1 (both books are one series)", len(tracked))
	}
	if tracked[0].Position != 5 {
		t.Errorf("Position = %v, want 5", tracked[0].Position)
	}
}

func TestTrackedSeriesSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nextleaf.db")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 3)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.Drop(ctx, "Mistborn", time.Now()); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	tracked, err := reopened.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tracked) != 1 {
		t.Fatalf("got %d tracked series after reopen, want 1", len(tracked))
	}
	if tracked[0].Decision != Dropped {
		t.Errorf("Decision = %v after reopen, want Dropped", tracked[0].Decision)
	}
}

func TestSeriesNamesMatchCaseInsensitively(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	snap := Snapshot{Reads: []library.Entry{readEntry("Mistborn", 1), readEntry("  MISTBORN ", 2)}}
	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tracked) != 1 {
		t.Fatalf("got %d tracked series, want 1", len(tracked))
	}
	if tracked[0].Position != 2 {
		t.Errorf("Position = %v, want 2", tracked[0].Position)
	}
}

func TestReconcileRecordsSeriesIdentityHints(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	e := readEntry("Mistborn", 3)
	e.Book.Series.Slug = "mistborn"
	e.Book.Series.Completed = true
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{e}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, ok, err := st.Get(ctx, "Mistborn")
	if err != nil || !ok {
		t.Fatalf("Get = (%v, %v)", ok, err)
	}
	if tracked.Slug != "mistborn" {
		t.Errorf("Slug = %q, want mistborn", tracked.Slug)
	}
	if !tracked.Completed {
		t.Error("Completed = false; a finished series should be recorded as such")
	}
}

func TestSeriesIdentityHintsAreNotErasedBySourcesThatLackThem(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	rich := readEntry("Mistborn", 3)
	rich.Book.Series.Slug = "mistborn"
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{rich}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Grimmory knows the same series by name alone; it must not blank the hint.
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 4)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.Slug != "mistborn" {
		t.Errorf("Slug = %q, want it kept from the source that knew it", tracked.Slug)
	}
}

func nextBook(title string) library.Entry {
	return library.Entry{Book: library.Book{
		Title:    title,
		Series:   &library.Series{Name: "Mistborn", Position: 4},
		CoverURL: "https://covers.example/" + title + ".jpg",
		URL:      "https://hardcover.app/books/" + title,
	}}
}

func TestNextBookIsRememberedForTheDrawer(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 3)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.SetNext(ctx, "Mistborn", 3, nextBook("alloy"), true, day0); err != nil {
		t.Fatalf("SetNext: %v", err)
	}

	tracked, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.NextTitle != "alloy" {
		t.Errorf("NextTitle = %q, want alloy", tracked.NextTitle)
	}
	if tracked.NextCoverURL == "" {
		t.Error("NextCoverURL is empty; the drawer has no cover to show")
	}
	if tracked.CaughtUp {
		t.Error("CaughtUp = true for a series with a next book")
	}
}

func TestASeriesWithNoNextBookIsMarkedCaughtUp(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 3)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.SetNext(ctx, "Mistborn", 3, library.Entry{}, false, day0); err != nil {
		t.Fatalf("SetNext: %v", err)
	}

	tracked, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !tracked.CaughtUp {
		t.Error("CaughtUp = false; a series with nothing left belongs in the Finished drawer")
	}
	if tracked.NextTitle != "" {
		t.Errorf("NextTitle = %q, want it cleared", tracked.NextTitle)
	}
}

func TestReadingTheRememberedNextBookClearsIt(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 3)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.SetNext(ctx, "Mistborn", 3, nextBook("alloy"), true, day0); err != nil {
		t.Fatalf("SetNext: %v", err)
	}
	// Reading book 4 makes the remembered "next" the book behind you; the
	// drawer must not keep offering it until the next resync.
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 4)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.NextTitle != "" {
		t.Errorf("NextTitle = %q, want it cleared once that book was read", tracked.NextTitle)
	}
}

func TestANewReleaseTakesASeriesBackOutOfFinished(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 3)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.SetNext(ctx, "Mistborn", 3, library.Entry{}, false, day0); err != nil {
		t.Fatalf("SetNext: %v", err)
	}
	// A later resync finds a book that did not exist before.
	if err := st.SetNext(ctx, "Mistborn", 3, nextBook("lost-metal"), true, day2); err != nil {
		t.Fatalf("SetNext: %v", err)
	}

	tracked, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.CaughtUp {
		t.Error("CaughtUp = true; a new release should take the series back out of Finished")
	}
}

func TestAStaleLookupResultDoesNotOverwriteNewerProgress(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 3)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// The reader finishes book 4 while a lookup made at position 3 is in
	// flight; the lookup's answer describes a book now behind them.
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 4)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.SetNext(ctx, "Mistborn", 3, nextBook("alloy"), true, day0); err != nil {
		t.Fatalf("SetNext: %v", err)
	}

	tracked, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.NextTitle == "alloy" {
		t.Error("a lookup made at an outdated position overwrote newer progress")
	}
}

func TestAStaleCaughtUpResultDoesNotFileTheSeriesUnderFinished(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 3)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{readEntry("Mistborn", 4)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// "Nothing after book 3" says nothing about what follows book 4.
	if err := st.SetNext(ctx, "Mistborn", 3, library.Entry{}, false, day0); err != nil {
		t.Fatalf("SetNext: %v", err)
	}

	tracked, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.CaughtUp {
		t.Error("a stale caught-up answer filed the series under Finished")
	}
}

func TestConcurrentPinsLeaveExactlyOneSeriesPinned(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = st.Pin(ctx, fmt.Sprintf("Series %d", n), day0, 2)
		}(i)
	}
	wg.Wait()

	tracked, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	pinned := 0
	for _, tr := range tracked {
		if tr.Decision == Pinned {
			pinned++
		}
	}
	if pinned != 1 {
		t.Errorf("%d series pinned after concurrent pins, want exactly 1", pinned)
	}
}

// readWithCover is a finished book that carries its cover art.
func readWithCover(name string, pos float64, cover string) library.Entry {
	e := readEntry(name, pos)
	e.Book.CoverURL = cover
	return e
}

func TestASeriesKeepsTheCoverOfTheFurthestBookRead(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	snap := Snapshot{Reads: []library.Entry{
		readWithCover("Mistborn", 3, "https://covers.example/three.jpg"),
		readWithCover("Mistborn", 1, "https://covers.example/one.jpg"),
	}}
	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Rereading book 1 should not roll the series' face back to book 1.
	if tracked.CoverURL != "https://covers.example/three.jpg" {
		t.Errorf("CoverURL = %q, want book 3's cover", tracked.CoverURL)
	}
}

func TestReadingFurtherMovesTheSeriesCoverForward(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	first := Snapshot{Reads: []library.Entry{readWithCover("Mistborn", 3, "https://covers.example/three.jpg")}}
	if err := st.Reconcile(ctx, first); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	later := Snapshot{Reads: []library.Entry{readWithCover("Mistborn", 4, "https://covers.example/four.jpg")}}
	if err := st.Reconcile(ctx, later); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.CoverURL != "https://covers.example/four.jpg" {
		t.Errorf("CoverURL = %q, want book 4's cover", tracked.CoverURL)
	}
}

func TestASeriesWithNoKnownPositionStillGetsACover(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	// Some sources name a series without saying where the book sits in it.
	snap := Snapshot{Reads: []library.Entry{readWithCover("The Lord of the Rings", 0, "https://covers.example/lotr.jpg")}}
	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, _, err := st.Get(ctx, "The Lord of the Rings")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.CoverURL == "" {
		t.Error("a series with no known position has no cover at all")
	}
}

func TestBeingCaughtUpDoesNotEraseTheSeriesCover(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	snap := Snapshot{Reads: []library.Entry{readWithCover("Mistborn", 3, "https://covers.example/three.jpg")}}
	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.SetNext(ctx, "Mistborn", 3, library.Entry{}, false, day0); err != nil {
		t.Fatalf("SetNext: %v", err)
	}

	tracked, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.CoverURL == "" {
		t.Error("a finished series lost the cover of the last book read")
	}
}
