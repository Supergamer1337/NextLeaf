---
status: superseded by ADR-0003
---

# Backfill the full read history on first run

Series tracking has to notice a new book in a series you finished years ago,
which means knowing your position in every series you have ever read. NextLeaf
previously asked each source for six recent books and nothing more, so on first
run it imports your complete read history instead, once, in the background.

## Considered Options

Forward-only tracking costs nothing and needs no new source capability, but
every series finished before the feature shipped stays invisible to it, which is
most of them. That makes the headline case work in a year rather than on day
one, so it was rejected.

## Consequences

- Sources gain an optional "all reads" capability, detected the same way as
  `SeriesResolver`. A source without it still contributes recent reads.
- The import is best effort per source: the banner clears when every capable
  source has finished, and a failed source retries on the 24h resync rather than
  disabling series tracking for good.
- First start is slower and hits the source's API harder than any later start,
  so the calls are throttled.

> Superseded: the backfill existed because history was assumed expensive to
> fetch. Measurement showed one request per source, so ADR 0003 recomputes the
> view from full data per request and the import machinery is gone.
