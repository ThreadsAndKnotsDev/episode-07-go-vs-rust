# Build Stage
FROM rust:1.80-bookworm AS builder

WORKDIR /usr/src/app

# Pre-cache dependencies by copying Cargo files first
COPY Cargo.toml ./
RUN mkdir src && echo "fn main() {}" > src/main.rs
RUN cargo build --release || true
RUN rm -rf src

# Copy real source code and build optimized release binary
COPY src ./src
RUN cargo build --release

# Runtime Stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /usr/src/app/target/release/rust-server /app/rust-server

ENV PORT=8081
ENV RUST_LOG=info

EXPOSE 8081

HEALTHCHECK --interval=2s --timeout=2s --start-period=3s --retries=5 \
    CMD curl -f http://localhost:8081/health || exit 1

ENTRYPOINT ["/app/rust-server"]
