package state

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client talks to the state server via HTTP
type Client struct {
	baseURL string
	http    *http.Client
}

func New(stateURL string) *Client {
	return &Client{
		baseURL: stateURL,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

type counterResponse struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// Increment atomically increments the named counter and returns the new value
func (c *Client) Increment(ctx context.Context, name string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/counter/"+name+"/increment", nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("state server unreachable: %w", err)
	}
	defer resp.Body.Close()
	var cr counterResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return 0, err
	}
	return cr.Value, nil
}

// Get returns the current value of the named counter (0 if not set)
func (c *Client) Get(ctx context.Context, name string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/counter/"+name, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("state server unreachable: %w", err)
	}
	defer resp.Body.Close()
	var cr counterResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return 0, err
	}
	return cr.Value, nil
}

// GetAll returns a snapshot of all counters as name → value
func (c *Client) GetAll(ctx context.Context) (map[string]int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/counters", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("state server unreachable: %w", err)
	}
	defer resp.Body.Close()
	var result map[string]int64
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}
