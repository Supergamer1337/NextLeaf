package hardcover

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"nextleaf/internal/library"
)

// Client is a reading Source, a SeriesResolver, and a Verifier.
var (
	_ library.Source          = (*Client)(nil)
	_ library.SeriesResolver  = (*Client)(nil)
	_ library.Verifier        = (*Client)(nil)
	_ library.HistoryProvider = (*Client)(nil)
)

// Name identifies this Source.
func (c *Client) Name() string { return "hardcover" }

// Verify checks the token is accepted by resolving the current user, the
// cheapest authenticated query. The looked-up id is cached, so the first real
// fetch reuses it rather than paying for a second round-trip.
func (c *Client) Verify(ctx context.Context) error {
	_, err := c.currentUserID(ctx)
	return err
}

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

// ReadHistory returns every finished book, newest first — the whole history
// rather than RecentReads' window, for the one-time series backfill. It
// satisfies library.HistoryProvider.
func (c *Client) ReadHistory(ctx context.Context) ([]library.Entry, error) {
	return c.RecentReads(ctx, 0)
}

const (
	// seriesLookahead is how many later rows to fetch per page. A single
	// position can hold a dozen rows — every translation of the book, plus
	// bundles and split editions — so this is generous.
	seriesLookahead = 30

	// maxSeriesPages bounds the pagination. Hitting it is an error, never
	// "no continuation": reporting a truncated read as a finished series
	// would silently hide a series that may well have a next book.
	maxSeriesPages = 10

	// readingLanguage is the edition language a next book must exist in.
	// Hardcover files every translation at the same series position with the
	// same featured flag and the same title-less metadata, so whether a book
	// has an edition in this language is the only reliable way to tell the
	// original from a translation the reader cannot read.
	readingLanguage = "eng"
)

// NextInSeries returns the first book after q's position that the reader could
// actually go and read: it is out, and it is one they want offered. The book
// may sit on none of their shelves, so it carries hardcover's provenance but
// stays unavailable. found is false when the series has nothing suitable left.
// It satisfies library.SeriesResolver.
func (c *Client) NextInSeries(ctx context.Context, q library.SeriesQuery) (library.Entry, bool, error) {
	s := q.Series
	// An unplaced book says nothing about what follows it, and a catalogue
	// asked "what comes after nothing" would answer with the series' first
	// volume — which would nag a reader who has in fact finished it.
	after, placed := s.Slot()
	if s.Name == "" || !placed {
		return library.Entry{}, false, nil
	}

	// Filtering happens after the fetch, so a page can be consumed entirely by
	// translations and split editions. Page until the source runs dry rather
	// than mistaking a truncated read for the end of the series.
	for page := 0; page < maxSeriesPages; page++ {
		rows, err := c.seriesPage(ctx, s.Name, after)
		if err != nil {
			return library.Entry{}, false, err
		}
		groups := byPosition(rows)
		full := len(rows) == seriesLookahead

		// On a full page the last position's rows may continue onto the next
		// page, so its group is deferred — unless it is the only one, where
		// deferring would never advance the cursor.
		if full && len(groups) > 1 {
			groups = groups[:len(groups)-1]
		}

		for _, group := range groups {
			// Hardcover files a book split across two volumes at .1 and .2 of
			// the position it already occupies, so those are halves of a book
			// the reader has finished rather than anything new to read.
			if isSplitEdition(group.position) {
				continue
			}
			if !q.IncludeNovellas && isNovella(group.position) {
				continue
			}
			chosen, ok := best(group.rows)
			if !ok {
				// Nothing at this position exists in the reading language, so
				// there is no honest answer here; a later one may have it.
				continue
			}
			book := mapBook(chosen)
			if !c.released(book) {
				continue
			}
			return library.Entry{
				Book:    book,
				Sources: []library.SourceRef{{Name: "hardcover", URL: book.URL, ID: strconv.Itoa(chosen.ID)}},
			}, true, nil
		}

		if !full {
			return library.Entry{}, false, nil
		}
		after = groups[len(groups)-1].position
	}
	return library.Entry{}, false, fmt.Errorf(
		"hardcover: series %q: no readable book within %d pages", s.Name, maxSeriesPages)
}

