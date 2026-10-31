.PHONY: help setup bench report clean go-dev rust-dev test

SHELL := /bin/bash
PYTHON ?= $(shell command -v python3 2>/dev/null || command -v python 2>/dev/null || echo python3)
DOCKER_COMPOSE ?= $(shell docker compose version >/dev/null 2>&1 && echo "docker compose" || echo "docker-compose")

help:
	@echo "================================================================================"
	@echo "  High-Throughput Go vs Rust Tokenization Benchmark (cl100k_base Data Plane)"
	@echo "================================================================================"
	@echo "  make setup    - Build isolated Docker containers (pinned to 2 CPUs, 1GB RAM)"
	@echo "  make bench    - Execute end-to-end benchmark pipeline (Go vs Rust via k6)"
	@echo "  make report   - Print telemetry comparison table from manifests/telemetry.json"
	@echo "  make clean    - Tear down containers and clean up temporary metrics files"
	@echo "  make go-dev   - Run Go server locally on port 8080 (ALLOC_PRESSURE=true)"
	@echo "  make rust-dev - Run Rust server locally on port 8081 (Release build)"
	@echo "================================================================================"

setup:
	@echo ">>> [1/2] Verifying runtime dependencies..."
	@which docker >/dev/null 2>&1 || (echo "ERROR: docker is required but not installed." && exit 1)
	@echo ">>> [2/2] Building Go and Rust benchmark containers with resource pinning..."
	@$(DOCKER_COMPOSE) build
	@mkdir -p manifests
	@echo ">>> Setup completed successfully."

bench:
	@echo ">>> Launching automated Go vs Rust tokenization benchmark harness..."
	@chmod +x scripts/run_benchmarks.sh
	@./scripts/run_benchmarks.sh

report:
	@$(PYTHON) scripts/extract_telemetry.py

clean:
	@echo ">>> Tearing down benchmark containers and cleaning artifacts..."
	@$(DOCKER_COMPOSE) down --remove-orphans 2>/dev/null || true
	@rm -f manifests/go_summary.json manifests/rust_summary.json manifests/go_stats.json manifests/rust_stats.json
	@echo ">>> Clean completed."

go-dev:
	@echo ">>> Starting Go server locally on :8080..."
	@cd go-server && ALLOC_PRESSURE=true GOGC=100 go run main.go

rust-dev:
	@echo ">>> Starting Rust server locally on :8081..."
	@cd rust-server && cargo run --release
