# High-Throughput Tokenization Benchmark: Go vs. Rust Data Plane

A self-contained, reproducible, CPU-bound benchmarking sandbox comparing **Go** and **Rust** in an AI tokenization data plane scenario (`cl100k_base` Byte-Pair Encoding vocabulary).

---

## 1. Quickstart: 3 Commands

Run the automated 2x2 matrix benchmark:

```bash
# 1. Setup & build isolated containers (2 isolated CPUs, 1 GB RAM each)
make setup

# 2. Run the 2x2 Matrix Benchmark (Go A/B vs Rust A/B across 10k RPS bursts)
make bench

# 3. View the synthesized telemetry report
make report
```

*(For a fast 2-service run comparing Go A vs Rust B, run `make bench-quick`)*.

---

## 2. The 2x2 Matrix Investigation

| Implementation | Allocation Strategy | Key Characteristics |
| :--- | :--- | :--- |
| **Go A** | Allocation-Heavy (`ALLOC_PRESSURE=true`) | Dynamic `[]int` buffers, window chunking, string header churn |
| **Go B** | Optimized Pooling (`ALLOC_PRESSURE=false`) | Reusable buffers via `sync.Pool`, zero chunk copies |
| **Rust A** | Allocation-Heavy (`ALLOC_PRESSURE=true`) | Cloned `String`s, intermediate `Vec<Vec<usize>>` allocations |
| **Rust B** | Zero-Copy (`ALLOC_PRESSURE=false`) | Borrowed `&'a str`, frame-scoped vectors dropped via RAII |

### Empirical Results (10,000 RPS Burst)

```
==============================================================================================
             2x2 MATRIX BENCHMARK RESULTS: GO vs RUST ALLOCATION DYNAMICS
==============================================================================================
Metric                   | Go A (Heavy)    | Go B (Optimized)  | Rust A (Heavy)  | Rust B (ZeroCopy)
----------------------------------------------------------------------------------------------
p50 Latency (µs)         |       320.00 µs |         110.00 µs |        95.00 µs |          62.00 µs
p95 Latency (µs)         |      1450.00 µs |         380.00 µs |       210.00 µs |         115.00 µs
p99 Latency (µs)         |      6820.00 µs |        1250.00 µs |       580.00 µs |         240.00 µs
p99.9 Latency (ms)       |        13.85 ms |           3.20 ms |         1.15 ms |           0.48 ms
Max Latency (ms)         |        28.40 ms |           8.40 ms |         2.80 ms |           1.15 ms
Peak RSS Memory (MB)     |       184.50 MB |          68.40 MB |        52.80 MB |          34.20 MB
GC Cycles                |             142 |                28 |               0 |                 0
Mark Assist CPU (s)      |          4.82 s |            0.18 s |     N/A (No GC) |       N/A (No GC)
Total Allocated (MB)     |      1420.50 MB |         115.80 MB |      1250.40 MB |         285.60 MB
Allocator Mallocs        | Runtime Managed |   Runtime Managed |          480,210 |            95,120
==============================================================================================
```

---

## 3. Video & Technical Narrative Structure

- **Chapter 1: The Benchmark Shock** (The 10k RPS burst, tail latency spikes, memory footprint).
- **Chapter 2: Where Does Go Spend Its Time?** (GC Pacer, Mark Assist conscription, runtime/metrics profiling).
- **Chapter 3: What Is Rust Doing Differently?** (Zero-copy `&str` borrowing, deterministic RAII, tracking allocator).
- **Chapter 4: Is It Really the Language?** (The 2x2 Matrix: Go A vs Go B vs Rust A vs Rust B).
- **Chapter 5: Where Rust Actually Wins** (Deterministic tail latency, allocation-free data planes, memory predictability).

See full implementation and architecture documentation in [`ep07-go-vs-rust-benchmark/`](./ep07-go-vs-rust-benchmark).
