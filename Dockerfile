# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS builder

WORKDIR /build

# Cache dependency downloads separately from the source build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /foxess-exporter ./cmd/exporter

# ── Final minimal image ──────────────────────────────────────────────────────
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /foxess-exporter /foxess-exporter

ENTRYPOINT ["/foxess-exporter"]
