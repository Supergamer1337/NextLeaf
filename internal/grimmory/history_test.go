package grimmory

import (
	"context"
	"testing"
)

// historyFixture has three finished books, one dated to the day.
const historyFixture = `[
	{"id":1,"title":"Reading","readStatus":"READING"},
	{"id":2,"readStatus":"READ","dateFinished":"2025-06-01T00:00:00Z","metadata":{"title":"Hyperion","publishedDate":"1989-05-26","seriesName":"Hyperion Cantos","seriesNumber":1}},
	{"id":3,"readStatus":"READ","dateFinished":"2024-06-01T00:00:00Z","metadata":{"title":"Endymion"}},
	{"id":4,"readStatus":"READ","dateFinished":"2023-06-01T00:00:00Z","metadata":{"title":"Ilium"}}
]`

func historyClient(t *testing.T) *Client {
	t.Helper()
	f := &fake{books: acceptLatest(historyFixture)}
	return New(f.server(t).URL, "user", "pass")
}

func TestPublishedDateBecomesAReleaseDate(t *testing.T) {
	c := historyClient(t)

	history, err := c.RecentReads(context.Background(), 0)
	if err != nil {
		t.Fatalf("RecentReads: %v", err)
	}
	// Dating to the day is what decides whether a series' next book is out.
	got := history[0].Book.ReleaseDate
	if want := "1989-05-26"; got.Format("2006-01-02") != want {
		t.Errorf("ReleaseDate = %v, want %s", got, want)
	}
}

func TestBooksCarryTheirISBNs(t *testing.T) {
	const withISBN = `[{"id":2,"readStatus":"READ","dateFinished":"2025-06-01T00:00:00Z",
	  "metadata":{"title":"Hyperion","isbn13":"9780553283686","isbn10":"0553283685"}}]`
	f := &fake{books: acceptLatest(withISBN)}
	c := New(f.server(t).URL, "user", "pass")

	reads, err := c.RecentReads(context.Background(), 0)
	if err != nil {
		t.Fatalf("RecentReads: %v", err)
	}
	// ISBNs are the neutral identifiers that join this book to another
	// source's copy without guessing from the title.
	got := reads[0].Book.ISBNs
	if len(got) != 2 || got[0] != "9780553283686" {
		t.Errorf("ISBNs = %v, want the isbn13 first then isbn10", got)
	}
}
