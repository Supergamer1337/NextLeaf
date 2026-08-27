package series

import (
	"context"
	"errors"
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
	return library.SeriesQuery{Series: library.Series{Name: name, Position: pos}, IncludeNovellas: true}
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

func TestLookaheadDoesNotCacheFailures(t *testing.T) {
	ctx := context.Background()
	r := &countingResolver{err: errors.New("rate limited")}
	l := NewLookahead(r, 24*time.Hour)
	l.now = func() time.Time { return day0 }

	// Caching an error would blind the reader to a series for a whole day.
	for i := 0; i < 2; i++ {
		if _, _, err := l.Next(ctx, query("Mistborn", 3)); err == nil {
			t.Fatal("Next should surface the resolver's error")
		}
	}
	if r.calls != 2 {
		t.Errorf("resolver called %d times, want 2: errors must not be cached", r.calls)
	}
}
