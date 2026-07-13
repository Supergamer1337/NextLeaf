# NextLeaf

A small self-hosted service that picks your next read from your
[Hardcover](https://hardcover.app) *Want to Read* list. The twist is that it
optimises for variety rather than similarity: it looks at what you've read
recently and weights the pick toward genres, authors and formats you've been
neglecting, so you don't end up reading the same kind of book five times in a
row. A series you're in the middle of still gets a fair shot.

## Configuration

Everything is configured through environment variables. In development a local
`.env` file is loaded automatically.

| Variable          | Default      | Description                        |
| ----------------- | ------------ | ---------------------------------- |
| `HARDCOVER_TOKEN` | *(required)* | Hardcover API token.               |
| `ADDR`            | `:8080`      | Address the server listens on.     |

Without a token the app still starts; the home page shows a setup hint instead
of a recommendation.

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
    environment:
      HARDCOVER_TOKEN: your-token
```

```sh
docker compose up -d
```

A plain `docker run` works just as well:

```sh
docker run -d --name nextleaf --restart unless-stopped \
  -p 8080:8080 -e HARDCOVER_TOKEN=your-token \
  ghcr.io/supergamer1337/nextleaf:latest
```

Either way the app is now at `http://localhost:8080`. There is no state to
persist, so no volumes are needed. `/healthcheck` returns 200 when the server
is up, which is handy for a reverse proxy or uptime monitor — but check it
from outside the container: the image is built `FROM scratch` (just the static
binary and CA certificates), so there is no shell or curl inside for a
Docker-level healthcheck to use.

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

For now, NextLeaf uses no external dependencies.
