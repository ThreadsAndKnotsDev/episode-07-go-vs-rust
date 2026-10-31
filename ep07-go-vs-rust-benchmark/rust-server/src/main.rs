use axum::{
    body::Bytes,
    extract::State,
    http::StatusCode,
    response::IntoResponse,
    routing::{get, post},
    Json, Router,
};
use serde::{Deserialize, Serialize};
use std::{
    alloc::{GlobalAlloc, Layout, System},
    fs,
    net::SocketAddr,
    sync::{
        atomic::{AtomicU64, Ordering},
        Arc,
    },
    time::Instant,
};
use tiktoken_rs::{cl100k_base, CoreBPE};
use tower_http::trace::TraceLayer;
use tracing::info;
use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt};

// =============================================================================
// TRACKING ALLOCATOR (Empirical Allocation Measurement)
// Instruments every heap allocation and deallocation to replace speculation
// with exact, reproducible metrics.
// =============================================================================
struct TrackingAllocator;

static ALLOC_COUNT: AtomicU64 = AtomicU64::new(0);
static DEALLOC_COUNT: AtomicU64 = AtomicU64::new(0);
static ALLOC_BYTES: AtomicU64 = AtomicU64::new(0);
static DEALLOC_BYTES: AtomicU64 = AtomicU64::new(0);

unsafe impl GlobalAlloc for TrackingAllocator {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        let ptr = System.alloc(layout);
        if !ptr.is_null() {
            ALLOC_COUNT.fetch_add(1, Ordering::Relaxed);
            ALLOC_BYTES.fetch_add(layout.size() as u64, Ordering::Relaxed);
        }
        ptr
    }

    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        System.dealloc(ptr, layout);
        DEALLOC_COUNT.fetch_add(1, Ordering::Relaxed);
        DEALLOC_BYTES.fetch_add(layout.size() as u64, Ordering::Relaxed);
    }
}

#[global_allocator]
static GLOBAL: TrackingAllocator = TrackingAllocator;

/// Zero-copy deserialization of incoming prompt payload.
/// When ALLOC_PRESSURE=false (Rust B), the &'a str borrows directly from the contiguous
/// Bytes buffer, requiring zero string copying when the payload has no escaped characters.
#[derive(Deserialize)]
struct TokenizeRequest<'a> {
    #[serde(borrow)]
    text: &'a str,
}

/// Tokenization response payload.
#[derive(Serialize)]
struct TokenizeResponse {
    token_count: usize,
}

/// Operational telemetry exposed to the benchmarking harness.
#[derive(Serialize)]
struct MetricsResponse {
    // GC Metrics (always 0 in Rust)
    gc_cycles: u32,
    gc_pause_total_ns: u64,
    gc_pause_max_ms: f64,

    // Empirical Allocator Metrics (Direct Measurement)
    alloc_count: u64,
    dealloc_count: u64,
    alloc_calls_per_req: f64,
    total_allocated_bytes: u64,
    current_heap_bytes: u64,

    // System & Throughput Telemetry
    peak_rss_mb: f64,
    alloc_pressure: bool,
    strategy: String,
    total_requests: u64,
    uptime_seconds: f64,
}

/// Shared application state holding cached BPE vocabulary and configuration.
struct AppState {
    bpe: Arc<CoreBPE>,
    start_time: Instant,
    request_counter: AtomicU64,
    alloc_pressure: bool,
}

/// Read Resident Set Size (RSS) in MB from Linux /proc/self/statm.
fn read_rss_mb() -> f64 {
    if let Ok(content) = fs::read_to_string("/proc/self/statm") {
        let parts: Vec<&str> = content.split_whitespace().collect();
        if parts.len() >= 2 {
            if let Ok(rss_pages) = parts[1].parse::<u64>() {
                let page_size = 4096u64; // Standard 4KB Linux page
                return (rss_pages * page_size) as f64 / (1024.0 * 1024.0);
            }
        }
    }
    0.0
}

/// Tokenize handler testing the 2x2 Matrix Strategy:
/// - Rust A (ALLOC_PRESSURE=true): Allocation-heavy. Clones owned strings, allocates
///   intermediate sub-vectors, and produces heap churn to test allocator contention.
/// - Rust B (ALLOC_PRESSURE=false): Zero-copy. Borrows &'a str directly from Bytes,
///   with scoped vector allocation dropped deterministically via RAII.
async fn handle_tokenize(
    State(state): State<Arc<AppState>>,
    body: Bytes,
) -> Result<Json<TokenizeResponse>, (StatusCode, String)> {
    let req: TokenizeRequest = serde_json::from_slice(&body)
        .map_err(|e| (StatusCode::BAD_REQUEST, format!("Invalid JSON payload: {}", e)))?;

    if req.text.is_empty() {
        return Err((
            StatusCode::BAD_REQUEST,
            "Field 'text' must not be empty".to_string(),
        ));
    }

    let token_count: usize;

    if state.alloc_pressure {
        // Rust A: Allocation-Heavy Mode
        let owned_text: String = req.text.to_string(); // Heap allocation 1

        let tokens = state.bpe.encode_ordinary(&owned_text); // Heap allocation 2
        token_count = tokens.len();

        let mut chunks: Vec<Vec<usize>> = Vec::with_capacity(tokens.len() / 128 + 1);
        for chunk in tokens.chunks(128) {
            chunks.push(chunk.to_vec()); // Multiple heap allocations
        }

        if owned_text.len() > 64 {
            let prefix = owned_text[..64].to_string(); // Heap allocation
            let _ = prefix;
        }
    } else {
        // Rust B: Zero-Copy Mode
        let tokens = state.bpe.encode_ordinary(req.text);
        token_count = tokens.len();
    }

    state.request_counter.fetch_add(1, Ordering::Relaxed);
    Ok(Json(TokenizeResponse { token_count }))
}

