package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jay739/omnifin/common"
)

type createFailureIntegration struct{ contactIDs []string }

func (s *createFailureIntegration) SetTransport(_ *http.Transport) {}
func (s *createFailureIntegration) ImportUser(_ string, _ newUserDTO, _ Profile) (error, bool) {
	return nil, true
}
func (s *createFailureIntegration) Enabled(_ *appContext, _ *Profile) bool { return true }
func (s *createFailureIntegration) Name() string                           { return "fixture integration" }
func (s *createFailureIntegration) SetContactMethods(id string, _ *string, _ *DiscordUser, _ *TelegramUser, _ *common.ContactPreferences) error {
	s.contactIDs = append(s.contactIDs, id)
	return nil
}

func TestFailedAdminCreationDoesNotWriteContacts(t *testing.T) {
	for _, tc := range []struct {
		name, username string
		status         int
	}{
		{name: "invalid name", username: "invalid+name", status: http.StatusBadRequest},
		{name: "existing user", username: "Alice", status: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, _ := newAnnouncementDeliveryFixture(t)
			integration := &createFailureIntegration{}
			app.thirdPartyServices = []ThirdPartyService{integration}
			payload, err := json.Marshal(newUserDTO{Username: tc.username, Password: "fixture-password", Email: "new@example.invalid"})
			if err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.POST("/user", app.NewUserFromAdmin)
			request := httptest.NewRequest(http.MethodPost, "/user", bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d, want %d: %s", response.Code, tc.status, response.Body.String())
			}
			var result newUserResponse
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.User || result.Error == "" {
				t.Fatalf("creation failure was not preserved: %+v", result)
			}
			if contact, ok := app.storage.GetEmailsKey(""); ok {
				t.Errorf("failed creation wrote an empty-ID contact: %+v", contact)
			}
			if len(integration.contactIDs) != 0 {
				t.Errorf("failed creation called contact synchronization: %q", integration.contactIDs)
			}
		})
	}
}
