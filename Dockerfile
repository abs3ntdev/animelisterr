# syntax=docker/dockerfile:1.7

# ---------- build stage ----------
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/animelisterr ./cmd/animelisterr

# ---------- runtime stage ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S app && adduser -S -G app app
COPY --from=build /out/animelisterr /usr/local/bin/animelisterr

# Migrations are embedded into the binary; no extra files to copy. DB
# defaults (localhost/postgres/postgres) are only useful for local
# development — production deployments must override DB_HOST + DB_PASS.
ENV HTTP_ADDR=":8080" \
    REFRESH_INTERVAL="1h"

USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/animelisterr"]
