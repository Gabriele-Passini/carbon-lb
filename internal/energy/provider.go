package energy

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"time"

	"github.com/carbon-lb/internal/config"
	"go.uber.org/zap"
)

// Provider measures or estimates node energy consumption
type Provider interface {
	PowerWatts(ctx context.Context) (float64, error)
}

// MockProvider simulates energy usage based on CPU load
type MockProvider struct {
	basePower float64
	log       *zap.Logger
}

func NewMockProvider(basePower float64, log *zap.Logger) Provider {
	return &MockProvider{basePower: basePower, log: log}
}

// PowerWatts returns a simulated power draw that correlates with CPU patterns
func (m *MockProvider) PowerWatts(ctx context.Context) (float64, error) {
	// Simulate power: base + load-dependent component with noise
	t := float64(time.Now().Unix())
	load := 0.3 + 0.4*math.Abs(math.Sin(t/60.0)) // 30-70% load cycle
	noise := 1.0 + (rand.Float64()*0.1 - 0.05)
	power := m.basePower * (0.3 + 0.7*load) * noise
	return power, nil
}

// CAdvisorProvider reads container energy metrics from cAdvisor
type CAdvisorProvider struct {
	endpoint  string
	container string
	client    *http.Client
	log       *zap.Logger
}

func NewCAdvisorProvider(cfg config.EnergyConfig, log *zap.Logger) Provider {
	return &CAdvisorProvider{
		endpoint:  cfg.Endpoint,
		container: "worker",
		client:    &http.Client{Timeout: 5 * time.Second},
		log:       log,
	}
}

type cadvisorStats struct {
	Stats []struct {
		CPU struct {
			Usage struct {
				Total uint64 `json:"total"`
			} `json:"usage"`
		} `json:"cpu"`
		Timestamp time.Time `json:"timestamp"`
	} `json:"stats"`
}

// PowerWatts estimates power from cAdvisor CPU metrics
// Real PowerAPI integration would use RAPL counters directly
func (c *CAdvisorProvider) PowerWatts(ctx context.Context) (float64, error) {
	url := fmt.Sprintf("%s/api/v1.3/containers/docker", c.endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("cadvisor unreachable: %w", err)
	}
	defer resp.Body.Close()

	var stats map[string]cadvisorStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return 0, err
	}

	// Estimate power: assume 1W per 10% CPU (simplified TDP model)
	// A real implementation would use Intel RAPL via PowerAPI
	for _, s := range stats {
		if len(s.Stats) >= 2 {
			last := s.Stats[len(s.Stats)-1]
			prev := s.Stats[len(s.Stats)-2]
			dt := last.Timestamp.Sub(prev.Timestamp).Seconds()
			if dt > 0 {
				cpuDelta := float64(last.CPU.Usage.Total-prev.CPU.Usage.Total) / 1e9
				cpuPct := (cpuDelta / dt) * 100
				estimatedPower := 20.0 + (cpuPct * 0.8) // 20W idle + 0.8W per %CPU
				return estimatedPower, nil
			}
		}
	}
	return 50.0, nil // fallback
}

// NewProvider creates the appropriate energy provider based on config
func NewProvider(cfg config.EnergyConfig, basePower float64, log *zap.Logger) Provider {
	switch cfg.Provider {
	case "cadvisor":
		return NewCAdvisorProvider(cfg, log)
	default:
		return NewMockProvider(basePower, log)
	}
}
