package hardcover

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"nextleaf/internal/library"
)

// Client is both a reading Source and a SeriesResolver.
var (
	_ library.Source         = (*Client)(nil)
	_ library.SeriesResolver = (*Client)(nil)
)

// Name identifies this Source.
func (c *Client) Name() string { return "hardcover" }

// CurrentlyReading returns the in-progress books, most recently updated first.
func (c *Client) CurrentlyReading(ctx context.Context) ([]library.Entry, error) {
	return c.fetchEntries(ctx, int(library.StatusCurrentlyRead), "updated_at: desc", 0)
}

// RecentReads returns the most recently finished books, newest first. Reads
// without a recorded finish date sort last (desc_nulls_last) so an undated
// "Read" book cannot masquerade as the most recent one.
func (c *Client) RecentReads(ctx context.Context, limit int) ([]library.Entry, error) {
	return c.fetchEntries(ctx, int(library.StatusRead), "last_read_date: desc_nulls_last", limit)
}

// ToRead returns the Want to Read list, oldest additions first — the picker
// later favours books that have waited longest.
func (c *Client) ToRead(ctx context.Context) ([]library.Entry, error) {
	return c.fetchEntries(ctx, int(library.StatusWantToRead), "date_added: asc", 0)
}

// NextInSeries returns the book that follows s.Position in the named series,
// even if it sits on none of the user's shelves. found is false when the series
// has no later book. It satisfies library.SeriesResolver.
func (c *Client) NextInSeries(ctx context.Context, s library.Series) (library.Book, bool, error) {
	// A missing name or unknown position (0) has no well-defined "next".
	if s.Name == "" || s.Position == 0 {
		return library.Book{}, false, nil
	}

	query := fmt.Sprintf(`
query NextInSeries($name: String!, $after: float8!) {
  book_series(
    where: {series: {name: {_eq: $name}}, position: {_gt: $after}}
    order_by: {position: asc}
    limit: 1
  ) {
    book {%s}
  }
}`, bookFields)

	var data struct {
		BookSeries []struct {
			Book bookData `json:"book"`
		} `json:"book_series"`
	}
	vars := map[string]any{"name": s.Name, "after": s.Position}
	if err := c.execute(ctx, query, vars, &data); err != nil {
		return library.Book{}, false, err
	}
	if len(data.BookSeries) == 0 {
		return library.Book{}, false, nil
	}
	return mapBook(data.BookSeries[0].Book), true, nil
}

// userBook mirrors the fields we request from a user_books row.
type userBook struct {
	StatusID     int      `json:"status_id"`
	Rating       *float64 `json:"rating"`
	DateAdded    string   `json:"date_added"`
	LastReadDate string   `json:"last_read_date"`
	Book         bookData `json:"book"`
}

