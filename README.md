# Carbon-Aware Load Balancer

A distributed, carbon and energy-aware load balancer written in Go. Tasks are routed to worker nodes by minimising a composite score that weighs **carbon intensity** (gCO₂/kWh), **power draw** (W), and **CPU load** — making the system prefer greener, more efficient nodes at all times.

---

## Architecture

```
Clients → Load Balancer → Workers (IT, FR, DE, ES, …)
              │                │
         Carbon API       Energy metrics
         (mock / Electricity Maps)   (mock / cAdvisor)
              │
         Service Registry ← heartbeats
              │
         Prometheus + Grafana
```

### Components

| Service | Port | Role |
|---------|------|------|
| `registry` | 9000 | Node registration, heartbeats, discovery |
| `loadbalancer` | 8080 / 2112 | Task dispatch + Prometheus metrics |
| `worker-*` | 8081 | CPU task processor, reports energy metrics |
| `prometheus` | 9090 | Metrics collection |
| `grafana` | 3000 | Dashboards (auto-provisioned) |

---

## Algorithms

### Carbon-Aware (default)
Composite score per node:

```
score = w_carbon × (CI / CI_max) + w_energy × (W / W_max) + w_load × (CPU / 100)
```

Default weights (configurable): `carbon=0.5, energy=0.3, load=0.2`

The node with the **lowest score** receives the next task.

### Round-Robin (baseline)
Cycles through nodes in order — no environmental awareness.

### Least-Connections
Routes to the node with the fewest active connections.

---

## Quick Start

### Prerequisites
- Docker + Docker Compose v2
- Go 1.22+ (for local development/tests)

### Run the stack

```bash
# Build and start everything
./scripts/run.sh up

# Check status
./scripts/run.sh status

# View logs
./scripts/run.sh logs
```

### Send tasks

```bash
# carbon_aware (default)
curl -X POST http://localhost:8080/task \
  -H "Content-Type: application/json" \
  -d '{"payload": "my-task"}'

# round_robin (comparison)
curl -X POST "http://localhost:8080/task?algo=round_robin" \
  -H "Content-Type: application/json" \
  -d '{"payload": "my-task"}'
```

The response headers reveal which node handled the request:

```
X-Served-By: worker-fr-1
X-Algorithm: carbon_aware
X-Carbon-Score: 0.1234
X-Carbon-Intensity: 63.2
```

### Run benchmarks

```bash
# 500 requests, 20 concurrent (default)
./scripts/benchmark.sh

# Custom
./scripts/benchmark.sh 1000 50
```

### Switch algorithm at runtime

```bash
./scripts/run.sh switch round_robin
./scripts/run.sh switch carbon_aware
```

---

## Configuration

All parameters are configurable via `config/config.yaml` **or** environment variables (prefix `CARBONLB_`).

| Parameter | Default | Description |
|-----------|---------|-------------|
| `loadbalancer.algorithm` | `carbon_aware` | Active algorithm |
| `loadbalancer.carbon_weight` | `0.5` | Weight for carbon intensity in score |
| `loadbalancer.energy_weight` | `0.3` | Weight for power draw in score |
| `loadbalancer.load_weight` | `0.2` | Weight for CPU load in score |
| `carbon.provider` | `mock` | `mock` or `electricity_maps` |
| `carbon.api_key` | `""` | Electricity Maps API key |
| `carbon.default_intensity` | `300.0` | Fallback intensity (gCO₂/kWh) |
| `energy.provider` | `mock` | `mock` or `cadvisor` |
| `worker.region` | `IT` | Zone code sent to carbon provider |
| `worker.base_power_watts` | `50` | Simulated base power draw |

### Real Electricity Maps API

```bash
export CARBONLB_CARBON_PROVIDER=electricity_maps
export CARBONLB_CARBON_API_KEY=your_key_here
./scripts/run.sh up
```

### Real Energy Metrics (cAdvisor)

