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

## 5. Configuration

AegisRPC supports three configuration sources with the following precedence (highest wins):

```
CLI flags  >  Environment variables  >  YAML config file  >  Built-in defaults
```

### 5.1 Environment Variables

| Variable | Default | Description |
|---|---|---|
| `AEGIS_CONFIG` | _(none)_ | Path to a YAML config file |
| `AEGIS_PORT` | `8080` | TCP port the proxy listens on |
| `AEGIS_UPSTREAMS` | `https://eth.llamarpc.com` | Comma-separated list of upstream RPC URLs |
| `AEGIS_MUTABLE_TTL` | `12s` | TTL for mutable cached responses (e.g. `eth_blockNumber`) |
| `AEGIS_MAX_CACHE_ENTRIES` | `10000` | LRU cap for the response cache (`0` = unlimited) |
| `AEGIS_FINALITY_DEPTH` | `12` | Number of block confirmations required to consider a block finalized |
| `AEGIS_HEALTH_INTERVAL` | `15s` | How often to poll each upstream for health |
| `AEGIS_PROBE_TIMEOUT` | `5s` | HTTP timeout for each individual health probe request |
| `AEGIS_LAG_THRESHOLD` | `10` | Max blocks a node may lag behind the best before being marked unhealthy |
| `AEGIS_LOG_LEVEL` | `info` | Log threshold: `debug`, `info`, `warn`, or `error`. `debug` reveals every per-probe outcome; `info` only logs state transitions and notable events. |
| `AEGIS_LOG_FORMAT` | `text` | Log encoding: `text` (human-readable `key=value`) or `json` (one JSON object per line, suited for ELK / Loki / Datadog). |
| `AEGIS_WRITE_TIMEOUT` | `30s` | Maximum response write duration. Default suits wallet/dApp traffic; archive deployments serving wide-range `eth_getLogs` or `debug_trace*` typically need `120s` or higher to avoid mid-response connection resets. |
| `AEGIS_WS_REPLAY_PENDING_CAP` | `1024` | Per-WS-session cap on upstream frames buffered during the reconnect / subscription replay window. Indexer deployments listening on heavy-traffic contracts (Uniswap-class) may want to raise this. |

Duration values accept Go duration strings: `5s`, `1m`, `500ms`, etc.

### 5.2 YAML Config File

Pass the path via `--config <file>` or `AEGIS_CONFIG=<file>`. Only fields present in the file override defaults; omitted fields keep their default values.

```yaml
port: 8080
upstreams:
  - https://eth.llamarpc.com
  - https://rpc.ankr.com/eth
mutable_ttl: 12s
max_cache_entries: 10000
finality_depth: 12
health_interval: 15s
probe_timeout: 5s
lag_threshold: 10
```

### 5.3 CLI Flags

Every parameter is also available as a flag. Run `aegis-rpc --help` to see all options. CLI flags override both ENV variables and the config file.
