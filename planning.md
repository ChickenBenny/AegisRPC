# AegisRPC Project Planning

## Phase 1: The Foundations
*Goal: Build a basic RPC proxy that can forward requests.*
- [x] **Task 1.1:** Initialize Go Project (`go mod init`).
- [x] **Task 1.2:** Implement a basic HTTP Reverse Proxy to forward `POST` requests.
- [x] **Task 1.3:** Define JSON-RPC Request/Response structs and parse payloads.
- [x] **Task 1.4:** Set up CI pipeline (GitHub Actions) to run tests on every push and PR.
- **Learning:** Understanding `net/http` and `httputil.ReverseProxy`.

## Phase 2: Reliability & Health
*Goal: Manage multiple nodes and auto-failover.*
- [x] **Task 2.1:** Implement Upstream Pool with multi-node support.
- [x] **Task 2.2:** Regular polling for `eth_blockNumber` and `eth_syncing`.
- [x] **Task 2.3:** Height-check logic to mark nodes as Unhealthy if lagging.
- **Learning:** Mastering Go Concurrency (Goroutines, Tickers) and Mutexes.

## Phase 3: Auto-Discovery
*Goal: Automatically classify nodes by capability, chain-agnostic design.*
- [x] **Task 3.1:** Define `Capability` bitmask (`uint16`) and `Prober` interface.
- [x] **Task 3.2:** Implement `EthProber` — probe Archive / Debug / Trace capabilities.
- [x] **Task 3.3:** Method-based routing via capability mapping (`debug_*` → CapDebug).
- [x] **Task 3.4:** Manual capability annotation via URL syntax (`url[archive,debug]`).
- **Learning:** Ethereum node architecture (State Pruning vs Archive). Interface-driven design for multi-chain extensibility.

## Phase 4: Performance & Cost Optimization
*Goal: Save RPC provider bills.*
- [x] **Task 4.1:** SingleFlight mode to coalesce concurrent identical requests.
- [x] **Task 4.2:** In-memory Cache for immutable data (transactions, blocks).
- [x] **Task 4.3:** Data-layered caching (Mutable vs Immutable).
- **Learning:** Concurrency patterns (SingleFlight) and Cache Invalidation.

## Phase 5: Advanced Consistency
*Goal: Handle Re-orgs and WebSockets.*
- [x] **Task 5.1:** Re-org Aware Cache based on Finality depth.
- [x] **Task 5.2:** Virtual WebSockets with transparent upstream failover.
- **Learning:** Blockchain Finality and Consistency models.

## Phase 5.3: WebSocket Reliability
*Goal: Prevent zombie connections from silently stalling active WebSocket sessions.*
- [x] **Task 5.3.1:** Upstream ping/pong heartbeat — send a WebSocket Ping to the upstream every 30s; if no Pong within 10s, treat the connection as dead and trigger failover.
- [x] **Task 5.3.2:** Fix `replaySubscriptions` ordering assumption — batch all replay requests first, then collect responses by ID (skip early notifications); handles nodes that push notifications before the subscribe response arrives.
- **Learning:** RFC 6455 WebSocket Ping/Pong keepalive. TCP half-open / zombie connection problem in cloud environments.

## Phase 5.5: Configuration & Environment
*Goal: Make all tunable parameters configurable via ENV for Docker / Kubernetes deployment.*
- [x] **Task 5.5.1:** Define all configurable parameters (timeouts, thresholds, ports, upstream URLs).
- [x] **Task 5.5.2:** Read config from ENV with sensible defaults (`AEGIS_PORT`, `AEGIS_UPSTREAMS`, `AEGIS_PROBE_TIMEOUT`, `AEGIS_LAG_THRESHOLD`, ...).
- [x] **Task 5.5.3:** Support config file (YAML/TOML) as an alternative to ENV.
- [x] **Task 5.5.4:** Document all ENV variables in README.
- **Learning:** Twelve-Factor App methodology. Config management in cloud-native services.

## Phase 6: Observability & Cloud
*Goal: Monitoring and Deployment.*
- [x] **Task 6.1:** Integrate Prometheus Metrics (Latency, RPS, Cache Hit Rate).
- [ ] **Task 6.2:** Develop a simple Monitoring Dashboard.
- [ ] **Task 6.3:** Support Docker Labels / K8s Ingress integration.
- **Learning:** Cloud-native monitoring and service discovery.

