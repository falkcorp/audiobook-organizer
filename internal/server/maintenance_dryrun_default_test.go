// file: internal/server/maintenance_dryrun_default_test.go
// version: 1.1.0
// guid: 6c1d84af-97b2-4e30-8f55-2b70e9c14d63
// last-edited: 2026-08-17

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
)

// ---------------------------------------------------------------------------
// Probe jobs
// ---------------------------------------------------------------------------

// dryRunProbeJob is a maintenance job that does nothing except record the
// dryRun value it was handed. It exists so the tests below can assert the value
// that reaches Run — the only place the flag actually decides between a preview
// and a mutation — rather than asserting on an intermediate the handler happens
// to compute.
type dryRunProbeJob struct {
	id string
	// params is returned verbatim from DefaultParams(), so each probe can
	// advertise a different shape (dry_run true, false, or absent entirely).
	params    any
	canResume bool

	mu   sync.Mutex
	runs []bool
}

func (j *dryRunProbeJob) ID() string          { return j.id }
func (j *dryRunProbeJob) Name() string        { return "Dry-Run Probe " + j.id }
func (j *dryRunProbeJob) Description() string { return "Test probe; records the dryRun it receives." }
func (j *dryRunProbeJob) Category() string    { return "test" }
func (j *dryRunProbeJob) DefaultParams() any  { return j.params }
func (j *dryRunProbeJob) CanResume() bool     { return j.canResume }

// Policy satisfies the interface for these probes. DefaultPolicy is the honest
// value: the probes exercise the dry_run persistence path, which the bridge drives
// with its own hardcoded policy, so a probe declaring anything else would be
// asserting a behaviour these tests do not exercise.
func (j *dryRunProbeJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}

func (j *dryRunProbeJob) Run(_ context.Context, _ maintenance.JobStore, _ maintenance.ProgressReporter, dryRun bool) error {
	j.mu.Lock()
	j.runs = append(j.runs, dryRun)
	j.mu.Unlock()
	return nil
}

// awaitRun blocks until the job has been run at least once and returns the
// dryRun value of the first run. Failing here means the request never reached
// Run at all, which is a different defect from resolving the wrong value — so
// it is reported separately rather than folded into the value assertion.
func (j *dryRunProbeJob) awaitRun(t *testing.T) bool {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		j.mu.Lock()
		n := len(j.runs)
		var first bool
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
	return false
}

var (
	// Advertises dry_run:true — the 18-of-34 case the fix is about.
	probeAdvertisesTrue = &dryRunProbeJob{
		id: "test-probe-advertises-dry-run-true",
		params: struct {
			DryRun bool `json:"dry_run"`
		}{DryRun: true},
		canResume: true,
	}
	// Advertises dry_run:false — must be untouched by the fix.
	probeAdvertisesFalse = &dryRunProbeJob{
		id: "test-probe-advertises-dry-run-false",
		params: struct {
			DryRun bool `json:"dry_run"`
		}{DryRun: false},
		canResume: true,
	}
	// Advertises no dry_run key at all — the reader must not invent one.
	probeAdvertisesNothing = &dryRunProbeJob{
		id: "test-probe-advertises-no-dry-run",
		params: struct {
			Limit int `json:"limit"`
		}{Limit: 5},
		canResume: true,
	}
)

func init() {
	// maintenance.Register panics on a duplicate ID and has no Unregister, so
	// these are registered exactly once for the package's whole test run.
	// maintenance.All() has a single production caller (listMaintenanceJobs),
	// and no test asserts a job count, so the extra entries are inert.
	maintenance.Register(probeAdvertisesTrue)
	maintenance.Register(probeAdvertisesFalse)
	maintenance.Register(probeAdvertisesNothing)
}

// ---------------------------------------------------------------------------
// Conformance: the reader must agree with what the catalogue publishes
// ---------------------------------------------------------------------------

