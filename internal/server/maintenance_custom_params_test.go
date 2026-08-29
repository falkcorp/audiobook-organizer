// file: internal/server/maintenance_custom_params_test.go
// version: 1.0.0
// guid: 4e1b7d38-92a5-4c06-8f31-6a0d5b3ce271
// last-edited: 2026-08-29

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// These tests exercise the LIVE dispatcher path end-to-end: HTTP request body →
// runMaintenanceJob → EnqueueOp → the registry's op Run closure →
// maintenance.WithRawParams → the job's own Run.
//
// That whole-path shape is the point. The five jobs that take a custom parameter
// (revert-metadata-fetch, bulk-fetch-metadata, bulk-deluge-import,
// scan-composer-tags, prune-book-snapshots) all read
// store.GetOperationParams(opID) — a Pebble key whose only writer,
// operations.SaveParams, lost its last maintenance-path caller when the v1 op
// minter was retired (#2784). The read survived its writer, so every one of them
// silently received nothing.
//
// It survived because the tests that covered it stubbed GetOperationParamsFunc
// on a MockStore. That hook is never populated in production; stubbing it
// simulates a writer that does not exist, so those tests asserted a route
// instead of observing one and stayed green across the whole outage. A test that
// hand-stubs the store cannot tell a live channel from a dead one — only driving
// the real handler can.
//
// The assertions below are on what reaches the JOB, never on the persisted
// params row: a test asserting on the row would still pass if the
// WithRawParams line in the Run closure were deleted.

// customParamsProbeJob records the raw params blob its Run received, and
// advertises a custom key plus dry_run:true in DefaultParams — the shape
// listMaintenanceJobs publishes to clients as `default_params`, and which the
// run route used to contradict.
type customParamsProbeJob struct {
	id string

	mu   sync.Mutex
	runs []customProbeRun
}

// customProbeRun is what one Run observed. dryRunArg is captured separately from
// the params blob because they are two different channels for the same flag, and
// the whole hazard is them disagreeing.
type customProbeRun struct {
	raw       json.RawMessage
	dryRunArg bool
}

func (j *customParamsProbeJob) ID() string   { return j.id }
func (j *customParamsProbeJob) Name() string { return "Custom Params Probe " + j.id }
func (j *customParamsProbeJob) Description() string {
	return "Test probe; records the raw params and dryRun argument it receives."
}
func (j *customParamsProbeJob) Category() string { return "test" }
func (j *customParamsProbeJob) CanResume() bool  { return false }

func (j *customParamsProbeJob) DefaultParams() any {
	return struct {
		DryRun     bool     `json:"dry_run"`
		FetchOpIDs []string `json:"fetch_op_ids"`
		FixMode    string   `json:"fix_mode"`
		KeepCount  int      `json:"keep_count"`
	}{DryRun: true, FetchOpIDs: []string{}, FixMode: "set_narrator", KeepCount: 10}
}

func (j *customParamsProbeJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}

func (j *customParamsProbeJob) Run(ctx context.Context, _ maintenance.JobStore, _ maintenance.ProgressReporter, dryRun bool) error {
	j.mu.Lock()
	j.runs = append(j.runs, customProbeRun{
		raw:       maintenance.RawParamsFromCtx(ctx),
		dryRunArg: dryRun,
	})
	j.mu.Unlock()
	return nil
}

func (j *customParamsProbeJob) reset() {
	j.mu.Lock()
	j.runs = nil
	j.mu.Unlock()
}

