package library

import (
	"context"
	"io"
)

// CoverProvider is an OPTIONAL Source capability: it streams a cover image the
// provider holds behind its own authentication, so the app can serve covers a
// browser could not fetch from the backend directly. Sources whose cover URLs
// are publicly reachable simply do not implement it.
type CoverProvider interface {
	// CoverImage returns the cover for the provider's book id, with its
	// content type. The caller must close the body.
	CoverImage(ctx context.Context, id string) (io.ReadCloser, string, error)
}

// AsCoverProvider finds the CoverProvider on the source named name, seeing
// through known decorators (Cached) and searching a Multi's sources. ok is
// false when no source of that name supports the capability.
//
// Cover bytes bypass wrapping decorators such as Cached (see Unwrap) — the
// browser's own cache is expected to absorb repeat requests.
func AsCoverProvider(s Source, name string) (CoverProvider, bool) {
	for s != nil {
		if m, ok := s.(*Multi); ok {
			for _, sub := range m.sources {
				if p, ok := AsCoverProvider(sub, name); ok {
					return p, true
				}
			}
			return nil, false
		}
		if s.Name() == name {
			if p, ok := s.(CoverProvider); ok {
				return p, true
			}
		}
		if u, ok := s.(unwrapper); ok {
			s = u.Unwrap()
			continue
		}
		return nil, false
	}
	return nil, false
}
