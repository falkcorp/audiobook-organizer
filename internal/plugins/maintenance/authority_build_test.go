// file: internal/plugins/maintenance/authority_build_test.go
// version: 1.1.0
// guid: 9e2d6a51-7c3b-4f18-a0e4-5b8c1d7f2a63
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// authorityCaptureRegistry collects every def the plugin registers.
type authorityCaptureRegistry struct{ defs []sdk.OperationDef }

func (c *authorityCaptureRegistry) RegisterOp(def sdk.OperationDef) error {
	c.defs = append(c.defs, def)
	return nil
}

func (c *authorityCaptureRegistry) EnqueueOp(context.Context, string, any, ...sdk.EnqueueOption) (string, error) {
	return "", nil
}

func authorityTestStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	ps, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

func authorityKeyCounts(t *testing.T, ps *database.PebbleStore) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, p := range authority.KeyPrefixes() {
		n, err := ps.CountPrefix(p)
		require.NoError(t, err)
		out[p] = n
	}
	return out
}

// writeExport writes a small owner library export. broken adds an item the
// decoder refuses.
func writeExport(t *testing.T, broken bool) string {
	t.Helper()
	items := `[
	 {"asin":"B0EXPORT01","title":"T1","authors":[{"name":"Ann Leckie","asin":"B001JP7W9E"}],"narrators":[{"name":"Adjoa Andoh","asin":null}],"publisher_name":"Orbit"},
	 {"asin":"B0EXPORT02","title":"T2","authors":[{"name":"Nicholas Briggs","asin":null}],"narrators":[{"name":"Nicholas Briggs","asin":null}],"publisher_name":"Big Finish Productions"}`
	if broken {
		items += `,
	 {"asin":"B0EXPORT03","authors":"broken"}`
	}
	items += "]"
	path := filepath.Join(t.TempDir(), "library.json")
	require.NoError(t, os.WriteFile(path, []byte(items), 0o600))
	return path
}

// resultReporterA is concurrentReporter plus SetResult, as the production
// reporter has, so the op's stored plan can be read back.
type resultReporterA struct {
	concurrentReporter
	result json.RawMessage
}

func (r *resultReporterA) SetResult(v any) error {
	raw, err := json.Marshal(v)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = raw
	return err
}

// storePlanRow records a dry run as a completed op row, the way the
// registry persists a finished op with its result.
func storePlanRow(t *testing.T, ps *database.PebbleStore, id string, result json.RawMessage) {
	t.Helper()
	rd := string(result)
	now := time.Now()
	require.NoError(t, ps.InsertOperationV2(database.OperationV2Row{
		ID: id, DefID: authorityBuildOpID, Plugin: "maintenance", Status: "completed",
		Params: "{}", QueuedAt: now, CompletedAt: &now, ResultData: &rd,
	}))
}

// dryRunPlan runs a dry run through the op and stores it as plan op id.
func dryRunPlan(t *testing.T, p *Plugin, ps *database.PebbleStore, id, params string) authorityPlanResult {
	t.Helper()
	rep := &resultReporterA{}
	require.NoError(t, p.runAuthorityBuild(context.Background(), json.RawMessage(params), rep))
	var plan authorityPlanResult
	require.NoError(t, json.Unmarshal(rep.result, &plan))
	require.True(t, plan.DryRun)
	storePlanRow(t, ps, id, rep.result)
	return plan
}

func TestAuthorityBuild_IsRegistered(t *testing.T) {
	reg := &authorityCaptureRegistry{}
	require.NoError(t, New(nil).Register(reg))
	var found bool
	for _, d := range reg.defs {
		if d.ID == authorityBuildOpID {
			found = true
		}
	}
	require.True(t, found, "%s must be registered by the maintenance plugin", authorityBuildOpID)
	def := New(nil).authorityBuildDef()
	require.NoError(t, registry.ValidateOpDef(def))
	require.Equal(t, registry.LivenessRunItems, def.Liveness)
}

func TestAuthorityBuild_DryRunWritesNothing(t *testing.T) {
	ps := authorityTestStore(t)
	before := authorityKeyCounts(t, ps)
	rep := &concurrentReporter{}
	out, err := buildAuthorityLists(context.Background(), ps, authorityBuildParams{LibraryExportPath: writeExport(t, true), Concurrency: 4}, true, "", rep, time.Now())
	require.NoError(t, err)
	require.True(t, out.DryRun)
	require.Positive(t, out.Writes)
	require.Equal(t, before, authorityKeyCounts(t, ps), "a dry run must not write")
	require.Contains(t, rep.loggedText(), "DRY RUN")

	st := out.Report.Sources[authority.SourceLibraryExport]
	require.Equal(t, 2, st.Items)
	require.Equal(t, 1, st.Undecodable)
	require.Equal(t, 1, st.CastContext, "the Big Finish product is a cast list")
	require.Positive(t, out.Report.Sources[authority.SourceSeed].Items)
	require.Zero(t, out.Report.Sources[authority.SourceCatalog].Items, "empty catalog: 0 rows")
	require.Equal(t, []string{"1 owner_library_export payloads did not decode"}, out.Result.Refusals)
	require.NotEmpty(t, out.Result.Digest)
}

