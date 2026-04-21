#!/usr/bin/env bash
# test-live-ws.sh — end-to-end WebSocket test against real Ethereum nodes.
#
# What it does:
#   1. Starts AegisRPC proxy with two public upstreams:
#        - https://eth.llamarpc.com   (HTTP RPC, no WebSocket support)
#        - wss://ethereum.publicnode.com (WebSocket, free public node)
#   2. Waits for the proxy to be ready.
#   3. Runs ws-client to verify:
#        - eth_subscribe newHeads works
#        - New block headers stream in
#        - eth_blockNumber polling works
#   4. Cleans up on exit (Ctrl-C or error).
#
# Usage:
#   chmod +x examples/test-live-ws.sh
#   ./examples/test-live-ws.sh
#
# Optional env vars:
#   PORT=8545            proxy port (default: 8545)
#   UPSTREAMS="..."      comma-separated upstream URLs

set -euo pipefail

PORT="${PORT:-8545}"
UPSTREAMS="${UPSTREAMS:-https://eth.llamarpc.com,wss://ethereum.publicnode.com}"
PROXY_PID=""

cleanup() {
    echo ""
    echo "[test] cleaning up..."
    if [[ -n "$PROXY_PID" ]] && kill -0 "$PROXY_PID" 2>/dev/null; then
        kill "$PROXY_PID"
        wait "$PROXY_PID" 2>/dev/null || true
    fi
    echo "[test] done."
}
trap cleanup EXIT INT TERM

# ── kill any leftover process on the port ────────────────────────────────────
if lsof -ti:"$PORT" &>/dev/null; then
    echo "[test] port $PORT in use, killing existing process..."
    lsof -ti:"$PORT" | xargs kill -9 2>/dev/null || true
    sleep 1
fi

# ── start proxy in background ─────────────────────────────────────────────────
echo "[test] starting proxy on :$PORT"
echo "[test] upstreams: $UPSTREAMS"
echo ""
go run ./cmd/aegis-rpc \
    -upstreams "$UPSTREAMS" \
    -port "$PORT" &
PROXY_PID=$!

# ── wait for proxy to be ready ────────────────────────────────────────────────
echo "[test] waiting for proxy to start..."
for i in $(seq 1 20); do
    if curl -sf -X POST "http://localhost:$PORT" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
        -o /dev/null 2>/dev/null; then
        echo "[test] proxy ready!"
        break
    fi
    sleep 1
    if [[ $i -eq 20 ]]; then
        echo "[test] ERROR: proxy did not start in time"
        exit 1
    fi
done

# ── quick HTTP sanity check ───────────────────────────────────────────────────
echo ""
echo "── HTTP eth_blockNumber ─────────────────────────────────────────────────"
curl -s -X POST "http://localhost:$PORT" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}'
echo ""
echo "─────────────────────────────────────────────────────────────────────────"
echo ""

# ── run ws-client ─────────────────────────────────────────────────────────────
echo "[test] launching ws-client (Ctrl-C to stop)"
echo ""
go run ./examples/ws-client -addr "localhost:$PORT"
