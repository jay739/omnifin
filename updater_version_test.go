package main

import (
	"testing"
	"time"
)

func TestTagIsNew(t *testing.T) {
	previousCommit := commit
	commit = "abcdef0"
	t.Cleanup(func() { commit = previousCommit })
	fresh := UnixTime{Time: buildTime.Add(21 * time.Minute)}
	old := UnixTime{Time: buildTime.Add(20 * time.Minute)}
	for _, tc := range []struct {
		name string
		tag  Tag
		want bool
	}{
		{name: "empty version", tag: Tag{Version: "", Ready: true, ReleaseDate: fresh}},
		{name: "short version", tag: Tag{Version: "v1", Ready: true, ReleaseDate: fresh}},
		{name: "six characters", tag: Tag{Version: "abcdef", Ready: true, ReleaseDate: fresh}},
		{name: "same commit", tag: Tag{Version: "abcdef0123456789", Ready: true, ReleaseDate: fresh}},
		{name: "different short commit", tag: Tag{Version: "1234567", Ready: true, ReleaseDate: fresh}, want: true},
		{name: "different full commit", tag: Tag{Version: "123456789abcdef", Ready: true, ReleaseDate: fresh}, want: true},
		{name: "unfinished build", tag: Tag{Version: "1234567", Ready: false, ReleaseDate: fresh}},
		{name: "within build window", tag: Tag{Version: "1234567", Ready: true, ReleaseDate: old}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tag.IsNew(); got != tc.want {
				t.Fatalf("IsNew() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTagIsNewWithFullBuildRevision(t *testing.T) {
	previous := commit
	t.Cleanup(func() { commit = previous })
	commit = "abcdef0123456789abcdef0123456789abcdef0123"
	for _, candidate := range []string{"abcdef0", commit} {
		tag := Tag{Version: candidate, Ready: true, ReleaseDate: UnixTime{Time: buildTime.Add(21 * time.Minute)}}
		if tag.IsNew() {
			t.Fatal("current full build revision was reported as an update")
		}
	}
}
