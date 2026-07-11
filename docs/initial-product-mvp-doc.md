# Nextleaf

Nextleaf is a small, self-hosted service that recommends what to read next from a user's existing Hardcover library.

Its purpose is simple:

> Recommend variety instead of similarity.

The app should help prevent repeated reading streaks such as only fantasy, only nonfiction, or one long series immediately followed by another.

---

## MVP goal

Given the user's Want to Read list and recent reading history, recommend one book using weighted randomness.

The recommendation should favor variety while still allowing:

- Continuing a good series
- Returning to a paused series later
- Older books on the list
- Recently added books occasionally
- Books from familiar genres occasionally

The result should feel guided, not deterministic.

---

## Data source

Use Hardcover as the primary source.

Import:

- Want to Read books
- Recently completed books
- Genres
- Fiction or nonfiction
- Authors
- Series and series position
- Publication year
- Date added, if available

Store recommendation history and local preferences in Nextleaf.

Grimmory is not required for the MVP.

---

## Recommendation rules

Each eligible book receives a score.

Increase its score when:

- Its genre has not appeared recently
- It changes from fiction to nonfiction, or the reverse, after a streak
- It has been on the Want to Read list for a long time
- It continues an active series
- It resumes a paused series after enough unrelated books

Decrease its score when:

- It repeats a recently dominant genre
- It was added very recently
- It was recommended recently
- It starts a new series immediately after a long series streak
- It is temporarily postponed

Exclude it when:

- It is already completed
- It is marked Not Interested
- Its series is marked Stopped
- It is still inside a cooldown

After scoring, choose randomly using the scores as weights.

Do not always choose the highest-scoring book.

---

## Series behavior

Series should keep momentum without dominating all reading.

Series states:

- **Active** — continuing is encouraged
- **Paused** — temporarily hidden
- **Dormant** — unfinished but not actively prioritized
- **Stopped** — excluded

The next book in an active series gets a bonus.

That bonus should gradually decrease after several consecutive books in the same series.

After a long series streak:

- Standalone books become more likely
- Different genres become more likely
- Starting another series becomes less likely

When pausing a series, allow the user to bring it back after:

- 1 other book
- 2 other books
- 3 other books
- Manual resume

Once the break is complete, the series should gradually become more likely again.

---

## User interface

The main page should show one recommendation with:

- Cover
- Title
- Author
- Series information, when relevant
- A short explanation

Actions:

- **Choose this**
- **Pick another**
- **Not now**
- **Not interested**
- **Pause series**
- **Resume series**
- **Sync Hardcover**

Example explanation:

> This book differs from your recent fantasy streak, has been on your list for two years, and continues a series you previously enjoyed.

---

## Technical stack

Use:

- Go
- `net/http`
- `html/template`
- `encoding/json`
- `embed`
- JSON file storage
- Docker

The application should run as:

- One binary
- One container
- One persistent data file
- No separate database
- No frontend framework
- No background worker

---

## Suggested structure

```text
nextleaf/
├── cmd/nextleaf/main.go
├── internal/hardcover/
├── internal/picker/
├── internal/storage/
├── internal/web/
├── web/templates/
├── web/static/
├── go.mod
├── Dockerfile
└── compose.yaml
```

Keep Hardcover integration, scoring, storage, and HTTP handling separate.

---

## Minimal routes

```text
GET  /
POST /recommend
POST /choose
POST /not-now
POST /exclude
POST /series/pause
POST /series/resume
POST /sync
GET  /healthz
```

Use regular HTML forms and server-rendered pages.

---

## Build order

1. Import Hardcover data.
2. Store books and reading history locally.
3. Implement scoring and weighted selection.
4. Add series continuation and pause logic.
5. Add recommendation explanations.
6. Add the minimal web interface.
7. Add Docker packaging.
8. Test the picker with repeated simulations.

---

## MVP acceptance criteria

The MVP is complete when:

- Hardcover data can be imported
- One recommendation can be generated
- Recent genre streaks reduce similar recommendations
- Fiction and nonfiction streaks are interrupted over time
- Older Want to Read books are more likely than newly added books
- Active series can continue
- Long series streaks gradually encourage a break
- Paused series can return later
- Every recommendation includes a reason
- The user can choose, reroll, postpone, or exclude a book
- State survives restarts
- The app runs as one Go binary or container

---

## Non-goals

Do not include in the MVP:

- External book discovery
- AI recommendations
- Automatic classic detection
- Reading-session tracking
- Energy or mood tracking
- Notifications
- Social features
- Multi-user support
- Machine learning
- Complex dashboards
- PostgreSQL
- Redis
- A JavaScript frontend framework

---

## Guiding principle

> Nextleaf should make it easier to choose a more varied next book from the books the user already intends to read.
