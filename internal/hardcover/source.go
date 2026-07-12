package hardcover

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"nextleaf/internal/library"
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

// userBook mirrors the fields we request from a user_books row.
type userBook struct {
	StatusID     int      `json:"status_id"`
	Rating       *float64 `json:"rating"`
	DateAdded    string   `json:"date_added"`
	LastReadDate string   `json:"last_read_date"`
	Book         struct {
		Title       string          `json:"title"`
		Subtitle    string          `json:"subtitle"`
		Slug        string          `json:"slug"`
		ReleaseYear int             `json:"release_year"`
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
	} `json:"book"`
}

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
    book {
      title
      subtitle
      slug
      release_year
      cached_tags
      image { url }
      contributions { author { name } }
      book_series { position featured series { name } }
    }
  }
}`, limitVar(limit), orderBy, limitClause)

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
		Book:       mapBook(ub),
		Status:     library.Status(ub.StatusID),
		DateAdded:  parseDate(ub.DateAdded),
		FinishedAt: parseDate(ub.LastReadDate),
	}
	if ub.Rating != nil {
		e.Rating = *ub.Rating
	}
	return e
}

func mapBook(ub userBook) library.Book {
	b := ub.Book
	book := library.Book{
		Title:       b.Title,
		Subtitle:    b.Subtitle,
		ReleaseYear: b.ReleaseYear,
		Authors:     authors(ub),
		Genres:      genres(b.CachedTags),
		Series:      series(ub),
	}
	if b.Image != nil {
		book.CoverURL = b.Image.URL
	}
	if b.Slug != "" {
		book.URL = "https://hardcover.app/books/" + b.Slug
	}
	return book
}

func authors(ub userBook) []string {
	var names []string
	for _, con := range ub.Book.Contributions {
		if con.Author != nil && con.Author.Name != "" {
			names = append(names, con.Author.Name)
		}
	}
	return names
}

// series returns the featured series if one is flagged, else the first listed.
func series(ub userBook) *library.Series {
	list := ub.Book.BookSeries
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

// genres extracts the "Genre" tag names from Hardcover's cached_tags jsonb,
// tolerating both the object form ({"tag": "..."}) and a plain string form.
func genres(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var byCategory map[string]json.RawMessage
	if err := json.Unmarshal(raw, &byCategory); err != nil {
		return nil
	}
	genreRaw, ok := byCategory["Genre"]
	if !ok {
		return nil
	}

	var objs []struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(genreRaw, &objs); err == nil {
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
	if err := json.Unmarshal(genreRaw, &strs); err == nil {
		return strs
	}
	return nil
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