// normalizeParamKey folds a JSON key to a form that collapses `dry_run`,
// `dryRun` and `DryRun` onto the same string. A struct field declared without a
// json tag marshals as `DryRun`, which advertisedDryRunDefault (matching the tag
// `dry_run`) would NOT bind — encoding/json's case-insensitive fallback matches
// on case, not on punctuation. That job would then advertise dry_run:true to the
// UI while the handler applied false: the exact advertised-vs-applied
// divergence this whole change exists to close, reintroduced by a missing tag.
func normalizeParamKey(k string) string {
	return strings.ToLower(strings.ReplaceAll(k, "_", ""))
}

// TestAdvertisedDryRunDefault_AgreesWithEveryRegisteredJob is the conformance
// test that keeps the fix true for job #35.
//
// It does not hardcode per-job expectations — the author of a new job would just
// write their own expectation and the test would agree with whatever they did.
// Instead it derives the expectation from the SAME bytes listMaintenanceJobs
// serves to clients, and requires the reader to reproduce it.
func TestAdvertisedDryRunDefault_AgreesWithEveryRegisteredJob(t *testing.T) {
	jobs := maintenance.All()
	if len(jobs) == 0 {
		t.Fatal("no maintenance jobs registered; this test would pass vacuously")
	}

	checkedAdvertisers := 0
	for _, job := range jobs {
		t.Run(job.ID(), func(t *testing.T) {
			raw, err := json.Marshal(job.DefaultParams())
			if err != nil {
				// A job whose defaults do not marshal cannot advertise a
				// dry_run at all, so false is the only answer available.
				if got := advertisedDryRunDefault(job); got {
					t.Fatalf("DefaultParams() does not marshal (%v) yet advertisedDryRunDefault returned true", err)
				}
				return
			}

			var asMap map[string]any
			if err := json.Unmarshal(raw, &asMap); err != nil {
				// Not a JSON object (nil, scalar, array): no dry_run key.
				if got := advertisedDryRunDefault(job); got {
					t.Fatalf("DefaultParams() marshals to non-object %s yet advertisedDryRunDefault returned true", raw)
				}
				return
			}

			var advertised *bool
			var advertisedKey string
			for k, v := range asMap {
				if normalizeParamKey(k) != "dryrun" {
					continue
				}
				b, ok := v.(bool)
				if !ok {
					t.Fatalf("job advertises %q as %T (%v); dry_run must be a bool or the UI checkbox cannot bind it", k, v, v)
				}
				advertised = &b
				advertisedKey = k
			}

			got := advertisedDryRunDefault(job)
			if advertised == nil {
				if got {
					t.Fatalf("job advertises no dry_run key (%s) yet advertisedDryRunDefault returned true", raw)
				}
				return
			}

			checkedAdvertisers++
			if advertisedKey != "dry_run" {
				t.Fatalf("job advertises its dry-run default under key %q, not \"dry_run\" — "+
					"the catalogue would publish %s to the UI while the handler reads \"dry_run\" and applies %v. "+
					"Add a `json:\"dry_run\"` tag to the DefaultParams field.", advertisedKey, raw, got)
			}
			if got != *advertised {
				t.Fatalf("catalogue advertises dry_run=%v (%s) but advertisedDryRunDefault returned %v", *advertised, raw, got)
			}
		})
	}

	if checkedAdvertisers == 0 {
		t.Fatal("no registered job advertises a dry_run default; the agreement assertion never ran")
	}
}

