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

func offlineJellyfin(t *testing.T, app *appContext) {
	t.Helper()
	stopped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to stopped server: %s", r.URL.Path)
	}))
	address := stopped.URL
	stopped.Close()
	mb, err := mediabrowser.NewServer(mediabrowser.JellyfinServer, address, "test", "test", "test", "test", func() {}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Model a cold Omnifin start where no Jellyfin session was established.
	mb.Authenticated = false
	app.jf.MediaBrowser = mb
	app.userCache = NewUserCache(time.Minute, time.Minute)
}

func TestStandaloneAdministratorLoginAndInvites(t *testing.T) {
	app, capture := newAnnouncementDeliveryFixture(t)
	offlineJellyfin(t, app)
	app.adminUsers = []User{{UserID: "local-admin", Username: "admin", Password: "fixture-password"}}
	app.config.Section("invite_emails").Key("enabled").SetValue("true")
	app.config.Section("ui").Key("email").SetValue("administrator@example.invalid")
	t.Setenv("JFA_SECRET", "standalone-fixture-secret")
	router := gin.New()
	router.GET("/token/login", app.getTokenLogin)
	router.POST("/invites", app.authenticate, app.GenerateInvite)
	router.GET("/invites", app.authenticate, app.GetInvites)
	router.POST("/users/announce", app.authenticate, app.Announce)
	login := httptest.NewRequest(http.MethodGet, "/token/login", nil)
	login.SetBasicAuth("admin", "fixture-password")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, login)
	if result.Code != http.StatusOK {
		t.Fatalf("local login: %d %s", result.Code, result.Body.String())
	}
	var token getTokenDTO
	if err := json.Unmarshal(result.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	for _, sendTo := range []string{"", "invitee@example.invalid"} {
		payload, _ := json.Marshal(map[string]any{"days": 1, "send-to": sendTo})
		req := httptest.NewRequest(http.MethodPost, "/invites", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token.Token)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("create invite: %d %s", response.Code, response.Body.String())
		}
	}
	if len(app.storage.GetInvites()) != 2 {
		t.Fatal("offline invitations were not saved")
	}
	if len(capture.messages) != 1 || capture.addresses[0][0] != "invitee@example.invalid" {
		t.Fatalf("offline invitation email not delivered: %v", capture.addresses)
	}
	req := httptest.NewRequest(http.MethodGet, "/invites", nil)
	req.Header.Set("Authorization", "Bearer "+token.Token)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("list invites: %d %s", response.Code, response.Body.String())
	}
	payload, _ := json.Marshal(announcementDTO{Test: true, Subject: "Test for {username}", Message: "Test notice"})
	testRequest := httptest.NewRequest(http.MethodPost, "/users/announce", bytes.NewReader(payload))
	testRequest.Header.Set("Authorization", "Bearer "+token.Token)
	testRequest.Header.Set("Content-Type", "application/json")
	testResult := httptest.NewRecorder()
	router.ServeHTTP(testResult, testRequest)
	if testResult.Code != http.StatusOK {
		t.Fatalf("local administrator test email: %d %s", testResult.Code, testResult.Body.String())
	}
	if len(capture.messages) != 2 || capture.addresses[1][0] != "administrator@example.invalid" || capture.messages[1].Subject != "Test for admin" {
		t.Fatalf("local administrator test email misdirected: messages=%+v addresses=%v", capture.messages, capture.addresses)
	}
}

