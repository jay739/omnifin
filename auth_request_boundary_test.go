package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt"
	"github.com/jay739/omnifin/logger"
)

func TestSessionRejectionPrecedesMutation(t *testing.T) {
	t.Setenv("JFA_SECRET", "session-boundary-fixture")
	app := &appContext{LoggerSet: LoggerSet{info: logger.NewEmptyLogger(), debug: logger.NewEmptyLogger(), err: logger.NewEmptyLogger()}, adminUsers: []User{{UserID: "fixture-admin"}}}
	writes := 0
	router := gin.New()
	router.POST("/write", app.authenticate, func(gc *gin.Context) { writes++; gc.JSON(200, gin.H{"success": true}) })
	token := func(expiry time.Time) string {
		result, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"id": "fixture-admin", "jfid": "", "admin": true, "valid": true, "type": "bearer", "exp": expiry.Unix()}).SignedString([]byte("session-boundary-fixture"))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	request := func(bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/write", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	rejected := request(token(time.Now().Add(-time.Minute)))
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rejected.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rejected.Code != 401 || body.Code != "AUTHENTICATION_REQUIRED" || rejected.Header().Get("X-Omnifin-Session") != "required" || writes != 0 {
		t.Fatalf("mutation boundary missing: status=%d code=%s writes=%d", rejected.Code, body.Code, writes)
	}
	accepted := request(token(time.Now().Add(time.Hour)))
	if accepted.Code != 200 || writes != 1 {
		t.Fatalf("renewed request did not run once: %d %d", accepted.Code, writes)
	}
}
