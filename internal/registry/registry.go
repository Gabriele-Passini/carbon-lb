package registry

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/carbon-lb/internal/config"
	"github.com/carbon-lb/pkg/models"
	"go.uber.org/zap"
)

// Registry maintains the live set of worker nodes
type Registry struct {
	mu    sync.RWMutex
	nodes map[string]*nodeRecord
	cfg   config.RegistryConfig
	log   *zap.Logger
}

type nodeRecord struct {
	info      models.NodeInfo
	stats     models.NodeStats
	expiresAt time.Time
}

func New(cfg config.RegistryConfig, log *zap.Logger) *Registry {
	r := &Registry{
		nodes: make(map[string]*nodeRecord),
		cfg:   cfg,
		log:   log,
	}
	go r.cleanup()
	return r
}

// RegisterHandler handles POST /register
func (r *Registry) RegisterHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var rr models.RegisterRequest
	if err := json.NewDecoder(req.Body).Decode(&rr); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	r.mu.Lock()
	r.nodes[rr.NodeID] = &nodeRecord{
		info: models.NodeInfo{
			ID:       rr.NodeID,
			Address:  rr.Address,
			Region:   rr.Region,
			Healthy:  true,
			LastSeen: time.Now(),
		},
		expiresAt: time.Now().Add(r.cfg.NodeTTL),
	}
	r.mu.Unlock()

	r.log.Info("node registered", zap.String("id", rr.NodeID), zap.String("addr", rr.Address), zap.String("region", rr.Region))
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "registered"})
}

// HeartbeatHandler handles POST /heartbeat
func (r *Registry) HeartbeatHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var hb models.HeartbeatRequest
	if err := json.NewDecoder(req.Body).Decode(&hb); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if rec, ok := r.nodes[hb.NodeID]; ok {
		rec.info.Healthy = true
		rec.info.LastSeen = time.Now()
		rec.expiresAt = time.Now().Add(r.cfg.NodeTTL)
		rec.stats = models.NodeStats{
			NodeID:      hb.NodeID,
			CPUUsage:    hb.CPUUsage,
			MemUsage:    hb.MemUsage,
			ActiveConns: hb.ActiveConns,
			EnergyWatts: hb.EnergyWatts,
			Timestamp:   time.Now(),
		}
	}
	w.WriteHeader(http.StatusOK)
}

// NodesHandler handles GET /nodes — returns all healthy nodes with stats
func (r *Registry) NodesHandler(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	type nodeWithStats struct {
		models.NodeInfo
		Stats models.NodeStats `json:"stats"`
	}
	result := make([]nodeWithStats, 0, len(r.nodes))
	for _, rec := range r.nodes {
		if rec.info.Healthy {
			result = append(result, nodeWithStats{NodeInfo: rec.info, Stats: rec.stats})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// DeregisterHandler handles DELETE /nodes/{id}
func (r *Registry) DeregisterHandler(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	delete(r.nodes, id)
	r.mu.Unlock()
	r.log.Info("node deregistered", zap.String("id", id))
	w.WriteHeader(http.StatusOK)
}

// HealthHandler returns registry health
func (r *Registry) HealthHandler(w http.ResponseWriter, req *http.Request) {
	r.mu.RLock()
	count := len(r.nodes)
	r.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "nodes": count})
}

// cleanup removes expired nodes periodically
func (r *Registry) cleanup() {
	ticker := time.NewTicker(r.cfg.CleanupPeriod)
	defer ticker.Stop()
	for range ticker.C {
		r.mu.Lock()
		for id, rec := range r.nodes {
			if time.Now().After(rec.expiresAt) {
				r.log.Warn("node expired", zap.String("id", id))
				delete(r.nodes, id)
			}
		}
		r.mu.Unlock()
	}
}
