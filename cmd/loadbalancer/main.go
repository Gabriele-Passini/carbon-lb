package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/carbon-lb/internal/balancer"
	"github.com/carbon-lb/internal/carbon"
	"github.com/carbon-lb/internal/config"
	"github.com/carbon-lb/internal/metrics"
	"github.com/carbon-lb/pkg/models"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

func main() {
	log, _ := zap.NewProduction()
	defer log.Sync()

	cfgPath := os.Getenv("CONFIG_PATH")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatal("config load failed", zap.Error(err))
	}

	registryURL := os.Getenv("REGISTRY_URL")
	if registryURL == "" {
		registryURL = "http://localhost:9000"
	}

	carbonProv := carbon.NewProvider(cfg.Carbon, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	carbonProv.Start(ctx)

	bal := balancer.New(cfg.LB, registryURL, carbonProv, log)
	bal.Start(ctx)

	mux := http.NewServeMux()

	// Main proxy endpoint
	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		handleTask(w, r, bal, cfg, log)
	})

	// Status endpoint
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		nodes := bal.Nodes()
		metrics.HealthyNodes.Set(float64(len(nodes)))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"algorithm":     cfg.LB.Algorithm,
			"healthy_nodes": len(nodes),
			"nodes":         nodes,
		})
	})

	// Prometheus metrics
	metricsServer := &http.Server{
		Addr:    cfg.Metrics.Address,
		Handler: promhttp.Handler(),
	}
	go func() {
		log.Info("metrics server", zap.String("addr", cfg.Metrics.Address))
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server error", zap.Error(err))
		}
	}()

	// Start metrics updater
	go updateMetricsLoop(ctx, bal, log)

	server := &http.Server{
		Addr:    cfg.LB.Address,
		Handler: mux,
	}

	go func() {
		log.Info("load balancer starting", zap.String("addr", cfg.LB.Address), zap.String("algorithm", cfg.LB.Algorithm))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("server error", zap.Error(err))
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("shutting down...")
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	server.Shutdown(shutCtx)
	metricsServer.Shutdown(shutCtx)
}

func handleTask(w http.ResponseWriter, r *http.Request, bal *balancer.Balancer, cfg *config.Config, log *zap.Logger) {
	start := time.Now()

	// Determine algorithm: from query param or config
	algo := models.AlgorithmType(cfg.LB.Algorithm)
	if q := r.URL.Query().Get("algo"); q != "" {
		algo = models.AlgorithmType(q)
	}

	node, err := bal.SelectNode(algo)
	if err != nil {
		http.Error(w, "no nodes available", http.StatusServiceUnavailable)
		metrics.RequestsTotal.WithLabelValues(string(algo), "none", "error").Inc()
		return
	}

	// Forward request to selected worker
	targetURL := fmt.Sprintf("http://%s%s", node.Address, r.URL.Path)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "proxy request failed", http.StatusInternalServerError)
		return
	}
	for k, v := range r.Header {
		proxyReq.Header[k] = v
	}
	proxyReq.Header.Set("X-Forwarded-By", "carbon-lb")
	proxyReq.Header.Set("X-Node-ID", node.ID)
	proxyReq.Header.Set("X-Algorithm", string(algo))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Error("worker request failed", zap.String("node", node.ID), zap.Error(err))
		http.Error(w, "worker error", http.StatusBadGateway)
		metrics.RequestsTotal.WithLabelValues(string(algo), node.ID, "error").Inc()
		return
	}
	defer resp.Body.Close()

	duration := time.Since(start)

	// Update metrics
	metrics.RequestsTotal.WithLabelValues(string(algo), node.ID, fmt.Sprintf("%d", resp.StatusCode)).Inc()
	metrics.RequestDuration.WithLabelValues(string(algo), node.ID).Observe(duration.Seconds())

	// Estimate carbon emitted for this request
	// Carbon (gCO2) ≈ energy (kWh) × intensity (gCO2/kWh)
	// Energy (kWh) ≈ power (W) × duration (h) / 1000
	energyKWh := (node.Stats.EnergyWatts * duration.Hours()) / 1000.0
	carbonGrams := energyKWh * node.Stats.CarbonIntensity
	metrics.TotalCarbonEmitted.WithLabelValues(string(algo)).Add(carbonGrams)

	// Copy response
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Set("X-Served-By", node.ID)
	w.Header().Set("X-Algorithm", string(algo))
	w.Header().Set("X-Carbon-Score", fmt.Sprintf("%.4f", node.CarbonScore))
	w.Header().Set("X-Carbon-Intensity", fmt.Sprintf("%.1f", node.Stats.CarbonIntensity))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	log.Info("task dispatched",
		zap.String("algo", string(algo)),
		zap.String("node", node.ID),
		zap.String("region", node.Region),
		zap.Float64("carbon_score", node.CarbonScore),
		zap.Float64("carbon_intensity", node.Stats.CarbonIntensity),
		zap.Float64("energy_w", node.Stats.EnergyWatts),
		zap.Duration("duration", duration),
	)
}

func updateMetricsLoop(ctx context.Context, bal *balancer.Balancer, log *zap.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nodes := bal.Nodes()
			metrics.HealthyNodes.Set(float64(len(nodes)))
			for _, n := range nodes {
				metrics.NodeCarbonIntensity.WithLabelValues(n.ID, n.Region).Set(n.Stats.CarbonIntensity)
				metrics.NodeEnergyWatts.WithLabelValues(n.ID).Set(n.Stats.EnergyWatts)
				metrics.NodeCarbonScore.WithLabelValues(n.ID).Set(n.CarbonScore)
				metrics.NodeActiveConns.WithLabelValues(n.ID).Set(float64(n.Stats.ActiveConns))
				metrics.NodeCPUUsage.WithLabelValues(n.ID).Set(n.Stats.CPUUsage)
			}
		}
	}
}
