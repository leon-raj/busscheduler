# ─── Stage 1: build the Go binary ─────────────────────────────────────────────
FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o busplan-server ./cmd/busplan-server

# ─── Stage 2: runtime ─────────────────────────────────────────────────────────
# debian:bookworm-slim provides glibc and common libs that pre-built VROOM
# release binaries expect. Alpine's musl libc is incompatible with them.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    libssl3 \
    libcurl4 \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/busplan-server .

# ── VROOM binary (optional) ────────────────────────────────────────────────────
# Place your vroom binary at  bin/vroom  before running docker build.
# The directory must exist (bin/.gitkeep keeps it tracked); the binary is ignored by git.
# Without vroom, set SOLVER=greedy (test-only) or remove -solver flag entirely;
# planning will fail at runtime if SOLVER=vroom and the binary is missing.
COPY bin/ /tmp/ext/
RUN if [ -f /tmp/ext/vroom ]; then \
      cp /tmp/ext/vroom /usr/local/bin/vroom && chmod +x /usr/local/bin/vroom \
      && echo "vroom installed: $(/usr/local/bin/vroom --version 2>&1 | head -1)"; \
    else \
      echo "WARNING: bin/vroom not found. Set SOLVER=greedy or add the binary before building."; \
    fi \
 && rm -rf /tmp/ext

EXPOSE 8080
CMD ["./busplan-server"]
