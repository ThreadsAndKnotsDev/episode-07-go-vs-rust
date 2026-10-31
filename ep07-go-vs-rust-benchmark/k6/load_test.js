import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

// Load synthetic corpus of realistic AI prompts (250 - 1,500 tokens)
const corpusData = JSON.parse(open('./corpus.json'));

// Custom metrics for telemetry extraction
export const tokenCountTrend = new Trend('token_count_per_req');
export const errorRate = new Rate('custom_error_rate');
export const tokensProcessed = new Counter('tokens_total_processed');

// Target service endpoint (default: Go server on 8080, overridden via TARGET_URL)
const TARGET_URL = __ENV.TARGET_URL || 'http://localhost:8080/tokenize';

// Stage configurations with environment variable overrides for custom test environments
const WARMUP_DURATION = __ENV.WARMUP_DURATION || '30s';
const BASELINE_DURATION = __ENV.BASELINE_DURATION || '60s';
const BURST_DURATION = __ENV.BURST_DURATION || '60s';

const WARMUP_RPS = parseInt(__ENV.WARMUP_RPS || '500', 10);
const BASELINE_RPS = parseInt(__ENV.BASELINE_RPS || '2000', 10);
const BURST_RPS = parseInt(__ENV.BURST_RPS || '10000', 10);

export const options = {
  // Explicitly export high-percentile latency metrics required for telemetry schema
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)', 'p(99.9)'],
  
  scenarios: {
    tokenization_pipeline: {
      executor: 'ramping-arrival-rate',
      startRate: WARMUP_RPS,
      timeUnit: '1s',
      preAllocatedVUs: 250,
      maxVUs: 4000,
      stages: [
        // Stage 1: Warmup - 30 seconds at 500 RPS
        { target: WARMUP_RPS, duration: WARMUP_DURATION },
        
        // Transition ramp to baseline
        { target: BASELINE_RPS, duration: '5s' },
        
        // Stage 2: Sustained Baseline - 60 seconds at 2,000 RPS
        { target: BASELINE_RPS, duration: BASELINE_DURATION },
        
        // Transition ramp to burst
        { target: BURST_RPS, duration: '5s' },
        
        // Stage 3: High-Throughput Burst - 60 seconds at 10,000+ RPS
        // Demonstrates Go GC Mark Assist conscription vs Rust deterministic RAII flat latency
        { target: BURST_RPS, duration: BURST_DURATION },
      ],
    },
  },
  
  thresholds: {
    // Assert general HTTP success rate > 95%
    http_req_failed: ['rate<0.05'],
  },
};

export default function () {
  // Uniform random sampling from corpus
  const sample = corpusData[Math.floor(Math.random() * corpusData.length)];
  
  const payload = JSON.stringify({
    text: sample.text,
  });

  const params = {
    headers: {
      'Content-Type': 'application/json',
      'Accept': 'application/json',
    },
    timeout: '10s',
  };

  const res = http.post(TARGET_URL, payload, params);

  const isSuccess = check(res, {
    'status is 200': (r) => r.status === 200,
    'has token_count': (r) => {
      try {
        const body = JSON.parse(r.body);
        return body && typeof body.token_count === 'number' && body.token_count > 0;
      } catch (e) {
        return false;
      }
    },
  });

  if (!isSuccess) {
    errorRate.add(1);
  } else {
    errorRate.add(0);
    try {
      const parsed = JSON.parse(res.body);
      tokenCountTrend.add(parsed.token_count);
      tokensProcessed.add(parsed.token_count);
    } catch (_) {}
  }
}