// seriesPage fetches one page of a series' rows past the given position.
func (c *Client) seriesPage(ctx context.Context, name string, after float64) ([]seriesRow, error) {
	query := fmt.Sprintf(`
query NextInSeries($name: String!, $after: float8!, $limit: Int!) {
  book_series(
    where: {series: {name: {_eq: $name}}, position: {_gt: $after}, featured: {_eq: true}}
    order_by: {position: asc}
    limit: $limit
  ) {
    position
    book {%s}
  }
}`, seriesBookFields)

	var data struct {
		BookSeries []seriesRow `json:"book_series"`
	}
	vars := map[string]any{"name": name, "after": after, "limit": seriesLookahead}
	if err := c.execute(ctx, query, vars, &data); err != nil {
		return nil, err
	}
	return data.BookSeries, nil
}

// seriesRow is one book at one position within a series.
type seriesRow struct {
	Position *float64 `json:"position"`
	Book     bookData `json:"book"`
}

// positionGroup is every row a series files at the same position.
type positionGroup struct {
	position float64
	rows     []bookData
}

// byPosition collects rows into one group per position, keeping the ascending
// order the query returned.
func byPosition(rows []seriesRow) []positionGroup {
	var groups []positionGroup
	for _, r := range rows {
		pos := 0.0
		if r.Position != nil {
			pos = *r.Position
		}
		if n := len(groups); n > 0 && groups[n-1].position == pos {
			groups[n-1].rows = append(groups[n-1].rows, r.Book)
			continue
		}
		groups = append(groups, positionGroup{position: pos, rows: []bookData{r.Book}})
	}
	return groups
}

// best picks the row that actually represents a position: it must have an
// edition the reader can read, and a single novel beats a bundle of several.
// ok is false when nothing at the position qualifies, which happens when a
// series files only translations at it. A book with no title is an unnamed
// announcement rather than something to go and read.
func best(rows []bookData) (bookData, bool) {
	var chosen bookData
	found := false
	for _, b := range rows {
		if len(b.Editions) == 0 || b.Title == "" {
			continue
		}
		if !found || (chosen.Compilation && !b.Compilation) {
			chosen, found = b, true
		}
	}
	return chosen, found
}

// isNovella treats a half position (3.5) as side material sitting between two
// novels, which is how catalogues file a novella.
func isNovella(pos float64) bool { return fraction(pos) == 0.5 }

// isSplitEdition spots the other fractional positions (1.1, 1.2), which
// Hardcover uses for the parts of a single novel published in halves. They are
// never a next read: the whole novel at that position is what counts.
func isSplitEdition(pos float64) bool {
	f := fraction(pos)
	return f != 0 && f != 0.5
}

// fraction is the part of a series position after the decimal point, rounded to
// two places so 1.1 does not arrive as 1.10000000000000009.
func fraction(pos float64) float64 {
	whole := float64(int64(pos))
	return math.Round((pos-whole)*100) / 100
}

// released reports whether a book is out yet, so an announced sequel is never
// recommended. A book with no date at all is assumed available: withholding
// every book Hardcover has not dated would be worse than the rare early offer.
func (c *Client) released(b library.Book) bool {
	now := c.now()
	if !b.ReleaseDate.IsZero() {
		return !b.ReleaseDate.After(now)
	}
	if b.ReleaseYear != 0 {
		return b.ReleaseYear <= now.Year()
	}
	return true
}

// userBook mirrors the fields we request from a user_books row.
type userBook struct {
	StatusID     int      `json:"status_id"`
	Rating       *float64 `json:"rating"`
	Owned        bool     `json:"owned"`
	DateAdded    string   `json:"date_added"`
	LastReadDate string   `json:"last_read_date"`
	Book         bookData `json:"book"`
}

