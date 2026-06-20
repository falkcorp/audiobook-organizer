// file: internal/itunes/service/regroup_test.go
// version: 1.0.0
// guid: 9b1c2d3e-4f5a-6b7c-8d9e-0a1b2c3d4e5f
// last-edited: 2026-06-20

package itunesservice

import (
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// abp builds an audiobook track with an explicit iTunes PID.
func abp(name, artist, album string, trackNum int, pid string) *itunes.Track {
	t := ab(name, artist, album, trackNum)
	t.PersistentID = pid
	return t
}

func healByTitle(groups []HealGroup) map[string][]string {
	out := map[string][]string{}
	for _, g := range groups {
		out[g.Title] = g.PIDs
	}
	return out
}

func TestGroupLibraryForHeal(t *testing.T) {
	t.Run("anthology -> one group titled after album, all PIDs sorted", func(t *testing.T) {
		l := lib(
			abp("Strings", "Stephen Leigh", "Wild Cards I", 17, "PID03"),
			abp("The Long Sleep", "Michael Cassutt", "Wild Cards I", 3, "PID01"),
			abp("Interlude", "Roger Zelazny", "Wild Cards I", 9, "PID02"),
		)
		got := GroupLibraryForHeal(l)
		if len(got) != 1 {
			t.Fatalf("want 1 group, got %d: %+v", len(got), got)
		}
		if got[0].Title != "Wild Cards I" {
			t.Errorf("title = %q, want Wild Cards I", got[0].Title)
		}
		want := []string{"PID01", "PID02", "PID03"}
		if !reflect.DeepEqual(got[0].PIDs, want) {
			t.Errorf("PIDs = %v, want sorted %v", got[0].PIDs, want)
		}
	})

	t.Run("empty-album parts -> one group titled by agreed stripped name", func(t *testing.T) {
		l := lib(
			abp("Aces Abroad - Part 1", "George R. R. Martin", "", 1, "PA1"),
			abp("Aces Abroad - Part 2", "George R. R. Martin", "", 2, "PA2"),
			abp("Aces Abroad - Part 3", "George R. R. Martin", "", 3, "PA3"),
		)
		got := GroupLibraryForHeal(l)
		byTitle := healByTitle(got)
		if pids, ok := byTitle["Aces Abroad"]; !ok || len(pids) != 3 {
			t.Fatalf("want one 'Aces Abroad' group with 3 PIDs, got %+v", got)
		}
	})

	t.Run("distinct albums -> two groups", func(t *testing.T) {
		l := lib(
			abp("Ch1", "Author A", "Book One", 1, "B1a"),
			abp("Ch2", "Author A", "Book One", 2, "B1b"),
			abp("Ch1", "Author B", "Book Two", 1, "B2a"),
		)
		got := GroupLibraryForHeal(l)
		if len(got) != 2 {
			t.Fatalf("want 2 groups, got %d: %+v", len(got), got)
		}
	})

	t.Run("deterministic across runs despite map iteration", func(t *testing.T) {
		mk := func() *itunes.Library {
			return lib(
				abp("Strings", "Stephen Leigh", "Wild Cards I", 17, "P3"),
				abp("The Long Sleep", "Michael Cassutt", "Wild Cards I", 3, "P1"),
				abp("Ch1", "Author B", "Book Two", 1, "Z1"),
				abp("Ch2", "Author B", "Book Two", 2, "Z2"),
			)
		}
		first := GroupLibraryForHeal(mk())
		for run := range 20 {
			if !reflect.DeepEqual(first, GroupLibraryForHeal(mk())) {
				t.Fatalf("non-deterministic output on run %d", run)
			}
		}
	})

	t.Run("track with empty PID is dropped from its group", func(t *testing.T) {
		l := lib(
			abp("Ch1", "Author A", "Book One", 1, "K1"),
			abp("Ch2", "Author A", "Book One", 2, ""), // no PID -> not healable
		)
		got := GroupLibraryForHeal(l)
		if len(got) != 1 || len(got[0].PIDs) != 1 || got[0].PIDs[0] != "K1" {
			t.Fatalf("want 1 group with only PID K1, got %+v", got)
		}
	})
}
