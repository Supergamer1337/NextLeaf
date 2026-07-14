package grimmory

import (
	"context"
	"testing"
	"time"

	"nextleaf/internal/library"
)

// statusFixture covers every readStatus Grimmory can report, plus a book with
// the field absent (NON_NULL serialization) and one with an unknown value.
const statusFixture = `[
	{"id":1,"title":"Unread","readStatus":"UNREAD","addedOn":"2024-05-01T00:00:00Z"},
	{"id":2,"title":"Unset","readStatus":"UNSET","addedOn":"2024-01-01T00:00:00Z"},
	{"id":3,"title":"Absent","addedOn":"2024-03-01T00:00:00Z"},
	{"id":4,"title":"Reading","readStatus":"READING"},
	{"id":5,"title":"Rereading","readStatus":"RE_READING"},
	{"id":6,"title":"ReadNew","readStatus":"READ","dateFinished":"2025-06-01T00:00:00Z","personalRating":9},
	{"id":7,"title":"ReadOld","readStatus":"READ","dateFinished":"2024-06-01T00:00:00Z"},
	{"id":8,"title":"ReadUndated","readStatus":"READ"},
	{"id":9,"title":"Paused","readStatus":"PAUSED"},
	{"id":10,"title":"Partial","readStatus":"PARTIALLY_READ"},
	{"id":11,"title":"Wont","readStatus":"WONT_READ"},
	{"id":12,"title":"Abandoned","readStatus":"ABANDONED"},
	{"id":13,"title":"Mystery","readStatus":"SOMETHING_NEW"}
]`

func statusClient(t *testing.T) *Client {
	t.Helper()
	f := &fake{books: acceptLatest(statusFixture)}
	return New(f.server(t).URL, "user", "pass")
}

func titles(entries []library.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Book.Title
	}
	return out
}

func assertTitles(t *testing.T, got []library.Entry, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("titles = %v, want %v", titles(got), want)
	}
	for i, w := range want {
		if got[i].Book.Title != w {
			t.Fatalf("titles = %v, want %v", titles(got), want)
		}
	}
}

func TestName(t *testing.T) {
	if got := New("http://gm.local", "u", "p").Name(); got != "grimmory" {
		t.Errorf("Name() = %q, want grimmory", got)
	}
}

func TestToRead(t *testing.T) {
	c := statusClient(t)
	entries, err := c.ToRead(context.Background())
	if err != nil {
		t.Fatalf("ToRead: %v", err)
	}

	// Unread + unset + absent-status books, oldest added first.
	assertTitles(t, entries, "Unset", "Absent", "Unread")
	for _, e := range entries {
		if e.Status != library.StatusWantToRead {
			t.Errorf("%s: status = %v, want StatusWantToRead", e.Book.Title, e.Status)
		}
	}
	wantAdded := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if !entries[0].DateAdded.Equal(wantAdded) {
		t.Errorf("DateAdded = %v, want %v", entries[0].DateAdded, wantAdded)
	}
}

func TestCurrentlyReading(t *testing.T) {
	c := statusClient(t)
	entries, err := c.CurrentlyReading(context.Background())
	if err != nil {
		t.Fatalf("CurrentlyReading: %v", err)
	}

	assertTitles(t, entries, "Reading", "Rereading")
	for _, e := range entries {
		if e.Status != library.StatusCurrentlyRead {
			t.Errorf("%s: status = %v, want StatusCurrentlyRead", e.Book.Title, e.Status)
		}
	}
}

func TestRecentReads(t *testing.T) {
	c := statusClient(t)

	entries, err := c.RecentReads(context.Background(), 0)
	if err != nil {
		t.Fatalf("RecentReads: %v", err)
	}
	// Newest finished first; a READ book without a finish date sorts last.
	assertTitles(t, entries, "ReadNew", "ReadOld", "ReadUndated")
	for _, e := range entries {
		if e.Status != library.StatusRead {
			t.Errorf("%s: status = %v, want StatusRead", e.Book.Title, e.Status)
		}
	}
	wantFinished := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	if !entries[0].FinishedAt.Equal(wantFinished) {
		t.Errorf("FinishedAt = %v, want %v", entries[0].FinishedAt, wantFinished)
	}
	// Grimmory rates 0-10; the neutral model uses 0-5.
	if entries[0].Rating != 4.5 {
		t.Errorf("Rating = %v, want 4.5 (halved from 9)", entries[0].Rating)
	}
	if !entries[2].FinishedAt.IsZero() {
		t.Errorf("undated FinishedAt = %v, want zero", entries[2].FinishedAt)
	}

	capped, err := c.RecentReads(context.Background(), 2)
	if err != nil {
		t.Fatalf("RecentReads(2): %v", err)
	}
	assertTitles(t, capped, "ReadNew", "ReadOld")
}

