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

// RecentReads merges every source's recent reads, newest first, capped at
// limit. Sources are always asked for their full history (limit 0) so their
// per-limit caches see one shape — the cap is applied after the merge, and
// ToRead's reconciliation reuses the same uncapped fetch.
func (m *Multi) RecentReads(ctx context.Context, limit int) ([]Entry, error) {
	var all []Entry
	for _, s := range m.sources {
		entries, err := s.RecentReads(ctx, 0)
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
// the pick, and a book any source reports as read or in progress is dropped —
// a copy left on another source's TBR is stale, not a fresh candidate. (A
// single source needs none of this and bypasses Multi via Combine.) Reads and
// in-progress lists themselves are history and stay per-source.
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

	exclude, err := m.readKeys(ctx)
	if err != nil {
		return nil, err
	}
	kept := all[:0] // dedup returns a fresh slice, so filtering in place is safe
	for _, e := range all {
		if key := dedupKey(e); key == "" || !exclude[key] {
			kept = append(kept, e)
		}
	}

	sort.SliceStable(kept, func(i, j int) bool {
		return kept[i].DateAdded.Before(kept[j].DateAdded)
	})
	return kept, nil
}

// readKeys identifies every book some source knows the user has read or is
// reading, for exclusion from the TBR pool. Title-less entries yield no key.
func (m *Multi) readKeys(ctx context.Context) (map[string]bool, error) {
	keys := make(map[string]bool)
	for _, s := range m.sources {
		reads, err := s.RecentReads(ctx, 0)
		if err != nil {
			return nil, err
		}
		reading, err := s.CurrentlyReading(ctx)
		if err != nil {
			return nil, err
		}
		// Two loops, not an append: the slices are the source's retained
		// copies and must not be grown in place.
		for _, e := range reads {
			if key := dedupKey(e); key != "" {
				keys[key] = true
			}
		}
		for _, e := range reading {
			if key := dedupKey(e); key != "" {
				keys[key] = true
			}
		}
	}
	return keys, nil
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
	sources := make([]SourceRef, 0, len(base.Sources)+len(dup.Sources))
	sources = append(sources, base.Sources...)
	for _, s := range dup.Sources {
		known := slices.ContainsFunc(sources, func(have SourceRef) bool {
			return have.Name == s.Name
		})
		if !known {
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
	if b.Description == "" {
		b.Description = d.Description
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
