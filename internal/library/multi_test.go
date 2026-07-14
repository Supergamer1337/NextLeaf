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

func TestMultiToReadDedupsAcrossSources(t *testing.T) {
	jan := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)

	m := Combine(
		listSource{name: "a", toRead: []Entry{{
			Book:      Book{Title: "Dune", Authors: []string{"Frank Herbert"}},
			DateAdded: feb,
			Sources:   []string{"a"},
		}}},
		listSource{name: "b", toRead: []Entry{{
			Book:      Book{Title: "Dune", Authors: []string{"Frank Herbert"}, PageCount: 412, CoverURL: "x"},
			DateAdded: jan,
			Sources:   []string{"b"},
			Available: true,
		}}},
	)

	got, err := m.ToRead(context.Background())
	if err != nil {
		t.Fatalf("ToRead: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %v, want one merged entry", titles(got))
	}
	e := got[0]
	if len(e.Sources) != 2 || e.Sources[0] != "a" || e.Sources[1] != "b" {
		t.Errorf("Sources = %v, want [a b]", e.Sources)
	}
	if !e.DateAdded.Equal(jan) {
		t.Errorf("DateAdded = %v, want the earliest (%v)", e.DateAdded, jan)
	}
	if e.Book.PageCount != 412 || e.Book.CoverURL != "x" {
		t.Errorf("Book = %+v, want zero fields filled from the duplicate", e.Book)
	}
	if !e.Available {
		t.Error("Available = false, want true (OR across duplicates)")
	}
}

func TestMultiToReadDedupNormalizesKey(t *testing.T) {
	now := time.Now()
	m := Combine(
		listSource{name: "a", toRead: []Entry{{
			Book: Book{Title: "  DUNE ", Authors: []string{"frank   herbert"}}, DateAdded: now,
		}}},
		listSource{name: "b", toRead: []Entry{{
			Book: Book{Title: "Dune", Authors: []string{"Frank Herbert"}}, DateAdded: now,
		}}},
	)

	got, err := m.ToRead(context.Background())
	if err != nil {
		t.Fatalf("ToRead: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %v, want one entry despite casing/whitespace", titles(got))
	}
}

func TestMultiToReadKeepsDistinctBooks(t *testing.T) {
	now := time.Now()
	cases := map[string][2]Entry{
		"same title, different author": {
			{Book: Book{Title: "Dune", Authors: []string{"Frank Herbert"}}, DateAdded: now},
			{Book: Book{Title: "Dune", Authors: []string{"Someone Else"}}, DateAdded: now},
		},
		"same author, different title": {
			{Book: Book{Title: "Dune", Authors: []string{"Frank Herbert"}}, DateAdded: now},
			{Book: Book{Title: "Dune Messiah", Authors: []string{"Frank Herbert"}}, DateAdded: now},
		},
		"empty titles never merge": {
			{Book: Book{}, DateAdded: now},
			{Book: Book{}, DateAdded: now},
		},
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			m := Combine(
				listSource{name: "a", toRead: []Entry{pair[0]}},
				listSource{name: "b", toRead: []Entry{pair[1]}},
			)
			got, err := m.ToRead(context.Background())
			if err != nil {
				t.Fatalf("ToRead: %v", err)
			}
			if len(got) != 2 {
				t.Errorf("got %v, want both entries kept", titles(got))
			}
		})
	}
}

func TestMultiToReadDedupPreservesDateOrder(t *testing.T) {
	jan := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	mar := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

	m := Combine(
		listSource{name: "a", toRead: []Entry{entry("solo-feb", feb), entry("dup", mar)}},
		listSource{name: "b", toRead: []Entry{entry("dup", jan)}},
	)

	got, err := m.ToRead(context.Background())
	if err != nil {
		t.Fatalf("ToRead: %v", err)
	}
	// dup merged with the earliest DateAdded (jan) sorts before solo-feb.
	want := []string{"dup", "solo-feb"}
	if ts := titles(got); len(ts) != 2 || ts[0] != want[0] || ts[1] != want[1] {
		t.Errorf("got %v, want %v", ts, want)
	}
}

func TestMultiToReadDedupDoesNotMutateInputs(t *testing.T) {
	now := time.Now()
	aEntries := []Entry{{
		Book:      Book{Title: "Dune", Authors: []string{"Frank Herbert"}},
		DateAdded: now,
		Sources:   []string{"a"},
	}}
	bEntries := []Entry{{
		Book:      Book{Title: "Dune", Authors: []string{"Frank Herbert"}, PageCount: 412},
		DateAdded: now,
		Sources:   []string{"b"},
		Available: true,
	}}

	m := Combine(
		listSource{name: "a", toRead: aEntries},
		listSource{name: "b", toRead: bEntries},
	)
	if _, err := m.ToRead(context.Background()); err != nil {
		t.Fatalf("ToRead: %v", err)
	}

	// Sources hand back retained slices; the merge must not write into them.
	if len(aEntries[0].Sources) != 1 || aEntries[0].Sources[0] != "a" ||
		aEntries[0].Available || aEntries[0].Book.PageCount != 0 {
		t.Errorf("source a's entry was mutated: %+v", aEntries[0])
	}
	if len(bEntries[0].Sources) != 1 || bEntries[0].Sources[0] != "b" {
		t.Errorf("source b's entry was mutated: %+v", bEntries[0])
	}
}

func TestMultiRecentReadsDoesNotDedup(t *testing.T) {
	now := time.Now()
	same := Entry{Book: Book{Title: "Dune", Authors: []string{"Frank Herbert"}}, FinishedAt: now}
	m := Combine(
		listSource{name: "a", reads: []Entry{same}},
		listSource{name: "b", reads: []Entry{same}},
	)

	got, err := m.RecentReads(context.Background(), 0)
	if err != nil {
		t.Fatalf("RecentReads: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %v, want reads kept per source (history, not a pool)", titles(got))
	}
}

func titles(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Book.Title
	}
	return out
}
