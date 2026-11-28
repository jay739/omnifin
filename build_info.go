package main

import (
	"encoding/json"
	"io"
	"strconv"
	"time"
)

var buildMode string

type buildInformation struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Mode    string `json:"build_mode"`
	Time    string `json:"build_time,omitempty"`
	Updater string `json:"updater_mode"`
}

func currentBuildInformation() buildInformation {
	result := buildInformation{Version: version, Commit: commit, Mode: buildMode, Updater: updater}
	if result.Version == "" {
		result.Version = "development"
	}
	if result.Commit == "" {
		result.Commit = "unknown"
	}
	if result.Mode == "" {
		result.Mode = "binary"
		if updater == "docker" {
			result.Mode = "docker"
		}
	}
	if timestamp, err := strconv.ParseInt(buildTimeUnix, 10, 64); err == nil && timestamp > 0 {
		result.Time = time.Unix(timestamp, 0).UTC().Format(time.RFC3339)
	}
	return result
}

func buildInformationRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-version" || arg == "--version" {
			return true
		}
	}
	return false
}

func writeBuildInformation(out io.Writer) error {
	return json.NewEncoder(out).Encode(currentBuildInformation())
}
