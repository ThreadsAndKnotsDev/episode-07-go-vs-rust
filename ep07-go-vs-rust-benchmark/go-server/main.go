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
	GCCycles          uint32  `json:"gc_cycles"`
	HeapAllocBytes    uint64  `json:"heap_alloc_bytes"`
	TotalAllocBytes   uint64  `json:"total_alloc_bytes"`
	HeapSysBytes      uint64  `json:"heap_sys_bytes"`
	GCPauseTotalNs    uint64  `json:"gc_pause_total_ns"`
	GCCPUFraction     float64 `json:"gc_cpu_fraction"`
	GCMarkAssistSec   float64 `json:"gc_mark_assist_cpu_seconds"`
	Goroutines        int     `json:"goroutines"`
	PeakRSSMB         float64 `json:"peak_rss_mb"`
	AllocPressure     bool    `json:"alloc_pressure"`
	TotalRequests     uint64  `json:"total_requests"`
	TotalTokens       uint64  `json:"total_tokens"`
	ServerUptimeSec   float64 `json:"uptime_seconds"`
}

var (
	tokenizer     *tiktoken.Tiktoken
	allocPressure = true
	startTime     = time.Now()

	// Metrics counters
	metricsMutex  sync.RWMutex
	totalRequests uint64
	totalTokens   uint64

	// Slice buffer pool used when ALLOC_PRESSURE=false
	tokenPool = sync.Pool{
		New: func() any {
			b := make([]int, 0, 2048)
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
// Falls back to runtime.MemStats Sys if running on non-Linux hosts.
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

// readGCMarkAssistSeconds queries Go 1.20+ runtime/metrics for GC mark assist CPU time.
func readGCMarkAssistSeconds() float64 {
	const metricName = "/cpu/classes/gc/mark/assist:cpu-seconds"
	sample := make([]metrics.Sample, 1)
	sample[0].Name = metricName
	metrics.Read(sample)
	if sample[0].Value.Kind() == metrics.KindFloat64 {
		return sample[0].Value.Float64()
	}
	return 0.0
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

	// Systems Knot demonstration:
	// When ALLOC_PRESSURE=true, we intentionally generate realistic AI gateway pipeline
	// allocation churn: dynamic slice reallocation, intermediate token copying,
	// window extraction, and string manipulation without memory pooling.
	// Under high RPS (2k-10k+ burst), this continuous allocation rate forces Go's
	// concurrent tri-color GC into "Mark Assist", conscripting HTTP worker goroutines
	// to pay off heap debt and driving p99/p99.9 latency to multi-millisecond spikes.
	if allocPressure {
		// 1. Dynamic buffer allocation for token transformation
		dynamicBuf := make([]int, tokenCount)
		copy(dynamicBuf, tokens)

		// 2. Simulate sliding-window chunking / prompt pre-processing
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
			// Keep dynamic allocation references alive to ensure heap mark visibility
			if len(windows) > 0 && len(windows[0]) > 0 {
				dynamicBuf[0] ^= windows[0][0]
			}
		}

		// 3. Dynamic header churn: substring allocations
		if len(req.Text) > 64 {
			prefixCopy := strings.Clone(req.Text[:64])
			_ = prefixCopy
		}
	} else {
		// Pooled allocation mode
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

	metricsMutex.RLock()
	reqs := totalRequests
	toks := totalTokens
	metricsMutex.RUnlock()

	resp := MetricsResponse{
		GCCycles:          m.NumGC,
		HeapAllocBytes:    m.HeapAlloc,
		TotalAllocBytes:   m.TotalAlloc,
		HeapSysBytes:      m.HeapSys,
		GCPauseTotalNs:    m.PauseTotalNs,
		GCCPUFraction:     m.GCCPUFraction,
		GCMarkAssistSec:   readGCMarkAssistSeconds(),
		Goroutines:        runtime.NumGoroutine(),
		PeakRSSMB:         readRSSMB(m.Sys),
		AllocPressure:     allocPressure,
		TotalRequests:     reqs,
		TotalTokens:       toks,
		ServerUptimeSec:   time.Since(startTime).Seconds(),
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

	// Handle cache directory if specified
	if cacheDir := os.Getenv("TIKTOKEN_CACHE_DIR"); cacheDir != "" {
		_ = os.MkdirAll(cacheDir, 0755)
	}

	log.Printf("[go-server] Initializing cl100k_base BPE vocabulary...")
	if err := initTokenizer(); err != nil {
		log.Fatalf("[go-server] Fatal: %v", err)
	}
	log.Printf("[go-server] cl100k_base loaded successfully.")

	if *warmupFlag || *preloadFlag {
		log.Printf("[go-server] Warmup/preload completed successfully. Exiting.")
		os.Exit(0)
	}

	// Determine port
	port := "8080"
	if *portFlag != "" {
		port = *portFlag
	} else if envPort := os.Getenv("PORT"); envPort != "" {
		port = envPort
	}

	// Determine ALLOC_PRESSURE configuration
	if envAlloc := os.Getenv("ALLOC_PRESSURE"); envAlloc != "" {
		allocPressure = (strings.ToLower(envAlloc) == "true" || envAlloc == "1")
	}

	log.Printf("[go-server] Configuration: PORT=%s, ALLOC_PRESSURE=%t, GOGC=%s, GOMAXPROCS=%d",
		port, allocPressure, os.Getenv("GOGC"), runtime.GOMAXPROCS(0))

	mux := http.NewServeMux()
	mux.HandleFunc("/tokenize", handleTokenize)
	mux.HandleFunc("/metrics", handleMetrics)
	mux.HandleFunc("/stats", handleMetrics)
	mux.HandleFunc("/health", handleHealth)

	// Mount default pprof handlers on mux
	mux.HandleFunc("/debug/pprof/", http.DefaultServeMux.ServeHTTP)

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Graceful shutdown handling
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
