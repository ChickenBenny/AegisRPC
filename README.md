# AegisRPC: The Intelligent Auto-Discovering Web3 RPC Gateway

## 1. Vision
AegisRPC aims to be the "Traefik" for Web3. It provides a robust, self-healing, and cost-efficient middleware layer for interacting with blockchain JSON-RPC nodes. Developers should no longer need to manually manage complex node lists, health checks, or caching strategies.

## 2. Core Pain Points Addressed
- **Semantic Errors (The "False 200 OK"):** Resolves issues where RPC nodes return HTTP 200 but the body contains logical errors (e.g., `Execution Reverted`).
- **Block Height Divergence:** Automatically filters out lagging nodes, preventing frontends from receiving stale data or non-existent blocks.
- **Capability Mismatch:** Distinguishes between Archive, Full, and Debug nodes to route specialized requests correctly (e.g., `debug_traceTransaction`).
- **High RPC Costs:** Uses "Request Coalescing" (SingleFlight) to drastically reduce duplicate upstream requests and provider bills.

## 3. Key Features
- **Auto-Discovery:** Automatically probes node capabilities (Archive vs Full, Debug support) and synchronization status.
- **Smart Caching:** Re-org aware caching that ensures data consistency even during chain reorganizations.
- **Request Coalescing:** Merges identical concurrent requests into one upstream call.
- **Cloud-Native Design:** Built for containerized environments with support for health probes and metrics.
- **Observability:** A dashboard to monitor latency, sync progress, cache hit rates, and cost savings.

## 4. Tech Stack
- **Language:** Go (Golang)
- **Cache:** Redis / In-memory LRU
- **Observability:** Prometheus + Grafana
- **Deployment:** Docker / Kubernetes
