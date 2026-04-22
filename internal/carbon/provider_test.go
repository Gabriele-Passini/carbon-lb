package carbon_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/carbon-lb/internal/carbon"
	"github.com/carbon-lb/internal/config"
)

func testLog() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}

func defaultCarbonCfg() config.CarbonConfig {
	return config.CarbonConfig{
		Provider:         "mock",
		RefreshPeriod:    config.Duration(60 * time.Second),
		DefaultIntensity: 300.0,
	}
}

func TestMockProviderReturnsPositiveValue(t *testing.T) {
	prov := carbon.NewProvider(defaultCarbonCfg(), testLog())

	val, err := prov.Intensity(context.Background(), "IT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val <= 0 {
		t.Errorf("expected positive carbon intensity, got %f", val)
	}
}

func TestMockProviderRegionalDifferences(t *testing.T) {
	prov := carbon.NewProvider(defaultCarbonCfg(), testLog())
	ctx := context.Background()

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

	if frAvg >= deAvg {
		t.Errorf("expected FR avg (%.1f) < DE avg (%.1f)", frAvg, deAvg)
	}
}

func TestCacheReturnsCachedValue(t *testing.T) {
	cfg := defaultCarbonCfg()
	cfg.RefreshPeriod = config.Duration(10 * time.Second)
	prov := carbon.NewProvider(cfg, testLog())
	ctx := context.Background()

	v1, err := prov.Intensity(ctx, "IT")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := prov.Intensity(ctx, "IT")
	if err != nil {
		t.Fatal(err)
	}
	if v1 != v2 {
		t.Errorf("expected cached value %f, got %f", v1, v2)
	}
}

func TestElectricityMapsAPIIntegration(t *testing.T) {
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
		RefreshPeriod:    config.Duration(60 * time.Second),
		DefaultIntensity: 300.0,
	}
	prov := carbon.NewProvider(cfg, testLog())

	val, err := prov.Intensity(context.Background(), "IT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != 123.45 {
		t.Errorf("expected 123.45, got %f", val)
	}
}

func TestElectricityMapsAPIFallsBackOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := config.CarbonConfig{
		Provider:         "electricity_maps",
		APIKey:           "key",
		BaseURL:          srv.URL,
		RefreshPeriod:    config.Duration(60 * time.Second),
		DefaultIntensity: 999.0,
	}
	prov := carbon.NewProvider(cfg, testLog())

	val, _ := prov.Intensity(context.Background(), "IT")
	if val != 999.0 {
		t.Errorf("expected fallback 999.0, got %f", val)
	}
}

func TestUnknownZoneUsesDefault(t *testing.T) {
	cfg := defaultCarbonCfg()
	cfg.DefaultIntensity = 400.0
	prov := carbon.NewProvider(cfg, testLog())

	val, err := prov.Intensity(context.Background(), "ZZ")
	if err != nil {
		t.Fatal(err)
	}
	if val < 200 || val > 600 {
		t.Errorf("unexpected value for unknown zone: %f", val)
	}
}
