#!/usr/bin/env bash
# run.sh — build, start, and manage the carbon-aware load balancer stack
set -euo pipefail

COMPOSE_INFRA="docker/docker-compose.yml"
COMPOSE_WORKERS="docker/docker-compose.workers.yml"
WORKERS_COUNT_FILE=".workers_count"
GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; RED='\033[0;31m'; NC='\033[0m'

log()  { echo -e "${CYAN}[INFO]${NC}  $*"; }
ok()   { echo -e "${GREEN}[OK]${NC}    $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC}  $*"; }
err()  { echo -e "${RED}[ERR]${NC}   $*"; exit 1; }

compose() {
  if [[ -f "$COMPOSE_WORKERS" ]]; then
    docker compose -f "$COMPOSE_INFRA" -f "$COMPOSE_WORKERS" "$@"
  else
    docker compose -f "$COMPOSE_INFRA" "$@"
  fi
}

# Region pool cycles if NUM_WORKERS > pool size
_REGIONS=(IT IT FR DE ES GB IT FR DE ES)
_POWERS=(60 55 50 70 45 65 58 48 68 42)

generate_workers() {
  local n="${1:-5}"
  local pool=${#_REGIONS[@]}
  {
    echo "services:"
    for i in $(seq 1 "$n"); do
      local idx=$(( (i - 1) % pool ))
      local region="${_REGIONS[$idx]}"
      local power="${_POWERS[$idx]}"
      local region_slug
      region_slug=$(echo "$region" | tr '[:upper:]' '[:lower:]' | tr -d '-')
      local name="worker-${region_slug}-$i"
      cat <<EOF

  $name:
    build:
      context: ..
      dockerfile: docker/Dockerfile.worker
    container_name: $name
    networks: [carbonlb]
    environment:
      - CONFIG_PATH=/config/config.json
      - NODE_ID=$name
      - NODE_REGION=$region
      - LISTEN_ADDR=:8081
      - NODE_ADDRESS=$name:8081
      - REGISTRY_URL=http://registry:9000
      - STATE_URL=http://stateserver:9001
      - CARBONLB_WORKER_BASE_POWER_WATTS=$power
    volumes:
      - ../config:/config:ro
    depends_on:
      registry:
        condition: service_healthy
      stateserver:
        condition: service_healthy
    restart: unless-stopped
EOF
    done
  } > "$COMPOSE_WORKERS"
  log "Generated $n worker(s) → $COMPOSE_WORKERS"
}

usage() {
  echo "Usage: $0 <command> [args]"
  echo ""
  echo "Commands:"
  echo "  up [N]    Build and start all services with N workers (default: 5)"
  echo "  down      Stop all services"
  echo "  logs      Tail logs (all services)"
  echo "  status    Show service status and node list"
  echo "  test      Run Go unit tests"
  echo "  bench     Run benchmark (requires running stack)"
  echo "  switch    Switch LB algorithm (e.g.: ./run.sh switch round_robin)"
  echo "  scale N   Restart workers scaled to N (e.g.: ./run.sh scale 6)"
  echo "  clean     Remove containers, volumes, and build cache"
}

cmd_up() {
  local n="${1:-5}"
  generate_workers "$n"
  echo "$n" > "$WORKERS_COUNT_FILE"
  log "Building and starting stack with $n worker(s)..."
  compose up -d --build
  log "Waiting for services to be ready..."
  sleep 5
  cmd_status
  ok "Stack running!"
  echo ""
  echo "  Load Balancer:  http://localhost:8080"
  echo "  Registry:       http://localhost:9000"
  echo "  Prometheus:     http://localhost:9090"
  echo "  Grafana:        http://localhost:3000 (admin/admin)"
  echo "  Metrics:        http://localhost:2112/metrics"
}

cmd_down() {
  log "Stopping stack..."
  compose down
  ok "Stopped."
}

cmd_logs() {
  compose logs -f --tail=50 "${@:-}"
}

cmd_status() {
  echo ""
  log "Service status:"
  compose ps
  echo ""
  log "Registered nodes:"
  curl -sf http://localhost:9000/nodes 2>/dev/null | \
    python3 -c "
import json,sys
nodes=json.load(sys.stdin)
print(f'  {\"ID\":<20} {\"Region\":<8} {\"Healthy\":<8} {\"Energy(W)\":<12} {\"CPU%\":<8}')
print('  ' + '-'*60)
for n in nodes:
    s=n.get('stats',{})
    print(f\"  {n['id']:<20} {n['region']:<8} {str(n['healthy']):<8} {s.get('energy_watts',0):<12.1f} {s.get('cpu_usage',0):<8.1f}\")
" 2>/dev/null || warn "Registry not available yet"
  echo ""
}

cmd_test() {
  log "Running Go tests..."
  go test ./... -v -timeout 60s -count=1 2>&1 | grep -E '(PASS|FAIL|RUN|carbon|error|---)'
  ok "Tests complete."
}

cmd_bench() {
  log "Running benchmark..."
  bash scripts/benchmark.sh "${@:-500 20}"
}

cmd_switch() {
  local algo="${1:-carbon_aware}"
  log "Switching algorithm to: $algo"
  compose stop loadbalancer
  CARBONLB_LOADBALANCER_ALGORITHM=$algo compose up -d loadbalancer
  sleep 2
  ok "Algorithm switched to: $algo"
}

cmd_scale() {
  local count="${1:-3}"
  if [[ -f "$WORKERS_COUNT_FILE" ]]; then
    local max
    max=$(cat "$WORKERS_COUNT_FILE")
    if [ "$count" -gt "$max" ]; then
      err "Cannot scale to $count: startup limit is $max worker(s)."
    fi
  fi
  log "Scaling workers to $count..."
  generate_workers "$count"
  compose up -d --build --remove-orphans
  ok "Workers scaled to $count."
}

cmd_clean() {
  warn "This will remove all containers, volumes, and build cache!"
  read -rp "Continue? [y/N] " ans
  [[ "$ans" == "y" ]] || exit 0
  compose down -v --rmi all
  ok "Cleaned."
}

case "${1:-}" in
  up)     shift; cmd_up "${1:-5}" ;;
  down)   cmd_down ;;
  logs)   shift; cmd_logs "$@" ;;
  status) cmd_status ;;
  test)   cmd_test ;;
  bench)  shift; cmd_bench "$@" ;;
  switch) shift; cmd_switch "$@" ;;
  scale)  shift; cmd_scale "$@" ;;
  clean)  cmd_clean ;;
  *)      usage; exit 1 ;;
esac
