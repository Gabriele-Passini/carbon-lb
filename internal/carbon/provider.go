package carbon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/carbon-lb/internal/config"
)

// Provider fetches carbon intensity data per region
type Provider interface {
	Intensity(ctx context.Context, zone string) (float64, error)
	Start(ctx context.Context)
}

type Cache struct {
	mu     sync.RWMutex
	data   map[string]cacheEntry
	cfg    config.CarbonConfig
	log    *slog.Logger
	client *http.Client
}

type cacheEntry struct {
	value     float64
	fetchedAt time.Time
}

func NewProvider(cfg config.CarbonConfig, log *slog.Logger) Provider {
	return &Cache{
		data:   make(map[string]cacheEntry),
		cfg:    cfg,
		log:    log,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Cache) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Duration(c.cfg.RefreshPeriod))
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.mu.RLock()
				zones := make([]string, 0, len(c.data))
				for z := range c.data {
					zones = append(zones, z)
				}
				c.mu.RUnlock()
				for _, z := range zones {
					if _, err := c.fetchAndCache(ctx, z); err != nil {
						c.log.Warn("carbon refresh failed", "zone", z, "error", err)
					}
				}
			}
		}
	}()
}

func (c *Cache) Intensity(ctx context.Context, zone string) (float64, error) {
	c.mu.RLock()
	if e, ok := c.data[zone]; ok && time.Since(e.fetchedAt) < time.Duration(c.cfg.RefreshPeriod) {
		c.mu.RUnlock()
		return e.value, nil
	}
	c.mu.RUnlock()
	return c.fetchAndCache(ctx, zone)
}

func (c *Cache) fetchAndCache(ctx context.Context, zone string) (float64, error) {
	var val float64
	var err error

	switch c.cfg.Provider {
	case "electricity_maps":
		val, err = c.fetchElectricityMaps(ctx, zone)
	default:
		val = c.mockIntensity(zone)
	}

	if err != nil {
		c.log.Warn("using default intensity", "zone", zone, "error", err)
		val = c.cfg.DefaultIntensity
	}

	c.mu.Lock()
	c.data[zone] = cacheEntry{value: val, fetchedAt: time.Now()}
	c.mu.Unlock()
	return val, nil
}

type electricityMapsResponse struct {
	CarbonIntensity float64 `json:"carbonIntensity"`
}

func (c *Cache) fetchElectricityMaps(ctx context.Context, zone string) (float64, error) {
	url := fmt.Sprintf("%s/carbon-intensity/latest?zone=%s", c.cfg.BaseURL, zone)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("auth-token", c.cfg.APIKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("electricity maps API returned %d", resp.StatusCode)
	}
	var result electricityMapsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	return result.CarbonIntensity, nil
}

// mockIntensity simulates carbon intensity with diurnal variation and regional differences
func (c *Cache) mockIntensity(zone string) float64 {
	hour := float64(time.Now().Hour())
	bases := map[string]float64{
		"IT": 300, "DE": 350, "FR": 60, "ES": 200,
		"US-CA": 180, "US-TX": 420, "GB": 200,
	}
	base, ok := bases[zone]
	if !ok {
		base = c.cfg.DefaultIntensity
	}
	variation := 1.0 + 0.2*math.Sin((hour-12)*math.Pi/12)
	noise := 1.0 + (rand.Float64()*0.1 - 0.05)
	return base * variation * noise
}
