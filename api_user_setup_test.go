package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/golang-jwt/jwt"
	"github.com/jay739/omnifin/common"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hrfee/mediabrowser"
)

const setupFixtureID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type setupFixtureState struct {
	failedPath atomic.Value
	creates    atomic.Int32
}

func newUserSetupFixture(t *testing.T) (*appContext, *setupFixtureState) {
	t.Helper()
	app, _ := newAnnouncementDeliveryFixture(t)
	app.userCache = NewUserCache(0, 0)
	state := &setupFixtureState{}
	state.failedPath.Store("")
	user := `{"Id":"` + setupFixtureID + `","Name":"new-account"}`
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := strings.ToLower(r.URL.Path)
		if r.Method == http.MethodPost && path == state.failedPath.Load().(string) {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"fixture setup unavailable"}`))
			return
		}
		switch path {
		case "/system/info/public":
			_, _ = w.Write([]byte(`{"ServerName":"Setup fixture"}`))
		case "/users":
			if state.creates.Load() > 0 {
				_, _ = w.Write([]byte("[" + user + "]"))
			} else {
				_, _ = w.Write([]byte("[]"))
			}
		case "/users/new":
			state.creates.Add(1)
			_, _ = w.Write([]byte(user))
		case "/users/" + setupFixtureID:
			_, _ = w.Write([]byte(user))
		default:
			w.WriteHeader(204)
		}
	}))
	t.Cleanup(fake.Close)
	mb, err := mediabrowser.NewServer(mediabrowser.JellyfinServer, fake.URL, "fixture", "test", "test", "test", func() {}, 1)
	if err != nil {
		t.Fatal(err)
	}
	mb.Authenticated = true
	app.jf.MediaBrowser = mb
	app.storage.SetProfileKey("setup", Profile{ProfileDTO: ProfileDTO{Name: "setup", Displayprefs: map[string]any{"customPrefs": map[string]any{}}}, Homescreen: true})
	return app, state
}

func setupFixturePost(t *testing.T, router *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(newUserDTO{Username: "new-account", Password: "fixture-password", Email: "new@example.invalid", Profile: "setup"})
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, req)
	return result
}

func TestNewUserSetupFailureIsNotSuccess(t *testing.T) {
	for _, path := range []string{"/users/" + setupFixtureID + "/policy", "/users/" + setupFixtureID + "/configuration", "/displaypreferences/usersettings"} {
		t.Run(path, func(t *testing.T) {
			app, state := newUserSetupFixture(t)
			state.failedPath.Store(path)
			router := gin.New()
			router.POST("/user", app.NewUserFromAdmin)
			result := setupFixturePost(t, router, "/user")
			if result.Code != 502 {
				t.Fatalf("incomplete setup returned %d, want 502: %s", result.Code, result.Body.String())
			}
			if state.creates.Load() != 1 {
				t.Fatal("expected one created Jellyfin account")
			}
		})
	}
}

func TestUserSetupRetryKeepsTheCreatedAccount(t *testing.T) {
	app, state := newUserSetupFixture(t)
	app.config.Section("welcome_email").Key("enabled").SetValue("true")
	state.failedPath.Store("/users/" + setupFixtureID + "/policy")
	router := gin.New()
	router.POST("/user", app.NewUserFromAdmin)
	router.POST("/users/:id/setup", app.RetryUserSetup)
	first := setupFixturePost(t, router, "/user")
	var incomplete newUserResponse
	if err := json.Unmarshal(first.Body.Bytes(), &incomplete); err != nil {
		t.Fatal(err)
	}
	if first.Code != 502 || !incomplete.User || incomplete.SetupComplete || incomplete.UserID != setupFixtureID || len(incomplete.FailedStages) == 0 {
		t.Fatalf("incorrect partial result: %d %+v", first.Code, incomplete)
	}
	if contact, ok := app.storage.GetEmailsKey(setupFixtureID); !ok || contact.Addr != "new@example.invalid" {
		t.Fatal("created account lost its contact")
	}
	state.failedPath.Store("")
	result := setupFixturePost(t, router, "/users/"+setupFixtureID+"/setup")
	var complete newUserResponse
	if err := json.Unmarshal(result.Body.Bytes(), &complete); err != nil {
		t.Fatal(err)
	}
	if result.Code != 200 || !complete.User || !complete.SetupComplete || complete.Email || state.creates.Load() != 1 {
		t.Fatalf("retry did not complete existing setup without creation or welcome replay: %d %+v creates=%d", result.Code, complete, state.creates.Load())
	}
}

type setupFixtureIntegration struct {
	createFailureIntegration
	failImport, warnImport, failContact bool
}

func (s *setupFixtureIntegration) ImportUser(_ string, _ newUserDTO, _ Profile) (error, bool) {
	if s.failImport || s.warnImport {
		return fmt.Errorf("fixture template failure"), s.warnImport
	}
	return nil, true
}
func (s *setupFixtureIntegration) SetContactMethods(id string, _ *string, _ *DiscordUser, _ *TelegramUser, _ *common.ContactPreferences) error {
	s.contactIDs = append(s.contactIDs, id)
	if s.failContact {
		return fmt.Errorf("fixture contact failure")
	}
	return nil
}

func TestUserSetupReportsIntegrationFailures(t *testing.T) {
	for _, kind := range []string{"import", "template after import", "contact"} {
		t.Run(kind, func(t *testing.T) {
			app, state := newUserSetupFixture(t)
			integration := &setupFixtureIntegration{failImport: kind == "import", warnImport: kind == "template after import", failContact: kind == "contact"}
			app.thirdPartyServices = []ThirdPartyService{integration}
			router := gin.New()
			router.POST("/user", app.NewUserFromAdmin)
			result := setupFixturePost(t, router, "/user")
			var got newUserResponse
			_ = json.Unmarshal(result.Body.Bytes(), &got)
			if result.Code != 502 || !got.User || got.SetupComplete || len(got.FailedStages) == 0 || state.creates.Load() != 1 {
				t.Fatalf("integration failure lost: %d %+v", result.Code, got)
			}
			for _, id := range integration.contactIDs {
				if id != setupFixtureID {
					t.Fatal("contact setup used a different account")
				}
			}
		})
	}
}

func TestInviteSetupFailureConsumesInviteAndReportsIncomplete(t *testing.T) {
	app, state := newUserSetupFixture(t)
	state.failedPath.Store("/users/" + setupFixtureID + "/policy")
	invite := Invite{Code: "setup-invite", Profile: "setup", Created: time.Now(), ValidTill: time.Now().Add(time.Hour), RemainingUses: 1}
	app.storage.SetInvitesKey(invite.Code, invite)
	router := gin.New()
	router.POST("/user/invite", app.NewUserFromInvite)
	data, _ := json.Marshal(newUserDTO{Username: "new-account", Password: "fixture-password", Email: "new@example.invalid", Profile: "setup", Code: invite.Code})
	req := httptest.NewRequest("POST", "/user/invite", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, req)
	var got newUserResponse
	_ = json.Unmarshal(result.Body.Bytes(), &got)
	if result.Code != 502 || !got.User || got.SetupComplete {
		t.Fatalf("signup claimed complete setup: %d %s", result.Code, result.Body.String())
	}
	if app.checkInvite(invite.Code, false, "") {
		t.Fatal("created account did not consume the invitation")
	}
	if state.creates.Load() != 1 {
		t.Fatal("unexpected account creation count")
	}
}

func TestConfirmationSetupFailureDoesNotRedirectToSuccess(t *testing.T) {
	app, state := newUserSetupFixture(t)
	state.failedPath.Store("/users/" + setupFixtureID + "/policy")
	app.config.Section("ui").Key("auto_redirect").SetValue("true")
	t.Setenv("JFA_SECRET", "setup-fixture-secret")
	invite := Invite{Code: "confirmation-invite", Profile: "setup", Created: time.Now(), ValidTill: time.Now().Add(time.Hour), RemainingUses: 1}
	app.storage.SetInvitesKey(invite.Code, invite)
	key, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"invite": invite.Code, "type": "confirmation", "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte("setup-fixture-secret"))
	if err != nil {
		t.Fatal(err)
	}
	app.ConfirmationKeys = map[string]map[string]ConfirmationKey{invite.Code: {key: {newUserDTO: newUserDTO{Username: "new-account", Password: "fixture-password", Email: "new@example.invalid", Code: invite.Code}}}}
	page, err := os.ReadFile("html/create-incomplete.html")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := template.New("create-incomplete.html").Parse(`{{define "header.txt"}}{{end}}` + string(page))
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.SetHTMLTemplate(tmpl)
	router.GET("/confirm", func(gc *gin.Context) { app.NewUserFromConfirmationKey(invite, key, "en-us", gc) })
	result := httptest.NewRecorder()
	router.ServeHTTP(result, httptest.NewRequest("GET", "/confirm", nil))
	if result.Code != 502 || result.Header().Get("Location") != "" || !strings.Contains(result.Body.String(), "Account setup incomplete") {
		t.Fatalf("confirmation claimed success: %d %s", result.Code, result.Body.String())
	}
	if _, ok := app.ConfirmationKeys[invite.Code][key]; ok {
		t.Fatal("confirmation key can create the same account again")
	}
}
