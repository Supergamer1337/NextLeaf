package library

import (
	"context"
	"errors"
	"testing"
	"time"
)

// listSource is a Source backed by fixed slices.
type listSource struct {
	name      string
	reading   []Entry
	reads     []Entry
	toRead    []Entry
	readsErr  error
	toReadErr error
}

func (s listSource) Name() string { return s.name }
func (s listSource) CurrentlyReading(_ context.Context) ([]Entry, error) {
	return s.reading, nil
}
func (s listSource) RecentReads(_ context.Context, _ int) ([]Entry, error) {
	return s.reads, s.readsErr
}
func (s listSource) ToRead(_ context.Context) ([]Entry, error) {
	return s.toRead, s.toReadErr
}

func entry(title string, d time.Time) Entry {
	return Entry{Book: Book{Title: title}, DateAdded: d, FinishedAt: d}
}

func TestCombine(t *testing.T) {
	if got := Combine(); got != nil {
		t.Errorf("Combine() = %v, want nil", got)
	}

	one := Combine(listSource{name: "a"})
	if _, ok := one.(*Multi); ok {
		t.Error("Combine(one) should not wrap a single source in Multi")
	}
	if one.Name() != "a" {
		t.Errorf("Combine(one).Name() = %q, want a", one.Name())
	}

	got := Combine(listSource{name: "a"}, listSource{name: "b"})
	if _, ok := got.(*Multi); !ok {
		t.Errorf("Combine(two) = %T, want *Multi", got)
	}
	if got.Name() != "a+b" {
		t.Errorf("Name() = %q, want a+b", got.Name())
	}
}

func TestMultiRecentReadsMergesAndSorts(t *testing.T) {
	jan := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	mar := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

	m := Combine(
		listSource{name: "a", reads: []Entry{entry("jan", jan), entry("mar", mar)}},
		listSource{name: "b", reads: []Entry{entry("feb", feb)}},
	)

	got, err := m.RecentReads(context.Background(), 0)
	if err != nil {
		t.Fatalf("RecentReads: %v", err)
	}
	want := []string{"mar", "feb", "jan"} // newest FinishedAt first
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, title := range want {
		if got[i].Book.Title != title {
			t.Errorf("entry %d = %q, want %q", i, got[i].Book.Title, title)
		}
	}
}

func TestMultiRecentReadsRespectsLimit(t *testing.T) {
	jan := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	mar := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

	m := Combine(
		listSource{name: "a", reads: []Entry{entry("jan", jan)}},
		listSource{name: "b", reads: []Entry{entry("feb", feb), entry("mar", mar)}},
	)

	got, err := m.RecentReads(context.Background(), 2)
	if err != nil {
		t.Fatalf("RecentReads: %v", err)
	}
	if len(got) != 2 || got[0].Book.Title != "mar" || got[1].Book.Title != "feb" {
		t.Errorf("got %v, want [mar feb]", titles(got))
	}
}

func TestMultiToReadMergesOldestFirst(t *testing.T) {
	jan := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)

	m := Combine(
		listSource{name: "a", toRead: []Entry{entry("feb", feb)}},
		listSource{name: "b", toRead: []Entry{entry("jan", jan)}},
	)

	got, err := m.ToRead(context.Background())
	if err != nil {
		t.Fatalf("ToRead: %v", err)
	}
	if len(got) != 2 || got[0].Book.Title != "jan" || got[1].Book.Title != "feb" {
		t.Errorf("got %v, want [jan feb]", titles(got))
	}
}

func TestMultiCurrentlyReadingConcatenates(t *testing.T) {
	now := time.Now()
	m := Combine(
		listSource{name: "a", reading: []Entry{entry("a1", now)}},
		listSource{name: "b", reading: []Entry{entry("b1", now), entry("b2", now)}},
	)

	got, err := m.CurrentlyReading(context.Background())
	if err != nil {
		t.Fatalf("CurrentlyReading: %v", err)
	}
	want := []string{"a1", "b1", "b2"} // source order preserved
	if titles := titles(got); len(titles) != 3 || titles[0] != want[0] || titles[2] != want[2] {
		t.Errorf("got %v, want %v", titles, want)
	}
}

func TestMultiPropagatesError(t *testing.T) {
	m := Combine(
		listSource{name: "a"},
		listSource{name: "b", readsErr: errors.New("boom")},
	)
	if _, err := m.RecentReads(context.Background(), 0); err == nil {
		t.Error("want error from failing source, got nil")
	}
}

func titles(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Book.Title
	}
	return out
}
