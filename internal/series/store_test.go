package series

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "nextleaf.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestStatementsRoundTripInOrder(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	for _, s := range []Statement{
		{Kind: KindPark, MadeAt: day0, ParkCount: 4, Name: "Mistborn", Anchors: []string{"a", "b"}},
		{Kind: KindPrefer, MadeAt: day1, PrefSource: "hardcover", PrefName: "The Expanse", Anchors: []string{"c"}},
	} {
		if err := st.Append(ctx, s); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := st.Statements(ctx)
	if err != nil {
		t.Fatalf("Statements: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d statements, want 2", len(got))
	}
	first := got[0]
	if first.Kind != KindPark || first.ParkCount != 4 || first.Name != "Mistborn" {
		t.Errorf("first = %+v, want the park intact", first)
	}
	if len(first.Anchors) != 2 || first.Anchors[0] != "a" {
		t.Errorf("Anchors = %v, want [a b]", first.Anchors)
	}
	if second := got[1]; second.PrefName != "The Expanse" || second.MadeAt.Unix() != day1.Unix() {
		t.Errorf("second = %+v, want the prefer intact", second)
	}
}

func TestStatementsSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nextleaf.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(ctx, Statement{Kind: KindDrop, MadeAt: day0, Anchors: []string{"k"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.Statements(ctx)
	if err != nil || len(got) != 1 || got[0].Kind != KindDrop {
		t.Errorf("Statements after reopen = (%v, %v), want the drop back", got, err)
	}
}

// seedOldSchema builds a database exactly as the stored-state architecture
// left it at the given step, stamped with that version.
func seedOldSchema(t *testing.T, path string, steps int, rows func(db *sql.DB)) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, step := range migrations[:steps] {
		for _, stmt := range step {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("seeding step: %v", err)
			}
		}
	}
	if rows != nil {
		rows(db)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", steps)); err != nil {
		t.Fatal(err)
	}
}

// shippedSteps is how many migration steps the stored-state architecture
// shipped; a live database from its last build is stamped with this.
const shippedSteps = 15

// TestShippedMigrationsAreUntouched pins the shipped prefix of the migration
// list. The version is a count of steps, so editing or regrouping shipped
// steps silently breaks every existing database — this failed twice during
// development before this test existed.
func TestShippedMigrationsAreUntouched(t *testing.T) {
	if len(migrations) < shippedSteps {
		t.Fatalf("migrations = %d steps, want the %d shipped ones kept verbatim", len(migrations), shippedSteps)
	}
	h := sha256.New()
	for _, step := range migrations[:shippedSteps] {
		for _, stmt := range step {
			_, _ = h.Write([]byte(stmt))
		}
	}
	const want = "537df6995f439ec7"
	if got := fmt.Sprintf("%x", h.Sum(nil))[:16]; got != want {
		t.Errorf("shipped migration checksum = %s, want %s — shipped steps must never change; append instead", got, want)
	}
}

func TestAStoredStateDatabaseMigratesItsDrops(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	seedOldSchema(t, path, shippedSteps, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO tracked_series (name, display_name, decision, decided_at)
			VALUES ('mistborn', 'Mistborn', 'dropped', ?),
			       ('saga', 'Saga', 'parked', ?)`, day0.Unix(), day0.Unix()); err != nil {
			t.Fatal(err)
		}
	})

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a stored-state database: %v", err)
	}
	defer func() { _ = st.Close() }()

	got, err := st.Statements(ctx)
	if err != nil {
		t.Fatalf("Statements: %v", err)
	}
	// The drop is durable and survives as a name-matched statement; the park
	// was ephemeral by design and is deliberately not carried.
	if len(got) != 1 || got[0].Kind != KindDrop || got[0].Name != "Mistborn" {
		t.Errorf("migrated statements = %+v, want just the drop under its name", got)
	}
}

func TestTheOldestShippedSchemaStillMigrates(t *testing.T) {
	// Version 10 is what the very first per-statement scheme stamped; every
	// later step must still replay on top of it.
	path := filepath.Join(t.TempDir(), "ancient.db")
	seedOldSchema(t, path, 10, nil)

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open on the oldest schema: %v", err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.Statements(context.Background()); err != nil {
		t.Errorf("Statements after full replay: %v", err)
	}
}
