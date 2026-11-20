package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hrfee/mediabrowser"
	"github.com/timshannon/badgerhold/v4"
)

type announcementCaptureClient struct{ messages []Message }

func (c *announcementCaptureClient) Send(_, _ string, message *Message, _ ...string) error {
	c.messages = append(c.messages, *message)
	return nil
}

func TestAnnouncementDeliveryExpandsComposerVariables(t *testing.T) {
	for _, mode := range []string{"immediate", "scheduled"} {
		t.Run(mode, func(t *testing.T) {
			oldMessages, oldEmail, oldDiscord := messagesEnabled, emailEnabled, discordEnabled
			t.Cleanup(func() { messagesEnabled, emailEnabled, discordEnabled = oldMessages, oldEmail, oldDiscord })
			e := testDummyEmailerInit(t)
			dbClose(e)
			opts := badgerhold.DefaultOptions
			opts.Dir = t.TempDir()
			opts.ValueDir = opts.Dir
			opts.Logger = nil
			store, err := badgerhold.Open(opts)
			if err != nil {
				t.Fatal(err)
			}
			e.storage.db = store
			db = store
			t.Cleanup(func() { dbClose(e) })
			messagesEnabled, emailEnabled, discordEnabled = true, true, false
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch strings.ToLower(r.URL.Path) {
				case "/system/info/public":
					_, _ = w.Write([]byte(`{"ServerName":"Fixture server"}`))
				case "/users":
					_, _ = w.Write([]byte(`[{"Id":"00000000000000000000000000000001","Name":"Alice"}]`))
				case "/api/announcement-summary":
					_, _ = w.Write([]byte(`{"vars":{"watch_plays_30d":"42"}}`))
				default:
					_, _ = w.Write([]byte(`{"Items":[]}`))
				}
			}))
			t.Cleanup(fake.Close)
			t.Setenv("JELLYFIN_STATS_API_URL", fake.URL)
			mb, err := mediabrowser.NewServer(mediabrowser.JellyfinServer, fake.URL, "test", "test", "test", "test", func() {}, 60)
			if err != nil {
				t.Fatal(err)
			}
			mb.Authenticated = true
			app := &appContext{config: e.config, storage: e.storage, email: e, LoggerSet: e.LoggerSet}
			app.jf.MediaBrowser = mb
			capture := &announcementCaptureClient{}
			e.sender = capture
			app.storage.SetEmailsKey("00000000000000000000000000000001", EmailAddress{Addr: "alice@example.invalid", Contact: true})
			subject, body := "News from {{ server_name }}", "{{ server_name }} has {{ watch_plays_30d }} plays."
			if mode == "immediate" {
				payload, err := json.Marshal(announcementDTO{Users: []string{"00000000000000000000000000000001"}, Subject: subject, Message: body})
				if err != nil {
					t.Fatal(err)
				}
				router := gin.New()
				router.POST("/users/announce", app.Announce)
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/users/announce", bytes.NewReader(payload))
				request.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					t.Fatalf("send status = %d: %s", response.Code, response.Body.String())
				}
			} else {
				job := ScheduledAnnouncement{ID: "00000000000000000000000000000001", Users: []string{"00000000000000000000000000000001"}, Subject: subject, Message: body, SendAt: time.Now().Add(-time.Minute)}
				if err := app.storage.db.Insert(job.ID, job); err != nil {
					t.Fatal(err)
				}
				scheduledAnnounceJob(app)
				var saved ScheduledAnnouncement
				if err := app.storage.db.Get(job.ID, &saved); err != nil {
					t.Fatal(err)
				}
				if !saved.Sent {
					t.Fatal("scheduled message was not marked sent")
				}
			}
			if len(capture.messages) != 1 {
				t.Fatalf("sent %d messages, want 1", len(capture.messages))
			}
			got := capture.messages[0]
			if got.Subject != "News from Fixture server" || !strings.Contains(got.Text, "Fixture server has 42 plays.") {
				t.Fatalf("variables not expanded: subject=%q text=%q", got.Subject, got.Text)
			}
		})
	}
}
