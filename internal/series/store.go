package series

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver; keeps CGO_ENABLED=0 builds working
)

// migrations are applied in order, each step advancing PRAGMA user_version by
// one. A step may hold several statements, which run in one transaction.
//
// Never edit a shipped step, and never regroup them: the version is a count of
// steps, so renumbering silently skips everything after the old count on every
// database in existence. TestShippedMigrationsAreUntouched pins the shipped
// prefix. Append only.
//
// Steps 1-15 built the stored-state schema the first architecture used; steps
// 16+ carry every database to the statement log of ADR 0003.
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
	// Two orderings of one franchise are told apart by their blurb, and by
	// which backend claims them.
	{`ALTER TABLE series_alternative ADD COLUMN description TEXT NOT NULL DEFAULT ''`},
	{`ALTER TABLE series_alternative ADD COLUMN source TEXT NOT NULL DEFAULT ''`},
	{`ALTER TABLE series_alternative ADD COLUMN position REAL`},
	// ADR 0003: the stored-state schema gives way to an append-only log of
	// reader statements. Only durable decisions migrate: drops survive, while
	// parks and pins are ephemeral by design (a park is spent by the next
	// finished book) and switch choices cannot be attributed to a source in
	// the old schema, so they are not carried over.
	{`CREATE TABLE statement (
		id          INTEGER PRIMARY KEY,
		kind        TEXT NOT NULL,
		made_at     INTEGER NOT NULL,
		park_count  INTEGER NOT NULL DEFAULT 0,
		pinned_book TEXT NOT NULL DEFAULT '',
		pref_source TEXT NOT NULL DEFAULT '',
		pref_name   TEXT NOT NULL DEFAULT '',
		name        TEXT NOT NULL DEFAULT ''
	)`},
	{`CREATE TABLE statement_anchor (
		statement_id INTEGER NOT NULL,
		book_key     TEXT NOT NULL
	)`},
	{`INSERT INTO statement (kind, made_at, name)
	  SELECT 'dropped', decided_at, display_name
	  FROM tracked_series WHERE decision = 'dropped'`},
	{
		`DROP TABLE tracked_series`,
		`DROP TABLE series_alternative`,
	},
}

// Statement is one thing the reader said: park, drop, pin, clear, or a series
// preference. Anchors are the book keys of the series it was said about, so
// it survives any renaming the sources do; Name is a display label kept as a
// matching fallback for statements that predate anchors.
type Statement struct {
	ID     int64
	Kind   string
	MadeAt time.Time
	// ParkCount is how many books were finished when the park was made;
	// finishing one more spends it.
	ParkCount int
	// PinnedBook is the book key the pin waits on; reading it clears the pin.
	PinnedBook string
	// PrefSource and PrefName say which series identity the reader wants the
	// anchored books tracked under.
	PrefSource, PrefName string
	Name                 string
	Anchors              []string
}

// Store is the durable record of the reader's statements — the only state
// NextLeaf owns.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path, applying any
// outstanding migrations. The parent directory is created too, so a container
// with a bare volume mount needs no shell to prepare it.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, err)
		}
	}

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

// Append records one statement with its anchors, atomically.
func (s *Store) Append(ctx context.Context, st Statement) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO statement (kind, made_at, park_count, pinned_book, pref_source, pref_name, name)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		st.Kind, st.MadeAt.Unix(), st.ParkCount, st.PinnedBook, st.PrefSource, st.PrefName, st.Name)
	if err != nil {
		return fmt.Errorf("recording %s statement: %w", st.Kind, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, anchor := range st.Anchors {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO statement_anchor (statement_id, book_key) VALUES (?, ?)`, id, anchor); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Statements returns every statement in the order it was made.
func (s *Store) Statements(ctx context.Context) ([]Statement, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, made_at, park_count, pinned_book, pref_source, pref_name, name
		FROM statement ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Statement
	byID := map[int64]int{}
	for rows.Next() {
		var st Statement
		var madeAt int64
		if err := rows.Scan(&st.ID, &st.Kind, &madeAt, &st.ParkCount,
			&st.PinnedBook, &st.PrefSource, &st.PrefName, &st.Name); err != nil {
			return nil, err
		}
		st.MadeAt = time.Unix(madeAt, 0)
		byID[st.ID] = len(out)
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	anchors, err := s.db.QueryContext(ctx,
		`SELECT statement_id, book_key FROM statement_anchor ORDER BY statement_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = anchors.Close() }()
	for anchors.Next() {
		var id int64
		var bookKey string
		if err := anchors.Scan(&id, &bookKey); err != nil {
			return nil, err
		}
		if i, ok := byID[id]; ok {
			out[i].Anchors = append(out[i].Anchors, bookKey)
		}
	}
	return out, anchors.Err()
}
