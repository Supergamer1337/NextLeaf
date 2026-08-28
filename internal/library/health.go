package library

import "time"

// Health describes how current one backend's data is. When a fetch fails and
// the cache falls back to last-known-good data, the page should say so rather
// than quietly showing an old library as if it were fresh.
type Health struct {
	Source string
	// Stale means the most recent fetch failed and older data is being served.
	Stale bool
	// Since is when the served data was last fetched successfully.
	Since time.Time
	// Err is the failure that forced the fallback, for the log and the notice.
	Err string
}

// healthReporter is implemented by decorators (Cached) that can fall back to
// stale data and therefore know whether they have.
type healthReporter interface {
	Health() Health
}

// HealthOf collects every source's health under s, seeing through known
// decorators and flattening a Multi. Sources without a reporting layer are
// omitted: nothing caches for them, so nothing can be stale.
func HealthOf(s Source) []Health {
	var out []Health
	for s != nil {
		if m, ok := s.(*Multi); ok {
			for _, sub := range m.sources {
				out = append(out, HealthOf(sub)...)
			}
			return out
		}
		if r, ok := s.(healthReporter); ok {
			return append(out, r.Health())
		}
		if u, ok := s.(unwrapper); ok {
			s = u.Unwrap()
			continue
		}
		return out
	}
	return out
}
