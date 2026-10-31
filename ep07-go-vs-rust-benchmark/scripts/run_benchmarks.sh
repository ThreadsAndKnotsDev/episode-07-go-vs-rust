#!/usr/bin/env bash
set -euo pipefail

# Determine script and project directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${PROJECT_DIR}"

MANIFESTS_DIR="${PROJECT_DIR}/manifests"
mkdir -p "${MANIFESTS_DIR}"

RUN_MATRIX="${RUN_MATRIX:-true}"

echo "================================================================================"
echo "    AI TOKENIZATION BENCHMARK: 2x2 MATRIX INVESTIGATION HARNESS"
echo "    Workload: cl100k_base BPE Tokenizer (GPT-4 / Llama 3 Format)"
echo "    Hardware Limit: 2 Pinned CPU Cores, 1 GB RAM per Container"
echo "    Matrix: [Go A, Go B] vs [Rust A, Rust B]"
echo "================================================================================"

# Resolve Docker Compose command
if docker compose version >/dev/null 2>&1; then
    DOCKER_COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
    DOCKER_COMPOSE="docker-compose"
else
    echo "ERROR: Neither 'docker compose' nor 'docker-compose' was found."
    exit 1
fi

RUN_K6() {
    local target_url="$1"
    local summary_file="$2"

    if command -v k6 >/dev/null 2>&1; then
        echo "[k6] Using local k6 binary..."
        TARGET_URL="${target_url}" k6 run \
            --summary-export="${summary_file}" \
            "${PROJECT_DIR}/k6/load_test.js"
    else
        echo "[k6] Local k6 not found. Running official container grafana/k6:latest..."
        docker run --rm \
            --network host \
            -v "${PROJECT_DIR}/k6:/scripts:ro" \
            -v "${MANIFESTS_DIR}:/manifests:rw" \
            -e TARGET_URL="${target_url}" \
            -e WARMUP_DURATION="${WARMUP_DURATION:-30s}" \
            -e BASELINE_DURATION="${BASELINE_DURATION:-60s}" \
            -e BURST_DURATION="${BURST_DURATION:-60s}" \
            -e WARMUP_RPS="${WARMUP_RPS:-500}" \
            -e BASELINE_RPS="${BASELINE_RPS:-2000}" \
            -e BURST_RPS="${BURST_RPS:-10000}" \
            grafana/k6:latest run \
            --summary-export="/manifests/$(basename "${summary_file}")" \
            /scripts/load_test.js
    fi
}

wait_for_health() {
    local url="$1"
    local service_name="$2"
    local max_retries=30
    local count=0

    echo "[Healthcheck] Probing ${service_name} at ${url}..."
    until curl -s -f -o /dev/null "${url}" || [ ${count} -eq ${max_retries} ]; do
        sleep 1
        count=$((count + 1))
        printf "."
    done
    echo ""

    if [ ${count} -eq ${max_retries} ]; then
        echo "ERROR: ${service_name} failed to become healthy at ${url} within ${max_retries} seconds."
        ${DOCKER_COMPOSE} logs "${service_name}"
        exit 1
    fi
    echo "[Healthcheck] ${service_name} is UP and ready."
}

flush_system_cache() {
    if [ "$(id -u)" -eq 0 ]; then
        echo ">>> Flushing kernel page cache..."
        sync
        echo 3 > /proc/sys/vm/drop_caches 2>/dev/null || true
    fi
    sleep 3
}

