#!/usr/bin/env bash
set -euo pipefail

# Determine script and project directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${PROJECT_DIR}"

MANIFESTS_DIR="${PROJECT_DIR}/manifests"
mkdir -p "${MANIFESTS_DIR}"

GO_SUMMARY="${MANIFESTS_DIR}/go_summary.json"
RUST_SUMMARY="${MANIFESTS_DIR}/rust_summary.json"
GO_STATS="${MANIFESTS_DIR}/go_stats.json"
RUST_STATS="${MANIFESTS_DIR}/rust_stats.json"

echo "================================================================================"
echo "    AI TOKENIZATION BENCHMARK: GO VS RUST DATA PLANE HARNESS"
echo "    Workload: cl100k_base BPE Tokenizer (GPT-4 / Llama 3 Format)"
echo "    Hardware Limit: 2 Pinned CPU Cores, 1 GB RAM per Container"
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

# Detect k6 runner (local binary vs containerized)
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

# ==============================================================================
# PHASE 1: BENCHMARK GO SERVER
# ==============================================================================
echo ""
echo "--------------------------------------------------------------------------------"
echo ">>> [Phase 1/2] Starting Go Server (Port 8080, ALLOC_PRESSURE=true, GOGC=100)..."
echo "--------------------------------------------------------------------------------"

${DOCKER_COMPOSE} down --remove-orphans >/dev/null 2>&1 || true
${DOCKER_COMPOSE} up -d --build go-server

wait_for_health "http://localhost:8080/health" "go-server"

echo ""
echo ">>> Executing k6 load test against Go server..."
RUN_K6 "http://localhost:8080/tokenize" "${GO_SUMMARY}"

echo ""
echo ">>> Capturing Go server operational telemetry & memory stats..."
# Capture docker memory stats
DOCKER_GO_STATS=$(docker stats --no-stream --format '{"mem_usage":"{{.MemUsage}}","cpu_perc":"{{.CPUPerc}}"}' benchmark-go-server 2>/dev/null || echo '{}')
# Query internal server telemetry
SERVER_GO_METRICS=$(curl -s http://localhost:8080/metrics 2>/dev/null || echo '{}')

cat <<EOF > "${GO_STATS}"
{
  "docker": ${DOCKER_GO_STATS},
  "metrics": ${SERVER_GO_METRICS}
}
EOF

echo "[Phase 1 Complete] Stopping Go Server..."
${DOCKER_COMPOSE} stop go-server >/dev/null 2>&1 || true

# Flush OS page cache to eliminate cross-benchmark caching noise (if permitted)
if [ "$(id -u)" -eq 0 ]; then
    echo ">>> Flushing kernel page cache..."
    sync
    echo 3 > /proc/sys/vm/drop_caches 2>/dev/null || true
fi
sleep 3

# ==============================================================================
# PHASE 2: BENCHMARK RUST SERVER
# ==============================================================================
echo ""
echo "--------------------------------------------------------------------------------"
echo ">>> [Phase 2/2] Starting Rust Server (Port 8081, Zero-Copy Tokio Runtime)..."
echo "--------------------------------------------------------------------------------"

${DOCKER_COMPOSE} up -d --build rust-server

wait_for_health "http://localhost:8081/health" "rust-server"

echo ""
echo ">>> Executing k6 load test against Rust server..."
RUN_K6 "http://localhost:8081/tokenize" "${RUST_SUMMARY}"

echo ""
echo ">>> Capturing Rust server operational telemetry & memory stats..."
# Capture docker memory stats
DOCKER_RUST_STATS=$(docker stats --no-stream --format '{"mem_usage":"{{.MemUsage}}","cpu_perc":"{{.CPUPerc}}"}' benchmark-rust-server 2>/dev/null || echo '{}')
# Query internal server telemetry
SERVER_RUST_METRICS=$(curl -s http://localhost:8081/metrics 2>/dev/null || echo '{}')

cat <<EOF > "${RUST_STATS}"
{
  "docker": ${DOCKER_RUST_STATS},
  "metrics": ${SERVER_RUST_METRICS}
}
EOF

echo "[Phase 2 Complete] Stopping Rust Server..."
${DOCKER_COMPOSE} stop rust-server >/dev/null 2>&1 || true

# ==============================================================================
# PHASE 3: EXTRACT AND SYNTHESIZE TELEMETRY
# ==============================================================================
echo ""
echo "--------------------------------------------------------------------------------"
echo ">>> Synthesizing Comparative Telemetry into manifests/telemetry.json..."
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
    --go-summary "${GO_SUMMARY}" \
    --rust-summary "${RUST_SUMMARY}" \
    --go-stats "${GO_STATS}" \
    --rust-stats "${RUST_STATS}" \
    --output "${MANIFESTS_DIR}/telemetry.json"

echo ""
echo ">>> Benchmark pipeline completed successfully!"
echo ">>> Inspect results at: ${MANIFESTS_DIR}/telemetry.json"

