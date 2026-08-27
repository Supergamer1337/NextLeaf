package series

import (
	"context"
	"database/sql"
	"time"

	"nextleaf/internal/library"
)

// clearSpentDecisions returns to Active every series whose standing decision
// has done its job. Each rule is driven by the reader's own library, so a
// decision expires from what they actually read rather than from anything
// NextLeaf has to remember them acting on.
func clearSpentDecisions(ctx context.Context, tx *sql.Tx, snap Snapshot) error {
	newest := newestFinish(snap.Reads)
	// A park is spent as soon as anything is finished after it was made: that
	// is the one instance of reading something else the reader asked for.
	if !newest.IsZero() {
		if _, err := tx.ExecContext(ctx,
			`UPDATE tracked_series SET decision = 'active', decided_at = 0, parked_after = 0
			 WHERE decision = 'parked' AND parked_after < ?`, newest.Unix()); err != nil {
			return err
		}
	}

	// A drop is undone by putting one of the series' books back on the TBR.
	// Only additions later than the drop count, or a copy that was already
	// shelved when the reader dropped the series would undo it immediately.
	for _, e := range snap.ToRead {
		if e.Book.Series == nil || e.DateAdded.IsZero() {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tracked_series SET decision = 'active', decided_at = 0
			 WHERE name = ? AND decision = 'dropped' AND decided_at < ?`,
			key(e.Book.Series.Name), e.DateAdded.Unix()); err != nil {
			return err
		}
	}

	// A pin says "this book is next", so reaching that book spends it.
	for _, group := range [][]library.Entry{snap.Reads, snap.Reading} {
		for _, e := range group {
			if e.Book.Series == nil {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE tracked_series SET decision = 'active', decided_at = 0, pinned_position = 0
				 WHERE name = ? AND decision = 'pinned' AND pinned_position <= ?`,
				key(e.Book.Series.Name), e.Book.Series.Position); err != nil {
				return err
			}
		}
	}
	return nil
}

// newestFinish is the latest completion date in reads. Sources hand these back
// newest-first, but entries with no date at all are common enough to warrant
// scanning rather than trusting the order.
func newestFinish(reads []library.Entry) time.Time {
	var newest time.Time
	for _, e := range reads {
		if e.FinishedAt.After(newest) {
			newest = e.FinishedAt
		}
	}
	return newest
}
