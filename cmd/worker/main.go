package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/carbon-lb/internal/config"
	"github.com/carbon-lb/internal/energy"
	"github.com/carbon-lb/pkg/models"
	"go.uber.org/zap"
)

var connCount int64
var online int32 = 1 // 1 = online, 0 = fault-injected offline

const (
	faultInterval      = 20 * time.Second
	failProbability    = 0.20
	recoverProbability = 0.30
)

func main() {
	log, _ := zap.NewProduction()
	defer log.Sync()

	cfgPath := os.Getenv("CONFIG_PATH")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatal("config load failed", zap.Error(err))
	}

	nodeID := cfg.Worker.ID
	if v := os.Getenv("NODE_ID"); v != "" {
		nodeID = v
	}
	region := cfg.Worker.Region
	if v := os.Getenv("NODE_REGION"); v != "" {
		region = v
	}
	listenAddr := cfg.Worker.Address
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		listenAddr = v
	}
	advertiseAddr := listenAddr
	if v := os.Getenv("NODE_ADDRESS"); v != "" {
		advertiseAddr = v
	}
	registryAddr := cfg.Worker.RegistryAddress
	if v := os.Getenv("REGISTRY_URL"); v != "" {
		registryAddr = v
	}

	energyProv := energy.NewProvider(cfg.Energy, cfg.Worker.BasePowerWatts, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := registerWithRegistry(registryAddr, nodeID, advertiseAddr, region, log); err != nil {
		log.Fatal("registration failed", zap.Error(err))
	}

	go heartbeatLoop(ctx, registryAddr, nodeID, energyProv, cfg.Worker.HeartbeatPeriod, log)
	go faultSimLoop(ctx, registryAddr, nodeID, advertiseAddr, region, log)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&online) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "offline", "node_id": nodeID})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "node_id": nodeID})
	})

	mux.HandleFunc("/task", func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&online) == 0 {
			http.Error(w, "node offline", http.StatusServiceUnavailable)
			return
		}
		atomic.AddInt64(&connCount, 1)
		defer atomic.AddInt64(&connCount, -1)
		start := time.Now()

		complexity := 500000
		if c := r.URL.Query().Get("complexity"); c != "" {
			fmt.Sscanf(c, "%d", &complexity)
		}

		result := simulateWork(complexity)
		duration := time.Since(start)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(models.TaskResult{
			NodeID:   nodeID,
			Result:   fmt.Sprintf("%d primes found", result),
			Duration: duration,
		})
		log.Debug("task done", zap.String("node", nodeID), zap.Duration("dur", duration))
	})

	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"node_id": nodeID, "region": region, "address": advertiseAddr,
			"goroutines": runtime.NumGoroutine(),
		})
	})

	server := &http.Server{Addr: listenAddr, Handler: mux}
	go func() {
		log.Info("worker starting", zap.String("id", nodeID), zap.String("addr", listenAddr), zap.String("region", region))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("server error", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("worker shutting down", zap.String("id", nodeID))
	cancel()
	deregister(registryAddr, nodeID, log)
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	server.Shutdown(shutCtx)
}

func registerWithRegistry(registryAddr, id, addr, region string, log *zap.Logger) error {
	rr := models.RegisterRequest{NodeID: id, Address: addr, Region: region}
	body, _ := json.Marshal(rr)
	for i := 0; i < 10; i++ {
		resp, err := http.Post(registryAddr+"/register", "application/json", bytes.NewReader(body))
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			log.Info("registered", zap.String("registry", registryAddr), zap.String("id", id))
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		log.Warn("registry not ready, retrying", zap.Int("attempt", i+1))
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("could not register after retries")
}

func deregister(registryAddr, id string, log *zap.Logger) {
	req, _ := http.NewRequest(http.MethodDelete, registryAddr+"/deregister?id="+id, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}

func heartbeatLoop(ctx context.Context, registryAddr, id string, ep energy.Provider, period time.Duration, log *zap.Logger) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if atomic.LoadInt32(&online) == 0 {
				continue
			}
			watts, err := ep.PowerWatts(ctx)
			if err != nil {
				watts = 50.0
			}
			hb := models.HeartbeatRequest{
				NodeID:      id,
				CPUUsage:    cpuUsage(),
				MemUsage:    memUsage(),
				ActiveConns: int(atomic.LoadInt64(&connCount)),
				EnergyWatts: watts,
			}
			body, _ := json.Marshal(hb)
			resp, err := http.Post(registryAddr+"/heartbeat", "application/json", bytes.NewReader(body))
			if err != nil {
				log.Warn("heartbeat failed", zap.Error(err))
			} else {
				resp.Body.Close()
			}
		}
	}
}

func faultSimLoop(ctx context.Context, registryAddr, nodeID, advertiseAddr, region string, log *zap.Logger) {
	ticker := time.NewTicker(faultInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if atomic.LoadInt32(&online) == 1 {
				if rand.Float64() < failProbability {
					atomic.StoreInt32(&online, 0)
					deregister(registryAddr, nodeID, log)
					log.Info("fault injected: node going offline", zap.String("id", nodeID))
				}
			} else {
				if rand.Float64() < recoverProbability {
					if err := registerWithRegistry(registryAddr, nodeID, advertiseAddr, region, log); err == nil {
						atomic.StoreInt32(&online, 1)
						log.Info("fault cleared: node back online", zap.String("id", nodeID))
					}
				}
			}
		}
	}
}

func simulateWork(n int) int {
	if n < 2 {
		return 0
	}
	sieve := make([]bool, n+1)
	for i := range sieve {
		sieve[i] = true
	}
	sieve[0], sieve[1] = false, false
	for i := 2; i*i <= n; i++ {
		if sieve[i] {
			for j := i * i; j <= n; j += i {
				sieve[j] = false
			}
		}
	}
	count := 0
	for _, v := range sieve {
		if v {
			count++
		}
	}
	time.Sleep(time.Duration(rand.Intn(50)+10) * time.Millisecond)
	return count
}

func cpuUsage() float64 {
	return math.Min(float64(runtime.NumGoroutine())*1.5, 100.0)
}

func memUsage() float64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return math.Min(float64(ms.Sys)/1024/1024/512*100, 100.0)
}
