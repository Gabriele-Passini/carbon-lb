package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carbon-lb/internal/balancer"
	"github.com/carbon-lb/internal/carbon"
	"github.com/carbon-lb/internal/config"
	"github.com/carbon-lb/internal/registry"
	"github.com/carbon-lb/pkg/models"
)

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startRegistry(t *testing.T) (*registry.Registry, *httptest.Server) {
	t.Helper()
	cfg := config.RegistryConfig{
		NodeTTL:       config.Duration(30 * time.Second),
		CleanupPeriod: config.Duration(5 * time.Second),
	}
	reg := registry.New(cfg, testLog())
	mux := http.NewServeMux()
	mux.HandleFunc("/register", reg.RegisterHandler)
	mux.HandleFunc("/heartbeat", reg.HeartbeatHandler)
	mux.HandleFunc("/nodes", reg.NodesHandler)
	mux.HandleFunc("/deregister", reg.DeregisterHandler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return reg, srv
}

type workerSim struct {
	id              string
	region          string
	server          *httptest.Server
	requestCount    atomic.Int64
	carbonIntensity float64
	powerWatts      float64
}

func startWorker(t *testing.T, id, region string, ci, watts float64) *workerSim {
	t.Helper()
	ws := &workerSim{id: id, region: region, carbonIntensity: ci, powerWatts: watts}
	mux := http.NewServeMux()
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		ws.requestCount.Add(1)
		time.Sleep(10 * time.Millisecond)
		json.NewEncoder(w).Encode(models.TaskResult{NodeID: id})
	})
	ws.server = httptest.NewServer(mux)
	t.Cleanup(ws.server.Close)
	return ws
}

func registerWorker(t *testing.T, registryURL string, ws *workerSim) {
	t.Helper()
	rr := models.RegisterRequest{
		NodeID:  ws.id,
		Address: ws.server.Listener.Addr().String(),
		Region:  ws.region,
	}
	body, _ := json.Marshal(rr)
	resp, err := http.Post(registryURL+"/register", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("worker registration failed: %v", err)
	}
	resp.Body.Close()

	hb := models.HeartbeatRequest{
		NodeID:      ws.id,
		CPUUsage:    20,
		EnergyWatts: ws.powerWatts,
		ActiveConns: 0,
	}
	hbBody, _ := json.Marshal(hb)
	hbResp, _ := http.Post(registryURL+"/heartbeat", "application/json", bytes.NewReader(hbBody))
	if hbResp != nil {
		hbResp.Body.Close()
	}
}

type mockCarbonProv struct {
	intensities map[string]float64
}

func (m *mockCarbonProv) Intensity(_ context.Context, zone string) (float64, error) {
	if v, ok := m.intensities[zone]; ok {
		return v, nil
	}
	return 300.0, nil
}
func (m *mockCarbonProv) Start(_ context.Context) {}

func TestIntegration_CarbonAwareVsRoundRobin(t *testing.T) {
	_, regSrv := startRegistry(t)

	workers := []*workerSim{
		startWorker(t, "worker-fr", "FR", 60, 40),
		startWorker(t, "worker-de", "DE", 400, 80),
		startWorker(t, "worker-it", "IT", 300, 60),
		startWorker(t, "worker-es", "ES", 200, 50),
	}

	for _, ws := range workers {
		registerWorker(t, regSrv.URL, ws)
	}

	carbonProv := &mockCarbonProv{
		intensities: map[string]float64{
			"FR": 60, "DE": 400, "IT": 300, "ES": 200,
		},
	}

	lbCfg := config.LBConfig{
		StatsRefreshPeriod: config.Duration(1 * time.Second),
		CarbonWeight:       0.5, EnergyWeight: 0.3, LoadWeight: 0.2,
	}

	balCA := balancer.New(lbCfg, regSrv.URL, carbonProv, testLog())
	balCA.Start(context.Background())
	time.Sleep(300 * time.Millisecond)

	balRR := balancer.New(lbCfg, regSrv.URL, carbon.NewProvider(
		config.CarbonConfig{Provider: "mock", DefaultIntensity: 300, RefreshPeriod: config.Duration(60 * time.Second)},
		testLog(),
	), testLog())
	balRR.Start(context.Background())
	time.Sleep(300 * time.Millisecond)

	const totalRequests = 100

	var caCarbon float64
	for i := 0; i < totalRequests; i++ {
		n, err := balCA.SelectNode(models.AlgoCarbonAware)
		if err != nil {
			t.Fatalf("CA select failed: %v", err)
		}
		caCarbon += n.Stats.CarbonIntensity
	}
	caAvg := caCarbon / float64(totalRequests)

	var rrCarbon float64
	for i := 0; i < totalRequests; i++ {
		n, err := balRR.SelectNode(models.AlgoRoundRobin)
		if err != nil {
			t.Fatalf("RR select failed: %v", err)
		}
		ci := carbonProv.intensities[n.Region]
		rrCarbon += ci
	}
	rrAvg := rrCarbon / float64(totalRequests)

	t.Logf("Carbon-aware avg carbon intensity: %.1f gCO2/kWh", caAvg)
	t.Logf("Round-robin avg carbon intensity:  %.1f gCO2/kWh", rrAvg)
	t.Logf("Carbon reduction: %.1f%% (CA vs RR)", (rrAvg-caAvg)/rrAvg*100)

	if caAvg >= rrAvg {
		t.Errorf("expected carbon_aware (%.1f) < round_robin (%.1f)", caAvg, rrAvg)
	}
}

