# NextLeaf

A small self-hosted service that picks your next read from your
[Hardcover](https://hardcover.app) *Want to Read* list and/or the unread
books in your [Grimmory](https://github.com/grimmory-tools/grimmory) library.
It optimises for variety rather than similarity: it looks at what you've read
recently and weights the pick toward genres, authors and formats you've been
neglecting. With both sources configured, one pick draws on both, and a book
you've read or are reading in either is never recommended.

## Series

When you finish a book in a series, the next one is offered first, even if it
isn't on your reading list. On the recommendation you can:

- **Park for one book**: skip the series once. It comes back as soon as you
  finish anything else.
- **Drop this series**: no more continuations, and its books leave the pool.
  Adding one of its books back to your reading list undoes it.

The **Series** drawer lists every series you're in. Each row has **Pick this**
(read it next), **Park** and **Drop**, plus an undo. Series you're caught up
with sit under **Finished** and move back out when a new book appears. When a
series is known under several names, the ⇄ button lets you choose which one
the row follows.

Books that aren't out yet are never recommended. Novellas at half-positions
(book 3.5) are offered unless you turn them off.

![NextLeaf recommending a book in light mode](docs/screenshots/light.png)

![NextLeaf recommending a book in dark mode](docs/screenshots/dark.png)

![The series drawer](docs/screenshots/drawer.png)

## Configuration

Everything is configured through environment variables. In development a local
`.env` file is loaded automatically.

| Variable            | Default      | Description                                     |
| ------------------- | ------------ | ----------------------------------------------- |
| `HARDCOVER_TOKEN`   | *(optional)* | Hardcover API token.                            |
| `GRIMMORY_URL`      | *(optional)* | Base URL of a Grimmory instance.                |
| `GRIMMORY_USERNAME` | *(optional)* | Grimmory account username.                      |
| `GRIMMORY_PASSWORD` | *(optional)* | Grimmory account password (local login).        |
| `ADDR`              | `:8080`      | Address the server listens on.                  |
| `DATA_DIR`          | `.`          | Directory holding the series database.          |
| `INCLUDE_NOVELLAS`  | `true`       | Offer novellas at half-positions (book 3.5).    |

At least one source is needed; without one the app starts and shows a setup
hint. Grimmory needs all three of its variables. Use your own Grimmory account,
since read status is per user. If you normally sign in through OIDC, set a
local password on that account for NextLeaf to use.

## Deployment

Docker Compose is the recommended way to run it — the config lives in a file
you can keep in version control, and the container comes back up after a
reboot:

```yaml
services:
  nextleaf:
    image: ghcr.io/supergamer1337/nextleaf:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - nextleaf-data:/data
    environment:
      HARDCOVER_TOKEN: your-token
      # Or (also works alongside Hardcover):
      # GRIMMORY_URL: https://grimmory.example.com
      # GRIMMORY_USERNAME: your-user
      # GRIMMORY_PASSWORD: your-password

volumes:
  nextleaf-data:
```

```sh
docker compose up -d
```

A plain `docker run` works just as well:

```sh
docker run -d --name nextleaf --restart unless-stopped \
  -p 8080:8080 -e HARDCOVER_TOKEN=your-token \
  -v nextleaf-data:/data \
  ghcr.io/supergamer1337/nextleaf:latest
```

Either way the app is now at `http://localhost:8080`. Keep the volume: it holds
your series decisions, and without it they are lost whenever the container is
replaced. `/healthcheck` returns 200 when the server is up. Check it from
outside the container, as the image has no shell or curl inside.

## Development

Requires Go 1.26+. On Nix, `nix develop` gives you the toolchain.

```sh
echo 'HARDCOVER_TOKEN=your-token' > .env   # or export it
go run ./cmd/nextleaf                       # serves http://localhost:8080
```

Tests:

```sh
go test ./...
```
