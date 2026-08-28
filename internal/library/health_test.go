package library

import (
	"context"
	"errors"
	"testing"
	"time"
)

// flakyListSource serves entries until broken, then errors.
type flakyListSource struct {
	name    string
	entries []Entry
	broken  bool
}

func (s *flakyListSource) Name() string { return s.name }
func (s *flakyListSource) CurrentlyReading(_ context.Context) ([]Entry, error) {
	return s.list()
}
func (s *flakyListSource) RecentReads(_ context.Context, _ int) ([]Entry, error) { return s.list() }
func (s *flakyListSource) ToRead(_ context.Context) ([]Entry, error)             { return s.list() }
func (s *flakyListSource) list() ([]Entry, error) {
	if s.broken {
		return nil, errors.New("connection refused")
	}
	return s.entries, nil
}

func TestCachedServesLastKnownGoodWhenTheSourceFails(t *testing.T) {
	ctx := context.Background()
	src := &flakyListSource{name: "gm", entries: []Entry{{Book: Book{Title: "Kept"}}}}
	now := time.Now()
	c := NewCached(src, time.Minute)
	c.now = func() time.Time { return now }

	if _, err := c.ToRead(ctx); err != nil {
		t.Fatalf("ToRead: %v", err)
	}

	// The source goes down and the cache expires: an outage should read as
	// old data, not as a broken page.
	src.broken = true
	now = now.Add(2 * time.Minute)
	entries, err := c.ToRead(ctx)
	if err != nil {
		t.Fatalf("ToRead during outage: %v", err)
	}
	if len(entries) != 1 || entries[0].Book.Title != "Kept" {
		t.Errorf("entries = %v, want the last known good data", entries)
	}
}

func TestCachedReportsStalenessSoThePageCanSaySo(t *testing.T) {
	ctx := context.Background()
	src := &flakyListSource{name: "gm", entries: []Entry{{Book: Book{Title: "X"}}}}
	now := time.Now()
	c := NewCached(src, time.Minute)
	c.now = func() time.Time { return now }

	if _, err := c.ToRead(ctx); err != nil {
		t.Fatalf("ToRead: %v", err)
	}
	if h := c.Health(); h.Stale {
		t.Errorf("Health = %+v after a clean fetch, want fresh", h)
	}

	src.broken = true
	now = now.Add(2 * time.Minute)
	if _, err := c.ToRead(ctx); err != nil {
		t.Fatalf("ToRead during outage: %v", err)
	}
	h := c.Health()
	if !h.Stale {
		t.Fatal("Health.Stale = false while serving fallback data")
	}
	if h.Source != "gm" || h.Since.IsZero() {
		t.Errorf("Health = %+v, want the source named and dated", h)
	}

	// Recovery clears the flag.
	src.broken = false
	now = now.Add(2 * time.Minute)
	if _, err := c.ToRead(ctx); err != nil {
		t.Fatalf("ToRead after recovery: %v", err)
	}
	if h := c.Health(); h.Stale {
		t.Errorf("Health = %+v after recovery, want fresh", h)
	}
}

func TestCachedStillErrorsWhenThereIsNothingToFallBackOn(t *testing.T) {
	ctx := context.Background()
	src := &flakyListSource{name: "gm", broken: true}
	c := NewCached(src, time.Minute)

	// A cold start during an outage has nothing honest to show.
	if _, err := c.ToRead(ctx); err == nil {
		t.Error("ToRead = nil error with no data ever fetched")
	}
}

func TestHealthOfIsCollectedThroughTheMerge(t *testing.T) {
	ctx := context.Background()
	a := &flakyListSource{name: "hc", entries: []Entry{{Book: Book{Title: "A"}}}}
	b := &flakyListSource{name: "gm", entries: []Entry{{Book: Book{Title: "B"}}}}
	now := time.Now()
	ca, cb := NewCached(a, time.Minute), NewCached(b, time.Minute)
	ca.now = func() time.Time { return now }
	cb.now = func() time.Time { return now }
	m := Combine(ca, cb)

	if _, err := m.ToRead(ctx); err != nil {
		t.Fatalf("ToRead: %v", err)
	}
	b.broken = true
	now = now.Add(2 * time.Minute)
	if _, err := m.ToRead(ctx); err != nil {
		t.Fatalf("ToRead with one source down: %v", err)
	}

	stale := 0
	for _, h := range HealthOf(m) {
		if h.Stale {
			stale++
			if h.Source != "gm" {
				t.Errorf("stale source = %q, want gm", h.Source)
			}
		}
	}
	if stale != 1 {
		t.Errorf("%d stale sources reported, want exactly the broken one", stale)
	}
}
