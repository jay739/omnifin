package main

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lithammer/shortuuid/v3"
	"github.com/timshannon/badgerhold/v4"
)

// ScheduledAnnouncement is a queued announcement waiting to be sent at SendAt.
// Stored in BadgerDB via badgerhold.
type ScheduledAnnouncement struct {
	ID             string    `badgerhold:"key"`
	Users          []string  // Jellyfin user IDs to deliver to
	Subject        string    // Email subject
	Message        string    // Markdown body
	SendAt         time.Time // When to send
	Sent           bool      // True once dispatched
	SentAt         time.Time // When dispatched (zero if not yet)
	CreatedAt      time.Time
	Attempts       int
	NextAttempt    time.Time
	Failed         bool
	LastError      string
	DeliveryStates map[string]string
}

type scheduledAnnouncementDTO struct {
	ID             string            `json:"id,omitempty"`
	Users          []string          `json:"users"`
	Subject        string            `json:"subject"`
	Message        string            `json:"message"`
	SendAt         time.Time         `json:"send_at"`
	Sent           bool              `json:"sent"`
	SentAt         time.Time         `json:"sent_at,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	Attempts       int               `json:"attempts"`
	NextAttempt    time.Time         `json:"next_attempt,omitempty"`
	Failed         bool              `json:"failed"`
	LastError      string            `json:"last_error,omitempty"`
	DeliveryStates map[string]string `json:"delivery_states,omitempty"`
}

type scheduledAnnouncementListDTO struct {
	Scheduled []scheduledAnnouncementDTO `json:"scheduled"`
}

// @Summary List queued scheduled announcements.
// @Produce json
// @Success 200 {object} scheduledAnnouncementListDTO
// @Router /users/announce/scheduled [get]
// @Security Bearer
// @tags Users
func (app *appContext) ListScheduledAnnouncements(gc *gin.Context) {
	var stored []ScheduledAnnouncement
	if err := app.storage.db.Find(&stored, (&badgerhold.Query{}).SortBy("SendAt")); err != nil {
		respondAPIError(500, "errorStorage", "STORAGE_QUERY", err.Error(), gc)
		return
	}
	out := make([]scheduledAnnouncementDTO, 0, len(stored))
	for _, s := range stored {
		out = append(out, scheduledAnnouncementDTO{
			ID:             s.ID,
			Users:          s.Users,
			Subject:        s.Subject,
			Message:        s.Message,
			SendAt:         s.SendAt,
			Sent:           s.Sent,
			SentAt:         s.SentAt,
			CreatedAt:      s.CreatedAt,
			Attempts:       s.Attempts,
			NextAttempt:    s.NextAttempt,
			Failed:         s.Failed,
			LastError:      s.LastError,
			DeliveryStates: s.DeliveryStates,
		})
	}
	gc.JSON(200, scheduledAnnouncementListDTO{Scheduled: out})
}

// @Summary Schedule an announcement to be sent at a future time.
// @Produce json
// @Param scheduledAnnouncementDTO body scheduledAnnouncementDTO true "Scheduled announcement"
// @Success 200 {object} scheduledAnnouncementDTO
// @Router /users/announce/scheduled [post]
// @Security Bearer
// @tags Users
func (app *appContext) CreateScheduledAnnouncement(gc *gin.Context) {
	var req scheduledAnnouncementDTO
	if err := gc.BindJSON(&req); err != nil {
		respondAPIError(400, "errorBadRequest", "BAD_REQUEST", err.Error(), gc)
		return
	}
	if req.SendAt.IsZero() || req.SendAt.Before(time.Now()) {
		respondAPIError(400, "errorScheduleInPast", "SCHEDULE_PAST", "send_at must be in the future", gc)
		return
	}
	if req.Subject == "" || req.Message == "" || len(req.Users) == 0 {
		respondAPIError(400, "errorScheduleIncomplete", "SCHEDULE_INCOMPLETE", "users, subject, and message are required", gc)
		return
	}
	rec := ScheduledAnnouncement{
		ID:        shortuuid.New(),
		Users:     req.Users,
		Subject:   req.Subject,
		Message:   req.Message,
		SendAt:    req.SendAt,
		CreatedAt: time.Now(),
	}
	if err := app.storage.db.Insert(rec.ID, rec); err != nil {
		respondAPIError(500, "errorStorage", "STORAGE_INSERT", err.Error(), gc)
		return
	}
	req.ID = rec.ID
	req.CreatedAt = rec.CreatedAt
	gc.JSON(200, req)
}

// @Summary Cancel a queued scheduled announcement.
// @Param id path string true "Scheduled announcement ID"
// @Success 200 {object} boolResponse
// @Failure 404 {object} stringResponse
// @Failure 409 {object} stringResponse
// @Failure 500 {object} stringResponse
// @Router /users/announce/scheduled/{id} [delete]
// @Security Bearer
// @tags Users
func (app *appContext) DeleteScheduledAnnouncement(gc *gin.Context) {
	id := gc.Param("id")
	lock := app.scheduledAnnouncementLock(id)
	if !lock.TryLock() {
		respond(409, "Delivery is already in progress. Refresh the queue after the current attempt finishes.", gc)
		return
	}
	defer lock.Unlock()
	var rec ScheduledAnnouncement
	if err := app.storage.db.Get(id, &rec); err != nil {
		if errors.Is(err, badgerhold.ErrNotFound) {
			respond(404, "This announcement is no longer queued. Refresh the list.", gc)
		} else {
			respond(500, "Could not read the queue. Check storage and retry.", gc)
		}
		return
	}
	if rec.Sent || rec.Failed {
		respond(409, "This announcement is already stopped or completed. Its delivery history has been retained.", gc)
		return
	}
	if err := app.storage.db.Delete(id, ScheduledAnnouncement{}); err != nil {
		respondAPIError(500, "errorStorage", "STORAGE_DELETE", err.Error(), gc)
		return
	}
	respondBool(200, true, gc)
}

func (app *appContext) scheduledAnnouncementLock(id string) *sync.Mutex {
	lock, _ := app.scheduledAnnouncementLocks.LoadOrStore(id, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// scheduledAnnounceJob is run on every daemon tick: dispatches any announcements
// whose send time is due. Completed and failed jobs remain available for review.
func scheduledAnnounceJob(app *appContext) {
	if !messagesEnabled {
		return
	}
	var due []ScheduledAnnouncement
	if err := app.storage.db.Find(&due, badgerhold.Where("SendAt").Le(time.Now()).And("Sent").Eq(false)); err != nil {
		app.debug.Printf("scheduled announce daemon: query failed: %v", err)
		return
	}
	if len(due) == 0 {
		return
	}
	for _, s := range due {
		dispatchScheduledAnnouncement(app, s.ID)
	}
}

// Re-read under the same per-job lock used by cancellation so a stale daemon
// snapshot cannot dispatch a job that was canceled in the meantime.
func dispatchScheduledAnnouncement(app *appContext, id string) {
	lock := app.scheduledAnnouncementLock(id)
	if !lock.TryLock() {
		return
	}
	defer lock.Unlock()
	var s ScheduledAnnouncement
	if err := app.storage.db.Get(id, &s); err != nil {
		return
	}
	if s.Sent || s.SendAt.After(time.Now()) {
		return
	}

	if s.Failed || s.NextAttempt.After(time.Now()) {
		return
	}
	if s.Attempts >= scheduledDeliveryMaxAttempts {
		if scheduledDeliveriesComplete(&s) {
			s.Sent = true
			s.SentAt = time.Now()
			s.LastError = ""
		} else {
			s.Failed = true
			if s.LastError == "" {
				s.LastError = "Delivery retry limit reached. Review the channel outcomes before retrying."
			}
		}
		s.NextAttempt = time.Time{}
		if err := app.storage.db.Update(s.ID, s); err != nil {
			app.err.Printf("scheduled announce: could not record terminal state for %s: %v", s.ID, err)
		}
		return
	}
	s.Attempts++
	app.debug.Printf("scheduled announce daemon: dispatching %s (%d users)", s.ID, len(s.Users))
	// Expand global variables at delivery time, as for immediate announcements.
	subject, message := app.expandAnnouncementGlobals(s.Subject, s.Message)
	if err := app.sendScheduledAnnouncement(subject, message, &s); err != nil {
		app.err.Printf("scheduled announce: send failed for %s: %v", s.ID, err)
		s.LastError = err.Error()
		s.Failed = s.Attempts >= scheduledDeliveryMaxAttempts || errors.Is(err, errScheduledDeliveryUncertain)
		s.NextAttempt = time.Time{}
		if !s.Failed {
			s.NextAttempt = time.Now().Add(time.Duration(1<<uint(s.Attempts-1)) * time.Minute)
		}
		if saveErr := app.storage.db.Update(s.ID, s); saveErr != nil {
			app.err.Printf("scheduled announce: could not record failure for %s: %v", s.ID, saveErr)
		}
		return
	}
	s.Sent = true
	s.SentAt = time.Now()
	s.NextAttempt = time.Time{}
	s.LastError = ""
	if err := app.storage.db.Update(s.ID, s); err != nil {
		app.err.Printf("scheduled announce: could not record completion for %s: %v", s.ID, err)
	}
}

// newScheduledAnnounceDaemon returns a GenericDaemon that polls for due
// scheduled announcements once per minute.
func newScheduledAnnounceDaemon(app *appContext) *GenericDaemon {
	d := NewGenericDaemon(60*time.Second, app, scheduledAnnounceJob)
	d.Name("Scheduled Announcements")
	return d
}

// sendScheduledAnnouncement constructs recipient-specific messages when needed.
func (app *appContext) sendScheduledAnnouncement(subject, message string, job *ScheduledAnnouncement) error {
	construct := func(values map[string]any) (*Message, error) {
		return app.email.construct(AnnouncementCustomContent(subject), CustomContent{Enabled: true, Content: message}, values)
	}
	if !announcementNeedsRecipient(subject, message) {
		msg, err := construct(map[string]any{"username": ""})
		if err != nil {
			return err
		}
		var failures []error
		for _, id := range job.Users {
			if err := app.sendScheduledMessage(msg, id, job); err != nil {
				failures = append(failures, err)
				if errors.Is(err, errScheduledDeliveryUncertain) {
					break
				}
			}
		}
		return errors.Join(failures...)
	}
	recipients, err := app.announcementDirectory()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range job.Users {
		values, found := recipients[id]
		if !found {
			failures = append(failures, fmt.Errorf("lookup recipient %s: no saved or current user", id))
			continue
		}
		msg, err := construct(values)
		if err != nil {
			failures = append(failures, fmt.Errorf("render recipient %s: %w", id, err))
			continue
		}
		if err := app.sendScheduledMessage(msg, id, job); err != nil {
			failures = append(failures, err)
			if errors.Is(err, errScheduledDeliveryUncertain) {
				break
			}
		}
	}
	return errors.Join(failures...)
}
