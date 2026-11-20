// file: internal/plugins/maintenance/deps_journal_test.go
// version: 0.1.0
// guid: 3e8a1d57-9b24-4c60-b1f5-6d0c2a7e4893
// last-edited: 2026-11-20

package maintenance

import (
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The journal the open returns writes for the rest of the run, so every append
// and removal is its own stamp of the journal's row, and a refusal after the open
// (a stopped run, a fence, a preview) leaves the journal untouched.
func TestBooksT6G4Writer_JournalWritesAfterTheOpenAreStamped(t *testing.T) {
	s, _ := g2Fixture(t)
	rec := &booksGateRecorder{}
	w := newT6G4Deps(s, g4HostSpy{root: t.TempDir()}, nil).NewWriter(rec)

	j, err := w.OpenWhisperJournal()
	require.NoError(t, err)
	require.Len(t, rec.stamps(), 1, "the open is one stamp")

	require.NoError(t, j.Complete("k1", "ep", "m", map[string]string{"text": "Gotham"}))
	_, hit, err := j.Lookup("k1")
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, rec.stamps(), 2, "Complete is a stamp, Lookup is not")
	require.NoError(t, j.Invalidate([]string{"k1"}))
	stamps := rec.stamps()
	require.Len(t, stamps, 3, "Invalidate is a stamp")
	for _, st := range stamps {
		require.Len(t, st.Resources, 1)
		require.Equal(t, string(ops.ResAIJournal), st.Resources[0].Kind)
	}

	rec.refuse = tasks.ErrOutsideEffects
	err = j.Complete("k2", "ep", "m", map[string]string{"text": "Arkham"})
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
	_, hit, err = j.Lookup("k2")
	require.NoError(t, err)
	require.False(t, hit, "a refused Complete is not journalled")
	require.ErrorIs(t, j.Invalidate([]string{"k2"}), tasks.ErrOutsideEffects)
}
