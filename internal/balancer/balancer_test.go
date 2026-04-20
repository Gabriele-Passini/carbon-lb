package balancer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/carbon-lb/internal/balancer"
	"github.com/carbon-lb/internal/config"
	"github.com/carbon-lb/pkg/models"
	"go.uber.org/zap"
)

// mockCarbonProvider always returns a fixed value per zone
type mockCarbonProvider struct {
	intensities map[string]float64
}

func (m *mockCarbonProvider) Intensity(_ context.Context, zone string) (float64, error) {
	if v, ok := m.intensities[zone]; ok {
		return v, nil
	}
	return 300.0, nil
}
func (m *mockCarbonProvider) Start(_ context.Context) {}

func defaultCfg() config.LBConfig {
	return config.LBConfig{
		Algorithm:          "carbon_aware",
		StatsRefreshPeriod: 5 * time.Second,
		CarbonWeight:       0.5,
		EnergyWeight:       0.3,
		LoadWeight:         0.2,
	}
}

// startMockRegistry returns a test server that serves a fixed node list
func startMockRegistry(nodes []nodeWithStats) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(nodes)
	}))
}

type nodeWithStats struct {
	models.NodeInfo
	Stats models.NodeStats `json:"stats"`
}

func makeNodes() []nodeWithStats {
	return []nodeWithStats{
		{
			NodeInfo: models.NodeInfo{ID: "worker-fr-1", Address: "fr:8081", Region: "FR", Healthy: true},
			Stats:    models.NodeStats{NodeID: "worker-fr-1", CPUUsage: 30, EnergyWatts: 40, ActiveConns: 2},
		},
		{
			NodeInfo: models.NodeInfo{ID: "worker-de-1", Address: "de:8081", Region: "DE", Healthy: true},
			Stats:    models.NodeStats{NodeID: "worker-de-1", CPUUsage: 50, EnergyWatts: 80, ActiveConns: 5},
		},
		{
			NodeInfo: models.NodeInfo{ID: "worker-it-1", Address: "it:8081", Region: "IT", Healthy: true},
			Stats:    models.NodeStats{NodeID: "worker-it-1", CPUUsage: 20, EnergyWatts: 50, ActiveConns: 1},
		},
	}
}

func TestCarbonAwareSelectsGreenestNode(t *testing.T) {
	nodes := makeNodes()
	srv := startMockRegistry(nodes)
	defer srv.Close()

	carbonProv := &mockCarbonProvider{
		intensities: map[string]float64{
			"FR": 60,  // very low - nuclear heavy
			"DE": 400, // high - coal heavy
			"IT": 300, // medium
		},
	}

	log, _ := zap.NewDevelopment()
	bal := balancer.New(defaultCfg(), srv.URL, carbonProv, log)

	// Manually trigger refresh
	ctx := context.Background()
	bal.Start(ctx)
	time.Sleep(200 * time.Millisecond) // let refresh run

	selected, err := bal.SelectNode(models.AlgoCarbonAware)
	if err != nil {
		t.Fatalf("SelectNode failed: %v", err)
	}

	// FR has lowest carbon intensity (60 gCO2/kWh) → should be selected
	if selected.Region != "FR" {
		t.Errorf("expected FR node (lowest carbon), got region=%s node=%s", selected.Region, selected.ID)
	}
}

func TestRoundRobinCyclesAllNodes(t *testing.T) {
	nodes := makeNodes()
	srv := startMockRegistry(nodes)
	defer srv.Close()

	carbonProv := &mockCarbonProvider{}
	log, _ := zap.NewDevelopment()
	bal := balancer.New(defaultCfg(), srv.URL, carbonProv, log)
	bal.Start(context.Background())
	time.Sleep(200 * time.Millisecond)

	seen := map[string]bool{}
	for i := 0; i < 9; i++ {
		n, err := bal.SelectNode(models.AlgoRoundRobin)
		if err != nil {
			t.Fatalf("SelectNode failed at i=%d: %v", i, err)
		}
		seen[n.ID] = true
	}

	// All 3 nodes should have been selected
	if len(seen) != 3 {
		t.Errorf("expected 3 distinct nodes visited, got %d: %v", len(seen), seen)
	}
}

func TestLeastConnectionsSelectsIdleNode(t *testing.T) {
	nodes := makeNodes() // worker-it-1 has ActiveConns=1 (lowest)
	srv := startMockRegistry(nodes)
	defer srv.Close()

	carbonProv := &mockCarbonProvider{}
	log, _ := zap.NewDevelopment()
	bal := balancer.New(defaultCfg(), srv.URL, carbonProv, log)
	bal.Start(context.Background())
	time.Sleep(200 * time.Millisecond)

	selected, err := bal.SelectNode(models.AlgoLeastConn)
	if err != nil {
		t.Fatalf("SelectNode failed: %v", err)
	}
	if selected.ID != "worker-it-1" {
		t.Errorf("expected worker-it-1 (1 conn), got %s (%d conns)", selected.ID, selected.Stats.ActiveConns)
	}
}

func TestNoHealthyNodesReturnsError(t *testing.T) {
	// Registry returns empty list
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]nodeWithStats{})
	}))
	defer srv.Close()

	carbonProv := &mockCarbonProvider{}
	log, _ := zap.NewDevelopment()
	bal := balancer.New(defaultCfg(), srv.URL, carbonProv, log)
	bal.Start(context.Background())
	time.Sleep(200 * time.Millisecond)

	_, err := bal.SelectNode(models.AlgoCarbonAware)
	if err == nil {
		t.Error("expected error when no nodes available, got nil")
	}
}

func TestCarbonScoreOrdering(t *testing.T) {
	// Verify that a node with lower carbon+energy always wins over higher ones
	nodes := []nodeWithStats{
		{
			NodeInfo: models.NodeInfo{ID: "dirty", Region: "DE", Healthy: true, Address: "x:1"},
			Stats:    models.NodeStats{CPUUsage: 80, EnergyWatts: 150, ActiveConns: 10},
		},
		{
			NodeInfo: models.NodeInfo{ID: "clean", Region: "FR", Healthy: true, Address: "y:1"},
			Stats:    models.NodeStats{CPUUsage: 10, EnergyWatts: 20, ActiveConns: 0},
		},
	}
	srv := startMockRegistry(nodes)
	defer srv.Close()

	carbonProv := &mockCarbonProvider{
		intensities: map[string]float64{"DE": 500, "FR": 50},
	}
	log, _ := zap.NewDevelopment()
	bal := balancer.New(defaultCfg(), srv.URL, carbonProv, log)
	bal.Start(context.Background())
	time.Sleep(200 * time.Millisecond)

	selected, err := bal.SelectNode(models.AlgoCarbonAware)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ID != "clean" {
		t.Errorf("expected clean FR node, got %s", selected.ID)
	}
}

func TestRegistryFailoverGraceful(t *testing.T) {
	// Registry is unavailable from the start → balancer should not panic
	carbonProv := &mockCarbonProvider{}
	log, _ := zap.NewDevelopment()
	bal := balancer.New(defaultCfg(), "http://localhost:19999", carbonProv, log)
	bal.Start(context.Background())
	time.Sleep(300 * time.Millisecond)

	_, err := bal.SelectNode(models.AlgoCarbonAware)
	if err == nil {
		t.Error("expected error when registry unreachable")
	}
	// Should not panic — test passes if we reach here
}
