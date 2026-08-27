package series

import (
	"context"
	"testing"
	"time"

	"nextleaf/internal/library"
)

var (
	day0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	day1 = day0.AddDate(0, 0, 1)
	day2 = day0.AddDate(0, 0, 2)
)

func finished(name string, pos float64, at time.Time) library.Entry {
	e := readEntry(name, pos)
	e.FinishedAt = at
	return e
}

func wanted(name string, pos float64, added time.Time) library.Entry {
	e := readEntry(name, pos)
	e.Status = library.StatusWantToRead
	e.DateAdded = added
	return e
}

func decisionOf(t *testing.T, st *Store, name string) Decision {
	t.Helper()
	tracked, ok, err := st.Get(context.Background(), name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("series %q is not tracked", name)
	}
	return tracked.Decision
}

func TestParkSurvivesUntilAnotherBookIsFinished(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	read := finished("Mistborn", 3, day0)
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{read}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.Park(ctx, "Mistborn", day0, day0); err != nil {
		t.Fatalf("Park: %v", err)
	}

	// Nothing new finished: the park stands, however many times we reconcile.
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{read}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := decisionOf(t, st, "Mistborn"); got != Parked {
		t.Errorf("Decision = %v with nothing newly finished, want Parked", got)
	}

	// One book finished elsewhere is the "one instance" a park costs.
	snap := Snapshot{Reads: []library.Entry{finished("Wheel of Time", 1, day1), read}}
	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := decisionOf(t, st, "Mistborn"); got != Active {
		t.Errorf("Decision = %v after finishing another book, want Active", got)
	}
}

func TestDropIsNotUndoneByABookAlreadyOnTheTBR(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	// Book 4 was already sitting on the TBR when the series was dropped.
	stale := wanted("Mistborn", 4, day0)
	snap := Snapshot{Reads: []library.Entry{finished("Mistborn", 3, day0)}, ToRead: []library.Entry{stale}}
	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.Drop(ctx, "Mistborn", day1); err != nil {
		t.Fatalf("Drop: %v", err)
	}

	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := decisionOf(t, st, "Mistborn"); got != Dropped {
		t.Errorf("Decision = %v with only a pre-existing TBR book, want Dropped", got)
	}
}

func TestDropIsUndoneByAddingABookAfterwards(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{finished("Mistborn", 3, day0)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.Drop(ctx, "Mistborn", day1); err != nil {
		t.Fatalf("Drop: %v", err)
	}

	snap := Snapshot{ToRead: []library.Entry{wanted("Mistborn", 7, day2)}}
	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := decisionOf(t, st, "Mistborn"); got != Active {
		t.Errorf("Decision = %v after adding a book back, want Active", got)
	}
}

func TestPinClearsOnceThePinnedBookIsStarted(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{finished("Mistborn", 3, day0)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.Pin(ctx, "Mistborn", day0, 4); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if got := decisionOf(t, st, "Mistborn"); got != Pinned {
		t.Fatalf("Decision = %v right after pinning, want Pinned", got)
	}

	reading := readEntry("Mistborn", 4)
	reading.Status = library.StatusCurrentlyRead
	if err := st.Reconcile(ctx, Snapshot{Reading: []library.Entry{reading}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := decisionOf(t, st, "Mistborn"); got != Active {
		t.Errorf("Decision = %v after starting the pinned book, want Active", got)
	}
}

func TestPinningASecondSeriesReplacesTheFirst(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	if err := st.Pin(ctx, "Mistborn", day0, 4); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := st.Pin(ctx, "Wheel of Time", day0, 2); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	if got := decisionOf(t, st, "Mistborn"); got != Active {
		t.Errorf("Mistborn Decision = %v after pinning another series, want Active", got)
	}
	if got := decisionOf(t, st, "Wheel of Time"); got != Pinned {
		t.Errorf("Wheel of Time Decision = %v, want Pinned", got)
	}
}

func TestPinWithoutAKnownPositionIsNotClearedImmediately(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	// Some catalogues give a series name but no position. The pin still means
	// "this series next", and must survive the reconcile that follows it.
	if err := st.Pin(ctx, "Mistborn", day0, 0); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{finished("Mistborn", 3, day0)}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := decisionOf(t, st, "Mistborn"); got != Pinned {
		t.Errorf("Decision = %v, want Pinned: a positionless pin was spent before it was ever shown", got)
	}
}
