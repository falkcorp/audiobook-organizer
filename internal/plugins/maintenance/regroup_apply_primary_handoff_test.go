// file: internal/plugins/maintenance/regroup_apply_primary_handoff_test.go
// version: 1.0.0
// guid: 5a7c3e91-8b24-4f06-9d1e-2c6b8f4a0e37
// last-edited: 2026-09-24

package maintenance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

func withLibraryRoot(t *testing.T, root string) {
	t.Helper()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
}

// The version-group apply crowns the member the shared rule picks -- the
// organized library copy -- not the smallest ULID.
func TestApplyVersionGroup_CrownsTheEligibleMember(t *testing.T) {
	f := vptest.New(t)
	withLibraryRoot(t, f.Root)
	a := f.Book(t, vptest.Spec{ID: "a", Primary: "true", State: "imported"})
	b := f.Book(t, vptest.Spec{ID: "b", Primary: "true"})

	require.NoError(t, ApplyVersionGroup(f.S)(context.Background(), versionGroupItem(t, "/lib/vg", []string{a, b})))
	got, err := f.S.GetBookByID(a)
	require.NoError(t, err)
	require.NotNil(t, got.VersionGroupID)
	f.RequireSinglePrimary(t, *got.VersionGroupID, b)
	require.Equal(t, "false", f.Flag(t, a))
}

// Reusing a group whose healthy primary is outside the hold keeps that
// primary instead of demoting it for a hold member.
func TestApplyVersionGroup_ReusedGroupKeepsHealthyIncumbent(t *testing.T) {
	f := vptest.New(t)
	withLibraryRoot(t, f.Root)
	inc := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g", Primary: "false"})
	b := f.Book(t, vptest.Spec{ID: "b", Primary: "true"})

	require.NoError(t, ApplyVersionGroup(f.S)(context.Background(), versionGroupItem(t, "/lib/vg", []string{a, b})))
	f.RequireSinglePrimary(t, "g", inc)
	require.Equal(t, "false", f.Flag(t, b))
}
