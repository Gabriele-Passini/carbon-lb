#!/usr/bin/env bash
# cycle.sh — invia cicli di task CPU e memory al load balancer
# Uso: ./scripts/cycle.sh [cicli] [concorrenza]
#   cicli       numero di cicli (default 20)
#   concorrenza richieste parallele per ciclo (default 3)
set -euo pipefail

CYCLES=${1:-20}
CONCURRENCY=${2:-3}
LB_URL=${LB_URL:-"http://localhost:8080"}
STATE_URL=${STATE_URL:-"http://localhost:9001"}

GREEN='\033[0;32m'; CYAN='\033[0;36m'; YELLOW='\033[1;33m'; NC='\033[0m'

log()  { echo -e "${CYAN}[cycle]${NC} $*"; }
ok()   { echo -e "${GREEN}[ok]${NC}    $*"; }
warn() { echo -e "${YELLOW}[warn]${NC}  $*"; }

# Nomi dei contatori usati nei task memory
COUNTERS=(ordini visite pagamenti)

send_cpu() {
  local out
  out=$(curl -sf -X POST -H "Content-Type: application/json" \
    -d '{"payload":"cycle-task"}' \
    -w '\t%{http_code}\t%{time_total}' \
    "$LB_URL/task" 2>/dev/null) || { warn "CPU task fallito"; return; }

  local body code ms
  body=$(echo "$out" | cut -f1)
  code=$(echo "$out" | cut -f2)
  ms=$(echo "$out"  | cut -f3)

  local node result
  node=$(echo "$body"   | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('node_id','?'))" 2>/dev/null || echo "?")
  result=$(echo "$body" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('result','?'))"  2>/dev/null || echo "?")

  echo -e "  ${GREEN}CPU${NC}  → node=${node}  result=${result}  http=${code}  time=${ms}s"
}

send_memory() {
  local counter=${COUNTERS[$((RANDOM % ${#COUNTERS[@]}))]}
  local out
  out=$(curl -sf -X POST -H "Content-Type: application/json" \
    -d "{\"type\":\"memory\",\"payload\":\"${counter}\"}" \
    -w '\t%{http_code}\t%{time_total}' \
    "$LB_URL/task" 2>/dev/null) || { warn "Memory task fallito (counter=${counter})"; return; }

  local body code ms
  body=$(echo "$out" | cut -f1)
  code=$(echo "$out" | cut -f2)
  ms=$(echo "$out"  | cut -f3)

  local node result
  node=$(echo "$body"   | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('node_id','?'))" 2>/dev/null || echo "?")
  result=$(echo "$body" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('result','?'))"  2>/dev/null || echo "?")

  echo -e "  ${YELLOW}MEM${NC}  → node=${node}  result=${result}  http=${code}  time=${ms}s"
}

show_counters() {
  echo ""
  log "Stato contatori su state server:"
  curl -sf "$STATE_URL/counters" 2>/dev/null | \
    python3 -c "
import json,sys
data=json.load(sys.stdin)
if not data:
    print('  (nessun contatore ancora)')
else:
    for k,v in sorted(data.items()):
        print(f'  {k}: {v}')
" 2>/dev/null || warn "State server non raggiungibile"
  echo ""
}

# Verifica che il LB sia attivo
if ! curl -sf "$LB_URL/status" | python3 -c "import json,sys; d=json.load(sys.stdin); exit(0 if d.get('healthy_nodes',0)>0 else 1)" 2>/dev/null; then
  warn "Load balancer non raggiungibile o nessun nodo sano. Avvia prima con: ./scripts/run.sh up"
  exit 1
fi

log "Avvio $CYCLES cicli con concorrenza $CONCURRENCY"
log "Ogni ciclo: ${CONCURRENCY} task CPU + ${CONCURRENCY} task memory"
echo ""

for cycle in $(seq 1 "$CYCLES"); do
  echo -e "${CYAN}── Ciclo $cycle/$CYCLES ──────────────────────────────${NC}"

  # Task CPU in parallelo
  pids=()
  for ((i=0; i<CONCURRENCY; i++)); do
    send_cpu &
    pids+=($!)
  done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done

  # Task memory in parallelo
  pids=()
  for ((i=0; i<CONCURRENCY; i++)); do
    send_memory &
    pids+=($!)
  done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done

  # Ogni 5 cicli mostra i contatori
  if (( cycle % 5 == 0 )); then
    show_counters
  fi

  sleep 0.5
done

show_counters
ok "Cicli completati."
