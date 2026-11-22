package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type scheduledSelectiveSender struct {
	attempts  map[string]int
	delivered map[string]int
	failBob   bool
}

func (s *scheduledSelectiveSender) Send(_, _ string, _ *Message, addresses ...string) error {
	for _, address := range addresses {
		s.attempts[address]++
		if address == "bob@example.invalid" && s.failBob {
			return errors.New("temporary recipient delivery failure")
		}
		s.delivered[address]++
	}
	return nil
}

func TestScheduledFailureDoesNotResendSuccessfulRecipient(t *testing.T) {
	app, _ := newAnnouncementDeliveryFixture(t)
	sender := &scheduledSelectiveSender{attempts: map[string]int{}, delivered: map[string]int{}, failBob: true}
	app.email.sender = sender
	job := ScheduledAnnouncement{
		ID: "partial", Users: []string{"00000000000000000000000000000001", "00000000000000000000000000000002"},
		Subject: "Notice", Message: "Please sign in.", SendAt: time.Now().Add(-time.Minute),
	}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	scheduledAnnounceJob(app)
	scheduledAnnounceJob(app)
	if sender.delivered["alice@example.invalid"] != 1 {
		t.Fatalf("successful recipient received %d messages, want 1", sender.delivered["alice@example.invalid"])
	}
	if sender.attempts["bob@example.invalid"] != 1 {
		t.Fatal("retry backoff was ignored")
	}
	var saved ScheduledAnnouncement
	if err := app.storage.db.Get(job.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Attempts != 1 || saved.NextAttempt.IsZero() || saved.LastError == "" || saved.Failed {
		t.Fatalf("incorrect retry state: %+v", saved)
	}
	sender.failBob = false
	saved.NextAttempt = time.Now().Add(-time.Minute)
	if err := app.storage.db.Update(saved.ID, saved); err != nil {
		t.Fatal(err)
	}
	scheduledAnnounceJob(app)
	if sender.delivered["alice@example.invalid"] != 1 || sender.delivered["bob@example.invalid"] != 1 {
		t.Fatalf("incorrect delivery counts after recovery: %v", sender.delivered)
	}
	saved = ScheduledAnnouncement{}
	if err := app.storage.db.Get(job.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Sent || saved.Failed || saved.LastError != "" {
		t.Fatalf("job did not complete: %+v", saved)
	}
}

func TestScheduledRetriesStopAfterAttemptLimit(t *testing.T) {
	app, _ := newAnnouncementDeliveryFixture(t)
	sender := &scheduledSelectiveSender{attempts: map[string]int{}, delivered: map[string]int{}, failBob: true}
	app.email.sender = sender
	job := ScheduledAnnouncement{ID: "retry-limit", Users: []string{"00000000000000000000000000000001", "00000000000000000000000000000002"}, Subject: "Notice", Message: "Hello", SendAt: time.Now().Add(-time.Minute)}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= scheduledDeliveryMaxAttempts; attempt++ {
		scheduledAnnounceJob(app)
		if err := app.storage.db.Get(job.ID, &job); err != nil {
			t.Fatal(err)
		}
		if job.Attempts != attempt {
			t.Fatalf("attempt count = %d, want %d", job.Attempts, attempt)
		}
		if attempt < scheduledDeliveryMaxAttempts {
			job.NextAttempt = time.Now().Add(-time.Minute)
			if err := app.storage.db.Update(job.ID, job); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !job.Failed || job.Sent || job.LastError == "" {
		t.Fatalf("job did not stop with an observable failure: %+v", job)
	}
	scheduledAnnounceJob(app)
	if sender.delivered["alice@example.invalid"] != 1 || sender.attempts["bob@example.invalid"] != scheduledDeliveryMaxAttempts {
		t.Fatalf("unexpected retries: delivered=%v attempts=%v", sender.delivered, sender.attempts)
	}
}

func TestScheduledChannelProgressSurvivesReload(t *testing.T) {
	app, _ := newAnnouncementDeliveryFixture(t)
	job := ScheduledAnnouncement{ID: "channels"}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	emailCalls, otherCalls := 0, 0
	failOther := true
	targets := []scheduledDeliveryTarget{
		{channel: "email", deliver: func() error { emailCalls++; return nil }},
		{channel: "other", deliver: func() error {
			otherCalls++
			if failOther {
				return errors.New("delivery failed")
			}
			return nil
		}},
	}
	if err := app.sendScheduledTargets(&job, "fixture", targets); err == nil {
		t.Fatal("expected partial channel failure")
	}
	var reloaded ScheduledAnnouncement
	if err := app.storage.db.Get(job.ID, &reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.DeliveryStates["fixture:email"] != scheduledDeliverySent || reloaded.DeliveryStates["fixture:other"] != scheduledDeliveryFailed {
		t.Fatalf("channel state was not persisted: %v", reloaded.DeliveryStates)
	}
	failOther = false
	if err := app.sendScheduledTargets(&reloaded, "fixture", targets); err != nil {
		t.Fatal(err)
	}
	if emailCalls != 1 || otherCalls != 2 {
		t.Fatalf("successful channel was repeated: email=%d other=%d", emailCalls, otherCalls)
	}
}

func TestInterruptedScheduledDeliveryRequiresReview(t *testing.T) {
	app, capture := newAnnouncementDeliveryFixture(t)
	id := "00000000000000000000000000000001"
	job := ScheduledAnnouncement{ID: "interrupted", Users: []string{id}, Subject: "Notice", Message: "Hello", SendAt: time.Now().Add(-time.Minute), DeliveryStates: map[string]string{id + ":email": scheduledDeliverySending}}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	scheduledAnnounceJob(app)
	if err := app.storage.db.Get(job.ID, &job); err != nil {
		t.Fatal(err)
	}
	if !job.Failed || job.Sent || job.LastError == "" {
		t.Fatalf("uncertain delivery was not stopped for review: %+v", job)
	}
	if len(capture.messages) != 0 {
		t.Fatal("an uncertain prior delivery was replayed")
	}
}

func TestScheduledDeliveryNeedsPersistedIntent(t *testing.T) {
	app, _ := newAnnouncementDeliveryFixture(t)
	job := ScheduledAnnouncement{ID: "storage-failure"}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	if err := app.storage.db.Close(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := app.sendScheduledTargets(&job, "fixture", []scheduledDeliveryTarget{{channel: "email", deliver: func() error { calls++; return nil }}})
	if err == nil || calls != 0 {
		t.Fatalf("delivery proceeded without durable state: calls=%d error=%v", calls, err)
	}
	if job.DeliveryStates["fixture:email"] == scheduledDeliverySending {
		t.Fatal("failed intent write left an in-memory sending marker")
	}
}

func TestScheduledSavedSuccessCompletesWithoutReplay(t *testing.T) {
	app, capture := newAnnouncementDeliveryFixture(t)
	id := "00000000000000000000000000000001"
	job := ScheduledAnnouncement{ID: "completion-recovery", Attempts: scheduledDeliveryMaxAttempts, Users: []string{id}, Subject: "Notice", Message: "Hello", SendAt: time.Now().Add(-time.Minute), DeliveryStates: map[string]string{id + ":email": scheduledDeliverySent}}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	scheduledAnnounceJob(app)
	var saved ScheduledAnnouncement
	if err := app.storage.db.Get(job.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Sent || saved.Failed || len(capture.messages) != 0 {
		t.Fatalf("saved successful delivery was replayed or left pending: sent=%v failed=%v messages=%d", saved.Sent, saved.Failed, len(capture.messages))
	}
}

func TestScheduledFailureDetailsAvailableInList(t *testing.T) {
	app, _ := newAnnouncementDeliveryFixture(t)
	job := ScheduledAnnouncement{ID: "failed-history", Failed: true, Attempts: scheduledDeliveryMaxAttempts, LastError: "recipient delivery failed", DeliveryStates: map[string]string{"fixture:email": scheduledDeliveryFailed}}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/users/announce/scheduled", app.ListScheduledAnnouncements)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users/announce/scheduled", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d", response.Code)
	}
	var result scheduledAnnouncementListDTO
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Scheduled) != 1 {
		t.Fatalf("listed %d jobs, want 1", len(result.Scheduled))
	}
	got := result.Scheduled[0]
	if !got.Failed || got.Attempts != job.Attempts || got.LastError != job.LastError || got.DeliveryStates["fixture:email"] != scheduledDeliveryFailed {
		t.Fatalf("failure details missing from queue list: %+v", got)
	}
}

func TestScheduledAttemptLimitSurvivesIncompleteStatusWrite(t *testing.T) {
	app, capture := newAnnouncementDeliveryFixture(t)
	id := "00000000000000000000000000000001"
	job := ScheduledAnnouncement{ID: "limit-recovery", Users: []string{id}, Subject: "Notice", Message: "Hello", SendAt: time.Now().Add(-time.Minute), Attempts: scheduledDeliveryMaxAttempts, DeliveryStates: map[string]string{id + ":email": scheduledDeliveryFailed}}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	scheduledAnnounceJob(app)
	var saved ScheduledAnnouncement
	if err := app.storage.db.Get(job.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Failed || saved.Attempts != scheduledDeliveryMaxAttempts || len(capture.messages) != 0 {
		t.Fatalf("retry limit bypassed after status-write failure: failed=%v attempts=%d sends=%d", saved.Failed, saved.Attempts, len(capture.messages))
	}
}