func TestMapEntryFullMetadata(t *testing.T) {
	c := New("http://gm.local:6060", "u", "p")
	e := c.mapEntry(book{
		ID:             7,
		Title:          "Top-Level Title",
		AddedOn:        "2025-03-01T10:00:00Z",
		PersonalRating: 4,
		ReadStatus:     "UNREAD",
		Metadata: &metadata{
			Title:         "Hyperion",
			Subtitle:      "A Cantos",
			Description:   "Seven pilgrims journey to the Time Tombs.",
			PublishedDate: "1989-05-26",
			SeriesName:    "Hyperion Cantos",
			SeriesNumber:  1,
			PageCount:     482,
			Authors:       []string{"Dan    Simmons "},
			Categories:    []string{"Science Fiction"},
			Moods:         []string{"dark"},
			ThumbnailURL:  "/api/v1/media/book/7/thumbnail",
			ExternalURL:   "https://example.com/hyperion",
		},
	})

	b := e.Book
	if b.Title != "Hyperion" || b.Subtitle != "A Cantos" {
		t.Errorf("title = %q / %q, want metadata title over top-level", b.Title, b.Subtitle)
	}
	if b.Description != "Seven pilgrims journey to the Time Tombs." {
		t.Errorf("Description = %q, want mapped from metadata", b.Description)
	}
	if b.Series == nil || b.Series.Name != "Hyperion Cantos" || b.Series.Position != 1 {
		t.Errorf("Series = %+v, want Hyperion Cantos #1", b.Series)
	}
	if b.ReleaseYear != 1989 {
		t.Errorf("ReleaseYear = %d, want 1989", b.ReleaseYear)
	}
	if b.PageCount != 482 {
		t.Errorf("PageCount = %d, want 482", b.PageCount)
	}
	if len(b.Authors) != 1 || b.Authors[0] != "Dan Simmons" {
		t.Errorf("Authors = %v, want [Dan Simmons] (whitespace collapsed)", b.Authors)
	}
	if len(b.Genres) != 1 || b.Genres[0] != "Science Fiction" {
		t.Errorf("Genres = %v, want [Science Fiction]", b.Genres)
	}
	if len(b.Moods) != 1 || b.Moods[0] != "dark" {
		t.Errorf("Moods = %v, want [dark]", b.Moods)
	}
	if b.Nonfiction != nil {
		t.Errorf("Nonfiction = %v, want nil (Grimmory has no signal)", *b.Nonfiction)
	}
	if b.URL != "https://example.com/hyperion" {
		t.Errorf("URL = %q, want external URL", b.URL)
	}
	if b.CoverURL != "http://gm.local:6060/api/v1/media/book/7/thumbnail" {
		t.Errorf("CoverURL = %q, want resolved against instance", b.CoverURL)
	}
	if e.Rating != 2 {
		t.Errorf("Rating = %v, want 2 (halved from 4)", e.Rating)
	}
	wantAdded := time.Date(2025, 3, 1, 10, 0, 0, 0, time.UTC)
	if !e.DateAdded.Equal(wantAdded) {
		t.Errorf("DateAdded = %v, want %v", e.DateAdded, wantAdded)
	}
}

func TestMapEntryNilMetadata(t *testing.T) {
	c := New("http://gm.local", "u", "p")
	e := c.mapEntry(book{ID: 8, Title: "Bare Book"})

	if e.Book.Title != "Bare Book" {
		t.Errorf("Title = %q, want top-level fallback", e.Book.Title)
	}
	if e.Book.Series != nil {
		t.Errorf("Series = %+v, want nil", e.Book.Series)
	}
	if e.Book.ReleaseYear != 0 || e.Book.PageCount != 0 || e.Book.CoverURL != "" {
		t.Errorf("Book = %+v, want zero optional fields", e.Book)
	}
	if !e.DateAdded.IsZero() {
		t.Errorf("DateAdded = %v, want zero", e.DateAdded)
	}
}

