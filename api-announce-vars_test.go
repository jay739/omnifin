package main

import (
	"html"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSubstituteAnnounceVars(t *testing.T) {
	vars := map[string]string{"server_name": "Batcave", "username": "jay"}
	cases := []struct {
		name, in, want string
	}{
		{"tight braces", "Hi {{username}}", "Hi jay"},
		{"spaced braces", "Welcome to {{ server_name }}", "Welcome to Batcave"},
		{"uneven whitespace", "{{server_name }} / {{  username}}", "Batcave / jay"},
		{"unknown placeholder removed", "Hello {{ missing_var }}!", "Hello !"},
		{"no placeholders", "plain text", "plain text"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := substituteAnnounceVars(c.in, vars); got != c.want {
				t.Errorf("substituteAnnounceVars(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSubstituteAnnounceVarsEmptyServerNameLeavesNoBraces(t *testing.T) {
	// An unfilled {{ server_name }} used to survive substitution and abort the whole send.
	got := substituteAnnounceVars("Welcome to {{ server_name }}", map[string]string{})
	if got != "Welcome to " {
		t.Errorf("got %q, want placeholder stripped", got)
	}
}

func TestFeaturedCardOverviewUsesCharacterBoundary(t *testing.T) {
	for _, tc := range []struct{ name, overview, want string }{
		{"accent at byte cutoff", strings.Repeat("a", 219) + "éfin", strings.Repeat("a", 219) + "é…"},
		{"CJK", strings.Repeat("界", 221), strings.Repeat("界", 220) + "…"},
		{"emoji", strings.Repeat("🍿", 221), strings.Repeat("🍿", 220) + "…"},
		{"exact limit", strings.Repeat("界", 220), strings.Repeat("界", 220)},
		{"escaped text", "A < B & C", "A < B & C"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := renderFeaturedCard(jfLatestItem{Id: "fixture", Name: "Fixture", Overview: tc.overview}, "https://media.example.invalid")
			if !utf8.ValidString(card) {
				t.Fatal("card contains invalid UTF-8")
			}
			want := html.EscapeString(tc.want) + "</p>"
			if !strings.Contains(card, want) {
				t.Fatalf("card does not contain expected overview %q", tc.want)
			}
		})
	}
}
