// file: internal/aiscan/cross_validation_test.go
// version: 1.1.0
// guid: e1f7a3b5-8c9d-0e1f-2a3b-4c5d6e7f8a9b
// last-edited: 2026-10-14

package aiscan

import (
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

func TestCrossValidateAgreed(t *testing.T) {
	groups := []database.ScanSuggestion{
		{Action: "merge", CanonicalName: "J. N. Chaney", Confidence: "high", AuthorIDs: []int{10, 20}, Reason: "name variants"},
	}
	full := []database.ScanSuggestion{
		{Action: "merge", CanonicalName: "J.N. Chaney", Confidence: "medium", AuthorIDs: []int{10, 20}, Reason: "same author"},
	}

	results := CrossValidate(1, groups, full)
	require.Len(t, results, 1)
	require.Equal(t, "agreed", results[0].Agreement)
	require.Equal(t, "high", results[0].Suggestion.Confidence)            // higher of the two
	require.Equal(t, "J. N. Chaney", results[0].Suggestion.CanonicalName) // groups' canonical preferred
	require.Equal(t, "cross_validate", results[0].Suggestion.Source)
}

func TestCrossValidateDisagreed(t *testing.T) {
	groups := []database.ScanSuggestion{
		{Action: "merge", CanonicalName: "GraphicAudio", Confidence: "high", AuthorIDs: []int{100, 200}},
	}
	full := []database.ScanSuggestion{
		{Action: "reclassify", CanonicalName: "GraphicAudio", Confidence: "high", AuthorIDs: []int{100, 200}},
	}

	results := CrossValidate(1, groups, full)
	require.Len(t, results, 2) // both disagreed results
	require.Equal(t, "disagreed", results[0].Agreement)
	require.Equal(t, "disagreed", results[1].Agreement)
	require.Equal(t, "merge", results[0].Suggestion.Action)
	require.Equal(t, "reclassify", results[1].Suggestion.Action)
}

func TestCrossValidateOneSided(t *testing.T) {
	groups := []database.ScanSuggestion{
		{Action: "merge", CanonicalName: "Stephen King", AuthorIDs: []int{1, 2}},
	}
	full := []database.ScanSuggestion{
		{Action: "reclassify", CanonicalName: "BBC Studios", AuthorIDs: []int{50}},
	}

	results := CrossValidate(1, groups, full)
	require.Len(t, results, 2)

	// Find groups_only and full_only
	var groupsOnly, fullOnly *database.ScanResult
	for i := range results {
		switch results[i].Agreement {
		case "groups_only":
			groupsOnly = &results[i]
		case "full_only":
			fullOnly = &results[i]
		}
	}
	require.NotNil(t, groupsOnly)
	require.NotNil(t, fullOnly)
	require.Equal(t, "Stephen King", groupsOnly.Suggestion.CanonicalName)
	require.Equal(t, "BBC Studios", fullOnly.Suggestion.CanonicalName)
}

func TestCrossValidateIDMerge(t *testing.T) {
	groups := []database.ScanSuggestion{
		{Action: "merge", CanonicalName: "Test", AuthorIDs: []int{1, 2, 3}},
	}
	full := []database.ScanSuggestion{
		{Action: "merge", CanonicalName: "Test", AuthorIDs: []int{2, 3, 4}},
	}

	results := CrossValidate(1, groups, full)
	require.Len(t, results, 1)
	require.Equal(t, "agreed", results[0].Agreement)
	require.ElementsMatch(t, []int{1, 2, 3, 4}, results[0].Suggestion.AuthorIDs)
}

func TestCrossValidateEmpty(t *testing.T) {
	results := CrossValidate(1, nil, nil)
	require.Empty(t, results)
}

func TestHigherConfidence(t *testing.T) {
	require.Equal(t, "high", higherConfidence("high", "low"))
	require.Equal(t, "high", higherConfidence("low", "high"))
	require.Equal(t, "medium", higherConfidence("medium", "low"))
	require.Equal(t, "medium", higherConfidence("medium", "medium"))
}

// An agreed row takes the groups scan's roles, else the full scan's, and records
// which: the groups ids are positions in the group, the full ids author ids.
func TestCrossValidateAgreed_RecordsWhichScanItsRolesCameFrom(t *testing.T) {
	positional := json.RawMessage(`{"narrator":{"name":"N","ids":[1]}}`)
	byID := json.RawMessage(`{"narrator":{"name":"N","ids":[20]}}`)
	agreed := func(gRoles, fRoles json.RawMessage) database.ScanSuggestion {
		t.Helper()
		results := CrossValidate(1,
			[]database.ScanSuggestion{{Action: "merge", CanonicalName: "Synthetic A", AuthorIDs: []int{10, 20}, Roles: gRoles}},
			[]database.ScanSuggestion{{Action: "merge", CanonicalName: "Synthetic A", AuthorIDs: []int{10, 20}, Roles: fRoles}})
		require.Len(t, results, 1)
		require.Equal(t, "agreed", results[0].Agreement)
		return results[0].Suggestion
	}

	s := agreed(positional, byID)
	require.JSONEq(t, string(positional), string(s.Roles), "the groups scan's roles win")
	require.Equal(t, "groups_scan", s.RolesSource)
	require.False(t, RolesNameAuthorIDs(s))

	s = agreed(nil, byID)
	require.JSONEq(t, string(byID), string(s.Roles))
	require.Equal(t, "full_scan", s.RolesSource)
	require.True(t, RolesNameAuthorIDs(s))

	s = agreed(nil, nil)
	require.Empty(t, s.Roles)
	require.Empty(t, s.RolesSource)
}

func TestRolesNameAuthorIDs(t *testing.T) {
	for _, tc := range []struct {
		source, rolesSource string
		want                bool
	}{
		{"full_scan", "", true},
		{"groups_scan", "", false},
		{"cross_validate", "full_scan", true},
		{"cross_validate", "groups_scan", false},
		{"cross_validate", "", false}, // stored before RolesSource: cannot tell, read as positions
	} {
		got := RolesNameAuthorIDs(database.ScanSuggestion{Source: tc.source, RolesSource: tc.rolesSource})
		require.Equal(t, tc.want, got, "%s/%q", tc.source, tc.rolesSource)
	}
}

// A groups answer that flagged a narrator by position refuses the row even when
// the row carries the full scan's id-based flag: the full scan's ids say nothing
// about which author the groups answer meant.
func TestUnaccounted_APositionalFlagIsNotSilencedByAnIDFlag(t *testing.T) {
	flags := RawRoleFlags{Authors: map[int]bool{20: true}, Groups: map[int]bool{4: true}}
	why := flags.Unaccounted("cross_validate", 4, []int{10, 20, 30}, map[int]bool{20: true})
	require.Contains(t, why, "by position")
	require.Empty(t, flags.Unaccounted("cross_validate", 5, []int{10, 20, 30}, map[int]bool{20: true}),
		"another group's positional flag does not touch this row")
}
