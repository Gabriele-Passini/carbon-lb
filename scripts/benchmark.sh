#!/usr/bin/env bash
# benchmark.sh — runs a benchmark using the carbon_aware scheduling algorithm
# Usage: ./scripts/benchmark.sh [requests] [concurrency]
set -euo pipefail

REQUESTS=${1:-500}
CONCURRENCY=${2:-20}
LB_URL=${LB_URL:-"http://localhost:8080"}
ALGO="carbon_aware"
RESULTS_DIR="./benchmark_results"

mkdir -p "$RESULTS_DIR"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
REPORT="$RESULTS_DIR/report_${TIMESTAMP}.txt"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log()  { echo -e "${CYAN}[BENCH]${NC} $*"; }
ok()   { echo -e "${GREEN}[OK]${NC}    $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC}  $*"; }

for cmd in curl jq bc; do
  command -v "$cmd" &>/dev/null || { echo "missing: $cmd"; exit 1; }
done

check_lb() {
  log "Checking load balancer at $LB_URL/status ..."
  if ! curl -sf "$LB_URL/status" | jq -e '.healthy_nodes > 0' &>/dev/null; then
    warn "Load balancer unreachable or no healthy nodes. Start with: ./scripts/run.sh up"
    exit 1
  fi
  ok "Load balancer ready"
}

run_benchmark() {
  local reqs="$1"
  local conc="$2"

  log "Running $ALGO: $reqs requests, $conc concurrent..."

  local success=0 failed=0 total_ms=0
  declare -A node_map
  local carbon_scores=()

  start_epoch=$(date +%s%N)

  local completed=0
  while [ $completed -lt $reqs ]; do
    local batch=$conc
    if [ $((completed + conc)) -gt $reqs ]; then
      batch=$((reqs - completed))
    fi

    local pids=() tmpfiles=()
    for ((i=0; i<batch; i++)); do
      local tmp
      tmp=$(mktemp)
      tmpfiles+=("$tmp")
      curl -sf \
        -w '\n{"http_code":"%{http_code}","time_ms":%{time_total},"node":"%header{x-served-by}","ci":"%header{x-carbon-intensity}"}' \
        -X POST \
        -H "Content-Type: application/json" \
        -d '{"payload":"benchmark-task"}' \
        "$LB_URL/task?algo=$ALGO" \
        -o /dev/null \
        2>/dev/null >> "$tmp" &
      pids+=($!)
    done

    for pid in "${pids[@]}"; do
      wait "$pid" 2>/dev/null || true
    done

    for tmp in "${tmpfiles[@]}"; do
      if [ -s "$tmp" ]; then
        local meta code t node ci
        meta=$(tail -1 "$tmp" 2>/dev/null || echo "{}")
        code=$(echo "$meta" | jq -r '.http_code // "000"' 2>/dev/null || echo "000")
        t=$(echo "$meta" | jq -r '.time_ms // "0"' 2>/dev/null || echo "0")
        node=$(echo "$meta" | jq -r '.node // "?"' 2>/dev/null || echo "?")
        ci=$(echo "$meta" | jq -r '.ci // "0"' 2>/dev/null || echo "0")

        if [[ "$code" == "200" ]]; then
          ((success++))
          total_ms=$(echo "$total_ms + $t" | bc)
          node_map["$node"]=$((${node_map["$node"]:-0} + 1))
          carbon_scores+=("$ci")
        else
          ((failed++))
        fi
      fi
      rm -f "$tmp"
    done

    completed=$((completed + batch))
    echo -ne "\r  Progress: $completed/$reqs"
  done

  local end_epoch elapsed_s avg_ms rps avg_ci
  end_epoch=$(date +%s%N)
  elapsed_s=$(echo "scale=3; ($end_epoch - $start_epoch) / 1000000000" | bc)
  echo ""

  avg_ms=0
  [ $success -gt 0 ] && avg_ms=$(echo "scale=3; $total_ms / $success" | bc)
  rps=$(echo "scale=1; $success / $elapsed_s" | bc 2>/dev/null || echo "0")

  avg_ci=0
  if [ ${#carbon_scores[@]} -gt 0 ]; then
    local ci_sum=0
    for ci in "${carbon_scores[@]}"; do
      ci_sum=$(echo "$ci_sum + $ci" | bc 2>/dev/null || echo "$ci_sum")
    done
    avg_ci=$(echo "scale=1; $ci_sum / ${#carbon_scores[@]}" | bc 2>/dev/null || echo "0")
  fi

  {
    echo "Algorithm:         $ALGO"
    echo "Requests:          $reqs ($success ok, $failed failed)"
    echo "Duration:          ${elapsed_s}s"
    echo "Throughput:        ${rps} req/s"
    echo "Avg latency:       ${avg_ms}s"
    echo "Avg carbon:        ${avg_ci} gCO₂/kWh"
    echo "Node distribution:"
    for node in "${!node_map[@]}"; do
      local pct
      pct=$(echo "scale=1; ${node_map[$node]} * 100 / $success" | bc 2>/dev/null || echo "?")
      echo "  $node: ${node_map[$node]} ($pct%)"
    done
  } | tee -a "$REPORT"
}

{
  echo "Carbon-Aware Load Balancer — Benchmark Report"
  echo "============================================="
  echo "Timestamp:   $TIMESTAMP"
  echo "Algorithm:   $ALGO"
  echo "Requests:    $REQUESTS"
  echo "Concurrency: $CONCURRENCY"
  echo "LB URL:      $LB_URL"
  echo ""
} | tee "$REPORT"

check_lb
run_benchmark "$REQUESTS" "$CONCURRENCY"

ok "Benchmark complete. Report: $REPORT"
