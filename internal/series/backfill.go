package series

import (
	"context"
	"log"
	"sync"

	"nextleaf/internal/library"
)

// Status describes how far the one-time history import has got, so the UI can
// explain why continuations are unavailable.
type Status struct {
	// Done is true once every capable source has been attempted. It does not
	// mean every source succeeded.
	Done bool
	// Imported counts the finished books folded into the store.
	Imported int
	// Failed names the sources whose history could not be read; they are
	// retried on the next resync.
	Failed []string
}

// Backfill imports the reader's complete finished-book history into the store,
// so series they finished long before installing NextLeaf are tracked. It runs
// in the background: the app serves variety picks throughout and withholds
// continuations only until Status reports Done.
type Backfill struct {
	store     *Store
	providers []library.HistoryProvider

	mu     sync.Mutex
	status Status
}

// NewBackfill prepares an import from providers. With no capable providers
// there is nothing to wait for, so it starts out Done.
func NewBackfill(store *Store, providers []library.HistoryProvider) *Backfill {
	return &Backfill{
		store:     store,
		providers: providers,
		status:    Status{Done: len(providers) == 0},
	}
}

// Status reports the import's progress. It is safe to call from any goroutine.
func (b *Backfill) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.status
	s.Failed = append([]string(nil), b.status.Failed...)
	return s
}

// Run imports each provider's history in turn, best effort: a source that fails
// is recorded and skipped rather than abandoning the ones that worked. Run
// always finishes Done, so a permanently broken backend cannot disable series
// tracking. It is safe to call again on a later resync.
func (b *Backfill) Run(ctx context.Context) {
	var (
		imported int
		failed   []string
	)

	for _, p := range b.providers {
		entries, err := p.ReadHistory(ctx)
		if err != nil {
			log.Printf("series backfill: reading %s history: %v", p.Name(), err)
			failed = append(failed, p.Name())
			continue
		}
		// Reconciled per source so one source's failure cannot roll back
		// another's history.
		if err := b.store.Reconcile(ctx, Snapshot{Reads: entries}); err != nil {
			log.Printf("series backfill: recording %s history: %v", p.Name(), err)
			failed = append(failed, p.Name())
			continue
		}
		imported += len(entries)
	}

	b.mu.Lock()
	b.status = Status{Done: true, Imported: imported, Failed: failed}
	b.mu.Unlock()
}
