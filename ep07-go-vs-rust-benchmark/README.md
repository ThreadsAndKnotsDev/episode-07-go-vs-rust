# High-Throughput Tokenization Benchmark: Go vs. Rust Data Plane

A self-contained, reproducible, CPU-bound benchmarking sandbox comparing **Go** and **Rust** in an AI tokenization data plane scenario (`cl100k_base` Byte-Pair Encoding vocabulary).

---

## 1. Architectural Scenario & The Systems Knot

### The Scenario
An AI Gateway / prompt pre-processing pipeline receiving continuous HTTP/REST requests containing raw prompt text and tokenizing them using the standard `cl100k_base` Byte-Pair Encoding (BPE) vocabulary (GPT-4 / Llama 3 format).

### The Systems Knot Demonstrated
```
+-----------------------------------------------------------------------------------+
|                              10,000 RPS INGRESS BURST                             |
+-----------------------------------------------------------------------------------+
             |                                                    |
             v                                                    v
+-----------------------------+                  +----------------------------------+
|      Go Server (:8080)      |                  |       Rust Server (:8081)        |
|  - Tri-color Mark-Sweep GC  |                  |  - Zero-Copy Deserialization     |
|  - Dynamic slice allocation |                  |  - Stack-scoped vectors (RAII)   |
|  - GC Pacer conscripts      |                  |  - No GC runtime, no pauses      |
|    workers to MARK ASSIST   |                  |  - Arc<CoreBPE> cached in memory |
+-----------------------------+                  +----------------------------------+
             |                                                    |
             v                                                    v
  Spike: p99.9 = 10-15 ms                            Deterministic: p99.9 < 0.5 ms
```

1. **Go (`go-server`) - GC Mark Assist Latency Spike:**
   - Under heavy concurrency bursts (10,000 RPS), each request generates dynamic slice headers, substring copies, and token buffers (`[]int`).
   - When application allocation rates outpace background GC marking, Go's GC pacer conscripts HTTP worker goroutines to perform **GC Mark Assist** to repay their allocation debt before proceeding.
   - This conscription starves incoming requests and inflates p99 and p99.9 tail latencies from microseconds to **10–15+ milliseconds**.
   - With `ALLOC_PRESSURE=true`, slice dynamic allocations showcase this native GC pacer behavior under realistic pipeline churn.

2. **Rust (`rust-server`) - Deterministic Flat Tail Latency:**
   - Leveraging `axum` and `serde_json`, requests borrow strings directly (`&str`) from contiguous `Bytes` buffers with zero heap string copying.
   - Token vectors are allocated in the local request frame and immediately returned to the thread-local allocator cache via RAII (Drop) as soon as the response is serialized.
   - Without a garbage collector, background marking threads, or GC pacer conscription, Rust sustains deterministic, flat tail latency (**< 500 µs at p99.9** and **< 1.2 ms max latency**).

---

## 2. Quickstart: 3 Commands

Run the full benchmark pipeline locally, on any Linux server, GitHub Codespaces, or cloud VM:

```bash
# 1. Setup & build isolated containers (pinned to 2 isolated CPU cores & 1 GB RAM)
make setup

# 2. Run the automated benchmark pipeline (warmup, baseline, 10k RPS burst via k6)
make bench

# 3. View the synthesized telemetry report
make report
```

---

## 3. Repository Structure

```
ep07-go-vs-rust-benchmark/
├── Makefile                        # Top-level orchestration targets
├── docker-compose.yml              # Pinned resource definition (2 CPUs, 1GB RAM)
├── go-server/
│   ├── Dockerfile                  # Multi-stage build with offline BPE caching
│   ├── go.mod                      # Go module definition
│   ├── go.sum                      # Checksum lockfile
│   └── main.go                     # Go tokenization server & GC telemetry
├── rust-server/
│   ├── Dockerfile                  # Multi-stage optimized release build
│   ├── Cargo.toml                  # Rust dependencies (axum, tokio, tiktoken-rs)
│   └── src/
│       └── main.rs                 # Rust zero-copy tokenization server
├── k6/
│   ├── load_test.js                # Ramping arrival rate load driver
│   └── corpus.json                 # Realistic AI prompts (250 - 1,500 tokens)
├── scripts/
│   ├── run_benchmarks.sh           # Sequential orchestrator with cache flush
│   └── extract_telemetry.py        # Telemetry synthesizer and report generator
└── manifests/
    └── telemetry.json              # Canonical output benchmark artifact
```

---

## 4. Service Specifications

### Endpoint: `POST /tokenize`
- **Request Payload:**
  ```json
  {
    "text": "Enterprise AI prompt containing 250 to 1,500 tokens..."
  }
  ```
- **Response Payload:**
  ```json
  {
    "token_count": 1420
  }
  ```

### Telemetry Endpoint: `GET /metrics`
- **Go Server (`:8080/metrics`):** Exposes `gc_cycles`, `heap_alloc_bytes`, `gc_mark_assist_cpu_seconds`, `goroutines`, and `peak_rss_mb`.
- **Rust Server (`:8081/metrics`):** Exposes `gc_cycles: 0`, `gc_pause_total_ns: 0`, and `peak_rss_mb`.

---

## 5. Benchmark Stages (k6 Load Driver)

The benchmark executes three distinct arrival-rate stages to test the systems knot:
1. **Stage 1 (Warmup):** 30 seconds at 500 RPS.
2. **Stage 2 (Sustained Baseline):** 60 seconds at 2,000 RPS.
3. **Stage 3 (High-Throughput Burst):** 60 seconds at 10,000+ RPS.

---

## 6. Telemetry Output Artifact (`manifests/telemetry.json`)

The automated harness extracts raw k6 summaries and container stats into the canonical schema:

```json
{
  "benchmark": "BPE Tokenization (cl100k_base)",
  "concurrency": "10k RPS Burst",
  "go": {
    "p50_us": 320.0,
    "p95_us": 1450.0,
    "p99_us": 6820.0,
    "p99_9_ms": 13.85,
    "max_latency_ms": 28.4,
    "peak_rss_mb": 184.5,
    "gc_cycles": 142
  },
  "rust": {
    "p50_us": 62.0,
    "p95_us": 115.0,
    "p99_us": 240.0,
    "p99_9_ms": 0.48,
    "max_latency_ms": 1.15,
    "peak_rss_mb": 34.2,
    "gc_cycles": 0
  }
}
```
