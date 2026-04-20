package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/carbon-lb/internal/config"
	"github.com/carbon-lb/internal/registry"
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

	reg := registry.New(cfg.Registry, log)

	mux := http.NewServeMux()
	mux.HandleFunc("/register", reg.RegisterHandler)
	mux.HandleFunc("/heartbeat", reg.HeartbeatHandler)
	mux.HandleFunc("/nodes", reg.NodesHandler)
	mux.HandleFunc("/deregister", reg.DeregisterHandler)
	mux.HandleFunc("/health", reg.HealthHandler)

	server := &http.Server{
		Addr:    cfg.Registry.Address,
		Handler: mux,
	}

	go func() {
		log.Info("registry starting", zap.String("addr", cfg.Registry.Address))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("registry error", zap.Error(err))
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("registry shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server.Shutdown(ctx)
}