func TestStandaloneRecipientsAfterRestart(t *testing.T) {
	for _, mode := range []string{"immediate", "scheduled"} {
		t.Run(mode, func(t *testing.T) {
			app, capture := newAnnouncementDeliveryFixture(t)
			app.userCache = NewUserCache(time.Minute, time.Minute)
			users, err := app.userCache.GetUserDTOs(app, true)
			if err != nil || len(users) != 3 {
				t.Fatalf("initial sync: %d %v", len(users), err)
			}
			opts := badgerhold.DefaultOptions
			opts.Dir = app.storage.db.Badger().Opts().Dir
			opts.ValueDir = opts.Dir
			opts.Logger = nil
			if err := app.storage.db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := badgerhold.Open(opts)
			if err != nil {
				t.Fatal(err)
			}
			app.storage.db = reopened
			db = reopened
			offlineJellyfin(t, app)
			// Local contact changes must take effect even when the saved Jellyfin data is older.
			email, _ := app.storage.GetEmailsKey("00000000000000000000000000000001")
			email.Addr = "updated@example.invalid"
			app.storage.SetEmailsKey("00000000000000000000000000000001", email)
			users, err = app.userCache.GetUserDTOs(app, true)
			if err != nil || len(users) != 3 {
				t.Fatalf("offline directory: %d %v", len(users), err)
			}
			if users[0].Name != "Alice" || users[0].Email != "updated@example.invalid" {
				t.Fatalf("stale contact data: %+v", users[0])
			}
			subject, body := "Notice for {username}", "Jellyfin is unavailable. Last login: {last_login}"
			if mode == "immediate" {
				payload, _ := json.Marshal(announcementDTO{Users: []string{users[0].ID}, Subject: subject, Message: body})
				router := gin.New()
				router.POST("/users/announce", app.Announce)
				req := httptest.NewRequest(http.MethodPost, "/users/announce", bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				if response.Code != http.StatusOK {
					t.Fatalf("offline announcement: %d %s", response.Code, response.Body.String())
				}
			} else {
				job := ScheduledAnnouncement{ID: "offline-announcement", Users: []string{users[0].ID}, Subject: subject, Message: body, SendAt: time.Now().Add(-time.Minute)}
				if err := app.storage.db.Insert(job.ID, job); err != nil {
					t.Fatal(err)
				}
				scheduledAnnounceJob(app)
				var saved ScheduledAnnouncement
				if err := app.storage.db.Get(job.ID, &saved); err != nil || !saved.Sent {
					t.Fatalf("offline scheduled delivery: %+v %v", saved, err)
				}
			}
			if len(capture.messages) != 1 || capture.messages[0].Subject != "Notice for Alice" || !strings.Contains(capture.messages[0].Text, "2026") || capture.addresses[0][0] != "updated@example.invalid" {
				t.Fatalf("offline delivery: messages=%+v addresses=%v", capture.messages, capture.addresses)
			}
		})
	}
}

func TestStandaloneContactsBeforeFirstDirectorySync(t *testing.T) {
	app, capture := newAnnouncementDeliveryFixture(t)
	offlineJellyfin(t, app)
	users, err := app.userCache.GetUserDTOs(app, true)
	if err != nil || len(users) != 3 {
		t.Fatalf("saved email contacts: %d %v", len(users), err)
	}
	payload, _ := json.Marshal(announcementDTO{Users: []string{users[0].ID}, Subject: "Notice for {username}", Message: "Last login: {last_login}"})
	router := gin.New()
	router.POST("/users/announce", app.Announce)
	req := httptest.NewRequest(http.MethodPost, "/users/announce", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("saved contact announcement: %d %s", response.Code, response.Body.String())
	}
	if len(capture.messages) != 1 || capture.messages[0].Subject != "Notice for alice@example.invalid" || !strings.Contains(capture.messages[0].Text, "Unavailable until Jellyfin has been synced") {
		t.Fatalf("unknown identity or login presented as current: %+v", capture.messages)
	}
}

func TestStandaloneDirectoryReplacedOnSuccessfulSync(t *testing.T) {
	app, _ := newAnnouncementDeliveryFixture(t)
	app.userCache = NewUserCache(time.Minute, time.Minute)
	if _, err := app.userCache.GetUserDTOs(app, true); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`[{"Id":"00000000000000000000000000000001","Name":"Renamed Alice"}]`, `[]`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.ToLower(r.URL.Path) == "/users" {
				_, _ = w.Write([]byte(body))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		}))
		mb, err := mediabrowser.NewServer(mediabrowser.JellyfinServer, server.URL, "test", "test", "test", "test", func() {}, 0)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		mb.Authenticated = true
		app.jf.MediaBrowser = mb
		app.userCache = NewUserCache(time.Minute, time.Minute)
		if _, err := app.userCache.GetUserDTOs(app, true); err != nil {
			server.Close()
			t.Fatal(err)
		}
		server.Close()
		offlineJellyfin(t, app)
		users, err := app.userCache.GetUserDTOs(app, true)
		if err != nil {
			t.Fatal(err)
		}
		if body == `[]` {
			if len(users) != 0 {
				t.Fatalf("deleted users resurrected from saved contacts: %+v", users)
			}
		} else if len(users) != 1 || users[0].Name != "Renamed Alice" {
			t.Fatalf("directory did not reconcile after recovery: %+v", users)
		}
	}
}
