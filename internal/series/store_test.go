package series

import (
	"context"
	"path/filepath"
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
