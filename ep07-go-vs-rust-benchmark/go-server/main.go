package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkoukk/tiktoken-go"
)

// TokenizeRequest represents the incoming AI prompt payload.
type TokenizeRequest struct {
	Text string `json:"text"`
}

// TokenizeResponse represents the tokenization result returned to the client.
type TokenizeResponse struct {
	TokenCount int `json:"token_count"`
}

// MetricsResponse holds operational telemetry exposed to benchmark runners.
type MetricsResponse struct {
	// Standard GC Metrics
	GCCycles       uint32  `json:"gc_cycles"`
	HeapAllocBytes uint64  `json:"heap_alloc_bytes"`
	HeapSysBytes   uint64  `json:"heap_sys_bytes"`
	GCPauseTotalNs uint64  `json:"gc_pause_total_ns"`
	GCPauseMaxMs   float64 `json:"gc_pause_max_ms"`
	GCCPUFraction  float64 `json:"gc_cpu_fraction"`
	Goroutines     int     `json:"goroutines"`
	PeakRSSMB      float64 `json:"peak_rss_mb"`
	AllocPressure  bool    `json:"alloc_pressure"`

	// Granular Heap Object Allocation Telemetry
	AllocCount       uint64  `json:"alloc_count"`
	TotalAllocBytes  uint64  `json:"total_alloc_bytes"`
	AllocCallsPerReq float64 `json:"alloc_calls_per_req"`

	// Granular Go Runtime Instrumentation (Proving the Causal Chain)
	GCMarkAssistCPUSeconds float64 `json:"gc_mark_assist_cpu_seconds"`
	GCTotalCPUSeconds      float64 `json:"gc_total_cpu_seconds"`
	UserCPUSeconds         float64 `json:"user_cpu_seconds"`
	GCScanHeapBytes        uint64  `json:"gc_scan_heap_bytes"`
	GCHeapAllocsBytes      uint64  `json:"gc_heap_allocs_bytes"`
	GCHeapFreesBytes       uint64  `json:"gc_heap_frees_bytes"`

	// Throughput Telemetry
	TotalRequests   uint64  `json:"total_requests"`
	TotalTokens     uint64  `json:"total_tokens"`
	ServerUptimeSec float64 `json:"uptime_seconds"`
}

var (
	tokenizer     *tiktoken.Tiktoken
	allocPressure = true
	startTime     = time.Now()

	// Metrics counters
	metricsMutex  sync.RWMutex
	totalRequests uint64
	totalTokens   uint64

	// Slice buffer pool used when ALLOC_PRESSURE=false (Go B: Optimized)
	tokenPool = sync.Pool{
		New: func() any {
			b := make([]int, 0, 4096)
			return &b
		},
	}
)

func initTokenizer() error {
	var err error
	tokenizer, err = tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		return fmt.Errorf("failed to load cl100k_base tokenizer: %w", err)
	}
	return nil
}

// readRSSMB attempts to read Resident Set Size (RSS) in MB from Linux /proc/self/statm.
func readRSSMB(sysBytes uint64) float64 {
	statm, err := os.ReadFile("/proc/self/statm")
	if err == nil {
		fields := strings.Fields(string(statm))
		if len(fields) >= 2 {
			rssPages, err := strconv.ParseUint(fields[1], 10, 64)
			if err == nil {
				pageSize := uint64(os.Getpagesize())
				return float64(rssPages*pageSize) / (1024 * 1024)
			}
		}
	}
	// Fallback to Sys memory
	return float64(sysBytes) / (1024 * 1024)
}

