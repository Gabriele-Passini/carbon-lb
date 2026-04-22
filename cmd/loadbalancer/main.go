package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfgPath := os.Getenv("CONFIG_PATH")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("config load failed", "error", err)
		os.Exit(1)
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

	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		handleTask(w, r, bal, cfg, log)
	})

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

	metricsServer := &http.Server{
		Addr:    cfg.Metrics.Address,
		Handler: promhttp.Handler(),
	}
	go func() {
		log.Info("metrics server starting", "addr", cfg.Metrics.Address)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server error", "error", err)
		}
	}()

	go updateMetricsLoop(ctx, bal)

	server := &http.Server{
		Addr:    cfg.LB.Address,
		Handler: mux,
	}

	go func() {
		log.Info("load balancer starting", "addr", cfg.LB.Address, "algorithm", cfg.LB.Algorithm)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

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

const maxRetries = 3

func handleTask(w http.ResponseWriter, r *http.Request, bal *balancer.Balancer, cfg *config.Config, log *slog.Logger) {
	start := time.Now()

	algo := models.AlgorithmType(cfg.LB.Algorithm)
	if q := r.URL.Query().Get("algo"); q != "" {
		algo = models.AlgorithmType(q)
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	tried := make(map[string]struct{})
	client := &http.Client{Timeout: 30 * time.Second}

	for attempt := 0; attempt < maxRetries; attempt++ {
		var node *balancer.NodeState
		if attempt == 0 {
			node, err = bal.SelectNode(algo)
		} else {
			node, err = bal.SelectNodeExcluding(algo, tried)
		}
		if err != nil {
			http.Error(w, "no nodes available", http.StatusServiceUnavailable)
			metrics.RequestsTotal.WithLabelValues(string(algo), "none", "error").Inc()
			return
		}
		tried[node.ID] = struct{}{}

		targetURL := fmt.Sprintf("http://%s%s", node.Address, r.URL.Path)
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
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

		resp, err := client.Do(proxyReq)
		if err != nil {
			log.Warn("worker unreachable, retrying", "node", node.ID, "attempt", attempt+1, "error", err)
			metrics.RequestsTotal.WithLabelValues(string(algo), node.ID, "error").Inc()
			continue
		}

		if resp.StatusCode == http.StatusServiceUnavailable {
			resp.Body.Close()
			log.Warn("worker offline (503), retrying", "node", node.ID, "attempt", attempt+1)
			metrics.RequestsTotal.WithLabelValues(string(algo), node.ID, "503").Inc()
			continue
		}

		duration := time.Since(start)
		defer resp.Body.Close()

		metrics.RequestsTotal.WithLabelValues(string(algo), node.ID, fmt.Sprintf("%d", resp.StatusCode)).Inc()
		metrics.RequestDuration.WithLabelValues(string(algo), node.ID).Observe(duration.Seconds())

		energyKWh := (node.Stats.EnergyWatts * duration.Hours()) / 1000.0
		carbonGrams := energyKWh * node.Stats.CarbonIntensity
		metrics.TotalCarbonEmitted.WithLabelValues(string(algo)).Add(carbonGrams)

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
			"algo", string(algo),
			"node", node.ID,
			"region", node.Region,
			"attempt", attempt+1,
			"carbon_score", node.CarbonScore,
			"carbon_intensity", node.Stats.CarbonIntensity,
			"energy_w", node.Stats.EnergyWatts,
			"duration_ms", duration.Milliseconds(),
		)
		return
	}

	http.Error(w, "all workers unavailable", http.StatusBadGateway)
	metrics.RequestsTotal.WithLabelValues(string(algo), "none", "error").Inc()
}

func updateMetricsLoop(ctx context.Context, bal *balancer.Balancer) {
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
