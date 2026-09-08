// file: internal/activity/sql_migration_report_test.go
// version: 1.1.0
// guid: 9c07b5e1-42fa-4d86-b3c9-0e5a7d18f6b2
// last-edited: 2026-09-08

// Tests for the migration's user-visible status row.
//
// The properties under test are the ones that make this reporting safe to attach
// to a multi-hour data migration: it must never be able to fail the run, it must
// leave no phantom row when the run never starts, and it must keep a record of a
// FAILURE that survives a reboot (the checkpoint blob does not — see finish).

package activity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeBus records published SSE events. Publish never fails in production
// (EventHub.Publish always returns nil), but failWith proves the reporter does
// not depend on that.
type fakeBus struct {
	names    []string
	payloads []map[string]any
	failWith error
}

func (f *fakeBus) Publish(_ context.Context, eventName string, payload any) error {
	f.names = append(f.names, eventName)
	p, _ := payload.(map[string]any)
	f.payloads = append(f.payloads, p)
	return f.failWith
}

// lastPayloadFor returns the most recent payload published under eventName.
func (f *fakeBus) lastPayloadFor(eventName string) map[string]any {
	var found map[string]any
	for i, n := range f.names {
		if n == eventName {
			found = f.payloads[i]
		}
	}
	return found
}

// fakeOpsRecorder records calls and can be made to fail every write.
type fakeOpsRecorder struct {
	inserted []database.OperationV2Row
	progress []string
	currents []int
	statuses []string
	lastErr  *string

	failWith error
}

func (f *fakeOpsRecorder) InsertOperationV2(row database.OperationV2Row) error {
	f.inserted = append(f.inserted, row)
	return f.failWith
}

func (f *fakeOpsRecorder) UpdateOpProgressV2(_ string, current, _ int, message string) error {
	f.progress = append(f.progress, message)
	f.currents = append(f.currents, current)
	return f.failWith
}

func (f *fakeOpsRecorder) UpdateOperationV2Status(
	_, status string, _, _ *time.Time, errMsg *string,
) error {
	f.statuses = append(f.statuses, status)
	if errMsg != nil {
		f.lastErr = errMsg
	}
	return f.failWith
}

func TestMigrationOpReporter_ReportsATierAndClosesCompleted(t *testing.T) {
	ops := &fakeOpsRecorder{}
	rep := &migrationOpReporter{ops: ops}

	rep.begin(time.Now().UTC())
	rep.observe(database.ActivityBackfillProgressUpdate{
		Tier: "change", TierIndex: 1, TiersTotal: 7,
		Scanned: 7_581_500, Copied: 4_739_375,
	})
	rep.finish("completed", "activity log migrated", nil)

	if len(ops.inserted) != 1 {
		t.Fatalf("want exactly 1 inserted row, got %d", len(ops.inserted))
	}
	row := ops.inserted[0]
	if row.DefID != migrationOpDefID || row.Plugin != migrationOpPlugin {
		t.Errorf("row mislabelled: def=%q plugin=%q", row.DefID, row.Plugin)
	}
	if row.Status != "running" {
		t.Errorf("row should start running, got %q", row.Status)
	}
	if row.ProgressTotal != 0 {
		t.Errorf("the migration reports NO denominator; got total=%d", row.ProgressTotal)
	}

	// The progress line must name the tier and its position, because "which of
	// the 7 tiers is this and how far in" is the whole question a user has.
	joined := strings.Join(ops.progress, "|")
	for _, want := range []string{"change", "1/7", "7.58M", "4.74M"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress %q missing %q", joined, want)
		}
	}
	if got := ops.statuses[len(ops.statuses)-1]; got != "completed" {
		t.Errorf("final status = %q, want completed", got)
	}
}

// A failed migration is the case the checkpoint blob CANNOT report after a
// restart: loadActivityBackfillProgress rewrites a `failed` tier verdict back to
// in_progress on the next boot. The op row is the durable record, so the cause
// has to reach it.
func TestMigrationOpReporter_FailureRecordsTheCause(t *testing.T) {
	ops := &fakeOpsRecorder{}
	rep := &migrationOpReporter{ops: ops}
	rep.begin(time.Now().UTC())

	rep.finish("failed", "migration failed", errors.New("stream tier=change: disk full"))

	if got := ops.statuses[len(ops.statuses)-1]; got != "failed" {
		t.Fatalf("final status = %q, want failed", got)
	}
	if ops.lastErr == nil {
		t.Fatal("a failed migration recorded NO error message — the reason is lost on reboot")
	}
	if !strings.Contains(*ops.lastErr, "disk full") {
		t.Errorf("error message %q does not carry the cause", *ops.lastErr)
	}
}

