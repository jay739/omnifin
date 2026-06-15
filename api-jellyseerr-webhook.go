package main

import (
	"crypto/subtle"
	"html"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// jellyseerrWebhookRequest is the "request" object inside a Jellyseerr webhook
// payload (Jellyseerr's default JSON template).
type jellyseerrWebhookRequest struct {
	RequestID        string `json:"request_id"`
	RequestedByEmail string `json:"requestedBy_email"`
	RequestedByUser  string `json:"requestedBy_username"`
}

// jellyseerrWebhookPayload is the subset of Jellyseerr's webhook JSON we use.
type jellyseerrWebhookPayload struct {
	NotificationType string                    `json:"notification_type"`
	Subject          string                    `json:"subject"`
	Message          string                    `json:"message"`
	Request          *jellyseerrWebhookRequest `json:"request"`
}

// JellyseerrWebhook receives Jellyseerr webhook events on a secret path and,
// for request approval/availability events, notifies the requesting user
// through their configured omnifin contact methods. It is authenticated only
// by the shared secret in the URL path, because Jellyseerr cannot present a
// JWT; the route is only registered when [jellyseerr] webhook_secret is set.
func (app *appContext) JellyseerrWebhook(gc *gin.Context) {
	secret := app.config.Section("jellyseerr").Key("webhook_secret").String()
	if secret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(gc.Param("secret"))) != 1 {
		gc.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	var payload jellyseerrWebhookPayload
	if err := gc.BindJSON(&payload); err != nil {
		gc.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// Let Jellyseerr's "Test" button succeed.
	if payload.NotificationType == "TEST_NOTIFICATION" {
		gc.Status(http.StatusOK)
		return
	}

	// Only bridge the user-facing "good news" events; acknowledge the rest so
	// Jellyseerr does not retry.
	switch payload.NotificationType {
	case "MEDIA_APPROVED", "MEDIA_AUTO_APPROVED", "MEDIA_AVAILABLE":
	default:
		gc.Status(http.StatusOK)
		return
	}

	if payload.Request == nil || payload.Request.RequestedByUser == "" {
		gc.Status(http.StatusOK)
		return
	}

	// Map the Jellyseerr requester to a Jellyfin user (omnifin/Jellyseerr users
	// share the Jellyfin username).
	jfUser, err := app.jf.UserByName(payload.Request.RequestedByUser, false)
	if err != nil {
		app.debug.Printf("Jellyseerr webhook: no Jellyfin user for requester %q: %v", payload.Request.RequestedByUser, err)
		gc.Status(http.StatusOK)
		return
	}

	subject := payload.Subject
	if subject == "" {
		subject = "Jellyseerr update"
	}
	msg := &Message{
		Subject:  subject,
		Text:     strings.TrimSpace(payload.Subject + "\n\n" + payload.Message),
		Markdown: strings.TrimSpace("**" + payload.Subject + "**\n\n" + payload.Message),
		HTML:     "<p><strong>" + html.EscapeString(payload.Subject) + "</strong></p><p>" + html.EscapeString(payload.Message) + "</p>",
	}
	if err := app.sendByID(msg, jfUser.ID); err != nil {
		app.err.Printf("Jellyseerr webhook: failed to notify %s: %v", jfUser.ID, err)
	}
	gc.Status(http.StatusOK)
}
