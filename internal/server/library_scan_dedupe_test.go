// file: internal/server/library_scan_dedupe_test.go
// version: 1.0.0
// guid: 9f4c6b21-3ad8-4e70-95b2-1c07de83af64
// last-edited: 2026-09-08

package server

import (
	"testing"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/require"
)

// TestLibraryScanOp_DedupesQueuedRuns guards the one field that stops a second
// library scan stacking up behind a running one.
//
// The mechanism itself is covered in internal/operations/registry
// (enqueue_dedupe_test.go). What is asserted here is that library.scan opts in,
// because the reasoning is specific to this def and invisible from the registry:
//
// EnqueueOp's ConcurrencyKey dedupe reuses an active op only when the incoming
// params are BYTE-IDENTICAL to the active row's. library.scan is ResumeRestart,
// and resumeRestart merges the saved checkpoint (resume_folder_idx /
// resume_item_offset) into the row's params. So once a scan has resumed even
// once, its stored params can no longer match a fresh enqueue's, the byte
// comparison fails, and a duplicate is queued. Observed on prod 2026-09-08: a
// library.scan running at resume_count=2 with a second scan queued behind it.
//
// A future edit that drops this flag would reintroduce that silently — the op
// still works, it just quietly runs twice — so it is pinned rather than trusted.
func TestLibraryScanOp_DedupesQueuedRuns(t *testing.T) {
	reg := capOpReg(t)
	require.NoError(t, (&Server{}).RegisterLibraryScanOp(reg))

	def, ok := reg.Def("library.scan")
	require.True(t, ok, "library.scan not registered")

	require.True(t, def.DedupeQueuedRuns,
		"library.scan must set DedupeQueuedRuns: its params carry resume checkpoint "+
			"state, so the byte-comparison dedupe cannot match and a second scan queues")

	// DedupeQueuedRuns only takes effect for a def that reaches the dedupe at
	// all, and that branch is gated on a non-empty ConcurrencyKey. Without this
	// the flag above would be inert and the assertion meaningless.
	require.NotEmpty(t, def.ConcurrencyKey,
		"DedupeQueuedRuns is only consulted inside the ConcurrencyKey branch of EnqueueOp")

	// The premise of the whole fix: params mutate across runs precisely because
	// the def restarts from a checkpoint. If this ever became ResumeDrop, the
	// params would stop drifting and the flag would deserve re-justification
	// rather than silent inheritance.
	require.Equal(t, opsregistry.ResumeRestart, def.ResumePolicy,
		"the checkpoint-merge that makes params drift is a consequence of ResumeRestart")

	// Not a set-parameterized def: it walks the whole root rather than carrying
	// its own list of items. That is what makes dropping a duplicate request
	// safe here, and unsafe for defs like metadata.batch-apply-cached whose
	// params ARE the work list.
	require.Nil(t, def.MergeQueuedParams,
		"library.scan must not be set-parameterized; if it gains MergeQueuedParams "+
			"then a queued run is a different batch and deduping it would lose work")
}