// bookData mirrors the book fields we request (see bookFields). It is shared by
// the user_books query and the series lookup so both map through mapBook.
type bookData struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle"`
	Description string `json:"description"`
	Slug        string `json:"slug"`
	ReleaseYear int    `json:"release_year"`
	ReleaseDate string `json:"release_date"`
	Compilation bool   `json:"compilation"`
	// Editions is requested only by the series lookup, filtered to the
	// reading language, so a non-empty list means the reader can read it.
	Editions []struct {
		ID int `json:"id"`
	} `json:"editions"`
	Pages      int             `json:"pages"`
	CachedTags json.RawMessage `json:"cached_tags"`
	Image      *struct {
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
			Name      string `json:"name"`
			Slug      string `json:"slug"`
			Completed bool   `json:"is_completed"`
			Books     int    `json:"books_count"`
			Desc      string `json:"description"`
		} `json:"series"`
	} `json:"book_series"`
}

// bookFields is the GraphQL selection set for a book, shared across queries.
const bookFields = `
      id
      title
      subtitle
      description
      slug
      release_year
      release_date
      compilation
      pages
      cached_tags
      image { url }
      contributions { author { name } }
      book_series { position featured series { name slug is_completed books_count description } }`

// seriesBookFields adds the evidence the series lookup needs to tell an
// original from its translations: whether the book has an edition in the
// reading language at all. It is not requested for shelf entries, where the
// reader's own library already settles the question.
const seriesBookFields = bookFields + `
      editions(limit: 1, where: {language: {code3: {_eq: "` + readingLanguage + `"}}}) { id }`

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
    owned
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
	book := mapBook(ub.Book)
	e := library.Entry{
		Book:       book,
		Status:     library.Status(ub.StatusID),
		DateAdded:  parseDate(ub.DateAdded),
		FinishedAt: parseDate(ub.LastReadDate),
		Sources:    []library.SourceRef{{Name: "hardcover", URL: book.URL, ID: strconv.Itoa(ub.Book.ID)}},
		Available:  ub.Owned,
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
		Description: b.Description,
		ReleaseYear: b.ReleaseYear,
		ReleaseDate: parseDate(b.ReleaseDate),
		PageCount:   b.Pages,
		Authors:     authors(b),
		Genres:      cleanGenres(rawGenres),
		Moods:       normalizeTags(tagCategory(b.CachedTags, "Mood")),
		Series:      nil,                     // set from the ranked memberships below
		Nonfiction:  classifyMode(rawGenres), // classify before filler is dropped
	}
	if all := seriesMemberships(b); len(all) > 0 {
		book.Series, book.OtherSeries = &all[0], all[1:]
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

// seriesMemberships returns every series the book belongs to, best candidate
// first. Hardcover promises no ordering and flags more than one row featured,
// so the ranking is made total: featured beats unfeatured, then the larger
// series (the longer runway of next books), then the name. Without that, the
// same book could resolve to a different series between two fetches — and the
// orderings number their volumes differently, so the reader's position would
// jump with it.
func seriesMemberships(b bookData) []library.Series {
	out := make([]library.Series, 0, len(b.BookSeries))
	featured := make([]bool, 0, len(b.BookSeries))
	counts := make([]int, 0, len(b.BookSeries))
	for _, row := range b.BookSeries {
		if row.Series == nil || row.Series.Name == "" {
			continue
		}
		out = append(out, library.Series{
			Name:        row.Series.Name,
			Slug:        row.Series.Slug,
			Completed:   row.Series.Completed,
			Position:    row.Position,
			Description: row.Series.Desc,
			Source:      "hardcover",
		})
		featured = append(featured, row.Featured)
		counts = append(counts, row.Series.Books)
	}

	idx := make([]int, len(out))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := idx[a], idx[b]
		if featured[x] != featured[y] {
			return featured[x]
		}
		if counts[x] != counts[y] {
			return counts[x] > counts[y]
		}
		return out[x].Name < out[y].Name
	})

	ranked := make([]library.Series, len(out))
	for i, j := range idx {
		ranked[i] = out[j]
	}
	return ranked
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
