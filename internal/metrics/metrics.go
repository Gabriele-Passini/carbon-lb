package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "carbonlb_requests_total",
		Help: "Total number of requests dispatched",
	}, []string{"algorithm", "node_id", "status"})

	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "carbonlb_request_duration_seconds",
		Help:    "Request duration to worker nodes",
		Buckets: prometheus.DefBuckets,
	}, []string{"algorithm", "node_id"})

	NodeCarbonIntensity = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "carbonlb_node_carbon_intensity_gco2_kwh",
		Help: "Carbon intensity of each node region (gCO2/kWh)",
	}, []string{"node_id", "region"})

	NodeEnergyWatts = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "carbonlb_node_energy_watts",
		Help: "Current power draw of each node (W)",
	}, []string{"node_id"})

	NodeCarbonScore = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "carbonlb_node_carbon_score",
		Help: "Composite carbon-aware score for each node (lower = greener)",
	}, []string{"node_id"})

	NodeActiveConns = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "carbonlb_node_active_connections",
		Help: "Active connections per node",
	}, []string{"node_id"})

	NodeCPUUsage = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "carbonlb_node_cpu_usage_percent",
		Help: "CPU usage percent per node",
	}, []string{"node_id"})

	TotalCarbonEmitted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "carbonlb_total_carbon_emitted_gco2",
		Help: "Estimated total gCO2 emitted per algorithm (for comparison)",
	}, []string{"algorithm"})

	HealthyNodes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "carbonlb_healthy_nodes",
		Help: "Number of currently healthy worker nodes",
	})
)