```bash
export CARBONLB_ENERGY_PROVIDER=cadvisor
export CARBONLB_ENERGY_ENDPOINT=http://cadvisor:8080
./scripts/run.sh up
```

---

## Testing

```bash
# All unit tests
go test ./... -v

# Benchmarks
go test ./... -bench=. -benchtime=5s

# With coverage
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

### Test coverage

| Package | Tests |
|---------|-------|
| `internal/balancer` | Algorithm selection, scoring, fault tolerance, concurrent access |
| `internal/carbon` | Mock provider, cache, Electricity Maps mock server, fallback |
| `internal/registry` | Register, heartbeat, TTL expiry, deregister, concurrent nodes |
| `test/` | Integration: CA vs RR comparison, concurrent load, statistical significance benchmark |

---

## Observability

### Prometheus metrics

| Metric | Labels | Description |
|--------|--------|-------------|
| `carbonlb_requests_total` | algorithm, node_id, status | Total dispatched requests |
| `carbonlb_request_duration_seconds` | algorithm, node_id | Request latency histogram |
| `carbonlb_node_carbon_intensity_gco2_kwh` | node_id, region | Live carbon intensity |
| `carbonlb_node_energy_watts` | node_id | Live power draw |
| `carbonlb_node_carbon_score` | node_id | Composite green score |
| `carbonlb_total_carbon_emitted_gco2` | algorithm | Cumulative estimated gCO₂ |
| `carbonlb_healthy_nodes` | — | Number of healthy workers |

### Grafana

Open http://localhost:3000 — the **Carbon-Aware Load Balancer** dashboard auto-loads, showing:
- Requests/s by algorithm
- Estimated CO₂ emitted (carbon_aware vs round_robin)
- Carbon intensity and power per node
- Latency percentiles (p50, p99)
- Node distribution and CPU usage

---

## Fault Tolerance

- **Node crash**: registry TTL (30 s) automatically removes dead nodes; the balancer re-fetches the node list every 5 s and stops routing to the missing node.
- **Registry crash**: balancer keeps its last known node list in-memory and continues serving requests until the registry recovers.
- **Carbon API failure**: falls back to `default_intensity` value from config.
- **Graceful shutdown**: workers deregister from the registry on `SIGTERM`; the load balancer drains in-flight requests within a 10 s timeout.

---

## Libraries used

| Library | Purpose |
|---------|---------|
| `github.com/prometheus/client_golang` | Prometheus metrics exposition |
| `github.com/spf13/viper` | Multi-source configuration (file + env) |
| `go.uber.org/zap` | Structured, high-performance logging |
| `gopkg.in/yaml.v3` | YAML config parsing |

All external HTTP APIs (Electricity Maps, cAdvisor) are accessed through Go's standard `net/http` package.

---

## Project structure

```
carbon-lb/
├── cmd/
│   ├── loadbalancer/main.go   # LB entry point
│   ├── registry/main.go       # Registry entry point
│   └── worker/main.go         # Worker entry point
├── internal/
│   ├── balancer/              # Scoring + selection algorithms
│   ├── carbon/                # Carbon intensity provider + cache
│   ├── config/                # Viper-based configuration
│   ├── energy/                # Energy metrics provider
│   ├── metrics/               # Prometheus metric definitions
│   └── registry/              # Node registry (register/heartbeat/TTL)
├── pkg/models/                # Shared data models
├── config/config.yaml         # Default configuration
├── docker/
│   ├── docker-compose.yml
│   ├── Dockerfile.lb
│   ├── Dockerfile.worker
│   ├── Dockerfile.registry
│   ├── prometheus.yml
│   └── grafana/provisioning/  # Auto-provisioned datasource + dashboard
├── test/integration_test.go   # Integration + benchmark tests
└── scripts/
    ├── run.sh                 # Stack management
    └── benchmark.sh           # Algorithm comparison
```
