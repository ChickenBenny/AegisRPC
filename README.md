# AegisRPC: The Autonomous Guardian of Web3 Integrity & Performance

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Report Card](https://goreportcard.com/badge/github.com/aegisrpc/aegisrpc)](https://goreportcard.com/report/github.com/aegisrpc/aegisrpc)

AegisRPC is a **Chain-Semantic Aware RPC Gateway** designed to be the "Traefik of Web3". Unlike traditional load balancers, AegisRPC understands the underlying JSON-RPC protocol, ensuring data integrity, reducing operational costs, and providing extreme stability for decentralized applications.

## 🛡️ Why AegisRPC?

In the current Web3 landscape, developers face critical infrastructure challenges that traditional proxies (Nginx, HAProxy) cannot solve:

- **The "False 200 OK" Trap:** Upstream nodes often return an HTTP 200 status code even when the internal JSON-RPC response contains logic errors (e.g., `Execution Reverted` or `Internal Error`). AegisRPC inspects the payload to ensure only valid data reaches your DApp.
- **Block Height Divergence:** Nodes can fall out of sync or lag behind the tip of the chain. AegisRPC continuously probes block heights and automatically ejects "zombie nodes" to prevent your frontend from displaying stale data.
- **Capability Mismatch:** Routing an `eth_debugTraceTransaction` request to a non-archive or non-debug node leads to failure. AegisRPC auto-classifies nodes (Archive, Full, Debug) and routes requests with precision.
- **The "RPC Tax":** High-traffic DApps pay exorbitant fees for redundant requests. AegisRPC employs **Request Coalescing** to merge identical concurrent requests, slashing your RPC bills instantly.

## 🚀 Key Features

### 🔍 Auto-Discovery & Health Probing
Real-time monitoring of upstream synchronization status, latency, and method capabilities. Automatically routes around failing or lagging nodes.

### ⚡ Smart Re-org Aware Caching
A sophisticated caching layer that understands chain finality. It caches data intelligently after block confirmation and automatically invalidates entries when a chain re-org is detected.

### 💎 Request Coalescing (SingleFlight)
Utilizes the SingleFlight pattern to merge high-frequency identical requests (e.g., `eth_blockNumber`, `eth_gasPrice`) into a single upstream call, drastically reducing load and cost.

### ☸️ Cloud-Native & DevOps Friendly
First-class support for Docker Labels and Kubernetes Ingress. AegisRPC seamlessly integrates into your existing CI/CD and orchestration workflows.

### 📊 Observability & Cost Analytics
Native Prometheus metrics and Grafana dashboards to monitor node health, cache hit rates, and direct cost savings in real-time.

## 🗺️ MVP Roadmap

- [ ] **Core Engine:** High-performance Golang-based Reverse Proxy core.
- [ ] **Upstream Health Manager:** Implementation of `eth_blockNumber` probes and auto-failover.
- [ ] **Method Classifier:** Automated identification of Node capabilities (Archive vs. Full).
- [ ] **SingleFlight Middleware:** Core logic for request merging and deduplication.
- [ ] **Flexible Config Provider:** Support for YAML and Environment Variable configuration.

## 🛠️ Tech Stack

- **Core:** Go (Golang)
- **Cache:** Redis / In-memory LRU
- **Observability:** Prometheus + Grafana
- **Deployment:** Docker / Kubernetes / Helm

## ⚖️ License

Distributed under the MIT License. See `LICENSE` for more information.
