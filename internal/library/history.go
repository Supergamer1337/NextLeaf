package library

import "context"

// HistoryProvider is an OPTIONAL Source capability: the reader's complete
// finished-book history rather than the recent window RecentReads offers.
// Series tracking needs it once, to notice series finished long before NextLeaf
// was installed (see docs/adr/0002-backfill-full-read-history.md). Sources that
// cannot page their whole history simply do not implement it.
type HistoryProvider interface {
	// Name identifies the backend, for reporting which histories were imported.
	Name() string
	// ReadHistory returns every finished book the source knows about. It is
	// expensive by nature and expected to be called rarely.
	ReadHistory(ctx context.Context) ([]Entry, error)
}

// AsHistoryProviders collects every source under s that can supply a full read
// history, seeing through known decorators (Cached) and flattening a Multi. The
// result is empty when nothing underlying supports the capability.
//
// Providers are returned individually rather than merged because the backfill
// is best effort per source: one backend failing must not discard another's
// history.
func AsHistoryProviders(s Source) []HistoryProvider {
	for s != nil {
		if m, ok := s.(*Multi); ok {
			var out []HistoryProvider
			for _, sub := range m.sources {
				out = append(out, AsHistoryProviders(sub)...)
			}
			return out
		}
		if p, ok := s.(HistoryProvider); ok {
			return []HistoryProvider{p}
		}
		if u, ok := s.(unwrapper); ok {
			s = u.Unwrap()
			continue
		}
		return nil
	}
	return nil
}
