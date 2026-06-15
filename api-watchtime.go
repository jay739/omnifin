package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// watchStatsBaseURL returns the configured jellyfin-stats-api base URL,
// falling back to the default compose hostname.
func watchStatsBaseURL() string {
	base := os.Getenv("JELLYFIN_STATS_API_URL")
	if base == "" {
		base = "http://jellyfin-stats-api:3020"
	}
	return strings.TrimRight(base, "/")
}

// watchStatsEnabled reports whether the per-user watch-time column should be
// shown. Gated on the stats sidecar URL being explicitly configured so the
// column only appears on installs that deliberately run the sidecar.
func watchStatsEnabled() bool {
	return os.Getenv("JELLYFIN_STATS_API_URL") != ""
}

// sidecarWatchTimeUser mirrors one entry of the sidecar's /api/user-watchtime
// response.
type sidecarWatchTimeUser struct {
	Plays            int    `json:"plays"`
	WatchTimeSeconds int64  `json:"watchTimeSeconds"`
	WatchTime        string `json:"watchTime"`
	LastSeen         string `json:"lastSeen"`
}

type sidecarWatchTimeResp struct {
	Source string                          `json:"source"`
	Days   int                             `json:"days"`
	Users  map[string]sidecarWatchTimeUser `json:"users"`
}

// fetchUserWatchTime queries the stats sidecar for per-user watch-time and
// returns a username -> watch-seconds map. Best-effort: returns an empty map
// on any error so the accounts page degrades gracefully.
func (app *appContext) fetchUserWatchTime(days int) map[string]int64 {
	out := map[string]int64{}
	endpoint := fmt.Sprintf("%s/api/user-watchtime?days=%d", watchStatsBaseURL(), days)
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return out
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		app.debug.Printf("Failed to fetch per-user watch-time from %s: %v", endpoint, err)
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out
	}
	var parsed sidecarWatchTimeResp
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		app.debug.Printf("Failed to decode per-user watch-time response: %v", err)
		return out
	}
	for username, entry := range parsed.Users {
		out[username] = entry.WatchTimeSeconds
	}
	return out
}

// @Summary Per-user watch-time (seconds) keyed by username, for the accounts page column.
// @Produce json
// @Success 200 {object} object
// @Router /users/watch-time [get]
// @Security Bearer
// @tags Users,Statistics
func (app *appContext) GetUserWatchTime(gc *gin.Context) {
	if !watchStatsEnabled() {
		gc.JSON(200, gin.H{"watch_time": map[string]int64{}})
		return
	}
	gc.JSON(200, gin.H{"watch_time": app.fetchUserWatchTime(30)})
}
