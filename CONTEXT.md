# NextLeaf

A single-user, self-hosted service that picks your next read from the reading
lists it is given. It optimises for *variety* over similarity, so you don't
read five of the same kind of book in a row, while still letting a series you
care about carry you forward.

## Language

### Reading data

**Source**:
A backend holding your reading data, such as Hardcover or Grimmory. Sources are
merged, so one recommendation draws on all of them.
_Avoid_: provider, backend, integration

**Entry**:
A book together with your relationship to it: its status, your rating, and the
dates that matter. A book with no entry is one you have never touched.
_Avoid_: item, record, shelf entry

**TBR**:
The books you intend to read. In Hardcover terms, *Want to Read*.
_Avoid_: reading list, backlog, queue, wishlist

**Variety pick**:
A recommendation drawn at weighted random from the TBR, scored to favour the
genres, authors and formats you have been neglecting.
_Avoid_: random pick, reroll, shuffle

### Series

**Tracked series**:
A series NextLeaf remembers across restarts, together with the furthest position
you have read in it and your standing decision about it. A series becomes
tracked the moment you read any book in it; you never add one by hand.
_Avoid_: followed series, subscription, watched series

**Position**:
A book's numbered slot in a series. Series number their volumes as they
please: halves for novellas, zero or negative numbers for prequels. A position
is therefore any number, and "no position" is not one of them.
_Avoid_: index, order, number, volume

**Unplaced**:
Belonging to a series without occupying a numbered slot — a prequel, a
companion volume, or simply a book whose source never said where it sits. An
unplaced book cannot say what follows it, so it anchors nothing; the series can
still be continued from volumes on the reader's own shelf.
_Avoid_: position zero, unnumbered, missing position

**Alternative series**:
Another series the same books belong to — usually the same franchise ordered
differently (published order versus chronological), or a franchise beside one
of its sub-series. A book is tracked under one series at a time; the reader can
switch it to an alternative.
_Avoid_: duplicate series, secondary series, variant

**Anchor**:
The read or in-progress book that decides which series is up for continuation,
and whose position defines what "next" means.
_Avoid_: last read, current book

**Continuation**:
A recommendation for the next book in a tracked series. It does not require the
book to be on your TBR, and does not require it to have existed when you started
the series.
_Avoid_: sequel pick, next-in-series

**Standing decision**:
Your explicit instruction about a tracked series, which persists until you
change it or it clears itself: *parked*, *dropped* or *pinned*. A series with no
standing decision is simply active.
_Avoid_: series status, series state, flag

**Caught up**:
Having read everything published in a tracked series, so it offers nothing
until a new volume appears. Distinct from the series itself being *completed*,
which is a fact about the author having ended it. The drawer files these under
"Finished".
_Avoid_: finished, completed, done, exhausted

**Backfill**:
The one-time import of your complete read history from every source, run once so
that series you finished long before installing NextLeaf are tracked. Until it
finishes, continuations are unavailable and the app says so.
_Avoid_: sync, initial load, seeding

**Parked**:
A standing decision to skip one turn of a tracked series: you want to read
something else first. The park clears by itself once you finish any other book.
Distinct from a book being *paused*, which is a per-book status a Source reports.
_Avoid_: paused, snoozed, deferred, on hold

**Dropped**:
A standing decision to abandon a tracked series entirely. A dropped series
offers no continuation and its books are withheld from variety picks. Adding one
of its books to your TBR afterwards undoes the drop. Distinct from a book being
*DNF*, which is a per-book status a Source reports.
_Avoid_: DNF, abandoned, ignored, blocked

**Pinned**:
A standing decision that one tracked series is what you are reading next. It
outranks every other series and every variety pick. Only one series is pinned at
a time.
_Avoid_: starred, favourited, prioritised, next-up
