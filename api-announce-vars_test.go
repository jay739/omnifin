package main

import "testing"

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
