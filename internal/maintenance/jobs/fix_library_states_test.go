// file: internal/maintenance/jobs/fix_library_states_test.go
// version: 2.0.0
// guid: e2f3a4b5-c6d7-8901-efab-234567890567
// last-edited: 2026-09-10

// Package jobs_test pins the ABSENCE of the retired `fix-library-states`
// maintenance job.
//
// The job used to reconcile `library_state` against filesystem presence by
// writing the values "present" and "missing". Nothing else in the codebase
// produces or consumes that vocabulary: the live vocabulary ABS, the dashboard's
// Needs-Organizing count, the filter chips and the list warmer all read is
// "organized" / "imported" (see #3097). Running the job therefore set every book
// to a value that fails the ABS filter — it would have emptied the ABS-visible
// library rather than repairing it. It was registered and reachable from the ops
// UI, one click away, so the job was deleted outright rather than hidden.
//
// This file is deliberately the whole test surface for that id: it exists so the
// job cannot be re-added silently. If a future change reintroduces a job with
// this id, this test fails and the reviewer is forced to read the paragraph
// above.
package jobs_test

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixLibraryStatesRetiredID is the id of the retired job. Spelled out once so a
// re-registration is caught by the string, not by a symbol that no longer exists.
const fixLibraryStatesRetiredID = "fix-library-states"

// TestFixLibraryStatesJob_NotRegistered replaces the former
// TestFixLibraryStatesJob_Registered. It asserts the opposite of what that test
// asserted: the id must NOT resolve, and must NOT appear in the registry listing
// the ops UI enumerates.
func TestFixLibraryStatesJob_NotRegistered(t *testing.T) {
	// Lookup must fail — this is what the maintenance dispatcher calls before
	// running anything, so a failing Get is what makes the job unreachable.
	j, err := maintenance.Get(fixLibraryStatesRetiredID)
	require.Error(t, err, "retired job %q must not be resolvable; see the package comment for why running it would empty the ABS-visible library", fixLibraryStatesRetiredID)
	assert.Nil(t, j, "retired job %q must not be returned by maintenance.Get", fixLibraryStatesRetiredID)

	// Absence from the listing is the half that actually pins re-addition: a
	// re-registered job would make Get succeed, so checking Get alone is not
	// enough to prove the id stays gone.
	all := maintenance.All()
	require.NotEmpty(t, all, "no maintenance jobs registered at all; this test would pass vacuously")
	for _, job := range all {
		assert.NotEqual(t, fixLibraryStatesRetiredID, job.ID(),
			"retired job %q is registered again; it writes a present/missing library_state vocabulary nothing consumes and would empty the ABS-visible library", fixLibraryStatesRetiredID)
	}
}
