// file: internal/plugins/maintenance/preview_declared_ops_test.go
// version: 1.0.0
// guid: 5ed6e847-8a48-4e72-bd76-be430cf6827f
// last-edited: 2026-10-18

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// The three ops below gained a write capability and a Preview declaration
// (05-PR9). Each test runs the registered def's Resolve for both modes and
// feeds the result to the op.

// itunes.playlist-import: the params Resolve writes for each mode decode into
// the type Run decodes, and opmode reads the mode from them the way Run does.
// A behavioural write count needs a parsed iTunes library with smart criteria,
// which no fixture here provides; the write itself (CreateUserPlaylist) sits
// behind PlaylistImportOptions.DryRun, which Run sets from this same value.
func TestPreviewBehaviour_ITunesPlaylistImport(t *testing.T) {
	def := (&Plugin{}).itunesPlaylistImportDef()
	require.Contains(t, def.Capabilities, sdk.CapLibraryWrite)
	for _, tc := range []struct {
		mode   string
		wantDR bool
	}{{"preview", true}, {"live", false}} {
		params, err := def.Preview.Resolve(json.RawMessage(`{}`), tc.mode)
		require.NoError(t, err, tc.mode)
		var decoded itunesPlaylistImportParams
		require.NoError(t, json.Unmarshal(params, &decoded), tc.mode)
		got, err := opmode.ResolveDryRun("itunes.playlist-import", decoded.DryRunSnake, decoded.DryRun)
		require.NoError(t, err, tc.mode)
		require.Equal(t, tc.wantDR, got, "%s resolved to %s", tc.mode, params)
	}
}

// maintenance.cover-text-read: no model call and no cover_text key for a
// preview; model calls and stored records for a live run.
func TestPreviewBehaviour_CoverTextRead(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		wantWrite bool
	}{{"preview", false}, {"live", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			st, deps, _ := coverOpFixture(t)
			fr := &fakeCoverReader{capacity: 2, seen: map[string]int{}}
			orig := newCoverTextReader
			newCoverTextReader = func() coverTextReader { return fr }
			t.Cleanup(func() { newCoverTextReader = orig })
			p := New(deps)
			def := p.coverTextReadDef()
			require.Contains(t, def.Capabilities, sdk.CapLibraryWrite)

			params, err := def.Preview.Resolve(json.RawMessage(`{}`), tc.mode)
			require.NoError(t, err)
			require.NoError(t, def.Run(context.Background(), params, &fakeReporter{}))

			stored, err := st.CountPrefix("cover_text")
			require.NoError(t, err)
			if tc.wantWrite {
				require.GreaterOrEqual(t, fr.calls.Load(), int64(1), "live run made no vision call")
				require.True(t, stored > 0, "live run stored no cover text (%d keys)", stored)
				return
			}
			require.Zero(t, fr.calls.Load(), "preview called the model")
			require.True(t, stored == 0, "preview stored %d cover_text keys", stored)
		})
	}
}

// maintenance.file-provenance-export: no file and no cursor move for a
// preview; the JSONL file and an advanced cursor for a live run.
func TestPreviewBehaviour_FileProvenanceExport(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		wantWrite bool
	}{{"preview", false}, {"live", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			p, store, out := newExportFixture(t)
			seedEvents(t, store, 3)
			def := p.fileProvenanceExportDef()
			require.Contains(t, def.Capabilities, sdk.CapLibraryWrite)
			require.Contains(t, def.Capabilities, sdk.CapFilesWrite)

			body, err := json.Marshal(map[string]any{"path": out})
			require.NoError(t, err)
			params, err := def.Preview.Resolve(body, tc.mode)
			require.NoError(t, err)
			require.NoError(t, def.Run(context.Background(), params, &fakeReporter{}))

			cursor, err := store.GetFileProvExportCursor()
			require.NoError(t, err)
			_, statErr := os.Stat(out)
			if tc.wantWrite {
				require.NoError(t, statErr, "live run wrote no export file")
				require.NotZero(t, cursor, "live run did not advance the cursor")
				return
			}
			require.True(t, os.IsNotExist(statErr), "preview created %s", out)
			require.Zero(t, cursor, "preview advanced the export cursor")
		})
	}
}
