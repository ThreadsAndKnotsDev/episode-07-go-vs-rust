# High-Throughput Tokenization Benchmark: Go vs. Rust Data Plane

A self-contained, reproducible, CPU-bound benchmarking sandbox comparing **Go** and **Rust** in an AI tokenization data plane scenario (`cl100k_base` Byte-Pair Encoding vocabulary).

---

## 1. Quickstart (Run in 3 Commands)

Execute the full automated benchmark pipeline:

```bash
# 1. Setup & build isolated containers (pinned to 2 isolated CPU cores & 1 GB RAM)
make setup

# 2. Run the automated benchmark pipeline (warmup, baseline, 10k RPS burst via k6)
make bench

# 3. View the synthesized telemetry report
make report
```

To run inside the project subdirectory directly:
```bash
cd ep07-go-vs-rust-benchmark
make setup
make bench
make report
```

---

## 2. Architectural Scenario & The Systems Knot

### Scenario
An AI Gateway / prompt pre-processing pipeline receiving continuous HTTP/REST requests containing raw prompt text and tokenizing them using the standard `cl100k_base` Byte-Pair Encoding (BPE) vocabulary (GPT-4 / Llama 3 format).

### The Systems Knot: Go GC Mark Assist vs. Rust RAII
- **Go (`go-server`):** High allocation churn from slice headers, sub-strings, and dynamic `[]uint` token buffers forces Go's concurrent tri-color mark-sweep garbage collector into **GC Mark Assist**. Under high concurrency bursts, worker goroutines are conscripted to pay off heap debt, causing p99/p99.9 tail latency to spike into multi-millisecond territory (~10–15 ms).
- **Rust (`rust-server`):** Zero-copy slice references (`&str`), stack allocation, RAII scoped drops, and a multi-threaded Tokio / Rayon async pipeline achieve flat deterministic tail latency under the identical load (< 500 µs at p99.9).

---

## 3. Telemetry Output (`manifests/telemetry.json`)

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

Detailed documentation and component implementations are in [`ep07-go-vs-rust-benchmark/`](./ep07-go-vs-rust-benchmark).
