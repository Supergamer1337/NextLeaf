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
// one. A step may hold several statements, which run in one transaction. Never
// edit a shipped step: append a new one.
// migrations are applied in order, each step advancing PRAGMA user_version by
// one. A step may hold several statements, which run in one transaction.
//
// Never edit a shipped step, and never regroup them: the version is a count
// of steps, so merging two would renumber every database in existence and
// silently skip everything after it. Append only.
var migrations = [][]string{
	{`CREATE TABLE tracked_series (
		name            TEXT PRIMARY KEY,
		display_name    TEXT NOT NULL,
		position        REAL NOT NULL DEFAULT 0,
		decision        TEXT NOT NULL DEFAULT 'active',
		decided_at      INTEGER NOT NULL DEFAULT 0,
		parked_after    INTEGER NOT NULL DEFAULT 0,
		pinned_position REAL NOT NULL DEFAULT 0
	)`},
	{`ALTER TABLE tracked_series ADD COLUMN slug TEXT NOT NULL DEFAULT ''`},
	{`ALTER TABLE tracked_series ADD COLUMN completed INTEGER NOT NULL DEFAULT 0`},
	{`ALTER TABLE tracked_series ADD COLUMN caught_up INTEGER NOT NULL DEFAULT 0`},
	{`ALTER TABLE tracked_series ADD COLUMN next_title TEXT NOT NULL DEFAULT ''`},
	{`ALTER TABLE tracked_series ADD COLUMN next_cover_url TEXT NOT NULL DEFAULT ''`},
	{`ALTER TABLE tracked_series ADD COLUMN next_url TEXT NOT NULL DEFAULT ''`},
	{`ALTER TABLE tracked_series ADD COLUMN next_position REAL NOT NULL DEFAULT 0`},
	{`ALTER TABLE tracked_series ADD COLUMN checked_at INTEGER NOT NULL DEFAULT 0`},
	{`ALTER TABLE tracked_series ADD COLUMN cover_url TEXT NOT NULL DEFAULT ''`},
	{
		// position becomes nullable, so a book that belongs to a series without
		// occupying a numbered slot is no longer indistinguishable from one at
		// position zero — series really do number prequels 0 or below. Every
		// stored zero meant "unknown" under the old rules, so it maps to NULL.
		`ALTER TABLE tracked_series RENAME TO tracked_series_old`,
		`CREATE TABLE tracked_series (
		name            TEXT PRIMARY KEY,
		display_name    TEXT NOT NULL,
		position        REAL,
		decision        TEXT NOT NULL DEFAULT 'active',
		decided_at      INTEGER NOT NULL DEFAULT 0,
		parked_after    INTEGER NOT NULL DEFAULT 0,
		pinned_position REAL NOT NULL DEFAULT 0,
		slug            TEXT NOT NULL DEFAULT '',
		completed       INTEGER NOT NULL DEFAULT 0,
		caught_up       INTEGER NOT NULL DEFAULT 0,
		next_title      TEXT NOT NULL DEFAULT '',
		next_cover_url  TEXT NOT NULL DEFAULT '',
		next_url        TEXT NOT NULL DEFAULT '',
		next_position   REAL NOT NULL DEFAULT 0,
		checked_at      INTEGER NOT NULL DEFAULT 0,
		cover_url       TEXT NOT NULL DEFAULT ''
	)`,
		`INSERT INTO tracked_series
	 SELECT name, display_name, NULLIF(position, 0), decision, decided_at,
	        parked_after, pinned_position, slug, completed, caught_up,
	        next_title, next_cover_url, next_url, next_position, checked_at, cover_url
	 FROM tracked_series_old`,
		`DROP TABLE tracked_series_old`,
	}, {
		// A book can belong to several series at once — a franchise and its
		// chronological reordering. One is tracked; the rest are offered so the
		// reader can switch, and their choice is remembered so reconciling does
		// not revert it.
		`CREATE TABLE series_alternative (
		name        TEXT NOT NULL,
		alternative TEXT NOT NULL,
		display     TEXT NOT NULL,
		PRIMARY KEY (name, alternative)
	)`,
		`ALTER TABLE tracked_series ADD COLUMN chosen INTEGER NOT NULL DEFAULT 0`,
	},
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
		for _, stmt := range migrations[i] {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", i+1, err)
			}
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

// preferred returns the reader's chosen series among the memberships of one
// book, so a switch survives the next reconcile. ok is false when they have
// expressed no preference, leaving the source's ranking to decide.
func preferred(ctx context.Context, tx *sql.Tx, memberships []library.Series) (library.Series, bool) {
	for _, m := range memberships {
		var chosen int
		err := tx.QueryRowContext(ctx,
			`SELECT chosen FROM tracked_series WHERE name = ?`, key(m.Name)).Scan(&chosen)
		if err == nil && chosen == 1 {
			return m, true
		}
	}
	return library.Series{}, false
}

// memberships lists every series a book belongs to, the tracked one first.
func memberships(b library.Book) []library.Series {
	if b.Series == nil {
		return nil
	}
	out := make([]library.Series, 0, 1+len(b.OtherSeries))
	out = append(out, *b.Series)
	return append(out, b.OtherSeries...)
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
			all := memberships(e.Book)
			if len(all) == 0 || key(all[0].Name) == "" {
				continue
			}
			tracked := all[0]
			if chosen, ok := preferred(ctx, tx, all); ok {
				// Each ordering numbers the same book differently, so the
				// chosen membership brings its own position with it.
				tracked = chosen
			}
			if err := observe(ctx, tx, tracked, e.Book.CoverURL); err != nil {
				return err
			}
			if err := recordAlternatives(ctx, tx, tracked, all); err != nil {
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
func observe(ctx context.Context, tx *sql.Tx, s library.Series, cover string) error {
	// An unplaced book records the series without claiming a slot in it.
	_, err := tx.ExecContext(ctx, `
		INSERT INTO tracked_series (name, display_name, position, slug, completed, cover_url)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			display_name = excluded.display_name,
			position = CASE
				WHEN excluded.position IS NULL THEN position
				WHEN position IS NULL THEN excluded.position
				ELSE MAX(position, excluded.position) END,
			-- A source that knows no slug must not blank one another supplied.
			slug = CASE WHEN excluded.slug != '' THEN excluded.slug ELSE slug END,
			completed = MAX(completed, excluded.completed),
			-- Reaching the remembered next book makes it the book behind you.
			next_title = CASE WHEN excluded.position >= next_position AND next_position > 0 THEN '' ELSE next_title END,
			next_cover_url = CASE WHEN excluded.position >= next_position AND next_position > 0 THEN '' ELSE next_cover_url END,
			next_url = CASE WHEN excluded.position >= next_position AND next_position > 0 THEN '' ELSE next_url END,
			next_position = CASE WHEN excluded.position >= next_position THEN 0 ELSE next_position END,
			-- The series wears the face of the furthest book read, so an
			-- earlier reread does not roll it backwards.
			cover_url = CASE
				WHEN excluded.cover_url = '' THEN cover_url
				WHEN cover_url = '' THEN excluded.cover_url
				WHEN excluded.position IS NOT NULL AND (position IS NULL OR excluded.position >= position)
					THEN excluded.cover_url
				ELSE cover_url END`,
		key(s.Name), s.Name, s.Position, s.Slug, boolToInt(s.Completed), cover)
	if err != nil {
		return fmt.Errorf("tracking series %q: %w", s.Name, err)
	}
	return nil
}

// SetNext records the book a lookup found after queried, the series position
// the lookup was made at, or marks the series caught up when there is nothing
// left. A later lookup finding a newly published book takes the series back out
// of "caught up".
//
// The result is discarded when the stored position has moved past queried: the
// reader finished a book while the lookup was in flight, so its answer — next
// book and caught-up alike — describes a question that is no longer being asked.
func (s *Store) SetNext(ctx context.Context, name string, queried float64, next library.Entry, found bool, at time.Time) error {
	var title, cover, url string
	var pos float64
	if found {
		title, cover, url = next.Book.Title, next.Book.CoverURL, next.Book.URL
		if next.Book.Series != nil {
			pos, _ = next.Book.Series.Slot()
		}
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE tracked_series SET
			caught_up = ?,
			next_title = ?,
			next_cover_url = ?,
			next_url = ?,
			next_position = ?,
			checked_at = ?
		WHERE name = ? AND position = ?`,
		boolToInt(!found), title, cover, url, pos, at.Unix(), key(name), queried)
	if err != nil {
		return fmt.Errorf("recording the next book of series %q: %w", name, err)
	}
	return nil
}

// recordAlternatives notes the other series the same book belongs to, so the
// drawer can offer them.
func recordAlternatives(ctx context.Context, tx *sql.Tx, tracked library.Series, all []library.Series) error {
	for _, m := range all {
		if key(m.Name) == key(tracked.Name) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO series_alternative (name, alternative, display)
			VALUES (?, ?, ?) ON CONFLICT(name, alternative) DO UPDATE SET display = excluded.display`,
			key(tracked.Name), key(m.Name), m.Name); err != nil {
			return fmt.Errorf("recording alternatives of %q: %w", tracked.Name, err)
		}
	}
	return nil
}

// alternatives lists the other series a tracked series' books belong to.
func (s *Store) alternatives(ctx context.Context, name string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT display FROM series_alternative WHERE name = ? ORDER BY display`, key(name))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var display string
		if err := rows.Scan(&display); err != nil {
			return nil, err
		}
		out = append(out, display)
	}
	return out, rows.Err()
}

// Switch tracks the series under one of its alternatives instead. The standing
// decision moves across — correcting which series a book is filed under is not
// a change of heart about reading it — and the old row is dropped so the next
// reconcile rebuilds it under the chosen name.
func (s *Store) Switch(ctx context.Context, from, to string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		decision               string
		decidedAt, parkedAfter int64
		pinnedPosition         float64
	)
	err = tx.QueryRowContext(ctx, `
		SELECT decision, decided_at, parked_after, pinned_position
		FROM tracked_series WHERE name = ?`, key(from)).
		Scan(&decision, &decidedAt, &parkedAfter, &pinnedPosition)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == sql.ErrNoRows {
		decision = Active.String()
	}

	// chosen marks the reader's pick so the next reconcile does not revert to
	// the source's ranking.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tracked_series (name, display_name, decision, decided_at, parked_after, pinned_position, chosen)
		VALUES (?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(name) DO UPDATE SET
			decision = excluded.decision,
			decided_at = excluded.decided_at,
			parked_after = excluded.parked_after,
			pinned_position = excluded.pinned_position,
			chosen = 1`,
		key(to), to, decision, decidedAt, parkedAfter, pinnedPosition); err != nil {
		return fmt.Errorf("switching to series %q: %w", to, err)
	}

	// The alternatives follow the books, so they move to the chosen name.
	if _, err := tx.ExecContext(ctx,
		`UPDATE OR REPLACE series_alternative SET name = ? WHERE name = ?`, key(to), key(from)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM series_alternative WHERE name = ? AND alternative = ?`, key(to), key(to)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO series_alternative (name, alternative, display) VALUES (?, ?, ?)
		 ON CONFLICT(name, alternative) DO UPDATE SET display = excluded.display`,
		key(to), key(from), from); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tracked_series WHERE name = ?`, key(from)); err != nil {
		return err
	}
	return tx.Commit()
}

// List returns every tracked series, ordered by name so the UI and tests see a
// stable order.
func (s *Store) List(ctx context.Context) ([]Tracked, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT display_name, position, decision, decided_at, parked_after, pinned_position, slug, completed, caught_up, next_title, next_cover_url, next_url, next_position, checked_at, cover_url
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
		if err := rows.Scan(&t.Name, &t.Position, &decision, &decidedAt, &parkedAfter, &t.PinnedPosition, &t.Slug, &t.Completed, &t.CaughtUp, &t.NextTitle, &t.NextCoverURL, &t.NextURL, &t.NextPosition, &checkedAt, &t.CoverURL); err != nil {
			return nil, err
		}
		t.Decision = parseDecision(decision)
		t.DecidedAt = unix(decidedAt)
		t.ParkedAfter = unix(parkedAfter)
		t.CheckedAt = unix(checkedAt)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Read after the cursor closes: the store holds a single connection.
	for i := range out {
		alts, err := s.alternatives(ctx, out[i].Name)
		if err != nil {
			return nil, err
		}
		out[i].Alternatives = alts
	}
	return out, nil
}

// Get returns the tracked series by name. ok is false when it isn't tracked.
func (s *Store) Get(ctx context.Context, name string) (Tracked, bool, error) {
	var (
		t                                 Tracked
		decision                          string
		decidedAt, parkedAfter, checkedAt int64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT display_name, position, decision, decided_at, parked_after, pinned_position, slug, completed, caught_up, next_title, next_cover_url, next_url, next_position, checked_at, cover_url
		FROM tracked_series WHERE name = ?`, key(name)).
		Scan(&t.Name, &t.Position, &decision, &decidedAt, &parkedAfter, &t.PinnedPosition, &t.Slug, &t.Completed, &t.CaughtUp, &t.NextTitle, &t.NextCoverURL, &t.NextURL, &t.NextPosition, &checkedAt, &t.CoverURL)
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
	alts, err := s.alternatives(ctx, t.Name)
	if err != nil {
		return Tracked{}, false, err
	}
	t.Alternatives = alts
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
// refers to; reading it clears the pin. The old pin's removal and the new pin
// commit together, so two racing pins can never leave both series pinned, nor
// a failure leave neither.
func (s *Store) Pin(ctx context.Context, name string, at time.Time, position float64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Only one series is pinned at a time, so an existing pin steps aside.
	if _, err := tx.ExecContext(ctx,
		`UPDATE tracked_series SET decision = 'active', decided_at = 0, pinned_position = 0
		 WHERE decision = 'pinned'`); err != nil {
		return err
	}
	if err := decideIn(ctx, tx, name, Pinned, at, decision{pinnedPosition: position}); err != nil {
		return err
	}
	return tx.Commit()
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

// decide writes a standing decision outside any wider transaction.
func (s *Store) decide(ctx context.Context, name string, d Decision, at time.Time, with func(*decision)) error {
	var q decision
	if with != nil {
		with(&q)
	}
	return decideIn(ctx, s.db, name, d, at, q)
}

// execer is the slice of database/sql shared by *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// decideIn writes a standing decision on db, tracking the series first if a
// book of it has never been seen — a series can be dropped before it is ever
// read.
func decideIn(ctx context.Context, db execer, name string, d Decision, at time.Time, q decision) error {
	_, err := db.ExecContext(ctx, `
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
