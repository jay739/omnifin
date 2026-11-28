package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// probeLiveness checks this process's listener, without requiring integrations.
func probeLiveness(endpoint string, client *http.Client) error {
	response, err := client.Get(endpoint)
	if err != nil {
		return fmt.Errorf("listener probe failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("listener returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result); err != nil {
		return fmt.Errorf("invalid health response: %w", err)
	}
	if result.Status != "ok" {
		return fmt.Errorf("listener status is not ok")
	}
	return nil
}

func runHealthcheck() int {
	endpoint := os.Getenv("OMNIFIN_HEALTH_URL")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8056/health"
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil}}
	if err := probeLiveness(endpoint, client); err != nil {
		fmt.Fprintln(os.Stderr, "Omnifin listener health check failed")
		return 1
	}
	return 0
}
