package series

import (
	"context"
	"testing"

	"nextleaf/internal/library"
)

// expanse is a book filed in two orderings of one franchise, numbered
// differently in each, as Hardcover really files The Expanse.
func expanse() library.Entry {
	e := library.Entry{
		Book: library.Book{
			Title:  "Leviathan Wakes",
			Series: &library.Series{Name: "The Expanse (Chronological)", Position: library.At(2)},
			OtherSeries: []library.Series{
				{Name: "The Expanse", Position: library.At(1)},
			},
		},
		Status: library.StatusRead,
	}
	e.FinishedAt = day0
	return e
}

func TestAlternativeSeriesAreOfferedForATrackedSeries(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{expanse()}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, ok, err := st.Get(ctx, "The Expanse (Chronological)")
	if err != nil || !ok {
		t.Fatalf("Get = (%v, %v)", ok, err)
	}
	if len(tracked.Alternatives) != 1 || tracked.Alternatives[0] != "The Expanse" {
		t.Errorf("Alternatives = %v, want [The Expanse]", tracked.Alternatives)
	}
}

func TestSwitchingTracksTheSeriesUnderTheChosenName(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{expanse()}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if err := st.Switch(ctx, "The Expanse (Chronological)", "The Expanse"); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	// The books have not changed, so reconciling again must respect the choice
	// rather than reverting to the ranking's pick.
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{expanse()}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tracked) != 1 {
		t.Fatalf("tracked = %v, want only the chosen series", names(tracked))
	}
	if tracked[0].Name != "The Expanse" {
		t.Errorf("tracked %q, want The Expanse", tracked[0].Name)
	}
	// Each ordering numbers the book differently, so the position must follow.
	if pos, ok := tracked[0].Slot(); !ok || pos != 1 {
		t.Errorf("Position = %v (ok=%v), want 1, the slot in the chosen ordering", pos, ok)
	}
}

func TestSwitchingCarriesTheStandingDecisionAcross(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{expanse()}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.Drop(ctx, "The Expanse (Chronological)", day1); err != nil {
		t.Fatalf("Drop: %v", err)
	}

	if err := st.Switch(ctx, "The Expanse (Chronological)", "The Expanse"); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if err := st.Reconcile(ctx, Snapshot{Reads: []library.Entry{expanse()}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Correcting which series a book is filed under is not a change of heart
	// about reading it.
	if got := decisionOf(t, st, "The Expanse"); got != Dropped {
		t.Errorf("Decision = %v after switching, want the drop to carry across", got)
	}
}

func TestSwitchingBackIsPossible(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	snap := Snapshot{Reads: []library.Entry{expanse()}}
	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.Switch(ctx, "The Expanse (Chronological)", "The Expanse"); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := st.Switch(ctx, "The Expanse", "The Expanse (Chronological)"); err != nil {
		t.Fatalf("Switch back: %v", err)
	}
	if err := st.Reconcile(ctx, snap); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	tracked, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tracked) != 1 || tracked[0].Name != "The Expanse (Chronological)" {
		t.Errorf("tracked = %v, want only The Expanse (Chronological)", names(tracked))
	}
}
