package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Duration is a time.Duration that unmarshals from a JSON string like "5s" or "30s"
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Config is the top-level configuration structure
type Config struct {
	LB       LBConfig       `json:"loadbalancer"`
	Worker   WorkerConfig   `json:"worker"`
	Registry RegistryConfig `json:"registry"`
	Carbon   CarbonConfig   `json:"carbon"`
	Energy   EnergyConfig   `json:"energy"`
	Metrics  MetricsConfig  `json:"metrics"`
}

type LBConfig struct {
	Address            string   `json:"address"`
	Algorithm          string   `json:"algorithm"`
	HealthCheckPeriod  Duration `json:"health_check_period"`
	StatsRefreshPeriod Duration `json:"stats_refresh_period"`
	CarbonWeight       float64  `json:"carbon_weight"`
	EnergyWeight       float64  `json:"energy_weight"`
	LoadWeight         float64  `json:"load_weight"`
}

type WorkerConfig struct {
	ID              string   `json:"id"`
	Address         string   `json:"address"`
	Region          string   `json:"region"`
	RegistryAddress string   `json:"registry_address"`
	HeartbeatPeriod Duration `json:"heartbeat_period"`
	SimulatePower   bool     `json:"simulate_power"`
	BasePowerWatts  float64  `json:"base_power_watts"`
}

type RegistryConfig struct {
	Address       string   `json:"address"`
	NodeTTL       Duration `json:"node_ttl"`
	CleanupPeriod Duration `json:"cleanup_period"`
}

type CarbonConfig struct {
	Provider         string   `json:"provider"`
	APIKey           string   `json:"api_key"`
	BaseURL          string   `json:"base_url"`
	RefreshPeriod    Duration `json:"refresh_period"`
	DefaultIntensity float64  `json:"default_intensity"`
}

type EnergyConfig struct {
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
}

type MetricsConfig struct {
	Address string `json:"address"`
	Path    string `json:"path"`
}

// defaults returns a Config fully populated with sensible default values
func defaults() Config {
	return Config{
		LB: LBConfig{
			Address:            ":8080",
			Algorithm:          "carbon_aware",
			HealthCheckPeriod:  Duration(10 * time.Second),
			StatsRefreshPeriod: Duration(5 * time.Second),
			CarbonWeight:       0.5,
			EnergyWeight:       0.3,
			LoadWeight:         0.2,
		},
		Registry: RegistryConfig{
			Address:       ":9000",
			NodeTTL:       Duration(30 * time.Second),
			CleanupPeriod: Duration(10 * time.Second),
		},
		Worker: WorkerConfig{
			HeartbeatPeriod: Duration(5 * time.Second),
			SimulatePower:   true,
			BasePowerWatts:  50.0,
		},
		Carbon: CarbonConfig{
			Provider:         "mock",
			BaseURL:          "https://api.electricitymap.org/v3",
			RefreshPeriod:    Duration(60 * time.Second),
			DefaultIntensity: 300.0,
		},
		Energy: EnergyConfig{
			Provider: "mock",
			Endpoint: "http://cadvisor:8080",
		},
		Metrics: MetricsConfig{
			Address: ":2112",
			Path:    "/metrics",
		},
	}
}

// Load reads config from a JSON file and applies CARBONLB_* environment overrides.
// If path is empty or the file is not found, defaults are used.
func Load(path string) (*Config, error) {
	cfg := defaults()

	if path == "" {
		for _, p := range []string{"config/config.json", "config.json", "/config/config.json"} {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}

	if path != "" {
		f, err := os.Open(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("opening config: %w", err)
		}
		if err == nil {
			defer f.Close()
			if err := json.NewDecoder(f).Decode(&cfg); err != nil {
				return nil, fmt.Errorf("parsing config: %w", err)
			}
		}
	}

	applyEnv(&cfg)
	return &cfg, nil
}

// applyEnv overrides config fields from CARBONLB_* environment variables
func applyEnv(cfg *Config) {
	cfg.Registry.Address = envString("CARBONLB_REGISTRY_ADDRESS", cfg.Registry.Address)
	cfg.Registry.NodeTTL = envDuration("CARBONLB_REGISTRY_NODE_TTL", cfg.Registry.NodeTTL)

	cfg.LB.Address = envString("CARBONLB_LOADBALANCER_ADDRESS", cfg.LB.Address)
	cfg.LB.Algorithm = envString("CARBONLB_LOADBALANCER_ALGORITHM", cfg.LB.Algorithm)
	cfg.LB.CarbonWeight = envFloat("CARBONLB_LOADBALANCER_CARBON_WEIGHT", cfg.LB.CarbonWeight)
	cfg.LB.EnergyWeight = envFloat("CARBONLB_LOADBALANCER_ENERGY_WEIGHT", cfg.LB.EnergyWeight)
	cfg.LB.LoadWeight = envFloat("CARBONLB_LOADBALANCER_LOAD_WEIGHT", cfg.LB.LoadWeight)

	cfg.Carbon.Provider = envString("CARBONLB_CARBON_PROVIDER", cfg.Carbon.Provider)
	cfg.Carbon.APIKey = envString("CARBONLB_CARBON_API_KEY", cfg.Carbon.APIKey)
	cfg.Carbon.DefaultIntensity = envFloat("CARBONLB_CARBON_DEFAULT_INTENSITY", cfg.Carbon.DefaultIntensity)

	cfg.Worker.BasePowerWatts = envFloat("CARBONLB_WORKER_BASE_POWER_WATTS", cfg.Worker.BasePowerWatts)
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func envDuration(key string, fallback Duration) Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return Duration(d)
		}
	}
	return fallback
}
