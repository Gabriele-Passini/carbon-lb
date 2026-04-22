package models

import "time"

// NodeInfo represents a registered worker node
type NodeInfo struct {
	ID       string    `json:"id"`
	Address  string    `json:"address"`
	Region   string    `json:"region"`
	Healthy  bool      `json:"healthy"`
	LastSeen time.Time `json:"last_seen"`
}

// NodeStats holds real-time metrics for a node
type NodeStats struct {
	NodeID          string    `json:"node_id"`
	CPUUsage        float64   `json:"cpu_usage"` // 0-100 %
	MemUsage        float64   `json:"mem_usage"` // 0-100 %
	ActiveConns     int       `json:"active_conns"`
	EnergyWatts     float64   `json:"energy_watts"`     // current power draw (W)
	CarbonIntensity float64   `json:"carbon_intensity"` // gCO2/kWh for node region
	CarbonScore     float64   `json:"carbon_score"`     // composite score (lower = greener)
	Timestamp       time.Time `json:"timestamp"`
}

// Task represents a computation task to be distributed
type Task struct {
	ID      string            `json:"id"`
	Type    string            `json:"type,omitempty"` // "cpu" | "memory" (default: "cpu")
	Payload string            `json:"payload"`
	Headers map[string]string `json:"headers,omitempty"`
}

// TaskResult is the response from a worker
type TaskResult struct {
	TaskID       string        `json:"task_id"`
	NodeID       string        `json:"node_id"`
	Result       string        `json:"result"`
	Duration     time.Duration `json:"duration_ms"`
	Algorithm    string        `json:"algorithm"`              // "carbon_aware" | "round_robin"
	CounterValue *int64        `json:"counter_value,omitempty"` // set for memory tasks
}

// RegisterRequest is sent by a worker to join the cluster
type RegisterRequest struct {
	NodeID  string `json:"node_id"`
	Address string `json:"address"`
	Region  string `json:"region"`
}

// HeartbeatRequest is sent by workers periodically
type HeartbeatRequest struct {
	NodeID      string  `json:"node_id"`
	CPUUsage    float64 `json:"cpu_usage"`
	MemUsage    float64 `json:"mem_usage"`
	ActiveConns int     `json:"active_conns"`
	EnergyWatts float64 `json:"energy_watts"`
}

// AlgorithmType selects the balancing algorithm
type AlgorithmType string

const (
	AlgoCarbonAware AlgorithmType = "carbon_aware"
	AlgoRoundRobin  AlgorithmType = "round_robin"
	AlgoLeastConn   AlgorithmType = "least_connections"
)