benchmark_service() {
    local service_name="$1"
    local container_name="$2"
    local port="$3"
    local summary_file="$4"
    local stats_file="$5"
    local description="$6"

    echo ""
    echo "================================================================================"
    echo ">>> Running Benchmark: ${description} (Container: ${container_name})"
    echo "================================================================================"

    ${DOCKER_COMPOSE} down --remove-orphans >/dev/null 2>&1 || true
    ${DOCKER_COMPOSE} up -d --build "${service_name}"

    wait_for_health "http://localhost:${port}/health" "${service_name}"

    echo ">>> Executing k6 load test (500 -> 2,000 -> 10,000 RPS)..."
    RUN_K6 "http://localhost:${port}/tokenize" "${summary_file}"

    echo ">>> Capturing telemetry and memory stats..."
    DOCKER_STATS=$(docker stats --no-stream --format '{"mem_usage":"{{.MemUsage}}","cpu_perc":"{{.CPUPerc}}"}' "${container_name}" 2>/dev/null || echo '{}')
    SERVER_METRICS=$(curl -s "http://localhost:${port}/metrics" 2>/dev/null || echo '{}')

    cat <<EOF > "${stats_file}"
{
  "docker": ${DOCKER_STATS},
  "metrics": ${SERVER_METRICS}
}
EOF

    echo "[Done] Stopping ${service_name}..."
    ${DOCKER_COMPOSE} stop "${service_name}" >/dev/null 2>&1 || true
    flush_system_cache
}

# ==============================================================================
# EXECUTE BENCHMARKS
# ==============================================================================

# 1. Go A: Allocation-Heavy
benchmark_service "go-server-a" "benchmark-go-server-a" "8080" \
    "${MANIFESTS_DIR}/go_a_summary.json" "${MANIFESTS_DIR}/go_a_stats.json" \
    "Go A: Allocation-Heavy (Dynamic Churn, GOGC=100)"

if [ "${RUN_MATRIX}" = "true" ]; then
    # 2. Go B: Optimized Buffer Reuse
    benchmark_service "go-server-b" "benchmark-go-server-b" "8080" \
        "${MANIFESTS_DIR}/go_b_summary.json" "${MANIFESTS_DIR}/go_b_stats.json" \
        "Go B: Optimized (sync.Pool Buffer Reuse)"

    # 3. Rust A: Allocation-Heavy
    benchmark_service "rust-server-a" "benchmark-rust-server-a" "8081" \
        "${MANIFESTS_DIR}/rust_a_summary.json" "${MANIFESTS_DIR}/rust_a_stats.json" \
        "Rust A: Allocation-Heavy (Cloned Strings & Dynamic Vectors)"
fi

# 4. Rust B: Zero-Copy
benchmark_service "rust-server-b" "benchmark-rust-server-b" "8081" \
    "${MANIFESTS_DIR}/rust_b_summary.json" "${MANIFESTS_DIR}/rust_b_stats.json" \
    "Rust B: Zero-Copy (Borrowed &'a str, Scoped Drops)"

# Maintain symlinks / copies for backward compatibility
cp "${MANIFESTS_DIR}/go_a_summary.json" "${MANIFESTS_DIR}/go_summary.json"
cp "${MANIFESTS_DIR}/go_a_stats.json" "${MANIFESTS_DIR}/go_stats.json"
cp "${MANIFESTS_DIR}/rust_b_summary.json" "${MANIFESTS_DIR}/rust_summary.json"
cp "${MANIFESTS_DIR}/rust_b_stats.json" "${MANIFESTS_DIR}/rust_stats.json"

# ==============================================================================
# SYNTHESIZE TELEMETRY MATRIX
# ==============================================================================
echo ""
echo "--------------------------------------------------------------------------------"
echo ">>> Synthesizing 2x2 Telemetry Matrix into manifests/telemetry.json..."
echo "--------------------------------------------------------------------------------"

PYTHON_BIN="python3"
if ! command -v python3 >/dev/null 2>&1; then
    if command -v python >/dev/null 2>&1; then
        PYTHON_BIN="python"
    else
        echo "ERROR: python3 or python not found."
        exit 1
    fi
fi

${PYTHON_BIN} "${PROJECT_DIR}/scripts/extract_telemetry.py" \
    --go-summary "${MANIFESTS_DIR}/go_summary.json" \
    --rust-summary "${MANIFESTS_DIR}/rust_summary.json" \
    --go-stats "${MANIFESTS_DIR}/go_stats.json" \
    --rust-stats "${MANIFESTS_DIR}/rust_stats.json" \
    --output "${MANIFESTS_DIR}/telemetry.json"

echo ""
echo ">>> Benchmark pipeline completed successfully!"
echo ">>> Canonical output: ${MANIFESTS_DIR}/telemetry.json"
