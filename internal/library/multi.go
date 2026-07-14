package library

import (
	"context"
	"slices"
	"sort"
	"strings"
)

// Multi presents several Sources as one, merging their results. It lets the app
// depend on a single Source while more backends are added over time.
type Multi struct {
	sources []Source
}

// Combine folds sources into a single Source: nil when none are enabled (so the
// app can detect the unconfigured case), the source itself when there is one,
// or a merging Multi when there are several.
func Combine(sources ...Source) Source {
	switch len(sources) {
	case 0:
		return nil
	case 1:
		return sources[0]
	default:
		return &Multi{sources: sources}
	}
}

func (m *Multi) Name() string {
	names := make([]string, len(m.sources))
	for i, s := range m.sources {
		names[i] = s.Name()
	}
	return strings.Join(names, "+")
}

// CurrentlyReading concatenates every source's in-progress books, keeping each
// source's own ordering.
func (m *Multi) CurrentlyReading(ctx context.Context) ([]Entry, error) {
	var all []Entry
	for _, s := range m.sources {
		entries, err := s.CurrentlyReading(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
	}
	return all, nil
}

// RecentReads merges every source's recent reads, newest first, capped at limit.
func (m *Multi) RecentReads(ctx context.Context, limit int) ([]Entry, error) {
	var all []Entry
	for _, s := range m.sources {
		entries, err := s.RecentReads(ctx, limit)
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].FinishedAt.After(all[j].FinishedAt)
	})
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// ToRead merges every source's TBR list, oldest additions first. The same book
// held by several sources becomes one entry so it doesn't get extra chances in
// the pick; reads and in-progress lists are history and stay per-source.
func (m *Multi) ToRead(ctx context.Context) ([]Entry, error) {
	var all []Entry
	for _, s := range m.sources {
		entries, err := s.ToRead(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
	}
	all = dedup(all)
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].DateAdded.Before(all[j].DateAdded)
	})
	return all, nil
}

// dedup folds entries describing the same book (matched on normalized title
// and first author) into one. Sources hand back retained slices, so merging
// copies entries rather than writing through them.
func dedup(all []Entry) []Entry {
	out := make([]Entry, 0, len(all))
	seen := make(map[string]int, len(all))
	for _, e := range all {
		key := dedupKey(e)
		if key == "" {
			out = append(out, e)
			continue
		}
		if i, ok := seen[key]; ok {
			out[i] = mergeEntry(out[i], e)
			continue
		}
		seen[key] = len(out)
		out = append(out, e)
	}
	return out
}

// dedupKey identifies a book for deduplication; empty means "never merge".
func dedupKey(e Entry) string {
	title := normalize(e.Book.Title)
	if title == "" {
		return ""
	}
	author := ""
	if len(e.Book.Authors) > 0 {
		author = normalize(e.Book.Authors[0])
	}
	return title + "\x00" + author
}

func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// mergeEntry combines two entries for the same book: provenance is unioned,
// the earliest addition date wins, and fields one source couldn't supply are
// filled from the other. Slices are reallocated, never extended in place.
func mergeEntry(base, dup Entry) Entry {
	sources := make([]string, 0, len(base.Sources)+len(dup.Sources))
	sources = append(sources, base.Sources...)
	for _, s := range dup.Sources {
		if !slices.Contains(sources, s) {
			sources = append(sources, s)
		}
	}
	base.Sources = sources

	if base.DateAdded.IsZero() || (!dup.DateAdded.IsZero() && dup.DateAdded.Before(base.DateAdded)) {
		base.DateAdded = dup.DateAdded
	}
	base.Available = base.Available || dup.Available
	if base.Rating == 0 {
		base.Rating = dup.Rating
	}
	if base.FinishedAt.IsZero() {
		base.FinishedAt = dup.FinishedAt
	}

	b, d := &base.Book, dup.Book
	if b.Subtitle == "" {
		b.Subtitle = d.Subtitle
	}
	if b.Authors == nil {
		b.Authors = d.Authors
	}
	if b.Genres == nil {
		b.Genres = d.Genres
	}
	if b.Moods == nil {
		b.Moods = d.Moods
	}
	if b.Series == nil {
		b.Series = d.Series
	}
	if b.ReleaseYear == 0 {
		b.ReleaseYear = d.ReleaseYear
	}
	if b.PageCount == 0 {
		b.PageCount = d.PageCount
	}
	if b.Nonfiction == nil {
		b.Nonfiction = d.Nonfiction
	}
	if b.CoverURL == "" {
		b.CoverURL = d.CoverURL
	}
	if b.URL == "" {
		b.URL = d.URL
	}
	return base
}
