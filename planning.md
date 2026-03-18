# AegisRPC Project Planning

## Phase 1: The Foundations
*Goal: Build a basic RPC proxy that can forward requests.*
- [x] **Task 1.1:** Initialize Go Project (`go mod init`).
- [ ] **Task 1.2:** Implement a basic HTTP Reverse Proxy to forward `POST` requests.
- [ ] **Task 1.3:** Define JSON-RPC Request/Response structs and parse payloads.
- **Learning:** Understanding `net/http` and `httputil.ReverseProxy`.

## Phase 2: Reliability & Health
*Goal: Manage multiple nodes and auto-failover.*
- [ ] **Task 2.1:** Implement Upstream Pool with multi-node support.
- [ ] **Task 2.2:** Regular polling for `eth_blockNumber` and `eth_syncing`.
- [ ] **Task 2.3:** Height-check logic to mark nodes as Unhealthy if lagging.
- **Learning:** Mastering Go Concurrency (Goroutines, Tickeres) and Mutexes.

## Phase 3: Auto-Discovery
*Goal: Automatically classify nodes (Archive/Full/Debug).*
- [ ] **Task 3.1:** Implement Node Probing for historical state and trace methods.
- [ ] **Task 3.2:** Method-based Routing: Direct `debug_*` to designated nodes.
- [ ] **Task 3.3:** Dynamic routing rules similar to Traefik.
- **Learning:** Ethereum node architecture (State Pruning vs Archive).

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

## Phase 6: Observability & Cloud
*Goal: Monitoring and Deployment.*
- [ ] **Task 6.1:** Integrate Prometheus Metrics (Latency, RPS, Cache Hit Rate).
- [ ] **Task 6.2:** Develop a simple Monitoring Dashboard.
- [ ] **Task 6.3:** Support Docker Labels / K8s Ingress integration.
- **Learning:** Cloud-native monitoring and service discovery.