// readRuntimeMetrics extracts detailed GC pacer and Mark Assist counters from runtime/metrics.
func readRuntimeMetrics() (markAssistSec, gcCPUSec, userCPUSec float64, scanHeapBytes, heapAllocsBytes, heapFreesBytes uint64) {
	const (
		metricMarkAssist = "/cpu/classes/gc/mark/assist:cpu-seconds"
		metricGCTotal    = "/cpu/classes/gc/total:cpu-seconds"
		metricUserCPU    = "/cpu/classes/user:cpu-seconds"
		metricScanHeap   = "/gc/scan/heap:bytes"
		metricHeapAllocs = "/gc/heap/allocs:bytes"
		metricHeapFrees  = "/gc/heap/frees:bytes"
	)

	samples := []metrics.Sample{
		{Name: metricMarkAssist},
		{Name: metricGCTotal},
		{Name: metricUserCPU},
		{Name: metricScanHeap},
		{Name: metricHeapAllocs},
		{Name: metricHeapFrees},
	}

	metrics.Read(samples)

	for _, sample := range samples {
		switch sample.Name {
		case metricMarkAssist:
			if sample.Value.Kind() == metrics.KindFloat64 {
				markAssistSec = sample.Value.Float64()
			}
		case metricGCTotal:
			if sample.Value.Kind() == metrics.KindFloat64 {
				gcCPUSec = sample.Value.Float64()
			}
		case metricUserCPU:
			if sample.Value.Kind() == metrics.KindFloat64 {
				userCPUSec = sample.Value.Float64()
			}
		case metricScanHeap:
			if sample.Value.Kind() == metrics.KindUint64 {
				scanHeapBytes = sample.Value.Uint64()
			}
		case metricHeapAllocs:
			if sample.Value.Kind() == metrics.KindUint64 {
				heapAllocsBytes = sample.Value.Uint64()
			}
		case metricHeapFrees:
			if sample.Value.Kind() == metrics.KindUint64 {
				heapFreesBytes = sample.Value.Uint64()
			}
		}
	}
	return
}

func handleTokenize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req TokenizeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	if req.Text == "" {
		http.Error(w, "Field 'text' must not be empty", http.StatusBadRequest)
		return
	}

	// Perform BPE tokenization using cl100k_base vocabulary
	tokens := tokenizer.Encode(req.Text, nil, nil)
	tokenCount := len(tokens)

	// =========================================================================
	// 2x2 Matrix Strategy:
	// Go A (ALLOC_PRESSURE=true): Allocation-heavy. Dynamic slice buffers,
	// window chunk copies, and substring headers. Under 10k RPS burst, the high
	// allocation rate triggers the GC pacer and conscripts worker goroutines
	// into GC Mark Assist mid-request.
	//
	// Go B (ALLOC_PRESSURE=false): Optimized, low-allocation. Reusable token
	// slice buffer from sync.Pool without intermediate chunk cloning.
	// =========================================================================
	if allocPressure {
		// 1. Dynamic buffer allocation on heap
		dynamicBuf := make([]int, tokenCount)
		copy(dynamicBuf, tokens)

		// 2. Intermediate window slicing simulation
		chunkSize := 128
		if chunkSize > tokenCount {
			chunkSize = tokenCount
		}
		if chunkSize > 0 {
			windows := make([][]int, 0, (tokenCount/chunkSize)+1)
			for i := 0; i < tokenCount; i += chunkSize {
				end := i + chunkSize
				if end > tokenCount {
					end = tokenCount
				}
				subSlice := make([]int, end-i)
				copy(subSlice, dynamicBuf[i:end])
				windows = append(windows, subSlice)
			}
			if len(windows) > 0 && len(windows[0]) > 0 {
				dynamicBuf[0] ^= windows[0][0]
			}
		}

		// 3. String header churn
		if len(req.Text) > 64 {
			prefixCopy := strings.Clone(req.Text[:64])
			_ = prefixCopy
		}
	} else {
		// Go B (Optimized): Reuse slice memory from sync.Pool
		bufPtr := tokenPool.Get().(*[]int)
		buf := (*bufPtr)[:0]
		buf = append(buf, tokens...)
		*bufPtr = buf
		tokenPool.Put(bufPtr)
	}

	// Update telemetry counters
	metricsMutex.Lock()
	totalRequests++
	totalTokens += uint64(tokenCount)
	metricsMutex.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(TokenizeResponse{
		TokenCount: tokenCount,
	})
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	markAssistSec, gcCPUSec, userCPUSec, scanHeapBytes, heapAllocsBytes, heapFreesBytes := readRuntimeMetrics()

	metricsMutex.RLock()
	reqs := totalRequests
	toks := totalTokens
	metricsMutex.RUnlock()

	var maxPauseNs uint64
	numPauses := int(m.NumGC)
	if numPauses > 256 {
		numPauses = 256
	}
	for i := 0; i < numPauses; i++ {
		if m.PauseNs[i] > maxPauseNs {
			maxPauseNs = m.PauseNs[i]
		}
	}

	callsPerReq := 0.0
	if reqs > 0 {
		callsPerReq = float64(m.Mallocs) / float64(reqs)
	}

	resp := MetricsResponse{
		GCCycles:               m.NumGC,
		HeapAllocBytes:         m.HeapAlloc,
		TotalAllocBytes:        m.TotalAlloc,
		AllocCount:             m.Mallocs,
		AllocCallsPerReq:       callsPerReq,
		HeapSysBytes:           m.HeapSys,
		GCPauseTotalNs:         m.PauseTotalNs,
		GCPauseMaxMs:           float64(maxPauseNs) / 1e6,
		GCCPUFraction:          m.GCCPUFraction,
		Goroutines:             runtime.NumGoroutine(),
		PeakRSSMB:              readRSSMB(m.Sys),
		AllocPressure:          allocPressure,
		GCMarkAssistCPUSeconds: markAssistSec,
		GCTotalCPUSeconds:      gcCPUSec,
		UserCPUSeconds:         userCPUSec,
		GCScanHeapBytes:        scanHeapBytes,
		GCHeapAllocsBytes:      heapAllocsBytes,
		GCHeapFreesBytes:       heapFreesBytes,
		TotalRequests:          reqs,
		TotalTokens:            toks,
		ServerUptimeSec:        time.Since(startTime).Seconds(),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","engine":"go-server","bpe":"cl100k_base"}`))
}

