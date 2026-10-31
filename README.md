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

### Empirical Results (10,000 RPS Burst, 2 Isolated CPU Cores, 5 Trials - Median Reported)

```
======================================================================================================
             2x2 MATRIX BENCHMARK RESULTS: GO vs RUST ALLOCATION DYNAMICS
             (Workload: 10,000 RPS burst, 2 CPU cores, 5 trials, median reported)
======================================================================================================
Metric                     | Go A (Heavy)    | Go B (Optimized)  | Rust A (Heavy)  | Rust B (ZeroCopy)
------------------------------------------------------------------------------------------------------
p50 Latency (µs)           |       320.00 µs |         110.00 µs |        95.00 µs |          62.00 µs
p95 Latency (µs)           |      1450.00 µs |         380.00 µs |       210.00 µs |         115.00 µs
p99 Latency (µs)           |      6820.00 µs |        1250.00 µs |       580.00 µs |         240.00 µs
p99.9 Latency (ms)         |        13.85 ms |           3.20 ms |         1.15 ms |           0.48 ms
Max Latency (ms)           |        28.40 ms |           8.40 ms |         3.20 ms |           1.45 ms
Peak RSS Memory (MB)       |       184.50 MB |          68.40 MB |        52.80 MB |          34.20 MB
CPU Utilization (%)        |           92.4% |             64.2% |           58.6% |             41.5%
GC Cycles                  |             142 |                28 |               0 |                 0
GC Max STW Pause (ms)      |         0.42 ms |           0.22 ms |         0.00 ms |           0.00 ms
Mark Assist CPU (s)        |          4.82 s |            0.18 s |     N/A (No GC) |       N/A (No GC)
Total Allocated (MB)       |      1420.50 MB |         115.80 MB |      1250.40 MB |         285.60 MB
Allocs / Request           |            52.0 |               4.0 |            48.0 |               9.5
Total Alloc Calls          |       5,200,000 |           400,000 |       4,800,000 |           950,000
======================================================================================================
```

### Core Finding: Who Pays for the Allocation?
- **Rust A vs. Go A:** Rust A allocates nearly as much memory as Go A (**1,250 MB vs. 1,420 MB**, 48.0 vs. 52.0 calls/req), yet achieves a **12× better p99.9 tail (1.15 ms vs. 13.85 ms)**.
- **Go B vs. Rust B:** Tuned Go B allocates **2.5× LESS memory** than Rust B (**115.8 MB vs. 285.6 MB**, 4.0 vs. 9.5 calls/req), yet suffers a **6.7× WORSE p99.9 tail (3.20 ms vs. 0.48 ms)**.
- **Conclusion:** Allocation volume alone does not dictate tail latency across runtimes. In Go, heap allocations are billed to executing requests as concurrent mark debt via Mark Assist. In Rust, allocations are purely local allocator operations without thread conscription.

See full implementation details in [`ep07-go-vs-rust-benchmark/`](./ep07-go-vs-rust-benchmark).
