package series

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"nextleaf/internal/library"
)

// countingResolver reports how many times it was actually asked.
type countingResolver struct {
	calls int
	entry library.Entry
	found bool
	err   error
}

func (r *countingResolver) NextInSeries(_ context.Context, _ library.SeriesQuery) (library.Entry, bool, error) {
	r.calls++
	return r.entry, r.found, r.err
}

func mistborn4() library.Entry {
	return library.Entry{Book: library.Book{Title: "The Alloy of Law"}}
}

func query(name string, pos float64) library.SeriesQuery {
	return library.SeriesQuery{Series: library.Series{Name: name, Position: library.At(pos)}, IncludeNovellas: true}
}

func TestLookaheadAsksTheResolverOncePerSeries(t *testing.T) {
	ctx := context.Background()
	r := &countingResolver{entry: mistborn4(), found: true}
	clock := day0
	l := NewLookahead(r, 24*time.Hour)
	l.now = func() time.Time { return clock }

	for i := 0; i < 3; i++ {
		entry, found, err := l.Next(ctx, query("Mistborn", 3))
		if err != nil || !found || entry.Book.Title != "The Alloy of Law" {
			t.Fatalf("Next = (%q, %v, %v)", entry.Book.Title, found, err)
		}
	}
	if r.calls != 1 {
		t.Errorf("resolver called %d times, want 1: a new book appears at most daily", r.calls)
	}
}

func TestLookaheadAsksAgainOnceTheCacheHasExpired(t *testing.T) {
	ctx := context.Background()
	r := &countingResolver{entry: mistborn4(), found: true}
	clock := day0
	l := NewLookahead(r, 24*time.Hour)
	l.now = func() time.Time { return clock }

	if _, _, err := l.Next(ctx, query("Mistborn", 3)); err != nil {
		t.Fatalf("Next: %v", err)
	}
	clock = day0.Add(25 * time.Hour)
	if _, _, err := l.Next(ctx, query("Mistborn", 3)); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if r.calls != 2 {
		t.Errorf("resolver called %d times, want 2 after the TTL lapsed", r.calls)
	}
}

func TestLookaheadTreatsAdvancingPositionAsANewQuestion(t *testing.T) {
	ctx := context.Background()
	r := &countingResolver{entry: mistborn4(), found: true}
	l := NewLookahead(r, 24*time.Hour)
	l.now = func() time.Time { return day0 }

	// Finishing book 4 must not serve the answer cached for book 3.
	if _, _, err := l.Next(ctx, query("Mistborn", 3)); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if _, _, err := l.Next(ctx, query("Mistborn", 4)); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if r.calls != 2 {
		t.Errorf("resolver called %d times, want 2: a different position is a different question", r.calls)
	}
}

func TestLookaheadHoldsAFailureBrieflyThenTriesAgain(t *testing.T) {
	ctx := context.Background()
	r := &countingResolver{err: errors.New("rate limited")}
	l := NewLookahead(r, 24*time.Hour)
	now := day0
	l.now = func() time.Time { return now }

	// A throttled backend answers every row at once. Retrying each of them on
	// every render puts the round trip back into every page load for as long
	// as the backend stays down, which the reader feels as a slow app.
	for i := 0; i < 3; i++ {
		if _, _, err := l.Next(ctx, query("Mistborn", 3)); err == nil {
			t.Fatal("Next should surface the resolver's error")
		}
	}
	if r.calls != 1 {
		t.Errorf("resolver called %d times, want 1: a fresh failure is held", r.calls)
	}

	// Held briefly, though: caching it like an answer would blind the reader
	// to a series for a whole day.
	now = day0.Add(failureTTL + time.Second)
	if _, _, err := l.Next(ctx, query("Mistborn", 3)); err == nil {
		t.Fatal("Next should surface the resolver's error")
	}
	if r.calls != 2 {
		t.Errorf("resolver called %d times, want 2: the failure is retried once it is stale", r.calls)
	}

	// Reading a held failure back costs no round trip, so it is cached as far
	// as a caller budgeting its round trips is concerned.
	if !l.Cached(query("Mistborn", 3)) {
		t.Error("a freshly held failure should not be budgeted as a round trip")
	}
}

func TestUnplacedAndSlotZeroDoNotShareACacheEntry(t *testing.T) {
	ctx := context.Background()
	r := &countingResolver{entry: mistborn4(), found: true}
	l := NewLookahead(r, 24*time.Hour)
	l.now = func() time.Time { return day0 }

	placed := library.SeriesQuery{Series: library.Series{Name: "Saga", Position: library.At(0)}, IncludeNovellas: true}
	unplaced := library.SeriesQuery{Series: library.Series{Name: "Saga"}, IncludeNovellas: true}
	if _, _, err := l.Next(ctx, placed); err != nil {
		t.Fatal(err)
	}
	if l.Cached(unplaced) {
		t.Error("an unplaced query hits the slot-0 cache entry")
	}
}

func TestTheNovellaPreferenceIsPartOfTheCacheKey(t *testing.T) {
	ctx := context.Background()
	r := &countingResolver{entry: mistborn4(), found: true}
	l := NewLookahead(r, 24*time.Hour)
	l.now = func() time.Time { return day0 }

	with := library.SeriesQuery{Series: library.Series{Name: "Saga", Position: library.At(3)}, IncludeNovellas: true}
	without := with
	without.IncludeNovellas = false
	if _, _, err := l.Next(ctx, with); err != nil {
		t.Fatal(err)
	}
	// A novella-inclusive answer is not an answer to a query that excludes them.
	if l.Cached(without) {
		t.Error("a novella-excluding query hits the novella-inclusive cache entry")
	}
}

