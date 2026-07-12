package library

import (
	"context"
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

// ToRead merges every source's TBR list, oldest additions first.
func (m *Multi) ToRead(ctx context.Context) ([]Entry, error) {
	var all []Entry
	for _, s := range m.sources {
		entries, err := s.ToRead(ctx)
		if err != nil {
			return nil, err
		}
		all = append(all, entries...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].DateAdded.Before(all[j].DateAdded)
	})
	return all, nil
}
