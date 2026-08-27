package series

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nextleaf/internal/library"

	_ "modernc.org/sqlite" // pure-Go driver; keeps CGO_ENABLED=0 builds working
)

// migrations are applied in order, each step advancing PRAGMA user_version by
// one. Never edit a shipped step: append a new one.
var migrations = []string{
	`CREATE TABLE tracked_series (
		name            TEXT PRIMARY KEY,
		display_name    TEXT NOT NULL,
		position        REAL NOT NULL DEFAULT 0,
		decision        TEXT NOT NULL DEFAULT 'active',
		decided_at      INTEGER NOT NULL DEFAULT 0,
		parked_after    INTEGER NOT NULL DEFAULT 0,
		pinned_position REAL NOT NULL DEFAULT 0
	)`,
	`ALTER TABLE tracked_series ADD COLUMN slug TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE tracked_series ADD COLUMN completed INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE tracked_series ADD COLUMN caught_up INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE tracked_series ADD COLUMN next_title TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE tracked_series ADD COLUMN next_cover_url TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE tracked_series ADD COLUMN next_url TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE tracked_series ADD COLUMN next_position REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE tracked_series ADD COLUMN checked_at INTEGER NOT NULL DEFAULT 0`,
}

// Store is the durable record of tracked series and standing decisions.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the series database at path, applying any
// outstanding migrations. The parent directory is created too, so a container
// with a bare volume mount needs no shell to prepare it.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	// WAL keeps the background backfill from blocking page loads; a single
	// connection removes lock contention entirely at this data volume.
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate applies every migration past the database's user_version. Each step
// runs in its own transaction, so a failure leaves the version at the last
// step that fully applied.
func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}

	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		// PRAGMA takes no bind parameters, and i is loop-bounded, not user input.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}

// Snapshot is the reading data the reconciliation rules need. Reads is expected
// newest-first, as sources provide it.
type Snapshot struct {
	Reads   []library.Entry
	Reading []library.Entry
	ToRead  []library.Entry
}

// Reconcile folds the reader's current library state into the store: every
// series they have read into becomes tracked at its furthest position, and
// standing decisions that have served their purpose are cleared.
func (s *Store) Reconcile(ctx context.Context, snap Snapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, group := range [][]library.Entry{snap.Reads, snap.Reading} {
		for _, e := range group {
			if e.Book.Series == nil || key(e.Book.Series.Name) == "" {
				continue
			}
			if err := observe(ctx, tx, *e.Book.Series); err != nil {
				return err
			}
		}
	}

	if err := clearSpentDecisions(ctx, tx, snap); err != nil {
		return err
	}
	return tx.Commit()
}

// observe records a series at the furthest position seen. A position never goes
// backwards: rereading book 2 of a series finished at book 5 is not a regression.
func observe(ctx context.Context, tx *sql.Tx, s library.Series) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO tracked_series (name, display_name, position, slug, completed)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			display_name = excluded.display_name,
			position = MAX(position, excluded.position),
			-- A source that knows no slug must not blank one another supplied.
			slug = CASE WHEN excluded.slug != '' THEN excluded.slug ELSE slug END,
			completed = MAX(completed, excluded.completed),
			-- Reaching the remembered next book makes it the book behind you.
			next_title = CASE WHEN excluded.position >= next_position AND next_position > 0 THEN '' ELSE next_title END,
			next_cover_url = CASE WHEN excluded.position >= next_position AND next_position > 0 THEN '' ELSE next_cover_url END,
			next_url = CASE WHEN excluded.position >= next_position AND next_position > 0 THEN '' ELSE next_url END,
			next_position = CASE WHEN excluded.position >= next_position THEN 0 ELSE next_position END`,
		key(s.Name), s.Name, s.Position, s.Slug, boolToInt(s.Completed))
	if err != nil {
		return fmt.Errorf("tracking series %q: %w", s.Name, err)
	}
	return nil
}

// SetNext records the book a lookup found after a series' current position, or
// marks the series caught up when there is nothing left. A later lookup finding
// a newly published book takes the series back out of "caught up".
func (s *Store) SetNext(ctx context.Context, name string, next library.Entry, found bool, at time.Time) error {
	var title, cover, url string
	var pos float64
	if found {
		title, cover, url = next.Book.Title, next.Book.CoverURL, next.Book.URL
		if next.Book.Series != nil {
			pos = next.Book.Series.Position
		}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tracked_series (name, display_name, caught_up, next_title, next_cover_url, next_url, next_position, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			caught_up = excluded.caught_up,
			next_title = excluded.next_title,
			next_cover_url = excluded.next_cover_url,
			next_url = excluded.next_url,
			next_position = excluded.next_position,
			checked_at = excluded.checked_at`,
		key(name), name, boolToInt(!found), title, cover, url, pos, at.Unix())
	if err != nil {
		return fmt.Errorf("recording the next book of series %q: %w", name, err)
	}
	return nil
}

