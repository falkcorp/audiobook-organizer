// file: internal/server/bulk_writeback_rename_refused_test.go
// version: 1.0.0
// guid: 9c2e5a71-4f3b-4d86-a0e7-3b8d6f1c9e24
// last-edited: 2026-09-13

package server

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// A bulk write-back with rename:true whose rename cannot be planned counts
// the book as failed and writes no tags: before 2026-09-13 the failed rename
// was logged and the tags were written anyway, counted as "written".
func TestBulkWriteBack_RefusedRenameCountsFailedAndWritesNoTags(t *testing.T) {
	if testing.Short() {
		t.Skip("touches the filesystem and the full write-back pipeline")
	}
	store := rowWritersStore(t)
	f := newRowWritersFixture(t, store) // restores config.AppConfig on cleanup

	// An unplannable file-name verb: RenameOnlyPreflight refuses before any
	// file moves (the same fixture shape as metafetch's
	// TestRenameOnlyPreflight_RefusesUnplannableRenameWithAutoRenameOff).
	config.AppConfig.FolderNamingPattern = "{author}/{title}"
	config.AppConfig.FileNamingPattern = "{title} - {track:x}"

	before := make(map[string][]byte, len(f.srcFiles))
	for _, p := range f.srcFiles {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		before[p] = b
	}

	srv := &Server{store: store, metadataFetchService: lockedMetafetch(store)}
	rec := &resumeRecorder{opID: "op-rename-refused"}
	require.NoError(t, srv.runBulkWriteBack(context.Background(), "op-rename-refused",
		[]string{f.book.ID}, true, 0, registryProgressAdapter{r: rec}, nil))

	lines := rec.logLines()
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "book "+f.book.ID+": rename refused before any move, tags not written",
		"the refusal must be logged against the book: %v", lines)
	require.NotContains(t, joined, "write-back failed", "tags must not be attempted after a refused rename: %v", lines)
	require.Contains(t, joined, "bulk write-back complete: 0 written, 1 failed, 0 skipped out of 1",
		"a refused rename is a failed book, not a written one: %v", lines)

	// Nothing moved and no byte of any file changed.
	for p, want := range before {
		got, err := os.ReadFile(p)
		require.NoError(t, err, "file %s moved", p)
		require.Equal(t, want, got, "file %s was rewritten", p)
	}
}
