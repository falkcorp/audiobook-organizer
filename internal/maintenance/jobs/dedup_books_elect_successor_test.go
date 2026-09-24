// file: internal/maintenance/jobs/dedup_books_elect_successor_test.go
// version: 1.0.0
// guid: 5e9b3d72-1a4c-4f08-b6d3-2c8e7a01f495
// last-edited: 2026-09-24

package jobs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

func ddUseRoot(t *testing.T, root string) {
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
}

// The successor is the member versionprimary elects, not the earliest
// created: an older copy that is not organized loses to the library copy.
func TestDDPlanPrimaryHandoff_ElectsLibraryCopyOverEarliestCreated(t *testing.T) {
	f := vptest.New(t)
	ddUseRoot(t, f.Root)
	retiring := f.Book(t, vptest.Spec{ID: "p", Group: "g", Primary: "true", Created: time.Unix(300, 0)})
	f.Book(t, vptest.Spec{ID: "old", Group: "g", Primary: "false", State: "imported", Created: time.Unix(100, 0)})
	lib := f.Book(t, vptest.Spec{ID: "lib", Group: "g", Primary: "false", Created: time.Unix(200, 0)})

	book, err := f.S.GetBookByID(retiring)
	require.NoError(t, err)
	got, err := ddPlanPrimaryHandoff(f.S, newDDSim(), book, nil)
	require.NoError(t, err)
	require.Equal(t, lib, got)

	// Applied end to end: the group ends with the library copy as its one
	// live primary.
	require.NoError(t, ddRetireBook(f.S, newDDSim(), book, nil, false, false))
	f.RequireSinglePrimary(t, "g", lib)
}

// A held group promotes nobody: no remaining member is in the library.
func TestDDPlanPrimaryHandoff_HeldPromotesNobody(t *testing.T) {
	f := vptest.New(t)
	ddUseRoot(t, f.Root)
	retiring := f.Book(t, vptest.Spec{ID: "p", Group: "g", Primary: "true"})
	other := f.Book(t, vptest.Spec{ID: "o", Group: "g", Primary: "false", State: "imported"})

	book, err := f.S.GetBookByID(retiring)
	require.NoError(t, err)
	got, err := ddPlanPrimaryHandoff(f.S, newDDSim(), book, nil)
	require.NoError(t, err)
	require.Empty(t, got)
	require.Equal(t, "false", f.Flag(t, other))
}
