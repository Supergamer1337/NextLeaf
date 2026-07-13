# NextLeaf

A small, self-hosted service that recommends what to read **next** from your
[Hardcover](https://hardcover.app) library — optimising for **variety, not
similarity**. Instead of feeding you another book just like the last one,
NextLeaf nudges you across genres, authors, and formats so your reading stays
varied, while still letting you continue a series you're enjoying.

It reads your *Want to Read* list and recent history, then serves a single,
weighted-random pick at `/`. Reroll for a fresh suggestion any time.

## Configuration

All configuration is via environment variables (a local `.env` file is loaded
automatically in development):

| Variable          | Required | Default | Description                                   |
| ----------------- | -------- | ------- | --------------------------------------------- |
| `HARDCOVER_TOKEN` | yes      | —       | Hardcover API token; enables the reading source. |
| `ADDR`            | no       | `:8080` | Address the server listens on.                |

Without `HARDCOVER_TOKEN`, the app still runs but the home page shows a setup hint.

## Running in production

A container image is published to GitHub Container Registry on every push to `main`:

```sh
docker run -d -p 8080:8080 -e HARDCOVER_TOKEN=your-token \
  ghcr.io/supergamer1337/nextleaf:latest
```

Then open `http://localhost:8080`. Point a health check at `/healthcheck`.

## Running in development

Requires Go 1.26+ (a Nix `devShell` with the toolchain is provided — `nix develop`).

```sh
echo 'HARDCOVER_TOKEN=your-token' > .env   # or export it
go run ./cmd/nextleaf                       # serves http://localhost:8080
```

Run the tests with:

```sh
go test ./...
```

## Notes

- Standard library only — no third-party runtime dependencies.
- The image is built `FROM scratch`: just the static binary and CA certificates.
