package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUserLogoutClearsIssuedCookie(t *testing.T) {
	previousDomain, previousProxyHost := externalDomain, UseProxyHost
	t.Cleanup(func() { externalDomain, UseProxyHost = previousDomain, previousProxyHost })
	for _, tc := range []struct {
		name, requestHost, domain string
		useProxy                  bool
	}{
		{name: "configured external domain", requestHost: "internal.example.invalid:8056", domain: "accounts.example.invalid"},
		{name: "request host mode", requestHost: "proxy.example.invalid:8443", domain: "proxy.example.invalid", useProxy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			externalDomain = "accounts.example.invalid:8443"
			UseProxyHost = tc.useProxy
			app := &appContext{}
			router := gin.New()
			router.POST("/my/logout", app.LogoutUser)
			request := httptest.NewRequest(http.MethodPost, "/my/logout", nil)
			request.Host = tc.requestHost
			request.AddCookie(&http.Cookie{Name: "user-refresh", Value: "fixture-refresh"})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("logout status=%d", response.Code)
			}
			cookies := response.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("got %d response cookies, want 1", len(cookies))
			}
			cookie := cookies[0]
			if cookie.Name != "user-refresh" || cookie.Path != "/my" || cookie.Domain != tc.domain || cookie.MaxAge >= 0 {
				t.Errorf("logout did not clear the issued cookie: name=%q path=%q domain=%q maxAge=%d", cookie.Name, cookie.Path, cookie.Domain, cookie.MaxAge)
			}
			if !cookie.HttpOnly || !cookie.Secure {
				t.Error("logout changed the cookie attributes")
			}
		})
	}
}
