package main

import (
	"errors"
	"fmt"
	"github.com/hrfee/mediabrowser"
	"strings"

	"github.com/gin-gonic/gin"
)

func (nu *NewUserData) addSetupFailure(stage string) {
	nu.FailedStages = append(nu.FailedStages, stage)
	nu.Success = false
	nu.Status = 502
	nu.Message = fmt.Sprintf("Account created, but setup is incomplete: %s. Retry setup for this existing account instead of creating it again.", strings.Join(nu.FailedStages, ", "))
}

func (nu NewUserData) response(email bool) newUserResponse {
	return newUserResponse{User: nu.Created, Email: email, Error: nu.Message, UserID: nu.User.ID, SetupComplete: nu.Success, FailedStages: nu.FailedStages}
}

func (app *appContext) applyUserSetup(nu *NewUserData, req newUserDTO, profile *Profile) {
	nu.Success = true
	nu.Status = 200
	if profile == nil {
		return
	}
	apply := func(stage string, run func() error) {
		if err := run(); err != nil {
			app.err.Printf("Account %s setup failed at %s: %v", nu.User.ID, stage, err)
			nu.addSetupFailure(stage)
		}
	}
	apply("Jellyfin policy", func() error { return app.jf.SetPolicy(nu.User.ID, profile.Policy) })
	// A profile without a homescreen template must leave the user's defaults alone.
	if profile.Homescreen {
		if err := app.jf.SetConfiguration(nu.User.ID, profile.Configuration); err != nil {
			app.err.Printf("Account %s setup failed at Jellyfin configuration: %v", nu.User.ID, err)
			nu.addSetupFailure("Jellyfin configuration")
			nu.addSetupFailure("Jellyfin display preferences (waiting for configuration)")
		} else {
			apply("Jellyfin display preferences", func() error { return app.jf.SetDisplayPreferences(nu.User.ID, profile.Displayprefs) })
		}
	}
	for _, service := range app.thirdPartyServices {
		if !service.Enabled(app, profile) {
			continue
		}
		err, ok := service.ImportUser(nu.User.ID, req, *profile)
		if !ok || err != nil {
			app.err.Printf("Account %s setup failed at %s: %v", nu.User.ID, service.Name(), err)
			nu.addSetupFailure(service.Name() + " setup")
		}
	}
}

func (app *appContext) completeAdminContactSetup(nu *NewUserData, req newUserDTO, profile *Profile) {
	if !emailEnabled || req.Email == "" {
		return
	}
	contact := EmailAddress{Addr: req.Email, Contact: true}
	app.storage.SetEmailsKey(nu.User.ID, contact)
	for _, service := range app.thirdPartyServices {
		if !service.Enabled(app, profile) {
			continue
		}
		if err := service.SetContactMethods(nu.User.ID, &req.Email, nil, nil, nil); err != nil {
			app.err.Printf("Account %s contact setup failed at %s: %v", nu.User.ID, service.Name(), err)
			nu.addSetupFailure(service.Name() + " contact synchronization")
		}
	}
}

// @Summary Retry profile setup for an existing Jellyfin account without creating another account.
// @Param id path string true "Existing Jellyfin user ID"
// @Param newUserDTO body newUserDTO true "Selected profile, email, and password for integration setup"
// @Router /users/{id}/setup [post]
// @Produce json
// @Success 200 {object} newUserResponse
// @Failure 400 {object} stringResponse
// @Failure 404 {object} stringResponse
// @Failure 502 {object} newUserResponse
// @Security Bearer
// @tags Users
func (app *appContext) RetryUserSetup(gc *gin.Context) {
	var req newUserDTO
	if err := gc.ShouldBindJSON(&req); err != nil {
		respond(400, "Enter the account details and selected profile to retry setup.", gc)
		return
	}
	user, err := app.safeUserByID(gc.Param("id"), false)
	if err != nil {
		var missing mediabrowser.ErrUserNotFound
		if errors.Is(err, mediabrowser.NotFound) || errors.As(err, &missing) {
			respond(404, "The account no longer exists. Refresh the account list.", gc)
			return
		}
		respond(502, "The existing account could not be read from Jellyfin. Check its availability and retry setup.", gc)
		return
	}
	if user.ID == "" {
		respond(404, "The account no longer exists. Refresh the account list.", gc)
		return
	}
	req.Username = user.Name
	if req.Email == "" {
		if contact, ok := app.storage.GetEmailsKey(user.ID); ok {
			req.Email = contact.Addr
		}
	}
	profile := app.storage.GetDefaultProfile()
	if req.Profile != "" && req.Profile != "none" {
		selected, ok := app.storage.GetProfileKey(req.Profile)
		if !ok {
			respond(400, "The selected profile no longer exists. Choose an existing profile.", gc)
			return
		}
		profile = selected
	}
	nu := NewUserData{Created: true, User: user}
	app.applyUserSetup(&nu, req, &profile)
	app.completeAdminContactSetup(&nu, req, &profile)
	app.InvalidateUserCaches()
	// Retrying setup must not replay invitations, creation events, or welcome emails.
	welcomeNotRequired := !app.config.Section("welcome_email").Key("enabled").MustBool(false)
	gc.JSON(nu.Status, nu.response(welcomeNotRequired))
}
