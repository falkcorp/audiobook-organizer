// file: internal/server/op_dedupe_decision_test.go
// version: 1.0.0
// guid: bdbf6e2b-8b2f-472e-a712-ac607e5b2f16
// last-edited: 2026-08-22

// ENQ-DEDUP-1 per-def table test.
//
// EnqueueOp only reuses an active op when the new request asks for the SAME
// work: byte-equal marshalled params, OR a cron-scheduled def (Schedule != nil),
// OR an explicit def-level opt-in (OperationDef.DedupeQueuedRuns). The opt-in is
// the one that can silently re-create the 2026-08-21 prod incident — it makes a
// def discard a differing request again — so every def that sets it must be
// listed here with a reason. The list is EMPTY today; adding a def to the field
// without adding it here fails this test.
//
// This lives in internal/server rather than internal/operations/registry because
// the registry package cannot enumerate the REAL defs: a bare registry has no
// defs registered, and registration happens across internal/server's opRegistrars
// and the plugin packages. NewServer is the only place the full set exists.
// Modelled on server_op_registration_test.go, which enumerates the same way.
package server

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
)

// dedupeOptInReasons lists every def id allowed to set DedupeQueuedRuns, with
// the reason. EMPTY BY DESIGN as of ENQ-DEDUP-1: no def opts in. A def whose
// params legitimately vary between runs that must NOT both happen belongs here;
// nothing else does.
var dedupeOptInReasons = map[string]string{}

// selectionCarryingDefs are the defs whose params carry an explicit selection of
// books. For these, a second request with a different selection MUST queue a
// second run rather than dedupe — that is exactly the 2026-08-21 prod incident
// (approving more books during metadata.batch-apply-cached applied nothing). So
// none of them may set DedupeQueuedRuns, and none may carry a Schedule (which
// would dedupe them on def id alone via the cron clause).
//
// Two ids differ from the ENQ-DEDUP-1 brief, which named them by their source
// file / ConcurrencyKey rather than their def id:
//   - maintenance.chapters-backfill — the brief called it
//     "maintenance.probe-directory-books", which is that def's ConcurrencyKey
//     (chapters_backfill.go:199) and also a SEPARATE def
//     (probe_directory_books.go:109).
//   - acoustid.fingerprint-rescan — the brief called it "acoustid.fingerprint".
var selectionCarryingDefs = []string{
	"metadata.batch-save",              // batch_save_op.go:41, params BookIDs at :31
	"metadata.batch-apply-cached",      // batch_apply_op.go:61, params BookIDs at :37 — the incident op
	"library.bulk-write-back",          // library_writeback_op.go:31, params BookIDs at :22
	"library.organize",                 // library_core_ops.go:246, params BookIDs at :53
	"maintenance.bulk-write-back",      // write_back.go:29, params BookIDs at :22
	"maintenance.regroup-shattered-ai", // regroup_shattered_ai.go:106, params MemberBookIDs at :68
	"maintenance.chapters-backfill",    // chapters_backfill.go:176, params BookIDs at :96
	"acoustid.fingerprint-rescan",      // fingerprint_rescan.go:51, params BookIDs at :29
}

// absentFromThisHarness excuses a selection-carrying def from being REGISTERED
// here, with the reason. It excuses nothing else: if the def turns up in the
// registry anyway, the loop below asserts it in full, so the exemption cannot
// quietly outlive its cause.
//
// The acoustid plugin's Build returns a nil *Plugin unless the container can
// supply both a dedup engine and an embedding store (register.go:28-36), and
// Register's nil-engine guard (plugin.go:42-44) then registers zero ops. A
// NewServer over a mock store supplies neither, so the whole acoustid.* family
// is absent from this harness. Its dedupe decision is asserted where the def is
// constructible instead: internal/plugins/acoustid/fingerprint_rescan_dedupe_test.go.
var absentFromThisHarness = map[string]string{
	"acoustid.fingerprint-rescan": "acoustid plugin needs a dedup engine + embedding store; " +
		"asserted in internal/plugins/acoustid/fingerprint_rescan_dedupe_test.go instead",
}

// TestOperationDefs_DedupeDecisionIsExplicit asserts that every registered def
// with a non-empty ConcurrencyKey has made an explicit dedupe decision, and that
// the selection-carrying defs are all on the queue-a-second-run default.
func TestOperationDefs_DedupeDecisionIsExplicit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origCfg := config.AppConfig
	config.AppConfig.RootDir = ""
	t.Cleanup(func() { config.AppConfig = origCfg })

	store := mocks.NewMockStore(t)
	store.EXPECT().SetRootDir(mock.Anything).Return().Maybe()
	allowOpDefinitionUpserts(store)

	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore) })

	srv := NewServer(store)
	t.Cleanup(func() {
		if srv.fileIOPool != nil {
			srv.fileIOPool.Stop()
		}
	})

	defs := srv.opRegistry.ActiveDefs()
	if len(defs) == 0 {
		t.Fatal("server registered ZERO operations — nothing to assert about")
	}

	withKey := 0
	optedIn := 0
	for _, d := range defs {
		if d.ConcurrencyKey == "" {
			continue
		}
		withKey++
		if !d.DedupeQueuedRuns {
			continue // (b) the default: dedupe only on byte-equal params
		}
		// (a) opted in — must be listed with a reason.
		optedIn++
		if reason, ok := dedupeOptInReasons[d.ID]; !ok || reason == "" {
			t.Errorf("def %q sets DedupeQueuedRuns but is not listed in dedupeOptInReasons — "+
				"an opt-in makes this def discard a request whose params differ, which is the "+
				"2026-08-21 prod incident. Add it with a reason, or leave the field false.", d.ID)
		}
	}

	if withKey == 0 {
		t.Fatal("no def with a non-empty ConcurrencyKey was registered — the enumeration is broken, " +
			"not the code under test")
	}

	// The opt-in list is empty today; if that changes, the loop above is what
	// enforces the reason, and this pins the count so a silent growth is visible.
	if optedIn != len(dedupeOptInReasons) {
		t.Errorf("defs setting DedupeQueuedRuns = %d, but dedupeOptInReasons has %d entries "+
			"(a stale entry is as wrong as a missing one)", optedIn, len(dedupeOptInReasons))
	}

	// Every selection-carrying def must be registered, must have a
	// ConcurrencyKey (otherwise nothing serializes its runs), must NOT opt in,
	// and must NOT be cron-scheduled.
	for _, id := range selectionCarryingDefs {
		def, ok := srv.opRegistry.Def(id)
		if !ok {
			if reason, excused := absentFromThisHarness[id]; excused {
				t.Logf("selection-carrying def %q not registered in this harness: %s", id, reason)
				continue
			}
			t.Errorf("selection-carrying def %q is not registered — the id changed; "+
				"re-derive the list from the params structs carrying BookIDs", id)
			continue
		}
		if def.ConcurrencyKey == "" {
			t.Errorf("selection-carrying def %q has an empty ConcurrencyKey: a second queued run "+
				"would not be serialized by dispatcher Gate 3", id)
		}
		if def.DedupeQueuedRuns {
			t.Errorf("selection-carrying def %q sets DedupeQueuedRuns: a request with a different "+
				"book selection would be silently dropped (prod, 2026-08-21)", id)
		}
		if def.Schedule != nil {
			t.Errorf("selection-carrying def %q has a non-nil Schedule: the cron clause in EnqueueOp "+
				"would dedupe it on def id alone and drop a differing selection", id)
		}
	}

	t.Logf("%d/%d registered defs carry a ConcurrencyKey; %d opt into DedupeQueuedRuns",
		withKey, len(defs), optedIn)
}
