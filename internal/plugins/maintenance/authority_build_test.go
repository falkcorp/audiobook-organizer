// file: internal/plugins/maintenance/authority_build_test.go
// version: 1.0.0
// guid: 9e2d6a51-7c3b-4f18-a0e4-5b8c1d7f2a63
// last-edited: 2026-10-04

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func writeExport(t *testing.T) string {
	t.Helper()
	items := `[
	 {"asin":"B0EXPORT01","title":"T1","authors":[{"name":"Ann Leckie","asin":"B001JP7W9E"}],"narrators":[{"name":"Adjoa Andoh","asin":null}],"publisher_name":"Orbit"},
	 {"asin":"B0EXPORT02","title":"T2","authors":[{"name":"Nicholas Briggs","asin":null}],"narrators":[{"name":"Nicholas Briggs","asin":null}],"publisher_name":"Big Finish Productions"},
	 {"asin":"B0EXPORT03","authors":"broken"}
	]`
	path := filepath.Join(t.TempDir(), "library.json")
	require.NoError(t, os.WriteFile(path, []byte(items), 0o600))
	return path
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
	out, err := buildAuthorityLists(context.Background(), ps, authorityBuildParams{LibraryExportPath: writeExport(t), Concurrency: 4}, true, rep, time.Now())
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
}

func TestAuthorityBuild_DefaultsToDryRun(t *testing.T) {
	ps := authorityTestStore(t)
	p := New(fakeDeps{store: ps})
	require.NoError(t, p.runAuthorityBuild(context.Background(), json.RawMessage(`{"skip_seed":true,"concurrency":2}`), &concurrentReporter{}))
	for _, n := range authorityKeyCounts(t, ps) {
		require.Zero(t, n)
	}
	require.Error(t, p.runAuthorityBuild(context.Background(), json.RawMessage(`{"dryrun_typo":false}`), &concurrentReporter{}),
		"an unknown param fails the run instead of being ignored")
}

func TestAuthorityBuild_ApplyIsIdempotent(t *testing.T) {
	ps := authorityTestStore(t)
	ctx := context.Background()
	params := authorityBuildParams{LibraryExportPath: writeExport(t), Concurrency: 4}
	out, err := buildAuthorityLists(ctx, ps, params, false, &concurrentReporter{}, time.Now())
	require.NoError(t, err)
	require.Equal(t, out.Writes, out.Apply.Written)
	counts := authorityKeyCounts(t, ps)
	require.Positive(t, counts[authority.PersonPrefix])
	require.Positive(t, counts[authority.SourcePrefix])
	require.Zero(t, counts[authority.OverridePrefix])

	require.NoError(t, authority.PutPersonOverride(ps, authority.PersonOverride{Name: "Dragon Born", Roles: map[authority.Role]bool{authority.RoleAuthor: false}}))
	out2, err := buildAuthorityLists(ctx, ps, params, false, &concurrentReporter{}, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Zero(t, out2.Apply.Written, "a second apply of the same sources writes nothing")
	require.Zero(t, out2.Apply.Deleted)
	after := authorityKeyCounts(t, ps)
	require.Equal(t, counts[authority.SourcePrefix], after[authority.SourcePrefix], "ledger unchanged")
	require.EqualValues(t, 1, after[authority.OverridePrefix], "overrides survive a rebuild")

	snap, err := authority.LoadSnapshot(ctx, ps)
	require.NoError(t, err)
	require.True(t, snap.IsKnownPerson("Ann Leckie", authority.RoleAuthor))
	require.False(t, snap.IsKnownPerson("Nicholas Briggs", authority.RoleAuthor), "cast never counts as author")
	require.True(t, snap.IsKnownPerson("Nicholas Briggs", authority.RoleNarrator))
}

func TestAuthorityBuild_RejectsRelativeExportPath(t *testing.T) {
	ps := authorityTestStore(t)
	_, err := buildAuthorityLists(context.Background(), ps, authorityBuildParams{LibraryExportPath: "library.json"}, true, &concurrentReporter{}, time.Now())
	require.Error(t, err)
	_, err = buildAuthorityLists(context.Background(), ps, authorityBuildParams{LibraryExportPath: t.TempDir()}, true, &concurrentReporter{}, time.Now())
	require.Error(t, err, "a directory is not an export")
}
