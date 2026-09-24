// file: internal/itunes/service/blocked_hash_primary_handoff_test.go
// version: 1.0.0
// guid: 8d4f2b6a-1e93-4c57-b0a8-3f6e9d2c7a15
// last-edited: 2026-09-24

package itunesservice

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A blocked-hash soft-delete of a group's primary hands the flag on to the
// group's library copy instead of leaving the group with none.
func TestSoftDeleteBlockedBook_HandsPrimaryOn(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	blocked := f.Book(t, vptest.Spec{ID: "blocked", Group: "g", Primary: "true"})
	lib := f.Book(t, vptest.Spec{ID: "lib", Group: "g", Primary: "false"})
	b, err := f.S.GetBookByID(blocked)
	require.NoError(t, err)

	imp := &Importer{store: f.S}
	require.True(t, imp.softDeleteBlockedBook(blocked, b.FilePath, "deadbeef", itunes.ImportModeImport, logger.New("test")))
	f.RequireSinglePrimary(t, "g", lib)
}
