# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off yields a static binary that runs on scratch with no libc.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nextleaf ./cmd/nextleaf

# The series database lives here. Scratch has no shell to prepare it, so the
# directory is built with the right ownership and copied in.
RUN mkdir -p /data && chown 65532:65532 /data

FROM scratch
WORKDIR /app
# The CA bundle is the one thing scratch can't provide for outbound HTTPS.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/nextleaf /app/nextleaf
COPY --from=build --chown=65532:65532 /data /data

ENV ADDR=:8080
# Series tracking is stateful; mount a volume here or decisions are lost when
# the container is replaced.
ENV DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080

# Numeric UID:GID so non-root works without an /etc/passwd on scratch.
USER 65532:65532

ENTRYPOINT ["/app/nextleaf"]