// Parity failure returns a nil error but must still be recorded as failed WITH a
// reason — this is the "clean verdict over unverified data" case the whole
// migration is built to avoid, so it must not close out looking successful.
func TestMigrationOpReporter_ParityFailureIsRecordedWithAReasonDespiteNilError(t *testing.T) {
	ops := &fakeOpsRecorder{}
	rep := &migrationOpReporter{ops: ops}
	rep.begin(time.Now().UTC())

	rep.finish("failed", "copy could not be verified", nil)

	if got := ops.statuses[len(ops.statuses)-1]; got != "failed" {
		t.Fatalf("final status = %q, want failed", got)
	}
	if ops.lastErr == nil || !strings.Contains(*ops.lastErr, "could not be verified") {
		t.Errorf("parity failure lost its reason: %v", ops.lastErr)
	}
}

// THE LOAD-BEARING ONE. A status write must never be able to kill a migration
// that is hours deep. Every store call fails here; nothing may panic and nothing
// may surface — the reporter has no error return by construction.
func TestMigrationOpReporter_StoreErrorsNeverReachTheMigration(t *testing.T) {
	ops := &fakeOpsRecorder{failWith: errors.New("pebble: write failed")}
	rep := &migrationOpReporter{ops: ops}

	rep.begin(time.Now().UTC())
	rep.observe(database.ActivityBackfillProgressUpdate{
		Tier: "change", TierIndex: 1, TiersTotal: 7, Scanned: 10, Copied: 10,
	})
	rep.finish("completed", "done", nil)

	// Reaching here without a panic is the assertion. Confirm it really did keep
	// trying rather than silently disabling itself after the first failure.
	if len(ops.progress) == 0 {
		t.Error("reporter stopped writing after an error instead of continuing best-effort")
	}
}

// The startup-ordering guard. resumeAfterStartup could flip a genuinely-running
// row to interrupted_dropped on a slow boot; the reporter re-asserts `running`
// once on the first progress update so the outcome does not depend on that
// ordering. Once, not every time — this is a correction, not a heartbeat.
func TestMigrationOpReporter_ReassertsRunningExactlyOnce(t *testing.T) {
	ops := &fakeOpsRecorder{}
	rep := &migrationOpReporter{ops: ops}
	rep.begin(time.Now().UTC())

	for i := range 5 {
		rep.observe(database.ActivityBackfillProgressUpdate{
			Tier: "change", TierIndex: 1, TiersTotal: 7, Scanned: i, Copied: i,
		})
	}

	running := 0
	for _, s := range ops.statuses {
		if s == "running" {
			running++
		}
	}
	if running != 1 {
		t.Errorf("re-asserted running %d times across 5 updates, want exactly 1", running)
	}
}

// No begin means no row: a shutdown during the settle delay must leave no
// phantom "running" operation for a user to puzzle over.
func TestMigrationOpReporter_WithoutBeginNothingIsWritten(t *testing.T) {
	ops := &fakeOpsRecorder{}
	rep := &migrationOpReporter{ops: ops}

	rep.observe(database.ActivityBackfillProgressUpdate{Tier: "change", TierIndex: 1})
	rep.finish("completed", "done", nil)

	if len(ops.inserted)+len(ops.progress)+len(ops.statuses) != 0 {
		t.Errorf("wrote status for a run that never started: %d inserts, %d progress, %d statuses",
			len(ops.inserted), len(ops.progress), len(ops.statuses))
	}
}

// A store that does not implement the recorder leaves ops nil, and the migration
// must run exactly as it did before this file existed.
func TestMigrationOpReporter_NilOpsIsANoOp(t *testing.T) {
	rep := &migrationOpReporter{ops: nil}
	rep.begin(time.Now().UTC())
	rep.observe(database.ActivityBackfillProgressUpdate{Tier: "change"})
	rep.finish("completed", "done", nil)
	// No panic == pass.
}

