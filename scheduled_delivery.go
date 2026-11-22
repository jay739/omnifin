package main

import (
	"errors"
	"fmt"
	"strings"
)

const (
	scheduledDeliverySending     = "sending"
	scheduledDeliverySent        = "sent"
	scheduledDeliveryFailed      = "failed"
	scheduledDeliveryMaxAttempts = 5
)

var errScheduledDeliveryUncertain = errors.New("delivery outcome is uncertain; review this job before retrying")

type scheduledDeliveryTarget struct {
	channel string
	deliver func() error
}

func (app *appContext) sendScheduledMessage(message *Message, id string, job *ScheduledAnnouncement) error {
	var targets []scheduledDeliveryTarget
	if contact, ok := app.storage.GetTelegramKey(id); ok && contact.Contact && telegramEnabled {
		targets = append(targets, scheduledDeliveryTarget{"telegram", func() error { return app.telegram.Send(message, contact.ChatID) }})
	}
	if contact, ok := app.storage.GetDiscordKey(id); ok && contact.Contact && discordEnabled {
		targets = append(targets, scheduledDeliveryTarget{"discord", func() error { return app.discord.Send(message, contact.ChannelID) }})
	}
	if contact, ok := app.storage.GetMatrixKey(id); ok && contact.Contact && matrixEnabled {
		targets = append(targets, scheduledDeliveryTarget{"matrix", func() error { return app.matrix.Send(message, contact) }})
	}
	if contact, ok := app.storage.GetEmailsKey(id); ok && contact.Contact && emailEnabled {
		targets = append(targets, scheduledDeliveryTarget{"email", func() error { return app.email.send(message, contact.Addr) }})
	}
	return app.sendScheduledTargets(job, id, targets)
}

func (app *appContext) sendScheduledTargets(job *ScheduledAnnouncement, id string, targets []scheduledDeliveryTarget) error {
	if job.DeliveryStates == nil {
		job.DeliveryStates = map[string]string{}
	}
	prefix := id + ":"
	deliveredPreviously := false
	for key, state := range job.DeliveryStates {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if state == scheduledDeliverySending {
			return fmt.Errorf("%s: %w", key, errScheduledDeliveryUncertain)
		}
		if state == scheduledDeliverySent {
			deliveredPreviously = true
		}
	}
	if len(targets) == 0 {
		if deliveredPreviously {
			return nil
		}
		return fmt.Errorf("%s: %w", id, ErrNoContactMethod)
	}
	var failures []error
	for _, target := range targets {
		key := prefix + target.channel
		if job.DeliveryStates[key] == scheduledDeliverySent {
			continue
		}
		// Persist intent first so an interrupted attempt is reviewed rather than replayed.
		previousState := job.DeliveryStates[key]
		job.DeliveryStates[key] = scheduledDeliverySending
		if err := app.storage.db.Update(job.ID, *job); err != nil {
			if previousState == "" {
				delete(job.DeliveryStates, key)
			} else {
				job.DeliveryStates[key] = previousState
			}
			return fmt.Errorf("record delivery intent for %s: %w", key, err)
		}
		err := target.deliver()
		if err != nil {
			job.DeliveryStates[key] = scheduledDeliveryFailed
			failures = append(failures, fmt.Errorf("%s: %w", key, err))
		} else {
			job.DeliveryStates[key] = scheduledDeliverySent
		}
		if saveErr := app.storage.db.Update(job.ID, *job); saveErr != nil {
			return fmt.Errorf("record delivery outcome for %s: %w", key, saveErr)
		}
	}
	return errors.Join(failures...)
}

// Check recorded outcomes when the final job-status write needs recovery.
func scheduledDeliveriesComplete(job *ScheduledAnnouncement) bool {
	for _, id := range job.Users {
		found := false
		prefix := id + ":"
		for key, state := range job.DeliveryStates {
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			found = true
			if state != scheduledDeliverySent {
				return false
			}
		}
		if !found {
			return false
		}
	}
	return true
}
