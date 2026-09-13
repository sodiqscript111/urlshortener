# urlshorter

A high-performance URL shortener service built with Go, Redis, and PostgreSQL, engineered to sustain over **400 Million requests per day (4,630+ RPS)** with sub-2 millisecond average latency on consumer-grade hardware.

## Performance Benchmark Results

The service was benchmarked under a sustained 5,000 requests/second load test targeting the redirect path (`GET /:code`):

| Metric | Measured Value | Daily Equivalent |
| :--- | :--- | :--- |
| **Total Requests** | 92,500 | - |
| **Throughput** | 4,633.1 requests/sec | 400,300,000 requests/day |
| **Success Rate** | 100.0% (92,497 / 92,500) | - |
| **Mean Latency** | 1.51 ms | - |
| **Median (P50) Latency** | < 1.0 ms | - |
| **95th Percentile (P95)** | 2.18 ms | - |
| **99th Percentile (P99)** | 8.86 ms | - |
| **Max Latency** | 30.0 s (ramp-up timeout) | - |

## Architectural Optimizations

Sustaining 4,600+ requests per second requires eliminating synchronous bottlenecks across both read and write paths.

### 1. Two-Tier Caching Architecture (L1 Memory + L2 Redis)
- **Bounded L1 In-Memory Cache**:
  - Hot redirect lookups are served directly from process memory (`BoundedL1Cache`) using a thread-safe `sync.RWMutex`.
  - Lookup latency is sub-microsecond with zero network overhead.
  - Strict Capacity Limit: Bounded to 50,000 entries (approximately 5MB RAM). When capacity is reached, expired keys are purged first, and older entries are evicted to prevent out-of-memory (OOM) conditions.
  - Active Eviction: A background worker runs every 10 seconds to reclaim memory from keys that have exceeded their 30-second TTL.
- **L2 Redis Cache**:
  - Cache misses in L1 query Redis (`GET slug:<code >`).
  - Successful lookups populate the L1 cache for subsequent requests.
- **Database Fallback with Singleflight**:
  - If a key misses in both L1 and Redis, it queries PostgreSQL using `singleflight` deduplication to prevent cache stampedes.

### 2. Channel-Based Asynchronous Click Buffer
Incrementing click counts synchronously on every redirect forces a network write to Redis or an `UPDATE` query on PostgreSQL. At 5,000 requests per second, this saturates connection pools and creates row-level database lock contention.

- **Non-Blocking Queue Push**:
  - On every redirect request, `HandleRedirect` drops the short code into a buffered Go channel (`make(chan string, 100000)`).
  - The push operation completes in approximately 10 nanoseconds, allowing the HTTP redirect (`302 Found`) to return immediately to the client.
- **Batch Flusher**:
  - A background worker collects incoming click events from the channel.
  - It flushes batches to Redis using `RPUSH outbox:clicks` either when 500 clicks accumulate or every 50 milliseconds.
  - This reduces Redis write network round-trips by over 99%, converting 5,000 individual operations into approximately 10 bulk pipeline operations per second.

### 3. Outbox Worker Pattern for Database Persistence
- An asynchronous worker pops click batches from Redis (`outbox:clicks`).
- It aggregates clicks per short code in memory (e.g., 500 clicks for a single code).
- It updates the atomic counter in Redis (`INCRBY clicks:<code > <count>`) using a pipeline.
- It updates PostgreSQL in batch (`UPDATE links SET clicks = clicks + <count> WHERE short_code = <code >`).
- This completely isolates disk writes and database locks from the client-facing HTTP redirect path.

### 4. Connection Pool and Runtime Tuning
- **Gin Web Engine**: Configured to `gin.ReleaseMode` and initialized with `gin.New()` and recovery middleware, eliminating standard output console logging bottlenecks during load.
- **PostgreSQL Connection Pool**: Configured GORM connection pool with `MaxOpenConns: 50`, `MaxIdleConns: 25`, and `ConnMaxLifetime: 5m`.
- **Redis Connection Pool**: Configured Redis client with `PoolSize: 300` and `MinIdleConns: 50`.

### 5. Solving Load Generator Socket Starvation
During load testing at 5,000 requests per second, the load generator experienced socket exhaustion on the client side:
- **Root Cause**: Go standard library `http.DefaultTransport` defaults `MaxIdleConnsPerHost` to 2. Under 800 concurrent workers against a single host, 798 workers closed their TCP connections after every request, generating 5,000 new TCP handshakes per second.
- **Impact**: Ephemeral ports were exhausted within seconds as closed connections sat in `TIME_WAIT`, causing over 120,000 blocked goroutines and 94,800 open socket handles.
- **Resolution**: Configured the load generator with a custom `http.Transport` setting `MaxIdleConnsPerHost: 5000` and `MaxIdleConns: 10000`. This enabled persistent HTTP Keep-Alive connection reuse across all workers, eliminating socket churn and unlocking wire speed.

## Automated Testing Suite (Testcontainers)

The project includes unit tests and integration tests powered by **Testcontainers for Go (`testcontainers-go`)**:

- **Real Ephemeral Containers**: Automatically spins up clean, isolated `postgres:16-alpine` and `redis:7-alpine` Docker containers on random host ports during test execution and terminates them on completion.
- **End-to-End Handlers**: Tests link creation (`POST /shorten`), HTTP 302 redirects (`GET /:code`), L1/L2 cache population, cache fallback to database, and service health checks (`GET /health`).
- **Asynchronous Persistence**: Verifies that the channel click buffer and outbox worker accurately flush counts to Redis and PostgreSQL.
- **Bounded Cache Verification**: Unit tests verify TTL expiration, strict 50,000 capacity limit enforcement, and race safety under `go test -race`.

To run the test suite:
```bash
go test -v -timeout 5m ./internal/api
```

## API Endpoints

| Method | Endpoint | Description |
| :--- | :--- | :--- |
| `POST` | `/shorten` | Creates a new short URL. Accepts JSON `{"url": "https://example.com"}`. |
| `GET` | `/:code` | Redirects to the original URL via HTTP 302 Found. |
| `GET` | `/health` | Returns health status of database and Redis. |
| `GET` | `/target` | Lightweight benchmark target endpoint. |

## Quickstart

### Prerequisites
- Go 1.25+
- Docker & Docker Compose

### 1. Start Infrastructure
```bash
docker-compose up -d
```

### 2. Run Application
```bash
go run ./main.go
```

To run with the rate limiter disabled for high-throughput benchmarking:
```bash
# Windows PowerShell
$env:DISABLE_RATE_LIMIT="true"; go run ./main.go

# Linux / macOS
DISABLE_RATE_LIMIT="true" go run ./main.go
```

### 3. Run Benchmark
Using Barrage with `config.yaml`:
```bash
./barrage run -c config.yaml -o
```

Check the generated `report.html` for detailed percentile latency graphs and breakdown.

## Tech Stack
- **Language**: Go 1.25
- **Routing**: Gin
- **Database**: PostgreSQL 17 (GORM)
- **Cache**: Redis 7 & In-Memory Bounded L1 Cache
- **Testing**: Testcontainers for Go, Testify
- **Load Testing**: Barrage (Vegeta engine)