func TestHumanCount(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1_000, "1.0k"},
		{7_581_500, "7.58M"},
	} {
		if got := humanCount(tc.in); got != tc.want {
			t.Errorf("humanCount(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Progress must never go BACKWARDS. ActivityBackfillProgressUpdate.Copied is
// per-tier and restarts at each tier's checkpoint, so reporting it raw would drop
// the count 7 times over a production run — each drop looking like lost work.
func TestMigrationOpReporter_ProgressNeverGoesBackwardsAcrossTiers(t *testing.T) {
	ops := &fakeOpsRecorder{}
	rep := &migrationOpReporter{ops: ops}
	rep.begin(time.Now().UTC())

	// Tier 1 copies 100 and completes; tier 2 then starts over from its own 0.
	rep.observe(database.ActivityBackfillProgressUpdate{
		Tier: "change", TierIndex: 1, TiersTotal: 7, Scanned: 100, Copied: 100})
	rep.observe(database.ActivityBackfillProgressUpdate{
		Tier: "change", TierIndex: 1, TiersTotal: 7, Scanned: 100, Copied: 100,
		Done: true, Verdict: "clean"})
	rep.observe(database.ActivityBackfillProgressUpdate{
		Tier: "debug", TierIndex: 2, TiersTotal: 7, Scanned: 5, Copied: 5})

	for i := 1; i < len(ops.currents); i++ {
		if ops.currents[i] < ops.currents[i-1] {
			t.Fatalf("progress went backwards: %v", ops.currents)
		}
	}
	if got := ops.currents[len(ops.currents)-1]; got != 105 {
		t.Errorf("after 100 + 5 across two tiers, current = %d, want 105", got)
	}
}

// Closing the row must not erase the count it just reported: UpdateOpProgressV2
// writes ProgressCurrent unconditionally, so a 0 in the final call would leave a
// completed migration displaying "0 processed".
// THE VISIBILITY TEST. Writing the row is not the same as showing it.
// useOperationsStore calls loadFromServer() at app mount and thereafter only in
// response to SSE events — there is no polling interval — so without these three
// events a 4-hour migration renders frozen at whatever count the page load
// happened to catch. The names and payload keys are what the store dispatches on
// (op.created → reload, op.updated → merge counts, op.terminal → reload).
func TestMigrationOpReporter_PublishesTheEventsTheUIListensFor(t *testing.T) {
	ops := &fakeOpsRecorder{}
	bus := &fakeBus{}
	rep := &migrationOpReporter{ops: ops, bus: bus}

	rep.begin(time.Now().UTC())
	rep.observe(database.ActivityBackfillProgressUpdate{
		Tier: "change", TierIndex: 1, TiersTotal: 7, Scanned: 900, Copied: 900,
	})
	rep.finish("completed", "activity log migrated", nil)

	for _, want := range []string{"op.created", "op.updated", "op.terminal"} {
		if bus.lastPayloadFor(want) == nil {
			t.Errorf("never published %q — the UI has no way to learn about this (published: %v)",
				want, bus.names)
		}
	}
	if got := bus.names[0]; got != "op.created" {
		t.Errorf("first event = %q, want op.created", got)
	}

	created := bus.lastPayloadFor("op.created")
	if created["def_id"] != migrationOpDefID || created["op_id"] != rep.opID {
		t.Errorf("op.created identifies the wrong operation: %v", created)
	}

	// op.updated must carry the message too. The registry's own op.updated omits
	// it; if this one did the same, the count would advance live while the tier
	// line stayed stuck on whatever the last full reload returned.
	updated := bus.lastPayloadFor("op.updated")
	msg, _ := updated["message"].(string)
	if !strings.Contains(msg, "change") || !strings.Contains(msg, "1/7") {
		t.Errorf("op.updated message %q does not name the tier and position", msg)
	}
	if updated["progress_current"] != 900 {
		t.Errorf("op.updated progress_current = %v, want 900", updated["progress_current"])
	}

	if got := bus.lastPayloadFor("op.terminal")["status"]; got != "completed" {
		t.Errorf("op.terminal status = %v, want completed", got)
	}
}

// op.created tells the UI to re-fetch the timeline. Published before the row
// existed, that fetch finds nothing and the migration stays invisible until some
// later event happens to trigger another reload.
func TestMigrationOpReporter_PublishesCreatedOnlyAfterTheRowExists(t *testing.T) {
	ops := &fakeOpsRecorder{}
	bus := &fakeBus{}
	// Recording the insert count at publish time is what makes the ordering
	// observable at all — both calls have returned by the time begin() has.
	var insertsWhenPublished = -1
	rep := &migrationOpReporter{ops: ops, bus: &orderingBus{inner: bus, ops: ops, seen: &insertsWhenPublished}}

	rep.begin(time.Now().UTC())

	if insertsWhenPublished != 1 {
		t.Errorf("op.created published with %d rows inserted, want 1 — a UI reload racing this event finds no row",
			insertsWhenPublished)
	}
}

// orderingBus records how many rows had been inserted at the moment of publish.
type orderingBus struct {
	inner *fakeBus
	ops   *fakeOpsRecorder
	seen  *int
}

func (o *orderingBus) Publish(ctx context.Context, eventName string, payload any) error {
	if eventName == "op.created" {
		*o.seen = len(o.ops.inserted)
	}
	return o.inner.Publish(ctx, eventName, payload)
}

// The bus is as incapable of failing the migration as the store is, and a nil bus
// (no hub wired) must leave the run working exactly as before — the row is still
// written and still correct, it just stops updating between page loads.
func TestMigrationOpReporter_BusFailuresAndNilBusNeverReachTheMigration(t *testing.T) {
	ops := &fakeOpsRecorder{}
	rep := &migrationOpReporter{ops: ops, bus: &fakeBus{failWith: errors.New("bus exploded")}}
	rep.begin(time.Now().UTC())
	rep.observe(database.ActivityBackfillProgressUpdate{Tier: "change", TierIndex: 1, TiersTotal: 7})
	rep.finish("completed", "done", nil)

	nilBus := &migrationOpReporter{ops: &fakeOpsRecorder{}, bus: nil}
	nilBus.begin(time.Now().UTC())
	nilBus.observe(database.ActivityBackfillProgressUpdate{Tier: "change", TierIndex: 1, TiersTotal: 7})
	nilBus.finish("completed", "done", nil)

	// No panic == pass. Confirm the STORE writes still happened in both cases:
	// a bus failure must not disable the durable record.
	if len(ops.statuses) == 0 {
		t.Error("a failing bus suppressed the store writes")
	}
}

// The published count must be the accumulated one, for the same reason the
// stored count is: per-tier counts restart at every tier boundary, and a live
// feed that dropped 7 times over a run would read as lost work.
func TestMigrationOpReporter_PublishedCountAccumulatesAcrossTiers(t *testing.T) {
	bus := &fakeBus{}
	rep := &migrationOpReporter{ops: &fakeOpsRecorder{}, bus: bus}
	rep.begin(time.Now().UTC())

	rep.observe(database.ActivityBackfillProgressUpdate{
		Tier: "change", TierIndex: 1, TiersTotal: 7, Scanned: 100, Copied: 100,
		Done: true, Verdict: "clean"})
	rep.observe(database.ActivityBackfillProgressUpdate{
		Tier: "debug", TierIndex: 2, TiersTotal: 7, Scanned: 5, Copied: 5})

	if got := bus.lastPayloadFor("op.updated")["progress_current"]; got != 105 {
		t.Errorf("published progress_current = %v, want 105 (100 from tier 1 + 5 from tier 2)", got)
	}
}

func TestMigrationOpReporter_FinishKeepsTheFinalCount(t *testing.T) {
	ops := &fakeOpsRecorder{}
	rep := &migrationOpReporter{ops: ops}
	rep.begin(time.Now().UTC())
	rep.observe(database.ActivityBackfillProgressUpdate{
		Tier: "change", TierIndex: 1, TiersTotal: 7, Scanned: 4_739_375, Copied: 4_739_375})

	rep.finish("completed", "activity log migrated", nil)

	if got := ops.currents[len(ops.currents)-1]; got != 4_739_375 {
		t.Errorf("final progress current = %d, want 4739375 (the count was erased)", got)
	}
}
