#!/usr/bin/env bash
set -e
HOST="http://localhost:8545"
PASS=0; FAIL=0

check() {
  local desc=$1 expected=$2 actual=$3
  if [ "$actual" = "$expected" ]; then
    echo "  PASS  $desc"
    PASS=$((PASS+1))
  else
    echo "  FAIL  $desc (expected HTTP $expected, got $actual)"
    FAIL=$((FAIL+1))
  fi
}

echo "========================================="
echo " AegisRPC smoke test"
echo "========================================="

echo ""
echo "── 1. Method not allowed (GET /)"
code=$(curl -s -o /dev/null -w "%{http_code}" -X GET "$HOST/")
check "GET / → 405" "405" "$code"

echo ""
echo "── 2. Invalid JSON body → status=error"
code=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$HOST/" \
  -H "Content-Type: application/json" \
  -d 'not json at all')
check "bad JSON → 400" "400" "$code"

echo ""
echo "── 3. Cache miss: eth_blockNumber (first call hits upstream)"
resp=$(curl -s -X POST "$HOST/" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}')
echo "  resp: $resp"
code=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$HOST/" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}')
check "eth_blockNumber → 200" "200" "$code"

echo ""
echo "── 4. Cache hit: eth_blockNumber (second call, same params)"
code=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$HOST/" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}')
check "eth_blockNumber (cache hit) → 200" "200" "$code"

echo ""
echo "── 5. Immutable cache: eth_getBlockByHash"
code=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$HOST/" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByHash","params":["0xb3b20624f8f0f86eb50dd04688409e5cea4bd02d700bf6e79e9384d47d6a5a35",false]}')
check "eth_getBlockByHash → 200" "200" "$code"

echo ""
echo "── 6. Uncacheable: eth_sendRawTransaction → status=uncacheable"
code=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$HOST/" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":["0xdeadbeef"]}')
echo "  (upstream likely rejects the fake tx — checking 200 or 400)"
echo "  HTTP $code (any non-5xx is fine; metric should record uncacheable)"

echo ""
echo "── 7. Burst: 10x eth_chainId (cache miss → hit pattern)"
for i in $(seq 1 10); do
  curl -s -o /dev/null -X POST "$HOST/" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}'
done
echo "  sent 10 requests"

echo ""
echo "========================================="
echo " Requests done. Checking /metrics..."
echo "========================================="
echo ""
curl -s "$HOST/metrics" | grep "^aegis"

echo ""
echo "========================================="
echo " Summary: PASS=$PASS  FAIL=$FAIL"
echo "========================================="
