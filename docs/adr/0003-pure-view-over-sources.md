---
status: accepted
---

# Compute the series view; persist only decisions and immutable facts

The first series-tracking architecture materialized derived state: a tracked
series row held a position high-water mark, a cached next book, alternatives,
and switch choices. Every hard bug found in review traced to that state
drifting from the sources — prequels invisible behind the high-water mark,
stale next-book caches after switching, reconciliation resurrecting rows the
reader had switched away from, merge semantics for coalescing rows, and a
migration bug that silently stopped all migrations.

The whole design rested on an assumption measured false mid-build: that a
reader's full history is expensive to fetch. It is one request per source.

So the series view is now a pure function, recomputed per request from the
sources' full data. NextLeaf persists only:

- **The reader's statements** — park, drop, pin, clear, and series preference —
  as an append-only log. A statement is never updated; it simply stops
  applying when its predicate says so (a park is spent when more books are
  finished than when it was made). Statements are anchored to *books*, not
  series names: names are display labels owned by the sources, and a metadata
  provider change must never orphan a decision.
- **Immutable facts**, such as an ISBN-to-book mapping, which cannot drift by
  definition.

Cross-source identity is established by evidence, strongest first: a neutral
identifier (ISBN) or the source's own book id; a normalized title-and-author
book key as the fallback join; and, for series, a shared book that both
sources file under the same name at the same slot. Anything weaker — a bare
name match — only ever *suggests*, and the reader's statement confirms it.
Heuristic failures are transient rendering artifacts, repaired by one
statement, never persisted corruption.

## Consequences

- The backfill, its banner, and reconciliation disappear
  (supersedes ADR 0002).
- A source outage degrades to *visibly stale* data (the cache serves
  last-known-good and the page says so) rather than wrong behaviour; the
  whole view is computed from one snapshot, so a stale page is a consistent
  old page.
- The next-in-series lookup cache remains the only background machinery, and
  it is disposable: losing it costs a re-fetch, never reader data.
- Backups reduce to the statement log: the `statement` table and its
  `statement_anchor` book keys, which travel together — anchors are what let
  a statement survive renames, so a backup of one without the other changes
  decisions after restore. The ISBN mapping is re-derivable source data, not
  reader state.
