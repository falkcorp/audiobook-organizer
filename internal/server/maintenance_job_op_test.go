// file: internal/server/maintenance_job_op_test.go
// version: 1.1.1
// guid: d6fc8245-22c3-4636-a194-c57f613cf3af
// last-edited: 2026-09-02

package server

import (
	"log/slog"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// PR-2 replaced the single "maintenance.job" OperationDef with one def per job.
// These tests assert the two properties that swap depends on: every job gets a
// def, and every def carries that job's OWN policy rather than a shared default.
//
// Why a zero-value &Server{} is enough: registration never dereferences the
// Server. It is captured by the Run closure, which these tests do not invoke.
// (Same rationale as w3decodeReg in op_params_decode_test.go.)
func maintReg(t *testing.T) *opsregistry.Registry {
	t.Helper()
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	return opsregistry.New(m, slog.New(slog.DiscardHandler), 1, nil)
}

// TestEveryMaintenanceJobGetsItsOwnOp checks that all registered jobs are
// reachable under "maintenance.<jobID>".
//
// It asserts per-job presence by name, not just a count, so dropping a job from
// the registration loop names the job it dropped. Mutation-checked: deleting one
// job from the loop fails here rather than passing quietly.
func TestEveryMaintenanceJobGetsItsOwnOp(t *testing.T) {
	jobs := maintenance.All()
	require.NotEmpty(t, jobs, "no maintenance jobs registered — the test would assert nothing")

	reg := maintReg(t)
	require.NoError(t, (&Server{}).RegisterMaintenanceJobOps(reg))

	for _, job := range jobs {
		wantID := "maintenance." + job.ID()
		def, ok := reg.Def(wantID)
		require.Truef(t, ok, "job %q has no OperationDef at %q", job.ID(), wantID)
		require.Equal(t, wantID, def.ID)
		require.NotNil(t, def.Run, "job %q registered a nil Run", job.ID())
		require.Equal(t, "maintenance", def.Plugin)
	}
}

// TestMaintenanceOpCarriesTheJobsOwnPolicy is the test that would have been
// impossible before PR-2: while one def served all 37 jobs, every job
// necessarily reported the bridge's hardcoded policy. Each field here is one the
// bridge used to fix for everyone.
func TestMaintenanceOpCarriesTheJobsOwnPolicy(t *testing.T) {
	reg := maintReg(t)
	require.NoError(t, (&Server{}).RegisterMaintenanceJobOps(reg))

	for _, job := range maintenance.All() {
		t.Run(job.ID(), func(t *testing.T) {
			def, ok := reg.Def("maintenance." + job.ID())
			require.True(t, ok)

			policy := job.Policy()
			require.Equal(t, policy.ResumePolicy, def.ResumePolicy, "ResumePolicy")
			require.Equal(t, policy.Timeout, def.Timeout, "Timeout")
			// ConcurrencyKey is the one field the def does NOT copy verbatim: an
			// empty policy key is replaced by the job's own op ID so the job
			// serializes against itself. See TestMaintenanceOpSerializesAgainstItself
			// for that rule and why it exists.
			wantKey := policy.ConcurrencyKey
			if wantKey == "" {
				wantKey = maintenanceOpID(job.ID())
			}
			require.Equal(t, wantKey, def.ConcurrencyKey, "ConcurrencyKey")
			require.Equal(t, policy.Liveness, def.Liveness, "Liveness")
			require.Equal(t, policy.Capabilities, def.Capabilities, "Capabilities")

			// Display metadata comes from the job, not a shared string.
			require.Equal(t, job.Name(), def.DisplayName)
			require.Equal(t, job.Description(), def.Description)
		})
	}
}

// TestBridgeOpIDIsRetired guards the swap itself. "maintenance.job" must no
// longer resolve — an in-flight v2 row naming it is dropped by
// resumeAfterStartup's unknown-def branch, which is the same path the bridge's
// own ResumeDrop policy already took.
func TestBridgeOpIDIsRetired(t *testing.T) {
	reg := maintReg(t)
	require.NoError(t, (&Server{}).RegisterMaintenanceJobOps(reg))

	_, ok := reg.Def("maintenance.job")
	require.False(t, ok, `the "maintenance.job" bridge def must not be registered`)
}

// TestMaintenanceOpIDIsRegistrable checks the op IDs PR-2 mints against the only
// two constraints RegisterOp enforces on an ID: non-empty, and no ':'. Job IDs
// are kebab-case today; this fails if one ever arrives colon-separated (the
// LEGACY v1 op type in maintenance_dispatcher.go IS "maintenance:"+jobID, so the
// mistake is one character away).
func TestMaintenanceOpIDIsRegistrable(t *testing.T) {
	for _, job := range maintenance.All() {
		id := maintenanceOpID(job.ID())
		require.NotEmpty(t, job.ID(), "a job reported an empty ID")
		require.NotContains(t, id, ":", "op id %q contains ':', which RegisterOp rejects", id)
		require.Equal(t, "maintenance."+job.ID(), id)
	}
}

// --- Permissions -----------------------------------------------------------
//
// Until #2536, OperationDef.Permissions was persisted and read by nothing, so
// the value registered here did not matter. It matters now: TriggerOperationV2
// enforces it. These tests cover the two halves of the rule.

// TestBulkFetchMetadataOpRequiresEditMetadata is the mutation-sensitive one, and
// the reason it hardcodes the expected permission instead of deriving it from
// the job: a test that asks the job what it wants and compares that to what the
// def declares mirrors the production logic exactly, so it passes even when both
// are wrong. Writing library.edit_metadata out in full is what makes this able
// to fail.
//
// bulkFetchMetadataJob is the only job implementing PermissionAware
// (bulk_fetch_metadata.go:43). Before this change its def declared
// settings.manage, so a caller holding exactly the permission the job asks for
// was refused.
func TestBulkFetchMetadataOpRequiresEditMetadata(t *testing.T) {
	reg := maintReg(t)
	require.NoError(t, (&Server{}).RegisterMaintenanceJobOps(reg))

	def, ok := reg.Def("maintenance.bulk-fetch-metadata")
	require.True(t, ok, "bulk-fetch-metadata has no OperationDef")
	require.Equal(t, []auth.Permission{auth.PermLibraryEditMetadata}, def.Permissions)
	require.NotContains(t, def.Permissions, auth.PermSettingsManage,
		"settings.manage must no longer be what this job requires")
}

// TestMaintenanceOpPermissionsMatchTheV1Rule covers the other half: every job
// that does NOT implement PermissionAware keeps settings.manage. The v1
// dispatcher applies exactly this rule (maintenance_dispatcher.go:91-96), and
// phase 1 deletes that route, so any divergence here silently changes who can
// run what at the moment the route goes.
//
// Both branches are asserted non-vacuous: if no job implemented PermissionAware,
// the interesting half of this test would assert nothing at all.
func TestMaintenanceOpPermissionsMatchTheV1Rule(t *testing.T) {
	reg := maintReg(t)
	require.NoError(t, (&Server{}).RegisterMaintenanceJobOps(reg))

	var aware, plain int
	for _, job := range maintenance.All() {
		def, ok := reg.Def("maintenance." + job.ID())
		require.Truef(t, ok, "job %q has no def", job.ID())
		require.Lenf(t, def.Permissions, 1, "job %q should declare exactly one permission", job.ID())

		if pa, isAware := job.(maintenance.PermissionAware); isAware && pa.Permission() != "" {
			aware++
			require.Equalf(t, auth.Permission(pa.Permission()), def.Permissions[0],
				"job %q implements PermissionAware but its def declares something else", job.ID())
			continue
		}
		plain++
		require.Equalf(t, auth.PermSettingsManage, def.Permissions[0],
			"job %q does not implement PermissionAware and must default to settings.manage", job.ID())
	}

	require.Positive(t, aware, "no job implements PermissionAware — the PermissionAware branch asserted nothing")
	require.Positive(t, plain, "every job implements PermissionAware — the default branch asserted nothing")
}