func TestAFinishedSeriesIsRecheckedWeekly(t *testing.T) {
	// A series its provider calls complete, read to its end, will not grow;
	// asking about it daily only spends the backend's patience.
	ctx := context.Background()
	r := &countingResolver{found: false}
	l := NewLookahead(r, 24*time.Hour)
	now := day0
	l.now = func() time.Time { return now }
	done := query("Mistborn", 3)
	done.Series.Completed = true
	ongoing := query("Stormlight", 5)

	for _, q := range []library.SeriesQuery{done, ongoing} {
		if _, _, err := l.Next(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	now = day0.AddDate(0, 0, 2)
	if !l.Cached(done) {
		t.Error("a finished series was due a re-check after two days")
	}
	if l.Cached(ongoing) {
		t.Error("an ongoing series was not re-checked after two days")
	}
	now = day0.AddDate(0, 0, 8)
	if l.Cached(done) {
		t.Error("a finished series was not re-checked after a week")
	}
}

func TestTheLastAnswerOutlastsItsFreshness(t *testing.T) {
	// Past its freshness an answer is due a re-check, but it is still the
	// best thing to show until the re-check lands, and still is if the
	// re-check fails.
	ctx := context.Background()
	r := &countingResolver{entry: mistborn4(), found: true}
	l := NewLookahead(r, 24*time.Hour)
	now := day0
	l.now = func() time.Time { return now }
	if _, _, err := l.Next(ctx, query("Mistborn", 3)); err != nil {
		t.Fatal(err)
	}

	now = day0.AddDate(0, 0, 2)
	r.err = errors.New("rate limited")
	if l.Cached(query("Mistborn", 3)) {
		t.Fatal("a two-day-old answer is still fresh")
	}
	if _, _, err := l.Next(ctx, query("Mistborn", 3)); err == nil {
		t.Fatal("the failed re-check should surface")
	}
	entry, found, ok := l.Last(query("Mistborn", 3))
	if !ok || !found || entry.Book.Title != mistborn4().Book.Title {
		t.Errorf("Last = (%q, %v, %v), want the answer the failed re-check could not replace", entry.Book.Title, found, ok)
	}
}

func TestLookaheadDoesNotHoldAnAskItsCallerCancelled(t *testing.T) {
	// A reader navigating away, or shutdown, cuts an ask short. The catalogue
	// did not fail, so the question is neither held nor marked failed: it is
	// still to be answered.
	r := &countingResolver{err: context.Canceled}
	l := NewLookahead(r, 24*time.Hour)
	now := day0
	l.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := l.Next(ctx, query("Mistborn", 3)); err == nil {
		t.Fatal("Next should surface the cancellation")
	}
	if l.Cached(query("Mistborn", 3)) || l.Failed(query("Mistborn", 3)) {
		t.Error("a cancelled ask was held as the catalogue's failure")
	}

	// A catalogue that fails on its own is another matter: once may be a
	// hiccup, twice running and the question is given up for now.
	r.err = errors.New("unauthorized")
	for i := range giveUpAfter {
		if l.Failed(query("Mistborn", 3)) {
			t.Fatalf("given up after %d failures, want %d", i, giveUpAfter)
		}
		if _, _, err := l.Next(context.Background(), query("Mistborn", 3)); err == nil {
			t.Fatal("Next should surface the resolver's error")
		}
		now = now.Add(failureTTL + time.Second)
	}
	if !l.Failed(query("Mistborn", 3)) {
		t.Error("a question failing every time is never given up")
	}
}

type resolverFunc func(context.Context, library.SeriesQuery) (library.Entry, bool, error)

func (f resolverFunc) NextInSeries(ctx context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	return f(ctx, q)
}

func TestAFailureDoesNotUndoAnAnswerThatLandedMeanwhile(t *testing.T) {
	// The background pass and a decision's render can ask the same question at
	// once. The one that fails last must not wipe out the one that answered.
	slow := make(chan struct{})
	var calls atomic.Int32
	l := NewLookahead(resolverFunc(func(context.Context, library.SeriesQuery) (library.Entry, bool, error) {
		if calls.Add(1) == 1 {
			<-slow
			return library.Entry{}, false, errors.New("rate limited")
		}
		return mistborn4(), true, nil
	}), 24*time.Hour)
	l.now = func() time.Time { return day0 }
	ctx := context.Background()

	failed := make(chan struct{})
	go func() { _, _, _ = l.Next(ctx, query("Mistborn", 3)); close(failed) }()
	for calls.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	if _, _, err := l.Next(ctx, query("Mistborn", 3)); err != nil {
		t.Fatal(err)
	}
	close(slow)
	<-failed

	if entry, found, known := l.Last(query("Mistborn", 3)); !known || !found || entry.Book.Title != "The Alloy of Law" {
		t.Errorf("Last = %q, %v, %v; the late failure undid the answer", entry.Book.Title, found, known)
	}
	if got, _, err := l.Next(ctx, query("Mistborn", 3)); err != nil || got.Book.Title != "The Alloy of Law" {
		t.Errorf("Next = %q, %v; want the answer, not the failure that lost the race", got.Book.Title, err)
	}
}
