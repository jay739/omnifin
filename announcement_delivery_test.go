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

type announcementCaptureClient struct {
	messages  []Message
	addresses [][]string
}

func (c *announcementCaptureClient) Send(_, _ string, message *Message, addresses ...string) error {
	c.messages = append(c.messages, *message)
	c.addresses = append(c.addresses, append([]string(nil), addresses...))
	return nil
}

func TestAnnouncementDeliveryExpandsComposerVariables(t *testing.T) {
	for _, mode := range []string{"immediate", "scheduled"} {
		t.Run(mode, func(t *testing.T) {
			app, capture := newAnnouncementDeliveryFixture(t)
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

func newAnnouncementDeliveryFixture(t *testing.T) (*appContext, *announcementCaptureClient) {
	t.Helper()
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
			_, _ = w.Write([]byte(`[{"Id":"00000000000000000000000000000001","Name":"Alice","LastLoginDate":"2026-01-01T15:30:45Z"},{"Id":"00000000000000000000000000000002","Name":"Bob","LastLoginDate":"2026-05-02T16:45:30Z"},{"Id":"00000000000000000000000000000003","Name":"Charlie"}]`))
		case "/users/ffffffffffffffffffffffffffffffff":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`"User not found"`))
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
	app.storage.SetEmailsKey("00000000000000000000000000000002", EmailAddress{Addr: "bob@example.invalid", Contact: true})
	app.storage.SetEmailsKey("00000000000000000000000000000003", EmailAddress{Addr: "charlie@example.invalid", Contact: true})
	return app, capture
}

func TestScheduledAnnouncementsPersonalizeEachRecipient(t *testing.T) {
	app, capture := newAnnouncementDeliveryFixture(t)
	job := ScheduledAnnouncement{
		ID: "personalized", Users: []string{"00000000000000000000000000000001", "00000000000000000000000000000002"},
		Subject: "Notice for {username}", Message: "Hi {username}!", SendAt: time.Now().Add(-time.Minute),
	}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	scheduledAnnounceJob(app)
	if len(capture.messages) != 2 {
		t.Fatalf("sent %d messages, want two personalized messages", len(capture.messages))
	}
	names := map[string]string{"alice@example.invalid": "Alice", "bob@example.invalid": "Bob"}
	for i, message := range capture.messages {
		if len(capture.addresses[i]) != 1 {
			t.Fatal("personalized message has multiple recipients")
		}
		name := names[capture.addresses[i][0]]
		if name == "" || message.Subject != "Notice for "+name || !strings.Contains(message.Text, "Hi "+name+"!") {
			t.Fatalf("incorrect recipient content: subject=%q text=%q", message.Subject, message.Text)
		}
	}
	var saved ScheduledAnnouncement
	if err := app.storage.db.Get(job.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Sent {
		t.Fatal("completed job remains pending")
	}
}

func TestImmediateAnnouncementPersonalizesSubjectWithoutBodyField(t *testing.T) {
	app, capture := newAnnouncementDeliveryFixture(t)
	payload, err := json.Marshal(announcementDTO{
		Users:   []string{"00000000000000000000000000000001", "00000000000000000000000000000002"},
		Subject: "Notice for {username}", Message: "Please sign in.",
	})
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
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if len(capture.messages) != 2 {
		t.Fatalf("sent %d messages, want 2", len(capture.messages))
	}
	names := map[string]string{"alice@example.invalid": "Alice", "bob@example.invalid": "Bob"}
	for i, message := range capture.messages {
		name := names[capture.addresses[i][0]]
		if name == "" || message.Subject != "Notice for "+name || !strings.Contains(message.Text, "Please sign in.") {
			t.Fatalf("incorrect recipient content: subject=%q text=%q", message.Subject, message.Text)
		}
	}
}

func TestAnnouncementLastLoginPerRecipient(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	oldLocal := time.Local
	time.Local = zone
	t.Cleanup(func() { time.Local = oldLocal })
	for _, mode := range []string{"immediate", "test", "scheduled"} {
		t.Run(mode, func(t *testing.T) {
			app, capture := newAnnouncementDeliveryFixture(t)
			ids := []string{"00000000000000000000000000000001", "00000000000000000000000000000002", "00000000000000000000000000000003"}
			subject, body := "Last login: {last_login}", "Seen {last_login}"
			if mode != "scheduled" {
				payload, err := json.Marshal(announcementDTO{Users: ids, Subject: subject, Message: body, Test: mode == "test"})
				if err != nil {
					t.Fatal(err)
				}
				router := gin.New()
				router.POST("/users/announce", func(c *gin.Context) {
					if mode == "test" {
						c.Set("jfId", ids[0])
					}
					app.Announce(c)
				})
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/users/announce", bytes.NewReader(payload))
				request.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					t.Fatalf("status = %d: %s", response.Code, response.Body.String())
				}
			} else {
				job := ScheduledAnnouncement{ID: "last-login", Users: ids, Subject: subject, Message: body, SendAt: time.Now().Add(-time.Minute)}
				if err := app.storage.db.Insert(job.ID, job); err != nil {
					t.Fatal(err)
				}
				scheduledAnnounceJob(app)
			}
			wantCount := 3
			if mode == "test" {
				wantCount = 1
			}
			if len(capture.messages) != wantCount {
				t.Fatalf("sent %d messages, want %d", len(capture.messages), wantCount)
			}
			expected := map[string]string{
				"alice@example.invalid":   "January 1, 2026 at 10:30:45 AM EST (UTC-05:00)",
				"bob@example.invalid":     "May 2, 2026 at 12:45:30 PM EDT (UTC-04:00)",
				"charlie@example.invalid": "No login recorded",
			}
			for i, message := range capture.messages {
				value := expected[capture.addresses[i][0]]
				if value == "" || message.Subject != "Last login: "+value || !strings.Contains(message.Text, "Seen "+value) {
					t.Fatalf("incorrect login timestamp: subject=%q text=%q", message.Subject, message.Text)
				}
			}
		})
	}
}

func TestAnnouncementReportsRecipientLookupFailures(t *testing.T) {
	missing := "ffffffffffffffffffffffffffffffff"
	alice := "00000000000000000000000000000001"
	for _, tc := range []struct {
		name     string
		users    []string
		wantSent int
	}{
		{name: "all recipients missing", users: []string{missing}},
		{name: "partial delivery", users: []string{alice, missing}, wantSent: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, capture := newAnnouncementDeliveryFixture(t)
			payload, err := json.Marshal(announcementDTO{Users: tc.users, Subject: "Notice", Message: "Hi {username}!"})
			if err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.POST("/users/announce", app.Announce)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/users/announce", bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502: %s", response.Code, response.Body.String())
			}
			var result struct {
				Success     bool     `json:"success"`
				Code        string   `json:"code"`
				SentUsers   []string `json:"sent_users"`
				FailedUsers []string `json:"failed_users"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Success || result.Code != "ANNOUNCE_RECIPIENT_LOOKUP" || len(result.SentUsers) != tc.wantSent || len(result.FailedUsers) != 1 || result.FailedUsers[0] != missing {
				t.Fatalf("incorrect delivery result: %+v", result)
			}
			if len(capture.messages) != tc.wantSent {
				t.Fatalf("captured %d deliveries, want %d", len(capture.messages), tc.wantSent)
			}
			if tc.wantSent == 1 && result.SentUsers[0] != alice {
				t.Fatalf("incorrect delivered recipient: %v", result.SentUsers)
			}
		})
	}
}