func main() {
	warmupFlag := flag.Bool("warmup", false, "Preload BPE vocabulary and exit")
	preloadFlag := flag.Bool("preload", false, "Alias for --warmup")
	portFlag := flag.String("port", "", "Server HTTP port")
	flag.Parse()

	if cacheDir := os.Getenv("TIKTOKEN_CACHE_DIR"); cacheDir != "" {
		_ = os.MkdirAll(cacheDir, 0755)
	}

	log.Printf("[go-server] Initializing cl100k_base BPE vocabulary...")
	if err := initTokenizer(); err != nil {
		log.Fatalf("[go-server] Fatal: %v", err)
	}
	log.Printf("[go-server] cl100k_base loaded successfully.")

	if *warmupFlag || *preloadFlag {
		log.Printf("[go-server] Preload completed successfully.")
		os.Exit(0)
	}

	port := "8080"
	if *portFlag != "" {
		port = *portFlag
	} else if envPort := os.Getenv("PORT"); envPort != "" {
		port = envPort
	}

	if envAlloc := os.Getenv("ALLOC_PRESSURE"); envAlloc != "" {
		allocPressure = (strings.ToLower(envAlloc) == "true" || envAlloc == "1")
	}

	strategy := "Go A (Allocation-Heavy / Dynamic Churn)"
	if !allocPressure {
		strategy = "Go B (Optimized / Buffer-Pool Reuse)"
	}

	log.Printf("[go-server] Strategy: %s", strategy)
	log.Printf("[go-server] Configuration: PORT=%s, ALLOC_PRESSURE=%t, GOGC=%s, GOMAXPROCS=%d",
		port, allocPressure, os.Getenv("GOGC"), runtime.GOMAXPROCS(0))

	mux := http.NewServeMux()
	mux.HandleFunc("/tokenize", handleTokenize)
	mux.HandleFunc("/metrics", handleMetrics)
	mux.HandleFunc("/stats", handleMetrics)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/debug/pprof/", http.DefaultServeMux.ServeHTTP)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("[go-server] Listening and serving HTTP on :%s", port)
		serverErrors <- server.ListenAndServe()
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("[go-server] Server error: %v", err)
		}
	case sig := <-shutdown:
		log.Printf("[go-server] Received shutdown signal %v. Gracefully stopping...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("[go-server] Graceful shutdown failed: %v", err)
			_ = server.Close()
		}
	}
	log.Printf("[go-server] Server stopped.")
}
