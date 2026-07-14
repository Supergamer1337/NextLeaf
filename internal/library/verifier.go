package library

import "context"

// Verifier is an OPTIONAL Source capability: a cheap authenticated round-trip
// that confirms the source's credentials are accepted, without fetching the
// whole library. It lets the app report at startup whether each configured
// backend can actually log in. Sources that need no credentials may omit it.
type Verifier interface {
	// Verify performs a lightweight authenticated request, returning nil when
	// the credentials are accepted and an error (e.g. ErrUnauthorized) otherwise.
	Verify(ctx context.Context) error
}

// AsVerifier finds a Verifier within s, seeing through known decorators
// (Cached). ok is false when nothing underlying supports verification. A Multi
// is not unwrapped: callers verify each activated source on its own so the log
// can name the one that failed.
func AsVerifier(s Source) (Verifier, bool) {
	for s != nil {
		if v, ok := s.(Verifier); ok {
			return v, true
		}
		if u, ok := s.(unwrapper); ok {
			s = u.Unwrap()
			continue
		}
		return nil, false
	}
	return nil, false
}