// await blocks until the job has run once. A timeout means the request never
// reached Run at all — a different defect from receiving the wrong params, so it
// is reported apart from the value assertions.
func (j *customParamsProbeJob) await(t *testing.T) customProbeRun {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		j.mu.Lock()
		n := len(j.runs)
		var first customProbeRun
		if n > 0 {
			first = j.runs[0]
		}
		j.mu.Unlock()
		if n > 0 {
			return first
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %q never ran within 20s", j.id)
	return customProbeRun{}
}

var customProbe = &customParamsProbeJob{id: "test-probe-custom-params"}

func init() {
	// maintenance.Register panics on a duplicate ID and has no Unregister, so
	// this registers exactly once for the package's whole test run.
	maintenance.Register(customProbe)
}

// TestRunMaintenanceJob_CustomParamsReachTheJob is the assertion the whole change
// exists for: keys the dispatcher does not know about must survive to the job.
//
// fetch_op_ids is the sharp one. revert-metadata-fetch REQUIRES it, so while the
// dispatcher bound only dry_run and enqueued a fixed three-field struct, that job
// could reach exactly one outcome — the error "fetch_op_ids required" — no matter
// what the operator sent. It was 100% non-functional from the route the UI uses.
//
// This test fails against a handler that marshals a fixed struct, which is the
// mutant a job-side-only fix would not kill: reading RawParamsFromCtx correctly
// still yields nothing if the dispatcher never put the key in the blob.
func TestRunMaintenanceJob_CustomParamsReachTheJob(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	customProbe.reset()

	w := postMaintenanceJob(t, server, customProbe.ID(),
		`{"dry_run": false, "fetch_op_ids": ["op-aaa", "op-bbb"], "fix_mode": "clear", "keep_count": 3}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}

	run := customProbe.await(t)
	if len(run.raw) == 0 {
		t.Fatal("the job received NO params on its context; a custom parameter has no route into Run")
	}

	var got struct {
		JobID      string   `json:"job_id"`
		DryRun     bool     `json:"dry_run"`
		FetchOpIDs []string `json:"fetch_op_ids"`
		FixMode    string   `json:"fix_mode"`
		KeepCount  int      `json:"keep_count"`
	}
	if err := json.Unmarshal(run.raw, &got); err != nil {
		t.Fatalf("decode params %s: %v", run.raw, err)
	}

	if len(got.FetchOpIDs) != 2 || got.FetchOpIDs[0] != "op-aaa" || got.FetchOpIDs[1] != "op-bbb" {
		t.Errorf("fetch_op_ids reached the job as %v, want [op-aaa op-bbb]; params were %s",
			got.FetchOpIDs, run.raw)
	}
	if got.FixMode != "clear" {
		t.Errorf("fix_mode reached the job as %q, want \"clear\"; params were %s", got.FixMode, run.raw)
	}
	if got.KeepCount != 3 {
		t.Errorf("keep_count reached the job as %d, want 3; params were %s", got.KeepCount, run.raw)
	}
	if got.JobID != customProbe.ID() {
		t.Errorf("job_id = %q, want %q; the job is reading someone else's params", got.JobID, customProbe.ID())
	}
}

// TestRunMaintenanceJob_CustomParamsAbsentWhenNotSent is the discriminating
// negative. A handler that hardcoded these keys, or a probe that reported them
// for any blob, would pass the test above. Verified against a known-good twin:
// the test above sends the keys and sees them, this one omits them and must not.
func TestRunMaintenanceJob_CustomParamsAbsentWhenNotSent(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	customProbe.reset()

	w := postMaintenanceJob(t, server, customProbe.ID(), `{"dry_run": false}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}

	run := customProbe.await(t)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(run.raw, &got); err != nil {
		t.Fatalf("decode params %s: %v", run.raw, err)
	}
	for _, k := range []string{"fetch_op_ids", "fix_mode", "keep_count"} {
		if _, present := got[k]; present {
			t.Errorf("%q appeared in the params with no such key in the request; params were %s", k, run.raw)
		}
	}
}

// TestRunMaintenanceJob_ResolvedDryRunIsPersistedInParams pins the one flag whose
// zero value is DESTRUCTIVE.
//
// This probe advertises dry_run:true in DefaultParams, so a body that omits
// dry_run must resolve to true via advertisedDryRunDefault — and that resolved
// value must be written INTO the params blob, not merely passed as the argument.
// Both registry resume paths copy row.Params verbatim, so a blob missing the key
// would resume decoding dry_run as Go's zero value: false. That turns an
// interrupted PREVIEW into a real mutation, which is the exact failure #2419
// worked around before the v2 row had a params field.
//
// The dryRun ARGUMENT is asserted alongside the blob because they are two
// channels for one flag and the hazard is them disagreeing.
func TestRunMaintenanceJob_ResolvedDryRunIsPersistedInParams(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	customProbe.reset()

	// No body at all — what a client that read default_params and POSTed nothing
	// sends.
	w := postMaintenanceJob(t, server, customProbe.ID(), "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}

	run := customProbe.await(t)
	if !run.dryRunArg {
		t.Error("dryRun argument was false for a job advertising dry_run:true with no body — a preview became a mutation")
	}

	var got struct {
		DryRun *bool `json:"dry_run"`
	}
	if err := json.Unmarshal(run.raw, &got); err != nil {
		t.Fatalf("decode params %s: %v", run.raw, err)
	}
	if got.DryRun == nil {
		t.Fatalf("dry_run is ABSENT from the persisted params; a resume would decode it as false. params were %s", run.raw)
	}
	if !*got.DryRun {
		t.Errorf("dry_run persisted as false for a job advertising true; params were %s", run.raw)
	}
}

// TestRunMaintenanceJob_ExplicitDryRunFalseSurvives is the known-good twin for
// the test above: an operator who explicitly says false must still get false, so
// the assertion there is measuring the advertised default rather than a handler
// that pins dry_run to true.
func TestRunMaintenanceJob_ExplicitDryRunFalseSurvives(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	customProbe.reset()

	w := postMaintenanceJob(t, server, customProbe.ID(), `{"dry_run": false, "fix_mode": "clear"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}

	run := customProbe.await(t)
	if run.dryRunArg {
		t.Error("dryRun argument was true despite an explicit \"dry_run\": false")
	}
	var got struct {
		DryRun *bool `json:"dry_run"`
	}
	if err := json.Unmarshal(run.raw, &got); err != nil {
		t.Fatalf("decode params %s: %v", run.raw, err)
	}
	if got.DryRun == nil || *got.DryRun {
		t.Errorf("dry_run persisted as %v, want false; params were %s", got.DryRun, run.raw)
	}
}

// TestRunMaintenanceJob_MalformedBodyStillRejectedAfterWidening keeps the
// widened parse from weakening the guard it replaced.
//
// Reading the body into a raw map instead of binding a struct is a strictly more
// permissive parse by default — an unknown key is now KEPT rather than dropped,
// which is the entire point — so the malformed cases have to be re-pinned
// explicitly. The failure mode is the original one: a body that fails to parse
// leaves dry_run false and the job runs FOR REAL behind a 202 byte-identical to
// the preview the operator asked for.
//
// TestRunMaintenanceJob_MalformedBodyIsRejected (maintenance_dryrun_default_test.go)
// already covers the truncated case and asserts nothing was enqueued. These are
// the shapes it does not cover, each of which the map parse could plausibly have
// started accepting.
func TestRunMaintenanceJob_MalformedBodyStillRejectedAfterWidening(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	cases := []struct {
		name string
		body string
		why  string
	}{
		{"trailing comma", `{"dry_run": true,}`, "a hand-edited body"},
		{"dry_run as string", `{"dry_run": "true"}`, "the type confusion that must not collapse to false"},
		{"dry_run as number", `{"dry_run": 1}`, "truthy in JS, not a bool here"},
		{"json array not object", `["dry_run"]`, "params must be an object"},
		{"bare string", `"dry_run"`, "params must be an object"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			customProbe.reset()
			w := postMaintenanceJob(t, server, customProbe.ID(), tc.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for body %s (%s); response = %s",
					w.Code, tc.body, tc.why, w.Body.String())
			}
		})
	}
}
