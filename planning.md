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
- [ ] **Task 4.1:** SingleFlight mode to coalesce concurrent identical requests.
- [ ] **Task 4.2:** In-memory Cache for immutable data (transactions, blocks).
- [ ] **Task 4.3:** Data-layered caching (Mutable vs Immutable).
- **Learning:** Concurrency patterns (SingleFlight) and Cache Invalidation.

## Phase 5: Advanced Consistency
*Goal: Handle Re-orgs and WebSockets.*
- [ ] **Task 5.1:** Re-org Aware Cache based on Finality depth.
- [ ] **Task 5.2:** Virtual WebSockets with transparent upstream failover.
- **Learning:** Blockchain Finality and Consistency models.

## Phase 5.5: Configuration & Environment
*Goal: Make all tunable parameters configurable via ENV for Docker / Kubernetes deployment.*
- [ ] **Task 5.5.1:** Define all configurable parameters (timeouts, thresholds, ports, upstream URLs).
- [ ] **Task 5.5.2:** Read config from ENV with sensible defaults (`AEGIS_PORT`, `AEGIS_UPSTREAMS`, `AEGIS_PROBE_TIMEOUT`, `AEGIS_LAG_THRESHOLD`, ...).
- [ ] **Task 5.5.3:** Support config file (YAML/TOML) as an alternative to ENV.
- [ ] **Task 5.5.4:** Document all ENV variables in README.
- **Learning:** Twelve-Factor App methodology. Config management in cloud-native services.

## Phase 6: Observability & Cloud
*Goal: Monitoring and Deployment.*
- [ ] **Task 6.1:** Integrate Prometheus Metrics (Latency, RPS, Cache Hit Rate).
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
