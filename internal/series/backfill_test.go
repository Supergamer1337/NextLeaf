package series

import (
	"context"
	"errors"
	"testing"

	"nextleaf/internal/library"
)

// fakeHistory is a HistoryProvider that hands back a fixed history, or fails.
type fakeHistory struct {
	name    string
	entries []library.Entry
	err     error
}

func (f fakeHistory) Name() string { return f.name }

func (f fakeHistory) ReadHistory(_ context.Context) ([]library.Entry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

func TestBackfillIsNotDoneBeforeItRuns(t *testing.T) {
	b := NewBackfill(openStore(t), []library.HistoryProvider{fakeHistory{name: "hardcover"}})
	if b.Status().Done {
		t.Error("a backfill that has not run yet reports Done")
	}
}

func TestBackfillWithNoCapableSourcesIsImmediatelyDone(t *testing.T) {
	// Nothing to wait for, so continuations must not sit behind a banner.
	b := NewBackfill(openStore(t), nil)
	if !b.Status().Done {
		t.Error("a backfill with no capable sources should be Done")
	}
}

func TestBackfillTracksSeriesFromEveryHistory(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	b := NewBackfill(st, []library.HistoryProvider{
		fakeHistory{name: "hardcover", entries: []library.Entry{finished("Mistborn", 3, day0)}},
		fakeHistory{name: "grimmory", entries: []library.Entry{finished("Stormlight", 4, day1)}},
	})

	b.Run(ctx)

	tracked, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tracked) != 2 {
		t.Fatalf("got %d tracked series, want 2 (one per history)", len(tracked))
	}
	if !b.Status().Done {
		t.Error("backfill should report Done after running")
	}
}

func TestBackfillKeepsOneSourcesHistoryWhenAnotherFails(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	b := NewBackfill(st, []library.HistoryProvider{
		fakeHistory{name: "hardcover", err: errors.New("rate limited")},
		fakeHistory{name: "grimmory", entries: []library.Entry{finished("Stormlight", 4, day1)}},
	})

	b.Run(ctx)

	tracked, err := st.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tracked) != 1 || tracked[0].Name != "Stormlight" {
		t.Errorf("tracked = %v, want the working source's series to survive", names(tracked))
	}
	// A flaky backend must not disable series tracking for good; the 24h
	// resync retries it.
	status := b.Status()
	if !status.Done {
		t.Error("backfill should report Done even when a source failed")
	}
	if len(status.Failed) != 1 || status.Failed[0] != "hardcover" {
		t.Errorf("Failed = %v, want [hardcover]", status.Failed)
	}
}