// List returns every tracked series, ordered by name so the UI and tests see a
// stable order.
func (s *Store) List(ctx context.Context) ([]Tracked, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT display_name, position, decision, decided_at, parked_after, pinned_position, slug, completed, caught_up, next_title, next_cover_url, next_url, next_position, checked_at
		FROM tracked_series ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Tracked
	for rows.Next() {
		var (
			t                                 Tracked
			decision                          string
			decidedAt, parkedAfter, checkedAt int64
		)
		if err := rows.Scan(&t.Name, &t.Position, &decision, &decidedAt, &parkedAfter, &t.PinnedPosition, &t.Slug, &t.Completed, &t.CaughtUp, &t.NextTitle, &t.NextCoverURL, &t.NextURL, &t.NextPosition, &checkedAt); err != nil {
			return nil, err
		}
		t.Decision = parseDecision(decision)
		t.DecidedAt = unix(decidedAt)
		t.ParkedAfter = unix(parkedAfter)
		t.CheckedAt = unix(checkedAt)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get returns the tracked series by name. ok is false when it isn't tracked.
func (s *Store) Get(ctx context.Context, name string) (Tracked, bool, error) {
	var (
		t                                 Tracked
		decision                          string
		decidedAt, parkedAfter, checkedAt int64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT display_name, position, decision, decided_at, parked_after, pinned_position, slug, completed, caught_up, next_title, next_cover_url, next_url, next_position, checked_at
		FROM tracked_series WHERE name = ?`, key(name)).
		Scan(&t.Name, &t.Position, &decision, &decidedAt, &parkedAfter, &t.PinnedPosition, &t.Slug, &t.Completed, &t.CaughtUp, &t.NextTitle, &t.NextCoverURL, &t.NextURL, &t.NextPosition, &checkedAt)
	switch {
	case err == sql.ErrNoRows:
		return Tracked{}, false, nil
	case err != nil:
		return Tracked{}, false, err
	}
	t.Decision = parseDecision(decision)
	t.DecidedAt = unix(decidedAt)
	t.ParkedAfter = unix(parkedAfter)
	t.CheckedAt = unix(checkedAt)
	return t, true, nil
}

// Park skips the series for one turn. after is the newest finish date currently
// known; finishing anything later than it clears the park.
func (s *Store) Park(ctx context.Context, name string, at, after time.Time) error {
	return s.decide(ctx, name, Parked, at, func(q *decision) { q.parkedAfter = after })
}

// Drop abandons the series. Only a TBR addition later than at will clear it.
func (s *Store) Drop(ctx context.Context, name string, at time.Time) error {
	return s.decide(ctx, name, Dropped, at, nil)
}

// Pin makes the series the next thing to read. position is the book the pin
// refers to; reading it clears the pin.
func (s *Store) Pin(ctx context.Context, name string, at time.Time, position float64) error {
	// Only one series is pinned at a time, so an existing pin steps aside.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tracked_series SET decision = 'active', decided_at = 0, pinned_position = 0
		 WHERE decision = 'pinned'`); err != nil {
		return err
	}
	return s.decide(ctx, name, Pinned, at, func(q *decision) { q.pinnedPosition = position })
}

// Clear returns the series to Active, undoing a park, drop or pin.
func (s *Store) Clear(ctx context.Context, name string) error {
	return s.decide(ctx, name, Active, time.Time{}, nil)
}

// decision carries the fields specific to one kind of standing decision.
type decision struct {
	parkedAfter    time.Time
	pinnedPosition float64
}

// decide writes a standing decision, tracking the series first if a book of it
// has never been seen — a series can be dropped before it is ever read.
func (s *Store) decide(ctx context.Context, name string, d Decision, at time.Time, with func(*decision)) error {
	var q decision
	if with != nil {
		with(&q)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tracked_series (name, display_name, decision, decided_at, parked_after, pinned_position)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			decision = excluded.decision,
			decided_at = excluded.decided_at,
			parked_after = excluded.parked_after,
			pinned_position = excluded.pinned_position`,
		key(name), name, d.String(), at.Unix(), q.parkedAfter.Unix(), q.pinnedPosition)
	if err != nil {
		return fmt.Errorf("recording %s for series %q: %w", d, name, err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// unix converts stored seconds back to a time, mapping the zero sentinel to the
// zero time rather than 1970.
func unix(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}
