package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the top-level configuration structure
type Config struct {
	LB       LBConfig       `mapstructure:"loadbalancer"`
	Worker   WorkerConfig   `mapstructure:"worker"`
	Registry RegistryConfig `mapstructure:"registry"`
	Carbon   CarbonConfig   `mapstructure:"carbon"`
	Energy   EnergyConfig   `mapstructure:"energy"`
	Metrics  MetricsConfig  `mapstructure:"metrics"`
}

type LBConfig struct {
	Address            string        `mapstructure:"address"`
	Algorithm          string        `mapstructure:"algorithm"`
	HealthCheckPeriod  time.Duration `mapstructure:"health_check_period"`
	StatsRefreshPeriod time.Duration `mapstructure:"stats_refresh_period"`
	// Weights for the carbon-aware scoring function
	CarbonWeight float64 `mapstructure:"carbon_weight"`
	EnergyWeight float64 `mapstructure:"energy_weight"`
	LoadWeight   float64 `mapstructure:"load_weight"`
}

type WorkerConfig struct {
	ID              string        `mapstructure:"id"`
	Address         string        `mapstructure:"address"`
	Region          string        `mapstructure:"region"`
	RegistryAddress string        `mapstructure:"registry_address"`
	HeartbeatPeriod time.Duration `mapstructure:"heartbeat_period"`
	SimulatePower   bool          `mapstructure:"simulate_power"` // use simulated power when PowerAPI unavailable
	BasePowerWatts  float64       `mapstructure:"base_power_watts"`
}

type RegistryConfig struct {
	Address       string        `mapstructure:"address"`
	NodeTTL       time.Duration `mapstructure:"node_ttl"`
	CleanupPeriod time.Duration `mapstructure:"cleanup_period"`
}

type CarbonConfig struct {
	Provider         string        `mapstructure:"provider"` // "electricity_maps" | "mock"
	APIKey           string        `mapstructure:"api_key"`
	BaseURL          string        `mapstructure:"base_url"`
	RefreshPeriod    time.Duration `mapstructure:"refresh_period"`
	DefaultIntensity float64       `mapstructure:"default_intensity"` // fallback gCO2/kWh
}

type EnergyConfig struct {
	Provider string `mapstructure:"provider"` // "cadvisor" | "powerapi" | "mock"
	Endpoint string `mapstructure:"endpoint"`
}

type MetricsConfig struct {
	Address string `mapstructure:"address"`
	Path    string `mapstructure:"path"`
}

// Load reads configuration from file and environment variables
func Load(path string) (*Config, error) {
	v := viper.New()

	// Defaults
	v.SetDefault("loadbalancer.address", ":8080")
	v.SetDefault("loadbalancer.algorithm", "carbon_aware")
	v.SetDefault("loadbalancer.health_check_period", "10s")
	v.SetDefault("loadbalancer.stats_refresh_period", "5s")
	v.SetDefault("loadbalancer.carbon_weight", 0.5)
	v.SetDefault("loadbalancer.energy_weight", 0.3)
	v.SetDefault("loadbalancer.load_weight", 0.2)

	v.SetDefault("registry.address", ":9000")
	v.SetDefault("registry.node_ttl", "30s")
	v.SetDefault("registry.cleanup_period", "10s")

	v.SetDefault("worker.heartbeat_period", "5s")
	v.SetDefault("worker.simulate_power", true)
	v.SetDefault("worker.base_power_watts", 50.0)

	v.SetDefault("carbon.provider", "mock")
	v.SetDefault("carbon.refresh_period", "60s")
	v.SetDefault("carbon.default_intensity", 300.0)
	v.SetDefault("carbon.base_url", "https://api.electricitymap.org/v3")

	v.SetDefault("energy.provider", "mock")

	v.SetDefault("metrics.address", ":2112")
	v.SetDefault("metrics.path", "/metrics")

	// Config file
	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("./config")
		v.AddConfigPath("/etc/carbon-lb")
	}

	// Environment overrides: CARBONLB_LOADBALANCER_ALGORITHM etc.
	v.SetEnvPrefix("CARBONLB")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}
	return &cfg, nil
}
