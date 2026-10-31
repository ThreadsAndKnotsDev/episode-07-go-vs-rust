.PHONY: help setup bench bench-matrix bench-quick report clean go-dev rust-dev test

SHELL := /bin/bash
PYTHON ?= $(shell command -v python3 2>/dev/null || command -v python 2>/dev/null || echo python3)
DOCKER_COMPOSE ?= $(shell docker compose version >/dev/null 2>&1 && echo "docker compose" || echo "docker-compose")

help:
	@echo "================================================================================"
	@echo "  High-Throughput Go vs Rust Tokenization Benchmark (2x2 Matrix Harness)"
	@echo "================================================================================"
	@echo "  make setup        - Build isolated Docker containers (2 CPUs, 1GB RAM each)"
	@echo "  make bench        - Execute full 2x2 matrix benchmark (Go A/B vs Rust A/B)"
	@echo "  make bench-quick  - Fast comparison: Go A (Allocation-Heavy) vs Rust B (Zero-Copy)"
	@echo "  make report       - Print telemetry comparison table from manifests/telemetry.json"
	@echo "  make clean        - Tear down containers and clean up temporary metrics files"
	@echo "  make go-dev       - Run Go server locally on port 8080 (ALLOC_PRESSURE=true)"
	@echo "  make rust-dev     - Run Rust server locally on port 8081 (Release build)"
	@echo "================================================================================"

setup:
	@echo ">>> [1/2] Verifying runtime dependencies..."
	@which docker >/dev/null 2>&1 || (echo "ERROR: docker is required but not installed." && exit 1)
	@echo ">>> [2/2] Building Go and Rust benchmark containers with resource pinning..."
	@$(DOCKER_COMPOSE) build
	@mkdir -p manifests
	@echo ">>> Setup completed successfully."

bench: bench-matrix

bench-matrix:
	@echo ">>> Launching 2x2 Matrix Benchmark (Go A/B vs Rust A/B)..."
	@chmod +x scripts/run_benchmarks.sh
	@RUN_MATRIX=true ./scripts/run_benchmarks.sh

bench-quick:
	@echo ">>> Launching Quick Benchmark (Go A vs Rust B)..."
	@chmod +x scripts/run_benchmarks.sh
	@RUN_MATRIX=false ./scripts/run_benchmarks.sh

report:
	@$(PYTHON) scripts/extract_telemetry.py

clean:
	@echo ">>> Tearing down benchmark containers and cleaning artifacts..."
	@$(DOCKER_COMPOSE) down --remove-orphans 2>/dev/null || true
	@rm -f manifests/*_summary.json manifests/*_stats.json
	@echo ">>> Clean completed."

go-dev:
	@echo ">>> Starting Go server locally on :8080..."
	@cd go-server && ALLOC_PRESSURE=true GOGC=100 go run main.go

rust-dev:
	@echo ">>> Starting Rust server locally on :8081..."
	@cd rust-server && cargo run --release