/// Metrics handler returning operational counters and exact allocator statistics.
async fn handle_metrics(State(state): State<Arc<AppState>>) -> Json<MetricsResponse> {
    let alloc_c = ALLOC_COUNT.load(Ordering::Relaxed);
    let dealloc_c = DEALLOC_COUNT.load(Ordering::Relaxed);
    let alloc_b = ALLOC_BYTES.load(Ordering::Relaxed);
    let dealloc_b = DEALLOC_BYTES.load(Ordering::Relaxed);
    let current_heap = if alloc_b >= dealloc_b {
        alloc_b - dealloc_b
    } else {
        0
    };

    let reqs = state.request_counter.load(Ordering::Relaxed);
    let calls_per_req = if reqs > 0 {
        alloc_c as f64 / reqs as f64
    } else {
        0.0
    };

    let strategy_label = if state.alloc_pressure {
        "Rust A (Allocation-Heavy / Cloned Churn)".to_string()
    } else {
        "Rust B (Zero-Copy / Low Allocation)".to_string()
    };

    Json(MetricsResponse {
        gc_cycles: 0,
        gc_pause_total_ns: 0,
        gc_pause_max_ms: 0.0,
        alloc_count: alloc_c,
        dealloc_count: dealloc_c,
        alloc_calls_per_req: calls_per_req,
        total_allocated_bytes: alloc_b,
        current_heap_bytes: current_heap,
        peak_rss_mb: read_rss_mb(),
        alloc_pressure: state.alloc_pressure,
        strategy: strategy_label,
        total_requests: reqs,
        uptime_seconds: state.start_time.elapsed().as_secs_f64(),
    })
}

/// Health check handler for orchestrator readiness verification.
async fn handle_health() -> impl IntoResponse {
    (
        StatusCode::OK,
        Json(serde_json::json!({
            "status": "ok",
            "engine": "rust-server",
            "bpe": "cl100k_base",
            "runtime": "tokio-multithreaded"
        })),
    )
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    tracing_subscriber::registry()
        .with(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "rust_server=info,tower_http=info".into()),
        )
        .with(tracing_subscriber::fmt::layer())
        .init();

    info!("[rust-server] Initializing cl100k_base BPE vocabulary...");
    let bpe = cl100k_base().expect("failed to load cl100k_base BPE vocabulary");
    info!("[rust-server] cl100k_base vocabulary loaded successfully into memory.");

    let alloc_pressure = match std::env::var("ALLOC_PRESSURE") {
        Ok(val) => val.to_lowercase() == "true" || val == "1",
        Err(_) => false,
    };

    let strategy_label = if alloc_pressure {
        "Rust A (Allocation-Heavy / Cloned Churn)"
    } else {
        "Rust B (Zero-Copy / Low Allocation)"
    };
    info!("[rust-server] Strategy: {}", strategy_label);

    let state = Arc::new(AppState {
        bpe: Arc::new(bpe),
        start_time: Instant::now(),
        request_counter: AtomicU64::new(0),
        alloc_pressure,
    });

    let port: u16 = std::env::var("PORT")
        .unwrap_or_else(|_| "8081".to_string())
        .parse()
        .unwrap_or(8081);

    let app = Router::new()
        .route("/tokenize", post(handle_tokenize))
        .route("/metrics", get(handle_metrics))
        .route("/stats", get(handle_metrics))
        .route("/health", get(handle_health))
        .layer(TraceLayer::new_for_http())
        .with_state(state);

    let addr = SocketAddr::from(([0, 0, 0, 0], port));
    info!("[rust-server] Listening and serving HTTP on {}", addr);

    let listener = tokio::net::TcpListener::bind(addr).await?;
    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal())
        .await?;

    info!("[rust-server] Server gracefully shut down.");
    Ok(())
}

async fn shutdown_signal() {
    let ctrl_c = async {
        tokio::signal::ctrl_c()
            .await
            .expect("failed to install Ctrl+C handler");
    };

    #[cfg(unix)]
    let terminate = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("failed to install SIGTERM handler")
            .recv()
            .await;
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => info!("[rust-server] Received Ctrl+C, shutting down..."),
        _ = terminate => info!("[rust-server] Received SIGTERM, shutting down..."),
    }
}
