package main

import (
	"errors"
	"testing"
	"time"
)

type expiryReminderTestSender struct {
	fail     bool
	attempts int
}

func (s *expiryReminderTestSender) Send(_, _ string, _ *Message, _ ...string) error {
	s.attempts++
	if s.fail {
		return errors.New("temporary delivery failure")
	}
	return nil
}

func TestExpiryReminderRecordsOnlySuccessfulDelivery(t *testing.T) {
	for _, phase := range []string{"transport", "rendering", "missing contact"} {
		t.Run(phase, func(t *testing.T) {
			app, _ := newAnnouncementDeliveryFixture(t)
			app.config.Section("user_expiry").Key("send_email").SetValue("true")
			app.config.Section("user_expiry").Key("auto_extend_on_activity").SetValue("false")
			id := "00000000000000000000000000000001"
			now := time.Now()
			timers := NewDayTimerSet([]string{"1"}, -24*time.Hour)
			timers.clock = fakeClock{now: now}
			app.storage.SetUserExpiryKey(id, UserExpiry{Expiry: now.Add(24 * time.Hour)})
			sender := &expiryReminderTestSender{fail: phase == "transport"}
			app.email.sender = sender
			if phase == "rendering" {
				app.storage.SetCustomContentKey("ExpiryReminder", CustomContent{Enabled: true, Content: "{{broken}}"})
			} else if phase == "missing contact" {
				app.storage.DeleteEmailsKey(id)
			}
			app.checkUsers(timers)
			stored, ok := app.storage.GetUserExpiryKey(id)
			if !ok || !stored.LastNotified.IsZero() {
				t.Fatalf("unsent reminder recorded as notified: %+v", stored)
			}
			attemptsBeforeRecovery := sender.attempts
			sender.fail = false
			app.storage.SetCustomContentKey("ExpiryReminder", CustomContent{Enabled: true, Content: "Hi {username}, your account is due to expire."})
			app.storage.SetEmailsKey(id, EmailAddress{Addr: "alice@example.invalid", Contact: true})
			app.checkUsers(timers)
			stored, ok = app.storage.GetUserExpiryKey(id)
			if !ok || stored.LastNotified.IsZero() {
				t.Fatal("successful reminder was not recorded")
			}
			if sender.attempts != attemptsBeforeRecovery+1 {
				t.Fatalf("recovery did not retry delivery: %d attempts", sender.attempts)
			}
			app.checkUsers(timers)
			if sender.attempts != attemptsBeforeRecovery+1 {
				t.Fatal("successful reminder was sent again")
			}
		})
	}
}
