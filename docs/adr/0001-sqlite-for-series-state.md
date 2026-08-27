---
status: accepted
---

# SQLite for series state

Series tracking needs decisions (parked, dropped, pinned) and reading positions
to survive restarts, which ends NextLeaf's run as a stateless service. We store
that state in SQLite via `modernc.org/sqlite`, the first external dependency the
project has taken.

## Considered Options

A JSON file written by atomic rename was the alternative, and on data volume
alone it wins: one row per series ever read, one user, one writer, every read
loads the whole set. SQLite's concurrency and query flexibility buy nothing at
that scale, leaving schema migrations as its only real advantage. It was chosen
anyway, deliberately, as the operator does not want hand-rolled JSON as a
storage format. `mattn/go-sqlite3` was rejected outright: it needs cgo, which
breaks the `CGO_ENABLED=0` static build and the `FROM scratch` image.
`modernc.org/sqlite` is pure Go and keeps both intact.

## Consequences

- The dependency is transitive-heavy (`modernc.org/libc` and friends), so the
  README's "no external dependencies" claim no longer holds.
- The README's "no state to persist, so no volumes are needed" no longer holds
  either; deployments need a mounted volume or they silently lose series state
  on every container replacement.
- Schema changes now need migrations.