// bookData mirrors the book fields we request (see bookFields). It is shared by
// the user_books query and the series lookup so both map through mapBook.
type bookData struct {
	Title       string          `json:"title"`
	Subtitle    string          `json:"subtitle"`
	Slug        string          `json:"slug"`
	ReleaseYear int             `json:"release_year"`
	Pages       int             `json:"pages"`
	CachedTags  json.RawMessage `json:"cached_tags"`
	Image       *struct {
		URL string `json:"url"`
	} `json:"image"`
	Contributions []struct {
		Author *struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"contributions"`
	BookSeries []struct {
		Position *float64 `json:"position"`
		Featured bool     `json:"featured"`
		Series   *struct {
			Name string `json:"name"`
		} `json:"series"`
	} `json:"book_series"`
}

// bookFields is the GraphQL selection set for a book, shared across queries.
const bookFields = `
      title
      subtitle
      slug
      release_year
      pages
      cached_tags
      image { url }
      contributions { author { name } }
      book_series { position featured series { name } }`

func (c *Client) fetchEntries(ctx context.Context, statusID int, orderBy string, limit int) ([]library.Entry, error) {
	userID, err := c.currentUserID(ctx)
	if err != nil {
		return nil, err
	}

	limitClause := ""
	vars := map[string]any{"userID": userID, "status": statusID}
	if limit > 0 {
		limitClause = ", limit: $limit"
		vars["limit"] = limit
	}

	query := fmt.Sprintf(`
query Entries($userID: Int!, $status: Int!%s) {
  user_books(
    where: {user_id: {_eq: $userID}, status_id: {_eq: $status}}
    order_by: {%s}%s
  ) {
    status_id
    rating
    date_added
    last_read_date
    book {%s}
  }
}`, limitVar(limit), orderBy, limitClause, bookFields)

	var data struct {
		UserBooks []userBook `json:"user_books"`
	}
	if err := c.execute(ctx, query, vars, &data); err != nil {
		return nil, err
	}

	entries := make([]library.Entry, 0, len(data.UserBooks))
	for _, ub := range data.UserBooks {
		entries = append(entries, mapEntry(ub))
	}
	return entries, nil
}

func limitVar(limit int) string {
	if limit > 0 {
		return ", $limit: Int!"
	}
	return ""
}

func mapEntry(ub userBook) library.Entry {
	e := library.Entry{
		Book:       mapBook(ub.Book),
		Status:     library.Status(ub.StatusID),
		DateAdded:  parseDate(ub.DateAdded),
		FinishedAt: parseDate(ub.LastReadDate),
	}
	if ub.Rating != nil {
		e.Rating = *ub.Rating
	}
	return e
}

func mapBook(b bookData) library.Book {
	rawGenres := tagCategory(b.CachedTags, "Genre")
	book := library.Book{
		Title:       b.Title,
		Subtitle:    b.Subtitle,
		ReleaseYear: b.ReleaseYear,
		PageCount:   b.Pages,
		Authors:     authors(b),
		Genres:      cleanGenres(rawGenres),
		Moods:       normalizeTags(tagCategory(b.CachedTags, "Mood")),
		Series:      series(b),
		Nonfiction:  classifyMode(rawGenres), // classify before filler is dropped
	}
	if b.Image != nil {
		book.CoverURL = b.Image.URL
	}
	if b.Slug != "" {
		book.URL = "https://hardcover.app/books/" + b.Slug
	}
	return book
}

// classifyMode reads fiction vs nonfiction off the genre tags, leaving it
// unknown (nil) when neither marker is present so the picker can skip the axis.
func classifyMode(genres []string) *bool {
	for _, g := range genres {
		switch strings.ToLower(g) {
		case "nonfiction", "non-fiction":
			t := true
			return &t
		case "fiction":
			f := false
			return &f
		}
	}
	return nil
}

func authors(b bookData) []string {
	var names []string
	for _, con := range b.Contributions {
		if con.Author != nil && con.Author.Name != "" {
			names = append(names, con.Author.Name)
		}
	}
	return names
}

// series returns the featured series if one is flagged, else the first listed.
func series(b bookData) *library.Series {
	list := b.BookSeries
	if len(list) == 0 {
		return nil
	}
	chosen := list[0]
	for _, s := range list {
		if s.Featured {
			chosen = s
			break
		}
	}
	if chosen.Series == nil || chosen.Series.Name == "" {
		return nil
	}
	s := &library.Series{Name: chosen.Series.Name}
	if chosen.Position != nil {
		s.Position = *chosen.Position
	}
	return s
}

// tagCategory extracts one category's tag names (e.g. "Genre", "Mood") from
// Hardcover's cached_tags jsonb, tolerating both the object form
// ({"tag": "..."}) and a plain string form.
func tagCategory(raw json.RawMessage, category string) []string {
	if len(raw) == 0 {
		return nil
	}
	var byCategory map[string]json.RawMessage
	if err := json.Unmarshal(raw, &byCategory); err != nil {
		return nil
	}
	catRaw, ok := byCategory[category]
	if !ok {
		return nil
	}

	var objs []struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(catRaw, &objs); err == nil {
		var names []string
		for _, o := range objs {
			if o.Tag != "" {
				names = append(names, o.Tag)
			}
		}
		if len(names) > 0 {
			return names
		}
	}

	var strs []string
	if err := json.Unmarshal(catRaw, &strs); err == nil {
		return strs
	}
	return nil
}

// genreFiller lists Hardcover Genre tags too generic to be a useful signal;
// they're dropped from display so they don't clutter chips or masquerade as a
// fresh genre. Fiction/Nonfiction are dropped too — they feed classifyMode
// instead (which runs before this filter).
var genreFiller = map[string]bool{
	"general":       true,
	"genre fiction": true,
	"fiction":       true,
	"nonfiction":    true,
	"non-fiction":   true,
}

// cleanGenres drops filler tags and normalises the rest to consistent casing,
// smoothing over Hardcover's mixed-case taxonomy ("political science" → "Political
// Science").
func cleanGenres(raw []string) []string {
	var out []string
	for _, g := range raw {
		if genreFiller[strings.ToLower(strings.TrimSpace(g))] {
			continue
		}
		out = append(out, normalizeTag(g))
	}
	return out
}

func normalizeTags(ts []string) []string {
	for i, t := range ts {
		ts[i] = normalizeTag(t)
	}
	return ts
}

// normalizeTag capitalises the first letter of each all-lowercase word, leaving
// words that already carry uppercase (acronyms like "LGBT", proper casing like
// "Science Fiction") untouched.
func normalizeTag(s string) string {
	words := strings.Split(s, " ")
	for i, w := range words {
		if w == "" || hasUpper(w) {
			continue
		}
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func parseDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}
	return t
}
