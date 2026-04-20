package carbon_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/carbon-lb/internal/carbon"
	"github.com/carbon-lb/internal/config"
	"go.uber.org/zap"
)

func defaultCarbonCfg() config.CarbonConfig {
	return config.CarbonConfig{
		Provider:         "mock",
		RefreshPeriod:    60 * time.Second,
		DefaultIntensity: 300.0,
	}
}

func TestMockProviderReturnsPositiveValue(t *testing.T) {
	cfg := defaultCarbonCfg()
	log, _ := zap.NewDevelopment()
	prov := carbon.NewProvider(cfg, log)

	val, err := prov.Intensity(context.Background(), "IT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val <= 0 {
		t.Errorf("expected positive carbon intensity, got %f", val)
	}
}

func TestMockProviderRegionalDifferences(t *testing.T) {
	// France (nuclear) should consistently be lower than Germany (coal-heavy)
	cfg := defaultCarbonCfg()
	log, _ := zap.NewDevelopment()
	prov := carbon.NewProvider(cfg, log)
	ctx := context.Background()

	// Sample multiple times to account for noise
	frSum, deSum := 0.0, 0.0
	samples := 20
	for i := 0; i < samples; i++ {
		fr, _ := prov.Intensity(ctx, "FR")
		de, _ := prov.Intensity(ctx, "DE")
		frSum += fr
		deSum += de
	}
	frAvg := frSum / float64(samples)
	deAvg := deSum / float64(samples)

	// FR (nuclear ~60) should be much lower than DE (~350)
	if frAvg >= deAvg {
		t.Errorf("expected FR avg (%.1f) < DE avg (%.1f)", frAvg, deAvg)
	}
}

func TestCacheReturnsCachedValue(t *testing.T) {
	cfg := defaultCarbonCfg()
	cfg.RefreshPeriod = 10 * time.Second // long TTL
	log, _ := zap.NewDevelopment()
	prov := carbon.NewProvider(cfg, log)
	ctx := context.Background()

	v1, err := prov.Intensity(ctx, "IT")
	if err != nil {
		t.Fatal(err)
	}
	// Second call should return cached value (same)
	v2, err := prov.Intensity(ctx, "IT")
	if err != nil {
		t.Fatal(err)
	}
	if v1 != v2 {
		t.Errorf("expected cached value %f, got %f", v1, v2)
	}
}

func TestElectricityMapsAPIIntegration(t *testing.T) {
	// Test against a mock HTTP server that mimics the Electricity Maps API
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("auth-token") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"carbonIntensity": 123.45, "zone": "IT"}`))
	}))
	defer srv.Close()

	cfg := config.CarbonConfig{
		Provider:         "electricity_maps",
		APIKey:           "test-key",
		BaseURL:          srv.URL,
		RefreshPeriod:    60 * time.Second,
		DefaultIntensity: 300.0,
	}
	log, _ := zap.NewDevelopment()
	prov := carbon.NewProvider(cfg, log)

	val, err := prov.Intensity(context.Background(), "IT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 123.45 {
		t.Errorf("expected 123.45, got %f", val)
	}
}

func TestElectricityMapsAPIFallsBackOnError(t *testing.T) {
	// Server returns 500 → provider should fall back to default intensity
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := config.CarbonConfig{
		Provider:         "electricity_maps",
		APIKey:           "key",
		BaseURL:          srv.URL,
		RefreshPeriod:    60 * time.Second,
		DefaultIntensity: 999.0,
	}
	log, _ := zap.NewDevelopment()
	prov := carbon.NewProvider(cfg, log)

	val, _ := prov.Intensity(context.Background(), "IT")
	if val != 999.0 {
		t.Errorf("expected fallback 999.0, got %f", val)
	}
}

func TestUnknownZoneUsesDefault(t *testing.T) {
	cfg := defaultCarbonCfg()
	cfg.DefaultIntensity = 400.0
	log, _ := zap.NewDevelopment()
	prov := carbon.NewProvider(cfg, log)

	val, err := prov.Intensity(context.Background(), "ZZ")
	if err != nil {
		t.Fatal(err)
	}
	// Unknown zone → base = default ≈ 400, ±20% variation → between 320 and 480
	if val < 200 || val > 600 {
		t.Errorf("unexpected value for unknown zone: %f", val)
	}
}
