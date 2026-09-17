package library

import "context"

// SeriesResolver is an OPTIONAL Source capability. Given a series the user is
// partway through, it returns the next book to read — even one the user has not
// added to any shelf. Sources that cannot look up series data simply do not
// implement it; callers detect support with AsSeriesResolver.
type SeriesResolver interface {
	// NextInSeries returns the entry that follows the position in q.Series,
	// honouring q's preferences and skipping books that are not out yet. The
	// entry carries the resolving source's provenance but is not on any
	// shelf, so Available stays false. found is false when there is no such
	// next book, e.g. they are at the end of the series.
	NextInSeries(ctx context.Context, q SeriesQuery) (Entry, bool, error)
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
			capable := multiResolver{}
			for _, sub := range m.sources {
				if r, ok := AsSeriesResolver(sub); ok {
					capable[sub.Name()] = r
				}
			}
			if len(capable) == 0 {
				return nil, false
			}
			return capable, true
		}
		if u, ok := s.(unwrapper); ok {
			s = u.Unwrap()
			continue
		}
		return nil, false
	}
	return nil, false
}

// multiResolver routes a query to the catalogue of the series' own provider.
// A series is one backend's claim; another backend may file something else
// under the same name, so nobody else is asked on its behalf.
type multiResolver map[string]SeriesResolver

func (mr multiResolver) NextInSeries(ctx context.Context, q SeriesQuery) (Entry, bool, error) {
	r, ok := mr[q.Series.Source]
	if !ok {
		return Entry{}, false, nil
	}
	return r.NextInSeries(ctx, q)
}

// ResolvesSeries reports whether the named provider's series can be looked up
// in a catalogue. When it cannot, what follows the reader's shelf is unknown,
// which is not the same as nothing. A lone source is the only provider there
// is, so it answers for every series it reports.
func ResolvesSeries(s Source, provider string) bool {
	r, ok := AsSeriesResolver(s)
	if !ok {
		return false
	}
	if mr, ok := r.(multiResolver); ok {
		_, ok := mr[provider]
		return ok
	}
	return true
}

// SeriesFinder is an OPTIONAL Source capability: given ISBNs, it says which
// series its catalogue files those books under. It is how a series known only
// to a catalogue-less backend finds its counterpart, on the same certain
// evidence that joins two copies of a book.
type SeriesFinder interface {
	// SeriesByISBN returns each known ISBN's series claims, best first, keyed
	// by the ISBN as it was given. Unknown ISBNs are simply absent.
	SeriesByISBN(ctx context.Context, isbns []string) (map[string][]Series, error)
}

// AsSeriesFinders returns every SeriesFinder within s, seeing through the same
// decorators AsSeriesResolver does.
func AsSeriesFinders(s Source) []SeriesFinder {
	for s != nil {
		if f, ok := s.(SeriesFinder); ok {
			return []SeriesFinder{f}
		}
		if m, ok := s.(*Multi); ok {
			var out []SeriesFinder
			for _, sub := range m.sources {
				out = append(out, AsSeriesFinders(sub)...)
			}
			return out
		}
		u, ok := s.(unwrapper)
		if !ok {
			return nil
		}
		s = u.Unwrap()
	}
	return nil
}
