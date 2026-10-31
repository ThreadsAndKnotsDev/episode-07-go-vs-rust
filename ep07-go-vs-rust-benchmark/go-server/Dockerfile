# Build Stage
FROM golang:1.23-bookworm AS builder

WORKDIR /app

# Download dependencies first to leverage Docker layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY main.go ./

# Compile statically linked Go binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /go-server .

# Preload cl100k_base vocabulary so container is self-contained and offline-ready
ENV TIKTOKEN_CACHE_DIR=/cache/tiktoken
RUN mkdir -p /cache/tiktoken && /go-server --warmup

# Runtime Stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binary and preloaded BPE vocabulary cache
COPY --from=builder /go-server /app/go-server
COPY --from=builder /cache/tiktoken /root/.cache/tiktoken

ENV PORT=8080
ENV ALLOC_PRESSURE=true
ENV TIKTOKEN_CACHE_DIR=/root/.cache/tiktoken

EXPOSE 8080

HEALTHCHECK --interval=2s --timeout=2s --start-period=3s --retries=5 \
    CMD curl -f http://localhost:8080/health || exit 1

ENTRYPOINT ["/app/go-server"]
