# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS build
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .

# CGO off yields a static binary that runs on scratch with no libc.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nextleaf ./cmd/nextleaf

FROM scratch
WORKDIR /app
# The CA bundle is the one thing scratch can't provide for outbound HTTPS.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/nextleaf /app/nextleaf

ENV ADDR=:8080
EXPOSE 8080

# Numeric UID:GID so non-root works without an /etc/passwd on scratch.
USER 65532:65532

ENTRYPOINT ["/app/nextleaf"]