func TestConcurrentRequests(t *testing.T) {
	_, regSrv := startRegistry(t)

	workers := []*workerSim{
		startWorker(t, "w1", "FR", 60, 40),
		startWorker(t, "w2", "IT", 300, 60),
		startWorker(t, "w3", "DE", 400, 80),
	}
	for _, ws := range workers {
		registerWorker(t, regSrv.URL, ws)
	}

	carbonProv := &mockCarbonProv{intensities: map[string]float64{"FR": 60, "IT": 300, "DE": 400}}
	lbCfg := config.LBConfig{
		StatsRefreshPeriod: config.Duration(1 * time.Second),
		CarbonWeight:       0.5, EnergyWeight: 0.3, LoadWeight: 0.2,
	}
	bal := balancer.New(lbCfg, regSrv.URL, carbonProv, testLog())
	bal.Start(context.Background())
	time.Sleep(200 * time.Millisecond)

	const goroutines = 20
	const requestsPerGoroutine = 50
	var wg sync.WaitGroup
	var errors atomic.Int64

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < requestsPerGoroutine; i++ {
				if _, err := bal.SelectNode(models.AlgoCarbonAware); err != nil {
					errors.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	total := goroutines * requestsPerGoroutine
	errs := errors.Load()
	t.Logf("Concurrent test: %d requests, %d errors", total, errs)
	if errs > 0 {
		t.Errorf("expected 0 errors, got %d", errs)
	}
}

func BenchmarkCarbonAware(b *testing.B) {
	_, regSrv := benchRegistry(b)
	benchWorkers(b, regSrv.URL)

	carbonProv := &mockCarbonProv{intensities: map[string]float64{"FR": 60, "DE": 400, "IT": 300, "ES": 200}}
	lbCfg := config.LBConfig{
		StatsRefreshPeriod: config.Duration(1 * time.Second),
		CarbonWeight:       0.5, EnergyWeight: 0.3, LoadWeight: 0.2,
	}
	bal := balancer.New(lbCfg, regSrv.URL, carbonProv, testLog())
	bal.Start(context.Background())
	time.Sleep(200 * time.Millisecond)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			bal.SelectNode(models.AlgoCarbonAware)
		}
	})
}

func BenchmarkRoundRobin(b *testing.B) {
	_, regSrv := benchRegistry(b)
	benchWorkers(b, regSrv.URL)

	lbCfg := config.LBConfig{
		StatsRefreshPeriod: config.Duration(1 * time.Second),
		CarbonWeight:       0.5, EnergyWeight: 0.3, LoadWeight: 0.2,
	}
	bal := balancer.New(lbCfg, regSrv.URL, carbon.NewProvider(
		config.CarbonConfig{Provider: "mock", DefaultIntensity: 300, RefreshPeriod: config.Duration(60 * time.Second)},
		testLog(),
	), testLog())
	bal.Start(context.Background())
	time.Sleep(200 * time.Millisecond)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			bal.SelectNode(models.AlgoRoundRobin)
		}
	})
}

