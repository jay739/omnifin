package main

import (
	"strings"
	"time"

	"github.com/hrfee/mediabrowser"
)

func announcementNeedsRecipient(subject, message string) bool {
	for _, field := range []string{"{username}", "{last_login}"} {
		if strings.Contains(subject, field) || strings.Contains(message, field) {
			return true
		}
	}
	return false
}

func announcementRecipientValues(user mediabrowser.User) map[string]any {
	lastLogin := "No login recorded"
	if !user.LastLoginDate.IsZero() {
		lastLogin = user.LastLoginDate.In(time.Local).Format("January 2, 2006 at 3:04:05 PM MST (UTC-07:00)")
	}
	return map[string]any{"username": user.Name, "last_login": lastLogin}
}
