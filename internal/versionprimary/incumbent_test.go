// file: internal/versionprimary/incumbent_test.go
// version: 1.0.0
// guid: 9d03a53c-7bc5-4dd5-9361-8479a58fb2f8
// last-edited: 2026-10-01

package versionprimary

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestIncumbent(t *testing.T) {
	tr, fa := true, false
	yes := true
	alive := func(id string) bool { return id == "live-survivor" }
	cases := []struct {
		name    string
		members []database.Book
		want    string // "" = nil
	}{
		{"explicit true wins over nil", []database.Book{
			{ID: "n"}, {ID: "t", IsPrimaryVersion: &tr}}, "t"},
		{"first explicit true wins", []database.Book{
			{ID: "t1", IsPrimaryVersion: &tr}, {ID: "t2", IsPrimaryVersion: &tr}}, "t1"},
		{"single nil beside false", []database.Book{
			{ID: "f", IsPrimaryVersion: &fa}, {ID: "n"}}, "n"},
		{"two nils are ambiguous", []database.Book{
			{ID: "n1"}, {ID: "n2"}, {ID: "f", IsPrimaryVersion: &fa}}, ""},
		{"all false", []database.Book{
			{ID: "f1", IsPrimaryVersion: &fa}, {ID: "f2", IsPrimaryVersion: &fa}}, ""},
		{"trashed true skipped", []database.Book{
			{ID: "t", IsPrimaryVersion: &tr, MarkedForDeletion: &yes}, {ID: "n"}}, "n"},
		{"trashed nil does not make nils ambiguous", []database.Book{
			{ID: "n1", MarkedForDeletion: &yes}, {ID: "n2"}}, "n2"},
		{"merge loser of a live survivor skipped", []database.Book{
			{ID: "t", IsPrimaryVersion: &tr, MergedIntoBookID: new("live-survivor")}, {ID: "n"}}, "n"},
		{"merge loser of a dead survivor electable", []database.Book{
			{ID: "t", IsPrimaryVersion: &tr, MergedIntoBookID: new("gone")}, {ID: "n"}}, "t"},
		{"survivor inside the group answered from the group", []database.Book{
			{ID: "t", IsPrimaryVersion: &tr, MergedIntoBookID: new("s")},
			{ID: "s", MarkedForDeletion: &yes}, {ID: "n"}}, "t"},
		{"empty", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Incumbent(tc.members, alive)
			gotID := ""
			if got != nil {
				gotID = got.ID
			}
			if gotID != tc.want {
				t.Errorf("Incumbent = %q, want %q", gotID, tc.want)
			}
		})
	}
}
