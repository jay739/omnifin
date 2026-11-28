package main

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDockerUpdaterDoesNotPoll(t *testing.T) {
	for _, version := range []string{"1.4.2", "1.5.0-rc1", "git"} {
		t.Run(version, func(t *testing.T) {
			calls := 0
			ud := NewUpdater("https://unused.invalid", "jay739", "omnifin", version, "abcdef0123456789", "docker")
			ud.httpClient = &http.Client{Transport: updaterTestTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, http.ErrServerClosed })}
			if _, status, err := ud.GetTag(); status != -1 || err != nil {
				t.Fatalf("tag: %d %v", status, err)
			}
			if _, status, err := ud.GetUpdate(Tag{}); status != -1 || err != nil {
				t.Fatalf("update: %d %v", status, err)
			}
			app := &appContext{updater: ud}
			done := make(chan struct{})
			go func() { app.checkForUpdates(); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("disabled polling did not return")
			}
			if calls != 0 {
				t.Fatalf("made %d outbound requests", calls)
			}
		})
	}
}

func TestDockerUpdateEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := &appContext{updater: &Updater{buildType: docker}, newUpdate: true, update: Update{CanUpdate: true, update: func() error { t.Fatal("container binary replacement attempted"); return nil }}}
	r := gin.New()
	r.GET("/config/update", app.CheckUpdate)
	r.POST("/config/update", app.ApplyUpdate)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/config/update", nil))
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 200 || body["new"] != false || body["container_managed"] != true || body["management_reason"] == "" {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/config/update", nil))
	if w.Code != 409 {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
}