func TestAuthorityBuild_DefaultsToDryRunAndApplyNeedsAPlan(t *testing.T) {
	ps := authorityTestStore(t)
	p := New(fakeDeps{store: ps})
	require.NoError(t, p.runAuthorityBuild(context.Background(), json.RawMessage(`{"skip_seed":true,"concurrency":2}`), &resultReporterA{}))
	for _, n := range authorityKeyCounts(t, ps) {
		require.Zero(t, n)
	}
	require.Error(t, p.runAuthorityBuild(context.Background(), json.RawMessage(`{"dryrun_typo":false}`), &resultReporterA{}),
		"an unknown param fails the run instead of being ignored")
	require.ErrorContains(t, p.runAuthorityBuild(context.Background(), json.RawMessage(`{"dry_run":false}`), &resultReporterA{}), "plan_op_id")
	require.ErrorContains(t, p.runAuthorityBuild(context.Background(), json.RawMessage(`{"dry_run":false,"plan_op_id":"nope"}`), &resultReporterA{}), "not found")
	require.ErrorContains(t, p.runAuthorityBuild(context.Background(), json.RawMessage(`{"dry_run":false,"plan_op_id":"x","skip_seed":true}`), &resultReporterA{}), "sources from plan")
}

// TestAuthorityBuild_ApplyExactlyTheReviewedPlan: dry run -> stored plan ->
// apply by plan id. A second apply of the same plan after the store changed
// is refused; a fresh dry run of unchanged sources then plans 0 writes.
func TestAuthorityBuild_ApplyExactlyTheReviewedPlan(t *testing.T) {
	ps := authorityTestStore(t)
	ctx := context.Background()
	p := New(fakeDeps{store: ps})
	export := writeExport(t, false)
	plan := dryRunPlan(t, p, ps, "plan1", fmt.Sprintf(`{"library_export_path":%q,"concurrency":4}`, export))
	require.Empty(t, plan.Refusals)
	require.Empty(t, plan.Prune, "nothing stored yet, nothing to prune")
	require.Positive(t, plan.Writes)

	rep := &resultReporterA{}
	require.NoError(t, p.runAuthorityBuild(ctx, json.RawMessage(`{"dry_run":false,"plan_op_id":"plan1"}`), rep))
	var applied authorityPlanResult
	require.NoError(t, json.Unmarshal(rep.result, &applied))
	require.Equal(t, "plan1", applied.PlanOpID)
	require.Equal(t, plan.Digest, applied.Digest)
	require.NotNil(t, applied.Apply)
	require.Equal(t, plan.Writes, applied.Apply.Written)
	counts := authorityKeyCounts(t, ps)
	require.Positive(t, counts[authority.PersonPrefix])
	require.Zero(t, counts[authority.OverridePrefix])

	// The store has moved on: plan1 no longer describes it.
	require.ErrorContains(t, p.runAuthorityBuild(ctx, json.RawMessage(`{"dry_run":false,"plan_op_id":"plan1"}`), &resultReporterA{}), "plan changed")

	// Overrides survive; a fresh dry run of the same sources plans nothing.
	require.NoError(t, authority.PutPersonOverride(ps, authority.PersonOverride{Name: "Dragon Born", Roles: map[authority.Role]bool{authority.RoleAuthor: false}}))
	plan2 := dryRunPlan(t, p, ps, "plan2", fmt.Sprintf(`{"library_export_path":%q}`, export))
	require.Zero(t, plan2.Writes, "a second build of the same sources writes nothing")
	require.Empty(t, plan2.Prune)
	require.NoError(t, p.runAuthorityBuild(ctx, json.RawMessage(`{"dry_run":false,"plan_op_id":"plan2"}`), &resultReporterA{}))
	after := authorityKeyCounts(t, ps)
	require.Equal(t, counts[authority.SourcePrefix], after[authority.SourcePrefix], "ledger unchanged")
	require.EqualValues(t, 1, after[authority.OverridePrefix], "overrides survive a rebuild")

	snap, err := authority.LoadSnapshot(ctx, ps)
	require.NoError(t, err)
	require.True(t, snap.IsKnownPerson("Ann Leckie", authority.RoleAuthor))
	require.False(t, snap.IsKnownPerson("Nicholas Briggs", authority.RoleAuthor), "cast never counts as author")
	require.True(t, snap.IsKnownPerson("Nicholas Briggs", authority.RoleNarrator))
}

