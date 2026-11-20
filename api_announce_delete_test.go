package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/timshannon/badgerhold/v4"
)

func TestDeleteAnnouncementTemplateResult(t *testing.T) {
	for _, tc := range []struct {
		name           string
		exists, closed bool
		status         int
		wantSuccess    bool
	}{
		{name: "deleted", exists: true, status: http.StatusOK, wantSuccess: true},
		{name: "missing", status: http.StatusNotFound},
		{name: "storage unavailable", exists: true, closed: true, status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := badgerhold.DefaultOptions
			opts.Dir = t.TempDir()
			opts.ValueDir = opts.Dir
			opts.Logger = nil
			store, err := badgerhold.Open(opts)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if tc.exists {
				if err := store.Insert("weekly", announcementTemplate{Name: "weekly", Subject: "Weekly"}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.closed {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			app := &appContext{storage: &Storage{db: store}}
			router := gin.New()
			router.DELETE("/users/announce/template/:name", app.DeleteAnnounceTemplate)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/users/announce/template/weekly", nil))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			var result boolResponse
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Success != tc.wantSuccess || result.Error == tc.wantSuccess {
				t.Fatalf("unexpected result: %+v", result)
			}
			if tc.wantSuccess {
				var saved announcementTemplate
				if err := store.Get("weekly", &saved); err != badgerhold.ErrNotFound {
					t.Fatalf("template remains or lookup failed: %v", err)
				}
			}
		})
	}
}
