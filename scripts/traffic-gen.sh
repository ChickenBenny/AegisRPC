#!/usr/bin/env bash
# Continuously sends JSON-RPC requests to AegisRPC to generate observable traffic.
# Designed to produce varied patterns across methods and cache behaviours so that
# the Grafana dashboard shows non-trivial data.
#
# Usage:
#   ./scripts/traffic-gen.sh [HOST] [INTERVAL_SECONDS]
#
# Defaults:
#   HOST     = http://localhost:8545
#   INTERVAL = 2  (seconds between rounds)

set -euo pipefail

HOST="${1:-http://localhost:8545}"
INTERVAL="${2:-2}"

# ── colours ─────────────────────────────────────────────────────────────────
BOLD="\033[1m"
DIM="\033[2m"
GREEN="\033[32m"
YELLOW="\033[33m"
RED="\033[31m"
CYAN="\033[36m"
RESET="\033[0m"

# ── state ───────────────────────────────────────────────────────────────────
ROUND=0
TOTAL_OK=0
TOTAL_FAIL=0

# ── helpers ─────────────────────────────────────────────────────────────────

rpc() {
  local method="$1"
  local params="$2"
  curl -s -o /dev/null -w "%{http_code}" \
    -X POST "$HOST/" \
    -H "Content-Type: application/json" \
    --max-time 10 \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"${method}\",\"params\":${params}}"
}

rpc_body() {
  local method="$1"
  local params="$2"
  curl -s \
    -X POST "$HOST/" \
    -H "Content-Type: application/json" \
    --max-time 10 \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"${method}\",\"params\":${params}}"
}

send() {
  local label="$1"
  local method="$2"
  local params="$3"
  local code
  code=$(rpc "$method" "$params")
  if [ "$code" = "200" ]; then
    printf "  ${GREEN}✓${RESET} %-30s ${DIM}HTTP %s${RESET}\n" "$label" "$code"
    TOTAL_OK=$((TOTAL_OK + 1))
  else
    printf "  ${RED}✗${RESET} %-30s ${DIM}HTTP %s${RESET}\n" "$label" "$code"
    TOTAL_FAIL=$((TOTAL_FAIL + 1))
  fi
}

burst() {
  local label="$1"
  local method="$2"
  local params="$3"
  local count="$4"
  local ok=0
  for ((i = 0; i < count; i++)); do
    code=$(rpc "$method" "$params")
    [ "$code" = "200" ] && ok=$((ok + 1))
  done
  TOTAL_OK=$((TOTAL_OK + ok))
  TOTAL_FAIL=$((TOTAL_FAIL + count - ok))
  printf "  ${CYAN}⇉${RESET} %-30s ${DIM}×%d  %d ok${RESET}\n" "$label" "$count" "$ok"
}

print_metrics() {
  local raw
  raw=$(curl -s --max-time 5 "$HOST/metrics" 2>/dev/null || echo "")
  if [ -z "$raw" ]; then
    printf "  ${YELLOW}(could not reach /metrics)${RESET}\n"
    return
  fi

  local req_ok req_hit req_err cache_hit cache_miss
  req_ok=$(echo  "$raw" | awk -F'} '  '/^aegis_rpc_requests_total\{.*status="ok"/    {sum+=$2} END{printf "%.0f", sum}')
  req_hit=$(echo "$raw" | awk -F'} '  '/^aegis_rpc_requests_total\{.*status="cache_hit"/ {sum+=$2} END{printf "%.0f", sum}')
  req_err=$(echo "$raw" | awk -F'} '  '/^aegis_rpc_requests_total\{.*status="error"/  {sum+=$2} END{printf "%.0f", sum}')
  cache_hit=$(echo  "$raw" | awk -F'} ' '/^aegis_cache_requests_total\{result="hit"/{print $2; exit}')
  cache_miss=$(echo "$raw" | awk -F'} ' '/^aegis_cache_requests_total\{result="miss"/{print $2; exit}')

  printf "  ${DIM}metrics  ok=%-6s cache_hit=%-6s error=%-4s | cache_hits=%-6s misses=%s${RESET}\n" \
    "${req_ok:-0}" "${req_hit:-0}" "${req_err:-0}" "${cache_hit:-0}" "${cache_miss:-0}"
}

cleanup() {
  echo ""
  printf "${BOLD}Stopped.${RESET}  total ok=${GREEN}${TOTAL_OK}${RESET}  fail=${RED}${TOTAL_FAIL}${RESET}\n"
  exit 0
}
trap cleanup INT TERM

# ── known block hash for immutable-cache demo ────────────────────────────────
# Block #1 on Ethereum mainnet — always the same hash.
BLOCK1_HASH="0x88e96d4537bea4d9c05d12549907b32561d3bf31f45aae734cdc119f13406cb6"

# ── main loop ───────────────────────────────────────────────────────────────

printf "${BOLD}AegisRPC traffic generator${RESET}\n"
printf "target: ${CYAN}%s${RESET}   interval: ${CYAN}%ss${RESET}\n" "$HOST" "$INTERVAL"
printf "Press Ctrl+C to stop.\n\n"

while true; do
  ROUND=$((ROUND + 1))
  printf "${BOLD}── round %d ─────────────────────────────────────${RESET}\n" "$ROUND"

  # Always-on basics (every round)
  send "eth_blockNumber"      "eth_blockNumber"      "[]"
  send "eth_chainId"          "eth_chainId"          "[]"
  send "eth_gasPrice"         "eth_gasPrice"         "[]"

  # Immutable cache: same block hash every time → hit after round 1
  send "eth_getBlockByHash"   "eth_getBlockByHash"   "[\"${BLOCK1_HASH}\",false]"

  # Uncacheable write methods: never cached, always goes upstream
  send "eth_sendRawTransaction (uncacheable)" \
    "eth_sendRawTransaction" "[\"0xdeadbeef\"]"

  # Cache-hit burst: 5× same request back-to-back to drive up hit rate
  if [ $((ROUND % 3)) -eq 0 ]; then
    burst "eth_blockNumber ×5 (burst)" "eth_blockNumber" "[]" 5
  fi

  # Varied block queries every other round to generate method-label diversity
  if [ $((ROUND % 2)) -eq 0 ]; then
    send "eth_getBalance"     "eth_getBalance"       "[\"0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045\",\"latest\"]"
    send "eth_call"           "eth_call"             "[{\"to\":\"0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045\",\"data\":\"0x\"},\"latest\"]"
  fi

  # Error-inducing request every 5 rounds (bad params → upstream error)
  if [ $((ROUND % 5)) -eq 0 ]; then
    printf "  ${YELLOW}→ injecting bad request (round %d)${RESET}\n" "$ROUND"
    send "eth_getBlockByNumber (bad)" \
      "eth_getBlockByNumber" "[\"not-a-block\",false]"
  fi

  printf "\n"
  print_metrics
  printf "\n"

  sleep "$INTERVAL"
done