// TestAdvertisedDryRunDefault_PinnedRealJobs guards against the conformance test
// above degenerating. If maintenance.All() ever came back empty or the probes
// were the only entries, the derived-expectation loop would still pass on the
// probes alone. These two are real production jobs on opposite sides.
func TestAdvertisedDryRunDefault_PinnedRealJobs(t *testing.T) {
	for _, tc := range []struct {
		jobID string
		want  bool
	}{
		// Phase 1 deletes every single-book series; 2,322 of 6,245 such series
		// on production are genuinely distinct real series, and series names
		// are not recoverable once deleted.
		{"cleanup-series", true},
		{"backfill-file-hashes", false},
	} {
		job, err := maintenance.Get(tc.jobID)
		if err != nil {
			t.Fatalf("maintenance.Get(%q): %v", tc.jobID, err)
		}
		if got := advertisedDryRunDefault(job); got != tc.want {
			t.Errorf("advertisedDryRunDefault(%q) = %v, want %v", tc.jobID, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// The HTTP entry point
// ---------------------------------------------------------------------------

// postMaintenanceJob drives runMaintenanceJob the way the route does. body==""
// means no body at all, which is what a client that read default_params and
// POSTed nothing sends — and the case that used to silently resolve to false.
func postMaintenanceJob(t *testing.T, s *Server, jobID, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "job_id", Value: jobID}}

	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(http.MethodPost, "/maintenance/jobs/"+jobID, http.NoBody)
	} else {
		req = httptest.NewRequest(http.MethodPost, "/maintenance/jobs/"+jobID, strings.NewReader(body))
	}
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	s.runMaintenanceJob(c)
	return w
}

func TestRunMaintenanceJob_ResolvesDryRunFromTheAdvertisedDefault(t *testing.T) {
	tests := []struct {
		name  string
		job   *dryRunProbeJob
		body  string
		want  bool
		notes string
	}{
		{
			name:  "advertises true, no body at all",
			job:   probeAdvertisesTrue,
			body:  "",
			want:  true,
			notes: "the regression: this used to resolve to false and mutate",
		},
		{
			name: "advertises true, empty JSON object",
			job:  probeAdvertisesTrue,
			body: `{}`,
			want: true,
		},
		{
			name:  "advertises true, explicit false",
			job:   probeAdvertisesTrue,
			body:  `{"dry_run": false}`,
			want:  false,
			notes: "an explicit apply still applies — callers that mean it say so",
		},
		{
			name: "advertises true, explicit true",
			job:  probeAdvertisesTrue,
			body: `{"dry_run": true}`,
			want: true,
		},
		{
			name:  "advertises false, no body at all",
			job:   probeAdvertisesFalse,
			body:  "",
			want:  false,
			notes: "unchanged by the fix",
		},
		{
			name: "advertises false, explicit true",
			job:  probeAdvertisesFalse,
			body: `{"dry_run": true}`,
			want: true,
		},
		{
			name:  "advertises no dry_run key, no body at all",
			job:   probeAdvertisesNothing,
			body:  "",
			want:  false,
			notes: "the reader must not invent a default the job never published",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, cleanup := setupTestServer(t)
			defer cleanup()
			if server.opRegistry == nil {
				t.Skip("ops registry not wired in this build")
			}

			// Each subtest gets a fresh recording window on the shared probe.
			tc.job.mu.Lock()
			tc.job.runs = nil
			tc.job.mu.Unlock()

			w := postMaintenanceJob(t, server, tc.job.ID(), tc.body)
			if w.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
			}

			if got := tc.job.awaitRun(t); got != tc.want {
				t.Fatalf("job ran with dryRun=%v, want %v (%s)", got, tc.want, tc.notes)
			}
		})
	}
}

// TestRunMaintenanceJob_MalformedBodyIsRejected locks in the behavior the *bool
// change had to preserve: a body that fails to parse is a 400, not a silent
// fall-through to the destructive default.
func TestRunMaintenanceJob_MalformedBodyIsRejected(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	probeAdvertisesTrue.mu.Lock()
	probeAdvertisesTrue.runs = nil
	probeAdvertisesTrue.mu.Unlock()

	w := postMaintenanceJob(t, server, probeAdvertisesTrue.ID(), `{"dry_run":`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}

	// Nothing should have been enqueued. Give a real (if short) window so this
	// asserts absence rather than merely racing the worker.
	time.Sleep(500 * time.Millisecond)
	probeAdvertisesTrue.mu.Lock()
	n := len(probeAdvertisesTrue.runs)
	probeAdvertisesTrue.mu.Unlock()
	if n != 0 {
		t.Fatalf("job ran %d time(s) despite a 400 response", n)
	}
}

