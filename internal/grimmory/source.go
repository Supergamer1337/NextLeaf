package grimmory

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"nextleaf/internal/library"
)

// Client is a reading Source that also serves cover images (they sit behind
// the instance's auth). It is not a SeriesResolver: Grimmory has no
// next-in-series lookup beyond the user's own shelves.
var (
	_ library.Source        = (*Client)(nil)
	_ library.CoverProvider = (*Client)(nil)
)

// Name identifies this Source.
func (c *Client) Name() string { return "grimmory" }

// CurrentlyReading returns the books being read (or re-read) right now.
func (c *Client) CurrentlyReading(ctx context.Context) ([]library.Entry, error) {
	return c.entriesWithStatus(ctx, library.StatusCurrentlyRead)
}

// RecentReads returns the finished books, newest first, capped at limit.
// Reads without a recorded finish date sort last so an undated "Read" book
// cannot masquerade as the most recent one.
func (c *Client) RecentReads(ctx context.Context, limit int) ([]library.Entry, error) {
	entries, err := c.entriesWithStatus(ctx, library.StatusRead)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i].FinishedAt, entries[j].FinishedAt
		if a.IsZero() || b.IsZero() {
			return !a.IsZero()
		}
		return a.After(b)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// ToRead returns the unread books in the user's library, oldest additions
// first — the picker later favours books that have waited longest. Books whose
// status was never set count as unread: a freshly imported library is a TBR.
func (c *Client) ToRead(ctx context.Context) ([]library.Entry, error) {
	entries, err := c.entriesWithStatus(ctx, library.StatusWantToRead)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].DateAdded.Before(entries[j].DateAdded)
	})
	return entries, nil
}

// entriesWithStatus fetches the library and keeps the books whose read status
// maps to want. Filtering happens client-side: the books endpoint returns the
// whole library in one call, and library.Cached keeps refetches rare.
func (c *Client) entriesWithStatus(ctx context.Context, want library.Status) ([]library.Entry, error) {
	books, err := c.fetchBooks(ctx)
	if err != nil {
		return nil, err
	}
	var entries []library.Entry
	for _, b := range books {
		if mapStatus(b.ReadStatus) != want {
			continue
		}
		entries = append(entries, c.mapEntry(b))
	}
	return entries, nil
}

// mapStatus translates Grimmory's readStatus into the neutral Status. An
// absent status means the user never touched the book, which for a personal
// library reads as "still to be read". Unknown values map to 0 so new upstream
// mapStatus converts a Grimmory read-status string to a library status.
// It returns zero for unrecognized statuses.
func mapStatus(s string) library.Status {
	switch s {
	case "UNREAD", "UNSET", "":
		return library.StatusWantToRead
	case "READING", "RE_READING":
		return library.StatusCurrentlyRead
	case "READ":
		return library.StatusRead
	case "PAUSED", "PARTIALLY_READ":
		return library.StatusPaused
	case "WONT_READ", "ABANDONED":
		return library.StatusDNF
	default:
		return 0
	}
}

func (c *Client) mapEntry(b book) library.Entry {
	e := library.Entry{
		Book:       c.mapBook(b),
		Status:     mapStatus(b.ReadStatus),
		Rating:     b.PersonalRating / 2, // Grimmory rates 0-10, the neutral model 0-5
		DateAdded:  parseInstant(b.AddedOn),
		FinishedAt: parseInstant(b.DateFinished),
		Sources:    []string{c.Name()},
		Available:  true, // a Grimmory library holds the files themselves
	}
	return e
}

func (c *Client) mapBook(b book) library.Book {
	out := library.Book{Title: b.Title}
	m := b.Metadata
	if m == nil {
		return out
	}
	if m.Title != "" {
		out.Title = m.Title
	}
	out.Subtitle = m.Subtitle
	out.Authors = cleanAuthors(m.Authors)
	out.Genres = m.Categories
	out.Moods = m.Moods
	out.ReleaseYear = parseYear(m.PublishedDate)
	out.PageCount = m.PageCount
	out.URL = m.ExternalURL
	out.CoverURL = c.coverURL(b.ID, m)
	if m.SeriesName != "" {
		out.Series = &library.Series{Name: m.SeriesName, Position: m.SeriesNumber}
	}
	return out
}

// coverURL picks the best cover reference for a book. An external thumbnail
// is publicly reachable and used as-is. Covers stored on the instance sit
// behind auth, so they go through the app's proxy route (see CoverImage) with
// the cover's update time as a cache-buster. An instance-relative thumbnail
// points at that same auth-gated endpoint, so the proxy takes precedence.
func (c *Client) coverURL(id int, m *metadata) string {
	if strings.Contains(m.ThumbnailURL, "://") {
		return m.ThumbnailURL
	}
	if m.CoverUpdatedOn != "" {
		u := "/cover/grimmory/" + strconv.Itoa(id)
		if t := parseInstant(m.CoverUpdatedOn); !t.IsZero() {
			u += "?v=" + strconv.FormatInt(t.Unix(), 10)
		}
		return u
	}
	return c.resolveCover(m.ThumbnailURL)
}

// resolveCover turns a thumbnail reference into an absolute URL. Grimmory
// serves covers as instance-relative paths; joining onto the configured base
// keeps a reverse-proxy subpath intact.
func (c *Client) resolveCover(thumb string) string {
	if thumb == "" || strings.Contains(thumb, "://") {
		return thumb
	}
	return c.baseRaw + "/" + strings.TrimPrefix(thumb, "/")
}

// cleanAuthors collapses stray whitespace in author names ("David    Allen"),
// which Grimmory metadata sometimes carries, so the picker's author dimension
// cleanAuthors normalizes author names by collapsing internal whitespace to single spaces.
func cleanAuthors(names []string) []string {
	for i, n := range names {
		names[i] = strings.Join(strings.Fields(n), " ")
	}
	return names
}

// parseInstant reads an ISO-8601 timestamp, returning the zero time on any
// parseInstant parses an RFC3339 timestamp with nanosecond precision, returning the zero time for empty or malformed input.
func parseInstant(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parseYear extracts the four-digit year from a date string, returning zero when the string is too short or does not start with a valid year.
func parseYear(s string) int {
	if len(s) < 4 {
		return 0
	}
	year, err := strconv.Atoi(s[:4])
	if err != nil {
		return 0
	}
	return year
}
