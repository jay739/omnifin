package main

import (
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestScheduledCancellationRetainsSentHistory(t *testing.T) {
	app, _ := newAnnouncementDeliveryFixture(t)
	job := ScheduledAnnouncement{ID: "sent-history", Users: []string{"00000000000000000000000000000001"}, Subject: "Sent notice", Message: "Hello", SendAt: time.Now().Add(-time.Hour), Sent: true, SentAt: time.Now()}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.DELETE("/users/announce/scheduled/:id", app.DeleteScheduledAnnouncement)
	result := httptest.NewRecorder()
	router.ServeHTTP(result, httptest.NewRequest("DELETE", "/users/announce/scheduled/"+job.ID, nil))
	if result.Code != 409 {
		t.Fatalf("sent history removed by cancellation: status=%d", result.Code)
	}
	var saved ScheduledAnnouncement
	if err := app.storage.db.Get(job.ID, &saved); err != nil {
		t.Fatal("sent history was not retained", err)
	}
}

func TestCanceledScheduledSnapshotCannotDeliver(t *testing.T) {
	app, capture := newAnnouncementDeliveryFixture(t)
	job := ScheduledAnnouncement{ID: "canceled-snapshot", Users: []string{"00000000000000000000000000000001"}, Subject: "Notice", Message: "Hello", SendAt: time.Now().Add(-time.Minute)}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.DELETE("/users/announce/scheduled/:id", app.DeleteScheduledAnnouncement)
	result := httptest.NewRecorder()
	router.ServeHTTP(result, httptest.NewRequest("DELETE", "/users/announce/scheduled/"+job.ID, nil))
	if result.Code != 200 {
		t.Fatalf("cancel failed: %d", result.Code)
	}
	dispatchScheduledAnnouncement(app, job.ID)
	scheduledAnnounceJob(app)
	if len(capture.messages) != 0 {
		t.Fatal("a canceled daemon snapshot sent a message")
	}
}

type blockingScheduledSender struct {
	started, release chan struct{}
	calls            atomic.Int32
}

func (s *blockingScheduledSender) Send(_, _ string, _ *Message, _ ...string) error {
	s.calls.Add(1)
	close(s.started)
	<-s.release
	return nil
}

func TestScheduledCancellationCannotClaimAnInFlightDelivery(t *testing.T) {
	app, _ := newAnnouncementDeliveryFixture(t)
	sender := &blockingScheduledSender{started: make(chan struct{}), release: make(chan struct{})}
	app.email.sender = sender
	job := ScheduledAnnouncement{ID: "in-flight", Users: []string{"00000000000000000000000000000001"}, Subject: "Notice", Message: "Hello", SendAt: time.Now().Add(-time.Minute)}
	if err := app.storage.db.Insert(job.ID, job); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); dispatchScheduledAnnouncement(app, job.ID) }()
	released := false
	defer func() {
		if !released {
			close(sender.release)
		}
		<-done
	}()
	select {
	case <-sender.started:
	case <-time.After(3 * time.Second):
		t.Fatal("delivery did not start")
	}
	router := gin.New()
	router.DELETE("/users/announce/scheduled/:id", app.DeleteScheduledAnnouncement)
	cancel := func() *httptest.ResponseRecorder {
		result := httptest.NewRecorder()
		router.ServeHTTP(result, httptest.NewRequest("DELETE", "/users/announce/scheduled/"+job.ID, nil))
		return result
	}
	if result := cancel(); result.Code != 409 {
		t.Fatalf("in-flight delivery falsely canceled: %d", result.Code)
	}
	dispatchScheduledAnnouncement(app, job.ID)
	if sender.calls.Load() != 1 {
		t.Fatal("two daemon attempts delivered the same job concurrently")
	}
	close(sender.release)
	released = true
	<-done
	if result := cancel(); result.Code != 409 {
		t.Fatalf("completed history deleted: %d", result.Code)
	}
	var saved ScheduledAnnouncement
	if err := app.storage.db.Get(job.ID, &saved); err != nil || !saved.Sent {
		t.Fatalf("delivery completion was lost: %v %+v", err, saved)
	}
}

func TestScheduledCancellationReportsStorageFailure(t *testing.T) {
	app, _ := newAnnouncementDeliveryFixture(t)
	_ = app.storage.Close()
	router := gin.New()
	router.DELETE("/users/announce/scheduled/:id", app.DeleteScheduledAnnouncement)
	result := httptest.NewRecorder()
	router.ServeHTTP(result, httptest.NewRequest("DELETE", "/users/announce/scheduled/unreadable", nil))
	if result.Code != 500 {
		t.Fatalf("storage failure returned %d instead of 500", result.Code)
	}
}
