package library

import "context"

// SeriesResolver is an OPTIONAL Source capability. Given a series the user is
// partway through, it returns the next book to read — even one the user has not
// added to any shelf. Sources that cannot look up series data simply do not
// implement it; callers detect support with AsSeriesResolver.
type SeriesResolver interface {
	// NextInSeries returns the entry that follows the last position the user
	// read in series (series carries its Name and that Position). The entry
	// carries the resolving source's provenance but is not on any shelf, so
	// Available stays false. found is false when there is no such next book,
	// e.g. they are at the end of the series.
	NextInSeries(ctx context.Context, series Series) (Entry, bool, error)
}

// unwrapper is implemented by decorators (such as Cached) that wrap a single
// underlying Source, letting AsSeriesResolver see through them.
type unwrapper interface {
	Unwrap() Source
}

// AsSeriesResolver finds a SeriesResolver within s, seeing through known
// decorators (Cached) and aggregating a Multi's capable sources. ok is false
// when nothing underlying actually supports the capability.
//
// Note: capability calls reach the provider directly, bypassing wrapping
// decorators such as Cached (see Unwrap) — acceptable while series lookups are
// rare and uncached.
func AsSeriesResolver(s Source) (SeriesResolver, bool) {
	for s != nil {
		if r, ok := s.(SeriesResolver); ok {
			return r, true
		}
		// A Multi does not implement SeriesResolver itself, so detection stays
		// honest: it resolves series only when at least one of its sources can.
		if m, ok := s.(*Multi); ok {
			var capable []SeriesResolver
			for _, sub := range m.sources {
				if r, ok := AsSeriesResolver(sub); ok {
					capable = append(capable, r)
				}
			}
			if len(capable) == 0 {
				return nil, false
			}
			// TODO: Series carries only Name/Position, so with more than one
			// catalog a name collision could mis-resolve. Bind resolution to the
			// source that produced the anchor entry before adding a second source.
			return multiResolver(capable), true
		}
		if u, ok := s.(unwrapper); ok {
			s = u.Unwrap()
			continue
		}
		return nil, false
	}
	return nil, false
}

// multiResolver tries each underlying resolver in turn, returning the first hit.
type multiResolver []SeriesResolver

func (mr multiResolver) NextInSeries(ctx context.Context, series Series) (Entry, bool, error) {
	for _, r := range mr {
		entry, found, err := r.NextInSeries(ctx, series)
		if err != nil {
			return Entry{}, false, err
		}
		if found {
			return entry, true, nil
		}
	}
	return Entry{}, false, nil
}
