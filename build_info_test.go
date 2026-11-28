package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestHealthAndVersionIdentifySameBuild(t *testing.T) {
	oldVersion, oldCommit, oldMode, oldTime, oldUpdater := version, commit, buildMode, buildTimeUnix, updater
	t.Cleanup(func() {
		version, commit, buildMode, buildTimeUnix, updater = oldVersion, oldCommit, oldMode, oldTime, oldUpdater
	})
	version, commit, buildMode, buildTimeUnix, updater = "1.4.3", strings.Repeat("a", 40), "docker", "1800000000", "docker"
	app := &appContext{version: "formatted display version"}
	router := gin.New()
	router.GET("/health", app.Health)
	result := httptest.NewRecorder()
	router.ServeHTTP(result, httptest.NewRequest("GET", "/health", nil))
	var health struct {
		Status string `json:"status"`
		buildInformation
	}
	if err := json.Unmarshal(result.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := writeBuildInformation(&output); err != nil {
		t.Fatal(err)
	}
	var cli buildInformation
	if err := json.Unmarshal(output.Bytes(), &cli); err != nil {
		t.Fatal(err)
	}
	if result.Code != 200 || health.Status != "ok" || health.buildInformation != cli || cli.Version != "1.4.3" || cli.Commit != commit || cli.Mode != "docker" || cli.Updater != "docker" {
		t.Fatalf("metadata mismatch: health=%+v cli=%+v", health, cli)
	}
	timestamp, err := time.Parse(time.RFC3339, cli.Time)
	if err != nil || timestamp.Unix() != 1800000000 {
		t.Fatalf("incorrect build time: %q %v", cli.Time, err)
	}
}

func TestDevelopmentBuildMetadataIsExplicit(t *testing.T) {
	oldVersion, oldCommit, oldMode, oldTime, oldUpdater := version, commit, buildMode, buildTimeUnix, updater
	t.Cleanup(func() {
		version, commit, buildMode, buildTimeUnix, updater = oldVersion, oldCommit, oldMode, oldTime, oldUpdater
	})
	version, commit, buildMode, buildTimeUnix, updater = "", "", "", "", "off"
	info := currentBuildInformation()
	if info.Version != "development" || info.Commit != "unknown" || info.Mode != "binary" {
		t.Fatalf("unidentified development build: %+v", info)
	}
}
