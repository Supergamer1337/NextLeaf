package series

import (
	"context"
	"errors"
	"testing"

	"nextleaf/internal/library"
)

// scriptedResolver answers per series name, and can fail for one of them.
type scriptedResolver struct {
	answers map[string]library.Entry
	fail    map[string]bool
	asked   []string
}

func (r *scriptedResolver) NextInSeries(_ context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	name := q.Series.Name
	r.asked = append(r.asked, name)
	if r.fail[name] {
		return library.Entry{}, false, errors.New("rate limited")
	}
	e, ok := r.answers[name]
	return e, ok, nil
}

// quick strips the pacing so tests do not sit through the real refresh delays.
func quick(r *Refresher) *Refresher {
	r.pause, r.backoff = 0, 0
	return r
}

func trackedStore(t *testing.T, reads ...library.Entry) *Store {
	t.Helper()
	st := openStore(t)
	if err := st.Reconcile(context.Background(), Snapshot{Reads: reads}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return st
}

func TestRefreshRemembersEachSeriesNextBook(t *testing.T) {
	ctx := context.Background()
	st := trackedStore(t, readEntry("Mistborn", 3), readEntry("Stormlight", 4))
	r := &scriptedResolver{answers: map[string]library.Entry{"Mistborn": nextBook("alloy")}}

	quick(NewRefresher(st, NewLookahead(r, 0), true)).Run(ctx)

	mistborn, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mistborn.NextTitle != "alloy" {
		t.Errorf("Mistborn NextTitle = %q, want alloy", mistborn.NextTitle)
	}
	// Stormlight had no answer, so the reader is caught up with it.
	stormlight, _, err := st.Get(ctx, "Stormlight")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !stormlight.CaughtUp {
		t.Error("Stormlight CaughtUp = false, want it filed under Finished")
	}
}

func TestRefreshLeavesTheOtherSeriesAloneWhenOneLookupFails(t *testing.T) {
	ctx := context.Background()
	st := trackedStore(t, readEntry("Mistborn", 3), readEntry("Stormlight", 4))
	r := &scriptedResolver{
		answers: map[string]library.Entry{"Stormlight": nextBook("words")},
		fail:    map[string]bool{"Mistborn": true},
	}

	quick(NewRefresher(st, NewLookahead(r, 0), true)).Run(ctx)

	stormlight, _, err := st.Get(ctx, "Stormlight")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stormlight.NextTitle != "words" {
		t.Errorf("Stormlight NextTitle = %q, want the working lookup to land", stormlight.NextTitle)
	}
	// A failed lookup must not be mistaken for "nothing left to read".
	mistborn, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if mistborn.CaughtUp {
		t.Error("a failed lookup wrongly filed Mistborn under Finished")
	}
}

func TestRefreshDoesNotAskAboutSeriesThatCannotHaveANextBook(t *testing.T) {
	ctx := context.Background()
	st := trackedStore(t, readEntry("Mistborn", 3), readEntry("Dune", 1), readEntry("Unknown", 0))
	if err := st.Drop(ctx, "Dune", day0); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	r := &scriptedResolver{answers: map[string]library.Entry{}}

	quick(NewRefresher(st, NewLookahead(r, 0), true)).Run(ctx)

	for _, name := range r.asked {
		if name == "Dune" {
			t.Error("asked about a dropped series")
		}
		if name == "Unknown" {
			t.Error("asked about a series with no known position")
		}
	}
	if len(r.asked) != 1 {
		t.Errorf("asked about %v, want only Mistborn", r.asked)
	}
}

func TestOrderLeavesOutSeriesTheReaderIsCaughtUpWith(t *testing.T) {
	// Nothing left to read means nothing to recommend; the drawer lists it
	// under Finished instead.
	tracked := []Tracked{
		{Name: "Mistborn", Position: 3, CaughtUp: true},
		{Name: "Stormlight", Position: 4},
	}
	got := names(Order(tracked, nil, nil))
	if len(got) != 1 || got[0] != "Stormlight" {
		t.Errorf("Order = %v, want just Stormlight", got)
	}
}

// flakyOnce fails a series' first lookup and answers the retry.
type flakyOnce struct {
	failed map[string]bool
	answer library.Entry
}

func (r *flakyOnce) NextInSeries(_ context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	if !r.failed[q.Series.Name] {
		r.failed[q.Series.Name] = true
		return library.Entry{}, false, errors.New("hardcover: rate limited")
	}
	return r.answer, true, nil
}

func TestRefreshRetriesAThrottledLookup(t *testing.T) {
	ctx := context.Background()
	st := trackedStore(t, readEntry("Mistborn", 3))
	r := &flakyOnce{failed: map[string]bool{}, answer: nextBook("alloy")}

	// Waiting a whole day to retry a series the source merely throttled would
	// leave the drawer wrong for far too long.
	ref := NewRefresher(st, NewLookahead(r, 0), true)
	ref.pause, ref.backoff = 0, 0
	ref.Run(ctx)

	tracked, _, err := st.Get(ctx, "Mistborn")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tracked.NextTitle != "alloy" {
		t.Errorf("NextTitle = %q, want the retry to have landed", tracked.NextTitle)
	}
}