// TestRunMaintenanceJob_PersistsResolvedDryRun asserts the value is written
// where a resume can find it. Without this the resume path has no record of the
// operator's choice and falls back to a guess.
//
// It reads the v2 row's params, not a v1 params blob. Retiring the v1 minter
// removed the operations.SaveParams call this used to check, and that is a
// STRENGTHENING rather than a loss: the v1 row carried no params field at all,
// which is the whole reason a side-table blob existed, whereas the v2 row stores
// params natively and both registry resume paths carry them across verbatim.
func TestRunMaintenanceJob_PersistsResolvedDryRun(t *testing.T) {
	server, cleanup := setupTestServer(t)
	defer cleanup()

	probeAdvertisesTrue.mu.Lock()
	probeAdvertisesTrue.runs = nil
	probeAdvertisesTrue.mu.Unlock()

	w := postMaintenanceJob(t, server, probeAdvertisesTrue.ID(), "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			OperationID string `json:"operation_id"`
		} `json:"data"`
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %s: %v", w.Body.String(), err)
	}
	opID := resp.Data.OperationID
	if opID == "" {
		opID = resp.OperationID
	}
	if opID == "" {
		t.Fatalf("no operation_id in response %s", w.Body.String())
	}

	row, err := server.Ops().GetOperationV2(opID)
	if err != nil {
		t.Fatalf("GetOperationV2(%q): %v", opID, err)
	}
	if row == nil {
		t.Fatalf("the id returned to the caller (%q) resolves to no v2 operation row", opID)
	}
	if row.Params == "" {
		t.Fatal("no params persisted on the operation; a restart would resume on a guess")
	}
	var saved maintenanceJobOpParams
	if err := json.Unmarshal([]byte(row.Params), &saved); err != nil {
		t.Fatalf("decode v2 params %s: %v", row.Params, err)
	}
	if saved.JobID != probeAdvertisesTrue.ID() {
		t.Errorf("persisted JobID = %q, want %q", saved.JobID, probeAdvertisesTrue.ID())
	}
	if !saved.DryRun {
		t.Error("persisted DryRun = false, want true (the advertised default that was applied)")
	}
}

// ---------------------------------------------------------------------------
// The restart entry point
// ---------------------------------------------------------------------------

// TestResumeLegacyOp_MaintenanceJobHonorsSavedDryRun covers the sharper of the
// two defects: an operator explicitly asked for a PREVIEW, the server restarted
// mid-run, and the resume re-enqueued with DryRun at Go's zero value — turning
// the preview into a real mutation under the original operation's own ID.
func TestResumeLegacyOp_MaintenanceJobHonorsSavedDryRun(t *testing.T) {
	tests := []struct {
		name     string
		job      *dryRunProbeJob
		savedDry *bool
		want     bool
		notes    string
	}{
		{
			name:     "saved dry-run preview resumes as a preview",
			job:      probeAdvertisesTrue,
			savedDry: boolPtr(true),
			want:     true,
			notes:    "the regression: this used to resume as a real mutation",
		},
		{
			name:     "saved apply resumes as an apply",
			job:      probeAdvertisesTrue,
			savedDry: boolPtr(false),
			want:     false,
			notes:    "faithful resume, not a blanket fail-safe",
		},
		{
			name:     "no saved params falls back to the advertised default",
			job:      probeAdvertisesTrue,
			savedDry: nil,
			want:     true,
			notes:    "covers ops enqueued before this shipped",
		},
		{
			name:     "no saved params on a job advertising false stays false",
			job:      probeAdvertisesFalse,
			savedDry: nil,
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, cleanup := setupTestServer(t)
			defer cleanup()
			if server.opRegistry == nil {
				t.Skip("ops registry not wired in this build")
			}

			tc.job.mu.Lock()
			tc.job.runs = nil
			tc.job.mu.Unlock()

			opID := ulid.Make().String()
			opType := "maintenance:" + tc.job.ID()
			if _, err := server.Ops().CreateOperation(opID, opType, nil); err != nil {
				t.Fatalf("CreateOperation: %v", err)
			}
			if tc.savedDry != nil {
				if err := operations.SaveParams(server.Ops(), opID, maintenanceJobOpParams{
					LegacyOpID: opID,
					JobID:      tc.job.ID(),
					DryRun:     *tc.savedDry,
				}); err != nil {
					t.Fatalf("SaveParams: %v", err)
				}
			}

			server.resumeLegacyOp(opID, opType)

			if got := tc.job.awaitRun(t); got != tc.want {
				t.Fatalf("resumed with dryRun=%v, want %v (%s)", got, tc.want, tc.notes)
			}
		})
	}
}
