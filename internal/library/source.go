package library

import "context"

// Source is a provider of a user's reading data. Implementations wrap a backend
// such as Hardcover; the rest of Nextleaf depends only on this interface so a
// backend can be swapped or supplemented without changes elsewhere.
//
// The slices and entries returned are read-only: callers must not mutate them,
// since a decorator like Cached hands back its own retained copies.
type Source interface {
	// Name identifies the backend, e.g. "hardcover".
	Name() string
	// CurrentlyReading returns the books the user is reading right now.
	CurrentlyReading(ctx context.Context) ([]Entry, error)
	// RecentReads returns the most recently finished books, newest first,
	// capped at limit.
	RecentReads(ctx context.Context, limit int) ([]Entry, error)
	// ToRead returns the books the user intends to read (their TBR list).
	ToRead(ctx context.Context) ([]Entry, error)
}