func benchRegistry(b *testing.B) (*registry.Registry, *httptest.Server) {
	b.Helper()
	cfg := config.RegistryConfig{
		NodeTTL:       config.Duration(30 * time.Second),
		CleanupPeriod: config.Duration(5 * time.Second),
	}
	reg := registry.New(cfg, testLog())
	mux := http.NewServeMux()
	mux.HandleFunc("/register", reg.RegisterHandler)
	mux.HandleFunc("/heartbeat", reg.HeartbeatHandler)
	mux.HandleFunc("/nodes", reg.NodesHandler)
	srv := httptest.NewServer(mux)
	b.Cleanup(srv.Close)
	return reg, srv
}

func benchWorkers(b *testing.B, registryURL string) []*workerSim {
	b.Helper()
	regions := []struct {
		id, region string
		ci, w      float64
	}{
		{"w-fr", "FR", 60, 40},
		{"w-de", "DE", 400, 80},
		{"w-it", "IT", 300, 60},
		{"w-es", "ES", 200, 50},
	}
	workers := make([]*workerSim, len(regions))
	for i, r := range regions {
		ws := startWorkerB(b, r.id, r.region, r.ci, r.w)
		workers[i] = ws
		rr := models.RegisterRequest{NodeID: r.id, Address: ws.server.Listener.Addr().String(), Region: r.region}
		body, _ := json.Marshal(rr)
		resp, _ := http.Post(registryURL+"/register", "application/json", bytes.NewReader(body))
		if resp != nil {
			resp.Body.Close()
		}
		hb := models.HeartbeatRequest{NodeID: r.id, CPUUsage: 20, EnergyWatts: r.w}
		hbBody, _ := json.Marshal(hb)
		hbResp, _ := http.Post(registryURL+"/heartbeat", "application/json", bytes.NewReader(hbBody))
		if hbResp != nil {
			hbResp.Body.Close()
		}
	}
	return workers
}

func startWorkerB(b *testing.B, id, region string, ci, watts float64) *workerSim {
	b.Helper()
	ws := &workerSim{id: id, region: region, carbonIntensity: ci, powerWatts: watts}
	mux := http.NewServeMux()
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		ws.requestCount.Add(1)
		json.NewEncoder(w).Encode(models.TaskResult{NodeID: id})
	})
	ws.server = httptest.NewServer(mux)
	b.Cleanup(ws.server.Close)
	return ws
}

func CarbonReductionPercent(caIntensities, rrIntensities []float64) float64 {
	if len(caIntensities) == 0 || len(rrIntensities) == 0 {
		return 0
	}
	caSum, rrSum := 0.0, 0.0
	for _, v := range caIntensities {
		caSum += v
	}
	for _, v := range rrIntensities {
		rrSum += v
	}
	rrAvg := rrSum / float64(len(rrIntensities))
	caAvg := caSum / float64(len(caIntensities))
	return (rrAvg - caAvg) / rrAvg * 100
}

func stdDev(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))
	variance := 0.0
	for _, v := range values {
		d := v - mean
		variance += d * d
	}
	return math.Sqrt(variance / float64(len(values)))
}

func TestCarbonReductionIsStatisticallySignificant(t *testing.T) {
	regions := []struct {
		region string
		ci     float64
	}{
		{"FR", 60}, {"DE", 400}, {"IT", 300}, {"ES", 200},
	}

	const n = 1000
	caCI := make([]float64, n)
	rrCI := make([]float64, n)

	for i := range caCI {
		caCI[i] = regions[0].ci
	}
	for i := range rrCI {
		rrCI[i] = regions[i%len(regions)].ci
	}

	reduction := CarbonReductionPercent(caCI, rrCI)
	t.Logf("Simulated carbon reduction: %.1f%%", reduction)
	t.Logf("CA stddev: %.1f, RR stddev: %.1f", stdDev(caCI), stdDev(rrCI))

	if reduction < 50 {
		t.Errorf("expected >=50%% carbon reduction, got %.1f%%", reduction)
	}
}

var _ = fmt.Sprintf