## Phase 7: Multi-Chain Support
*Goal: Extend AegisRPC beyond Ethereum to other chains.*
- [ ] **Task 7.1:** EVM-compatible chains (Polygon, BSC, Arbitrum, Optimism) — reuse `EthProber`, add chain ID detection.
- [ ] **Task 7.2:** `SolanaProber` — probe RPC vs Vote node, Geyser plugin availability.
- [ ] **Task 7.3:** Chain-aware health check — each chain has different block time expectations.
- [ ] **Task 7.4:** Multi-chain pool management — route by chain ID in addition to capability.
- **Learning:** Non-EVM chain architectures (UTXO, account model variants, Solana's slot/epoch model).

---

## Fixes & Improvements (from code review)
- [x] Fix silent error ignoring in `health.go`
- [x] Fix goroutine leak in `StartHealthChecks` (add `sync.WaitGroup`)
- [x] Fix HTTP status code not checked in health check
- [x] Fix upstream URL not trimmed on whitespace
- [x] Add load balancing (round-robin) to `Pool.Next()`
- [x] Add request body size limit (1MB via `MaxBytesReader` / `LimitReader`)
- [x] Add graceful shutdown (SIGTERM/SIGINT)
- [ ] Add Pool-level Mutex for dynamic node management (deferred — nodes slice is immutable for now)

## Fix: Bug Sweep (`fix/bug-sweep`)
*Goal: Fix real bugs discovered by a full-project scan before starting Phase 5.3.*

### Verified issues (fixed on this branch)
- [x] **CRITICAL — `FinalityChecker` data race** (`internal/cache/finality.go`): `head`
  field was read by request handlers (`Classify` / `IsFinalized`) while
  `SetHead` was called from the health-check callback goroutine, with no
  synchronisation. Fixed by adding `sync.RWMutex`: writers lock, readers
  RLock and snapshot `head` + `depth` before evaluating the finality predicate.
- [x] **HIGH — unchecked `json.Marshal` in `replaySubscriptions`** (`internal/proxy/ws.go`):
  the replay request was built via `req, _ := json.Marshal(...)`. A silent `nil`
  frame would be sent on the wire if marshalling ever failed, losing a
  subscription across failover. Fixed by returning the error so the replay
  round fails loudly into the normal reconnect path.
- [x] **MEDIUM — `NextWithCapability` non-atomic counter** (`internal/upstream/upstream.go`):
  used `counter.Load()` + later `counter.Add(i+1)`, so two concurrent callers
  could read the same `start` and route to the same node. Fixed by reserving
  a starting slot atomically via `counter.Add(1) - 1` up front, matching
  `Next()`'s handling.
- [x] **MEDIUM — unbounded `s.pending` growth** (`internal/proxy/ws.go`): a
  misbehaving upstream that never responds to `eth_subscribe` would leave
  pending entries in the map for the lifetime of the session. Fixed by
  capping the pending map at 1024 entries and returning a local JSON-RPC
  error (code `-32000`) to the client once the cap is reached.

### Covered by Phase 5.3 (do not fix here)
- Upstream `ReadMessage()` blocks without ctx cancellation → Task 5.3.1 (heartbeat).
- Client reader has no read deadline → Task 5.3.1 (heartbeat).

### False positives from the scan (documented so they don't get re-flagged)
- `s.subs` keyed by volatile upstream ID — the key is actually the stable
  client-facing ID assigned on first subscribe and is never rewritten.
- `replaySubscriptions` vs `forwardToClient` race — replay runs before `pump`
  starts, so there is no concurrent reader of `s.upToClient` during replay.
- `cache.deleteExpired` modifies map during iteration — Go's spec explicitly
  allows deletion of keys during `range`.
- `result := v.([]byte)` without `, ok` — the value is produced inside the same
  package by our own singleflight closure, so the type is controlled.
- `fromClient` channel "leak" in `ws.go run()` — the reader goroutine unblocks
  when `conn.Close()` fires in the caller's defer, so no long-lived goroutine survives.

## Fix: Upstream Rate-Limit Handling (`fix/upstream-rate-limit-handling`)
*Goal: Prevent HTTP 429 from the upstream from poisoning health state and negative cache.*

### Root Cause
Public RPC providers (e.g. `eth.llamarpc.com`) aggressively rate-limit both health-check
requests and actual RPC calls with HTTP 429. Before this fix:

1. **Health check** (`health.go`): a 429 was treated the same as a real failure → node marked
   `unhealthy`. When the previous state was already unhealthy (e.g. after an initial timeout),
   consecutive 429s would keep the node unhealthy until the next successful OK, causing all
   WebSocket and HTTP requests to return "no upstream" / "upstream error" during that window.

2. **WebSocket dial** (`ws.go`): `websocket.DefaultDialer.Dial` did not include an `Origin`
   header. Many public Ethereum WebSocket endpoints reject the upgrade without it, returning a
   non-101 status that gorilla/websocket surfaces as "bad handshake".

3. **HTTP handler** (`handler.go`): when the upstream returned 429 to an actual RPC request,
   `fetchFromUpstream` returned a generic error. This caused a 1-second negative-cache entry
   which was then reset on every retry, creating a continuous "upstream error" loop even while
   the node was nominally healthy.

4. **Health check URL scheme** (`health.go`): `checkNode` used `node.URL.String()` directly
   as the HTTP target. For `wss://` or `ws://` upstreams this produced an unsupported-scheme
   error in `http.DefaultClient`, marking every WebSocket-only upstream permanently unhealthy.

### Changes
- [x] **`health.go` — `wsToHTTP()`**: convert `wss://→https://` and `ws://→http://` before
  issuing health-check POST requests, so WebSocket-upstream URLs are accepted by `http.DefaultClient`.
- [x] **`health.go` — 429 handling**: on HTTP 429 mark the node `healthy` (reachable but busy)
  and skip the round rather than marking it unhealthy.
- [x] **`ws.go` — `connectUpstream()`**: pass `Origin: http://localhost` header when dialling
  upstream WebSocket, satisfying providers that validate the Origin field.
- [x] **`handler.go` — `errRateLimited` sentinel**: distinguish 429 from real upstream errors;
  do not write a negative-cache entry on 429, and surface it as `HTTP 429` to the caller instead
  of the misleading "upstream error" / `502`.
