package main

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/hrfee/mediabrowser"
	"github.com/timshannon/badgerhold/v4"
)

// SavedUserDirectory keeps display data for messaging and account listings.
// Login and account permissions continue to use their existing authentication paths.
type SavedUserDirectory struct {
	Users    []SavedUser
	SyncedAt time.Time
}

type SavedUser struct {
	ID           string
	Name         string
	LastLogin    time.Time
	LastActivity time.Time
	Admin        bool
	Disabled     bool
}

const savedUserDirectoryKey = "jellyfin-users"

func (app *appContext) saveUserDirectory(users []mediabrowser.User) error {
	directory := SavedUserDirectory{SyncedAt: time.Now(), Users: make([]SavedUser, 0, len(users))}
	for _, user := range users {
		directory.Users = append(directory.Users, SavedUser{ID: user.ID, Name: user.Name, LastLogin: user.LastLoginDate.Time, LastActivity: user.LastActivityDate.Time, Admin: user.Policy.IsAdministrator, Disabled: user.Policy.IsDisabled})
	}
	// Replace the complete directory atomically, including a successful empty result.
	return app.storage.db.Upsert(savedUserDirectoryKey, directory)
}

func (app *appContext) savedUserDirectory() ([]mediabrowser.User, time.Time, error) {
	var directory SavedUserDirectory
	err := app.storage.db.Get(savedUserDirectoryKey, &directory)
	if errors.Is(err, badgerhold.ErrNotFound) {
		// Older installations can still send notices to their saved email contacts
		// before the first directory sync. An address is shown when no username is known.
		var emails []EmailAddress
		if err := app.storage.db.Find(&emails, &badgerhold.Query{}); err != nil {
			return nil, time.Time{}, fmt.Errorf("read saved email contacts: %w", err)
		}
		users := make([]mediabrowser.User, 0, len(emails))
		for _, email := range emails {
			users = append(users, mediabrowser.User{ID: email.JellyfinID, Name: email.Addr})
		}
		sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
		return users, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read saved user directory: %w", err)
	}
	users := make([]mediabrowser.User, 0, len(directory.Users))
	for _, saved := range directory.Users {
		user := mediabrowser.User{ID: saved.ID, Name: saved.Name, LastLoginDate: mediabrowser.Time{Time: saved.LastLogin}, LastActivityDate: mediabrowser.Time{Time: saved.LastActivity}}
		user.Policy.IsAdministrator = saved.Admin
		user.Policy.IsDisabled = saved.Disabled
		users = append(users, user)
	}
	return users, directory.SyncedAt, nil
}

func (app *appContext) announcementDirectory() (map[string]map[string]any, error) {
	users, err := app.safeGetUsers(false)
	knownLogin := true
	if err == nil {
		if saveErr := app.saveUserDirectory(users); saveErr != nil {
			app.err.Printf("Could not save messaging directory: %v", saveErr)
		}
	} else {
		var syncedAt time.Time
		users, syncedAt, err = app.savedUserDirectory()
		if err != nil {
			return nil, err
		}
		knownLogin = !syncedAt.IsZero()
	}
	values := make(map[string]map[string]any, len(users))
	for _, user := range users {
		recipient := announcementRecipientValues(user)
		if !knownLogin {
			recipient["last_login"] = "Unavailable until Jellyfin has been synced"
		}
		values[user.ID] = recipient
	}
	return values, nil
}
