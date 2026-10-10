// file: internal/server/handlers/organize_params_test.go
// version: 1.0.0
// guid: ce160b36-66aa-4d7b-aff2-76ada15206a8
// last-edited: 2026-10-10

package handlers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// This is the mirror of server.libraryOrganizeParams, and the dedupe
// byte-compares what the two produce, so it carries the same round-trip test:
// an explicit empty book_ids must stay [] (organize nothing) and a nil one
// must stay omitted (no restriction).
func TestLibraryOrganizeParams_EmptyBookIDsSurviveTheRoundTrip(t *testing.T) {
	raw, err := json.Marshal(libraryOrganizeParams{BookIDs: []string{}})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"book_ids":[]`)

	var back libraryOrganizeParams
	require.NoError(t, json.Unmarshal(raw, &back))
	require.NotNil(t, back.BookIDs, "an explicit empty scope must not decode as 'no scope'")
	require.Empty(t, back.BookIDs)

	raw, err = json.Marshal(libraryOrganizeParams{})
	require.NoError(t, err)
	require.Equal(t, `{"fetch_metadata_first":false}`, string(raw))
	back = libraryOrganizeParams{}
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Nil(t, back.BookIDs)
}
