package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
)

// counters holds named int64 counters, each as a pointer for atomic access
var (
	mu       sync.RWMutex
	counters = map[string]*int64{}
)

func getOrCreate(name string) *int64 {
	mu.Lock()
	defer mu.Unlock()
	if p, ok := counters[name]; ok {
		return p
	}
	p := new(int64)
	counters[name] = p
	return p
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	addr := ":9001"
	if v := os.Getenv("STATE_ADDR"); v != "" {
		addr = v
	}

	mux := http.NewServeMux()

	// POST /counter/{name}/increment — atomically increments the counter and returns the new value
	mux.HandleFunc("POST /counter/{name}/increment", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		val := atomic.AddInt64(getOrCreate(name), 1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"name": name, "value": val})
	})

	// GET /counter/{name} — returns the current value of the named counter
	mux.HandleFunc("GET /counter/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		mu.RLock()
		p, ok := counters[name]
		mu.RUnlock()
		var val int64
		if ok {
			val = atomic.LoadInt64(p)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"name": name, "value": val})
	})

	// GET /counters — returns a snapshot of all counters
	mux.HandleFunc("GET /counters", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		snapshot := make(map[string]int64, len(counters))
		for k, p := range counters {
			snapshot[k] = atomic.LoadInt64(p)
		}
		mu.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snapshot)
	})

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	server := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Info("state server starting", "addr", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info("state server shutting down")
}
