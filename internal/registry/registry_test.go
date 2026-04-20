package registry_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/carbon-lb/internal/config"
	"github.com/carbon-lb/internal/registry"
	"github.com/carbon-lb/pkg/models"
	"go.uber.org/zap"
)

func newTestRegistry() *registry.Registry {
	cfg := config.RegistryConfig{
		NodeTTL:       10 * time.Second,
		CleanupPeriod: 1 * time.Second,
	}
	log, _ := zap.NewDevelopment()
	return registry.New(cfg, log)
}

func doPost(t *testing.T, handler http.HandlerFunc, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

func doGet(t *testing.T, handler http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

func TestRegisterAndListNodes(t *testing.T) {
	reg := newTestRegistry()

	rr := models.RegisterRequest{NodeID: "w1", Address: "host:8081", Region: "IT"}
	resp := doPost(t, reg.RegisterHandler, "/register", rr)
	if resp.Code != http.StatusOK {
		t.Fatalf("register returned %d", resp.Code)
	}

	listResp := doGet(t, reg.NodesHandler, "/nodes")
	if listResp.Code != http.StatusOK {
		t.Fatalf("nodes returned %d", listResp.Code)
	}

	var nodes []interface{}
	json.NewDecoder(listResp.Body).Decode(&nodes)
	if len(nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(nodes))
	}
}

func TestHeartbeatUpdatesStats(t *testing.T) {
	reg := newTestRegistry()

	// Register first
	rr := models.RegisterRequest{NodeID: "w1", Address: "host:8081", Region: "IT"}
	doPost(t, reg.RegisterHandler, "/register", rr)

	// Send heartbeat with stats
	hb := models.HeartbeatRequest{
		NodeID:      "w1",
		CPUUsage:    75.0,
		EnergyWatts: 90.0,
		ActiveConns: 3,
	}
	resp := doPost(t, reg.HeartbeatHandler, "/heartbeat", hb)
	if resp.Code != http.StatusOK {
		t.Fatalf("heartbeat returned %d", resp.Code)
	}

	// List nodes and verify stats updated
	listResp := doGet(t, reg.NodesHandler, "/nodes")
	var raw []json.RawMessage
	json.NewDecoder(listResp.Body).Decode(&raw)
	if len(raw) == 0 {
		t.Fatal("no nodes returned")
	}

	var node struct {
		ID    string `json:"id"`
		Stats struct {
			CPUUsage    float64 `json:"cpu_usage"`
			EnergyWatts float64 `json:"energy_watts"`
		} `json:"stats"`
	}
	json.Unmarshal(raw[0], &node)
	if node.Stats.CPUUsage != 75.0 {
		t.Errorf("expected cpu_usage=75, got %f", node.Stats.CPUUsage)
	}
	if node.Stats.EnergyWatts != 90.0 {
		t.Errorf("expected energy_watts=90, got %f", node.Stats.EnergyWatts)
	}
}

func TestDeregisterRemovesNode(t *testing.T) {
	reg := newTestRegistry()

	rr := models.RegisterRequest{NodeID: "w1", Address: "host:8081", Region: "IT"}
	doPost(t, reg.RegisterHandler, "/register", rr)

	// Deregister
	req := httptest.NewRequest(http.MethodDelete, "/deregister?id=w1", nil)
	w := httptest.NewRecorder()
	reg.DeregisterHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("deregister returned %d", w.Code)
	}

	// Node list should be empty
	listResp := doGet(t, reg.NodesHandler, "/nodes")
	var nodes []interface{}
	json.NewDecoder(listResp.Body).Decode(&nodes)
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes after deregister, got %d", len(nodes))
	}
}

func TestMultipleNodesRegistration(t *testing.T) {
	reg := newTestRegistry()

	for i, id := range []string{"w1", "w2", "w3"} {
		rr := models.RegisterRequest{NodeID: id, Address: "host:808" + string(rune('1'+i)), Region: "IT"}
		resp := doPost(t, reg.RegisterHandler, "/register", rr)
		if resp.Code != http.StatusOK {
			t.Fatalf("register %s returned %d", id, resp.Code)
		}
	}

	listResp := doGet(t, reg.NodesHandler, "/nodes")
	var nodes []interface{}
	json.NewDecoder(listResp.Body).Decode(&nodes)
	if len(nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(nodes))
	}
}

func TestNodeTTLExpiry(t *testing.T) {
	cfg := config.RegistryConfig{
		NodeTTL:       200 * time.Millisecond, // very short TTL for test
		CleanupPeriod: 100 * time.Millisecond,
	}
	log, _ := zap.NewDevelopment()
	reg := registry.New(cfg, log)

	rr := models.RegisterRequest{NodeID: "w1", Address: "host:8081", Region: "IT"}
	doPost(t, reg.RegisterHandler, "/register", rr)

	// Wait for TTL to expire + cleanup to run
	time.Sleep(500 * time.Millisecond)

	listResp := doGet(t, reg.NodesHandler, "/nodes")
	var nodes []interface{}
	json.NewDecoder(listResp.Body).Decode(&nodes)
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes after TTL expiry, got %d", len(nodes))
	}
}

func TestHealthEndpoint(t *testing.T) {
	reg := newTestRegistry()
	resp := doGet(t, reg.HealthHandler, "/health")
	if resp.Code != http.StatusOK {
		t.Fatalf("health returned %d", resp.Code)
	}
	var body map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
}
