package main

import (
	"testing"
	"time"

	"github.com/lithammer/shortuuid/v3"
)

func testCaptchaApp(e *Emailer) *appContext {
	return &appContext{
		config:    e.config,
		storage:   e.storage,
		LoggerSet: e.LoggerSet,
	}
}

// An unknown captcha ID left the outer "found" flag untouched, so verification fell
// through to comparing a zero-value Captcha's empty answer against the submitted
// text. Empty text then matched, bypassing the captcha entirely.
func TestVerifyCaptchaRejectsUnknownID(t *testing.T) {
	e := testDummyEmailerInit(t)
	defer dbClose(e)
	app := testCaptchaApp(e)

	code := shortuuid.New()
	app.storage.SetInvitesKey(code, Invite{
		Captchas: map[string]Captcha{
			"known-id": {Answer: "abcdef", Generated: time.Now()},
		},
	})

	for _, text := range []string{"", "abcdef", "wrong"} {
		if app.verifyCaptcha(code, "unknown-id", text, false) {
			t.Fatalf("unknown captcha ID accepted with text %q", text)
		}
	}
}

func TestVerifyCaptchaInvite(t *testing.T) {
	e := testDummyEmailerInit(t)
	defer dbClose(e)
	app := testCaptchaApp(e)

	code := shortuuid.New()
	app.storage.SetInvitesKey(code, Invite{
		Captchas: map[string]Captcha{"id1": {Answer: "AbCdEf", Generated: time.Now()}},
	})

	if !app.verifyCaptcha(code, "id1", "abcdef", false) {
		t.Fatal("correct answer rejected, comparison should be case-insensitive")
	}
	if app.verifyCaptcha(code, "id1", "nope", false) {
		t.Fatal("wrong answer accepted")
	}
	if app.verifyCaptcha(code, "id1", "", false) {
		t.Fatal("empty text accepted against a real captcha")
	}
}

func TestVerifyCaptchaPWR(t *testing.T) {
	e := testDummyEmailerInit(t)
	defer dbClose(e)
	app := testCaptchaApp(e)

	pin := shortuuid.New()
	app.setPWRCaptcha(pin, Captcha{Answer: "123abc", Generated: time.Now()})

	if !app.verifyCaptcha(pin, pin, "123ABC", true) {
		t.Fatal("correct PWR answer rejected")
	}
	if app.verifyCaptcha(pin, pin, "", true) {
		t.Fatal("empty text accepted for a real PWR captcha")
	}
	if app.verifyCaptcha("no-such-pin", "no-such-pin", "", true) {
		t.Fatal("unknown PWR code accepted with empty text")
	}
}

// The captcha maps are read and written from both request handlers and the
// housekeeping daemon, so they must tolerate concurrent access.
func TestPWRCaptchaConcurrentAccess(t *testing.T) {
	e := testDummyEmailerInit(t)
	defer dbClose(e)
	app := testCaptchaApp(e)

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			code := shortuuid.New()
			for j := 0; j < 200; j++ {
				app.setPWRCaptcha(code, Captcha{Answer: "abc", Generated: time.Now()})
				app.getPWRCaptcha(code)
				app.prunePWRCaptchas()
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
