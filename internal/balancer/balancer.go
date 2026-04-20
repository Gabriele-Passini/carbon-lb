package balancer

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/carbon-lb/internal/carbon"
	"github.com/carbon-lb/internal/config"
	"github.com/carbon-lb/pkg/models"
	"go.uber.org/zap"
)

// NodeState is the balancer's view of a worker node
type NodeState struct {
	models.NodeInfo
	Stats       models.NodeStats
	CarbonScore float64 // computed composite score (lower = greener)
}

// Balancer dispatches tasks to worker nodes
type Balancer struct {
	mu          sync.RWMutex
	nodes       []*NodeState
	rrIndex     atomic.Uint64 // round-robin cursor
	cfg         config.LBConfig
	carbonProv  carbon.Provider
	registryURL string
	log         *zap.Logger
	client      *http.Client
}

func New(cfg config.LBConfig, registryURL string, carbonProv carbon.Provider, log *zap.Logger) *Balancer {
	return &Balancer{
		cfg:         cfg,
		carbonProv:  carbonProv,
		registryURL: registryURL,
		log:         log,
		client:      &http.Client{Timeout: 10 * time.Second},
	}
}

// Start begins periodic node discovery and scoring
func (b *Balancer) Start(ctx context.Context) {
	go b.refreshLoop(ctx)
}

func (b *Balancer) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(b.cfg.StatsRefreshPeriod)
	defer ticker.Stop()
	// initial fetch
	b.refreshNodes(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.refreshNodes(ctx)
		}
	}
}

// refreshNodes fetches node list from registry and computes carbon scores
func (b *Balancer) refreshNodes(ctx context.Context) {
	url := fmt.Sprintf("%s/nodes", b.registryURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		b.log.Error("registry request build failed", zap.Error(err))
		return
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.log.Error("registry unreachable", zap.Error(err))
		return
	}
	defer resp.Body.Close()

	type nodeWithStats struct {
		models.NodeInfo
		Stats models.NodeStats `json:"stats"`
	}
	var raw []nodeWithStats
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		b.log.Error("registry decode failed", zap.Error(err))
		return
	}

	// Fetch carbon intensity for each region
	states := make([]*NodeState, 0, len(raw))
	for _, n := range raw {
		if !n.Healthy {
			continue
		}
		ci, err := b.carbonProv.Intensity(ctx, n.Region)
		if err != nil {
			b.log.Warn("carbon fetch failed", zap.String("region", n.Region), zap.Error(err))
			ci = 300
		}
		n.Stats.CarbonIntensity = ci
		n.Stats.CarbonScore = b.computeScore(n.Stats, ci)
		states = append(states, &NodeState{NodeInfo: n.NodeInfo, Stats: n.Stats, CarbonScore: n.Stats.CarbonScore})
	}

	b.mu.Lock()
	b.nodes = states
	b.mu.Unlock()
	b.log.Debug("nodes refreshed", zap.Int("count", len(states)))
}

// computeScore calculates a composite environmental score.
// Lower score = better choice.
// Score = w_c*(CI/CI_max) + w_e*(W/W_max) + w_l*(load/100)
func (b *Balancer) computeScore(s models.NodeStats, ci float64) float64 {
	const ciMax = 600.0 // gCO2/kWh reference maximum
	const wMax = 200.0  // Watts reference maximum

	carbonNorm := math.Min(ci/ciMax, 1.0)
	energyNorm := math.Min(s.EnergyWatts/wMax, 1.0)
	loadNorm := math.Min(s.CPUUsage/100.0, 1.0)

	return b.cfg.CarbonWeight*carbonNorm +
		b.cfg.EnergyWeight*energyNorm +
		b.cfg.LoadWeight*loadNorm
}

// SelectNode returns the best node according to the configured algorithm
func (b *Balancer) SelectNode(algo models.AlgorithmType) (*NodeState, error) {
	b.mu.RLock()
	nodes := b.nodes
	b.mu.RUnlock()

	if len(nodes) == 0 {
		return nil, fmt.Errorf("no healthy nodes available")
	}

	switch algo {
	case models.AlgoCarbonAware:
		return b.selectCarbonAware(nodes)
	case models.AlgoLeastConn:
		return b.selectLeastConnections(nodes)
	default:
		return b.selectRoundRobin(nodes)
	}
}

// selectCarbonAware picks the node with the lowest composite score
func (b *Balancer) selectCarbonAware(nodes []*NodeState) (*NodeState, error) {
	best := nodes[0]
	for _, n := range nodes[1:] {
		if n.CarbonScore < best.CarbonScore {
			best = n
		}
	}
	return best, nil
}

// selectRoundRobin cycles through nodes in order
func (b *Balancer) selectRoundRobin(nodes []*NodeState) (*NodeState, error) {
	idx := b.rrIndex.Add(1) - 1
	return nodes[int(idx)%len(nodes)], nil
}

// selectLeastConnections picks the node with fewest active connections
func (b *Balancer) selectLeastConnections(nodes []*NodeState) (*NodeState, error) {
	best := nodes[0]
	for _, n := range nodes[1:] {
		if n.Stats.ActiveConns < best.Stats.ActiveConns {
			best = n
		}
	}
	return best, nil
}

// Nodes returns a snapshot of current node states (for metrics/dashboard)
func (b *Balancer) Nodes() []*NodeState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cp := make([]*NodeState, len(b.nodes))
	copy(cp, b.nodes)
	return cp
}