// TestAuthorityBuild_PartialSourcesAreRefusedAndHeld: once owner-export rows
// exist, a build without the export is refused for apply, and its dry run
// holds the export's rows instead of listing them for pruning.
func TestAuthorityBuild_PartialSourcesAreRefusedAndHeld(t *testing.T) {
	ps := authorityTestStore(t)
	p := New(fakeDeps{store: ps})
	export := writeExport(t, false)
	dryRunPlan(t, p, ps, "plan1", fmt.Sprintf(`{"library_export_path":%q}`, export))
	require.NoError(t, p.runAuthorityBuild(context.Background(), json.RawMessage(`{"dry_run":false,"plan_op_id":"plan1"}`), &resultReporterA{}))

	plan := dryRunPlan(t, p, ps, "plan2", `{}`)
	require.Len(t, plan.Refusals, 1)
	require.Contains(t, plan.Refusals[0], "owner-export ledger rows")
	require.Contains(t, plan.Held, authority.SourceKey(authority.SourceLibraryExport, "B0EXPORT01"))
	require.Contains(t, plan.Held, authority.PersonPrefix+"nicholasbriggs", "export-only person is held, not pruned")
	require.NotContains(t, plan.Prune, authority.PersonPrefix+"nicholasbriggs")
	before := authorityKeyCounts(t, ps)
	require.ErrorContains(t, p.runAuthorityBuild(context.Background(), json.RawMessage(`{"dry_run":false,"plan_op_id":"plan2"}`), &resultReporterA{}), "apply refused")
	require.Equal(t, before, authorityKeyCounts(t, ps), "a refused apply writes nothing")

	skip := dryRunPlan(t, p, ps, "plan3", fmt.Sprintf(`{"library_export_path":%q,"skip_seed":true,"skip_catalog":true}`, export))
	require.Len(t, skip.Refusals, 2)
}

func TestAuthorityBuild_RejectsRelativeExportPath(t *testing.T) {
	ps := authorityTestStore(t)
	_, err := buildAuthorityLists(context.Background(), ps, authorityBuildParams{LibraryExportPath: "library.json"}, true, "", &concurrentReporter{}, time.Now())
	require.Error(t, err)
	_, err = buildAuthorityLists(context.Background(), ps, authorityBuildParams{LibraryExportPath: t.TempDir()}, true, "", &concurrentReporter{}, time.Now())
	require.Error(t, err, "a directory is not an export")
}

// TestAuthorityBuild_CatalogPagesConcurrently puts more than two cat_raw
// pages (including the same product ASIN in two marketplaces) through
// buildAuthorityLists with several workers, under -race, and checks the
// result does not depend on the worker count.
func TestAuthorityBuild_CatalogPagesConcurrently(t *testing.T) {
	ps := authorityTestStore(t)
	cs := database.NewCatalogStore(ps.DB())
	n := 2*authorityCatalogPageSize + 137
	var ups []database.CatalogUpsert
	for i := range n {
		asin := fmt.Sprintf("B0P%07d", i%(n-50)) // the last 50 repeat earlier ASINs
		mkt := "us"
		if i >= n-50 {
			mkt = "uk"
		}
		raw := fmt.Sprintf(`{"asin":%q,"title":"T","authors":[{"name":"Author %d","asin":"B0A%07d"}],"narrators":[{"name":"Narrator %d","asin":null}],"publisher_name":"Pub %d"}`,
			asin, i%300, i%300, i%97, i%13)
		ups = append(ups, database.CatalogUpsert{Entry: database.CatalogEntry{Provider: "audible", Marketplace: mkt, ProviderID: asin, Title: "T"}, Raw: []byte(raw)})
	}
	_, err := cs.UpsertEntries(ups, "harvest")
	require.NoError(t, err)

	var digests []string
	for _, workers := range []int{1, 8} {
		out, err := buildAuthorityLists(context.Background(), ps, authorityBuildParams{SkipSeed: true, Concurrency: workers}, true, "", &concurrentReporter{}, time.Now())
		require.NoError(t, err)
		st := out.Report.Sources[authority.SourceCatalog]
		require.Equal(t, n-50, st.Items)
		require.Equal(t, 50, st.Duplicates)
		require.Zero(t, st.Undecodable)
		digests = append(digests, out.Result.Digest)
	}
	require.Equal(t, digests[0], digests[1], "the plan must not depend on worker count or order")
}

// TestAuthorityBuild_ResultReadsTopDown: the stored result leads with the
// summary and refusals, and carries the full prune list and its count.
func TestAuthorityBuild_ResultReadsTopDown(t *testing.T) {
	ps := authorityTestStore(t)
	p := New(fakeDeps{store: ps})
	rep := &resultReporterA{}
	require.NoError(t, p.runAuthorityBuild(context.Background(), json.RawMessage(`{"skip_catalog":true}`), rep))
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rep.result, &top))
	for _, k := range []string{"summary", "refusals", "prune", "prune_count", "held", "held_count", "digest", "report", "counts"} {
		require.Contains(t, top, k)
	}
	require.True(t, strings.HasPrefix(string(rep.result), `{"summary":"DRY RUN: `), string(rep.result[:60]))
	require.Contains(t, string(top["summary"]), "apply refused (1 reasons)")
}