func TestResolveCover(t *testing.T) {
	cases := []struct {
		base, thumb, want string
	}{
		{"http://gm.local:6060", "/api/v1/media/book/7/thumbnail", "http://gm.local:6060/api/v1/media/book/7/thumbnail"},
		{"http://gm.local/", "/covers/7.jpg", "http://gm.local/covers/7.jpg"},
		{"http://gm.local/sub", "/api/v1/media/book/7/thumbnail", "http://gm.local/sub/api/v1/media/book/7/thumbnail"},
		{"http://gm.local", "covers/7.jpg", "http://gm.local/covers/7.jpg"},
		{"http://gm.local", "https://cdn.example/7.jpg", "https://cdn.example/7.jpg"},
		{"http://gm.local", "", ""},
	}
	for _, tc := range cases {
		c := New(tc.base, "u", "p")
		if got := c.resolveCover(tc.thumb); got != tc.want {
			t.Errorf("resolveCover(base %q, %q) = %q, want %q", tc.base, tc.thumb, got, tc.want)
		}
	}
}

func TestMapBookCoverPrecedence(t *testing.T) {
	c := New("http://gm.local", "u", "p")
	cases := []struct {
		name string
		meta metadata
		id   int
		want string
	}{
		{
			name: "local cover uses the app proxy with a cache-buster",
			meta: metadata{CoverUpdatedOn: "2025-06-01T10:00:00Z"},
			id:   7,
			want: "/cover/grimmory/7?v=1748772000",
		},
		{
			name: "malformed cover timestamp drops the cache-buster",
			meta: metadata{CoverUpdatedOn: "not-a-date"},
			id:   7,
			want: "/cover/grimmory/7",
		},
		{
			name: "external thumbnail wins over the proxy",
			meta: metadata{ThumbnailURL: "https://cdn.example/x.jpg", CoverUpdatedOn: "2025-06-01T10:00:00Z"},
			id:   7,
			want: "https://cdn.example/x.jpg",
		},
		{
			name: "instance-relative thumbnail yields to the proxy",
			meta: metadata{ThumbnailURL: "/api/v1/media/book/7/thumbnail", CoverUpdatedOn: "2025-06-01T10:00:00Z"},
			id:   7,
			want: "/cover/grimmory/7?v=1748772000",
		},
		{
			name: "no cover at all stays empty",
			meta: metadata{},
			id:   7,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.meta
			b := c.mapBook(book{ID: tc.id, Metadata: &m})
			if b.CoverURL != tc.want {
				t.Errorf("CoverURL = %q, want %q", b.CoverURL, tc.want)
			}
		})
	}
}

func TestMapEntryStampsProvenance(t *testing.T) {
	c := New("http://gm.local", "u", "p")
	e := c.mapEntry(book{ID: 7, Title: "Bare Book"})

	want := library.SourceRef{Name: "grimmory", URL: "http://gm.local/book/7"}
	if len(e.Sources) != 1 || e.Sources[0] != want {
		t.Errorf("Sources = %v, want [%v]", e.Sources, want)
	}
	if !e.Available {
		t.Error("Available = false, want true: a library book is on the shelf")
	}
}

func TestMapEntryURLFallsBackToInstancePage(t *testing.T) {
	c := New("http://gm.local", "u", "p")

	// No metadata at all: still link to the instance's book page.
	if got := c.mapEntry(book{ID: 8}).Book.URL; got != "http://gm.local/book/8" {
		t.Errorf("URL = %q, want the instance page", got)
	}
	// Metadata without an externalUrl: same fallback.
	e := c.mapEntry(book{ID: 9, Metadata: &metadata{Title: "T"}})
	if e.Book.URL != "http://gm.local/book/9" {
		t.Errorf("URL = %q, want the instance page", e.Book.URL)
	}
	// An externalUrl is the user's chosen canonical page and wins.
	e = c.mapEntry(book{ID: 10, Metadata: &metadata{ExternalURL: "https://example.com/b"}})
	if e.Book.URL != "https://example.com/b" {
		t.Errorf("URL = %q, want the externalUrl kept", e.Book.URL)
	}
}

func TestSeriesNamelessDropped(t *testing.T) {
	c := New("http://gm.local", "u", "p")
	e := c.mapEntry(book{Metadata: &metadata{Title: "Solo", SeriesNumber: 3}})
	if e.Book.Series != nil {
		t.Errorf("Series = %+v, want nil when the name is empty", e.Book.Series)
	}
}
