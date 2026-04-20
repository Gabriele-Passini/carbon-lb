#!/usr/bin/env bash
# benchmark.sh — runs a comparative benchmark between carbon_aware and round_robin
# Usage: ./scripts/benchmark.sh [requests] [concurrency]
set -euo pipefail

REQUESTS=${1:-500}
CONCURRENCY=${2:-20}
LB_URL=${LB_URL:-"http://localhost:8080"}
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

# Check dependencies
for cmd in curl jq bc; do
  command -v "$cmd" &>/dev/null || { echo "missing: $cmd"; exit 1; }
done

check_lb() {
  log "Checking load balancer at $LB_URL/status ..."
  if ! curl -sf "$LB_URL/status" | jq -e '.healthy_nodes > 0' &>/dev/null; then
    warn "Load balancer unreachable or no healthy nodes. Start with: docker compose up -d"
    exit 1
  fi
  ok "Load balancer ready"
}

# ── run_benchmark <algorithm> <requests> <concurrency> ──────────────────────
run_benchmark() {
  local algo="$1"
  local reqs="$2"
  local conc="$3"
  local out="$RESULTS_DIR/${algo}_${TIMESTAMP}.json"

  log "Running $algo: $reqs requests, $conc concurrent..."

  local success=0 failed=0 total_ms=0
  local node_counts=()
  local carbon_scores=()

  # Use a simple parallel runner (no external tools needed)
  declare -A node_map

  start_epoch=$(date +%s%N)

  # Run requests in batches of $conc
  local completed=0
  while [ $completed -lt $reqs ]; do
    batch=$conc
    if [ $((completed + conc)) -gt $reqs ]; then
      batch=$((reqs - completed))
    fi

    pids=()
    tmpfiles=()
    for ((i=0; i<batch; i++)); do
      tmp=$(mktemp)
      tmpfiles+=("$tmp")
      curl -sf \
        -w '\n{"http_code":"%{http_code}","time_ms":%{time_total},"node":"%header{x-served-by}","algo":"%header{x-algorithm}","ci":"%header{x-carbon-intensity}","score":"%header{x-carbon-score}"}' \
        -X POST \
        -H "Content-Type: application/json" \
        -d '{"payload":"benchmark-task"}' \
        "$LB_URL/task?algo=$algo" \
        -o /dev/null \
        2>/dev/null >> "$tmp" &
      pids+=($!)
    done

    for pid in "${pids[@]}"; do
      wait "$pid" 2>/dev/null || true
    done

    for tmp in "${tmpfiles[@]}"; do
      if [ -s "$tmp" ]; then
        meta=$(tail -1 "$tmp" 2>/dev/null || echo "{}")
        code=$(echo "$meta" | jq -r '.http_code // "000"' 2>/dev/null || echo "000")
        t=$(echo "$meta" | jq -r '.time_ms // "0"' 2>/dev/null || echo "0")
        node=$(echo "$meta" | jq -r '.node // "?"' 2>/dev/null || echo "?")
        ci=$(echo "$meta" | jq -r '.ci // "0"' 2>/dev/null || echo "0")
        score=$(echo "$meta" | jq -r '.score // "0"' 2>/dev/null || echo "0")

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

  end_epoch=$(date +%s%N)
  elapsed_s=$(echo "scale=3; ($end_epoch - $start_epoch) / 1000000000" | bc)
  echo ""

  # Compute stats
  avg_ms=0
  if [ $success -gt 0 ]; then
    avg_ms=$(echo "scale=3; $total_ms / $success" | bc)
  fi
  rps=$(echo "scale=1; $success / $elapsed_s" | bc 2>/dev/null || echo "0")

  # Average carbon intensity
  avg_ci=0
  if [ ${#carbon_scores[@]} -gt 0 ]; then
    ci_sum=0
    for ci in "${carbon_scores[@]}"; do
      ci_sum=$(echo "$ci_sum + $ci" | bc 2>/dev/null || echo "$ci_sum")
    done
    avg_ci=$(echo "scale=1; $ci_sum / ${#carbon_scores[@]}" | bc 2>/dev/null || echo "0")
  fi

  # Write JSON result
  cat > "$out" <<EOF
{
  "algorithm": "$algo",
  "timestamp": "$TIMESTAMP",
  "requests": $reqs,
  "concurrency": $conc,
  "success": $success,
  "failed": $failed,
  "elapsed_s": $elapsed_s,
  "rps": $rps,
  "avg_latency_ms": $avg_ms,
  "avg_carbon_intensity": $avg_ci,
  "node_distribution": {
EOF
  first=true
  for node in "${!node_map[@]}"; do
    if [ "$first" = true ]; then first=false; else echo "," >> "$out"; fi
    echo -n "    \"$node\": ${node_map[$node]}" >> "$out"
  done
  echo "" >> "$out"
  echo "  }" >> "$out"
  echo "}" >> "$out"

  # Print summary
  echo ""
  echo "  ── $algo results ─────────────────────────────"
  echo "  Requests:          $reqs ($success ok, $failed failed)"
  echo "  Duration:          ${elapsed_s}s"
  echo "  Throughput:        ${rps} req/s"
  echo "  Avg latency:       ${avg_ms}s"
  echo "  Avg carbon:        ${avg_ci} gCO₂/kWh"
  echo "  Node distribution:"
  for node in "${!node_map[@]}"; do
    pct=$(echo "scale=1; ${node_map[$node]} * 100 / $success" | bc 2>/dev/null || echo "?")
    echo "    $node: ${node_map[$node]} ($pct%)"
  done
  echo ""

  echo "$avg_ci $avg_ms $rps"
}

# ── main ─────────────────────────────────────────────────────────────────────
{
  echo "Carbon-Aware Load Balancer — Benchmark Report"
  echo "============================================="
  echo "Timestamp:   $TIMESTAMP"
  echo "Requests:    $REQUESTS"
  echo "Concurrency: $CONCURRENCY"
  echo "LB URL:      $LB_URL"
  echo ""
} | tee "$REPORT"

check_lb

log "Starting benchmark comparison..."
echo ""

read -r ca_ci ca_lat ca_rps < <(run_benchmark "carbon_aware" "$REQUESTS" "$CONCURRENCY")
read -r rr_ci rr_lat rr_rps < <(run_benchmark "round_robin"  "$REQUESTS" "$CONCURRENCY")

# Compute reduction
carbon_reduction=0
if (( $(echo "$rr_ci > 0" | bc -l) )); then
  carbon_reduction=$(echo "scale=1; ($rr_ci - $ca_ci) * 100 / $rr_ci" | bc 2>/dev/null || echo "0")
fi

{
  echo "══════════════════════════════════════════════"
  echo "COMPARISON SUMMARY"
  echo "══════════════════════════════════════════════"
  printf "%-30s %-15s %-15s\n" "Metric" "carbon_aware" "round_robin"
  printf "%-30s %-15s %-15s\n" "──────────────────────────────" "─────────────" "─────────────"
  printf "%-30s %-15s %-15s\n" "Avg carbon intensity (gCO₂/kWh)" "$ca_ci" "$rr_ci"
  printf "%-30s %-15s %-15s\n" "Avg latency (s)" "$ca_lat" "$rr_lat"
  printf "%-30s %-15s %-15s\n" "Throughput (req/s)" "$ca_rps" "$rr_rps"
  echo ""
  echo "Carbon reduction:  ${carbon_reduction}% (carbon_aware vs round_robin)"
  echo "Results saved to:  $RESULTS_DIR/"
  echo ""
} | tee -a "$REPORT"

ok "Benchmark complete. Report: $REPORT"
