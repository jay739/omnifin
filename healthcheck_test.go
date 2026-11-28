package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLivenessProbe(t *testing.T) {
	for _, test := range []struct {
		name, body        string
		status            int
		slow, wantFailure bool
	}{
		{"healthy", `{"status":"ok"}`, 200, false, false},
		{"optional integration unavailable", `{"status":"ok","jellyfin":"unavailable"}`, 200, false, false},
		{"invalid JSON", `oops`, 200, false, true},
		{"unhealthy", `{"status":"failed"}`, 200, false, true},
		{"failed HTTP", `{"status":"ok"}`, 503, false, true},
		{"stalled listener", `{"status":"ok"}`, 200, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.slow {
					time.Sleep(100 * time.Millisecond)
				}
				w.WriteHeader(test.status)
				w.Write([]byte(test.body))
			}))
			defer server.Close()
			err := probeLiveness(server.URL, &http.Client{Timeout: 30 * time.Millisecond})
			if (err != nil) != test.wantFailure {
				t.Fatalf("error=%v, want failure=%v", err, test.wantFailure)
			}
		})
	}
}
