// file: internal/metafetch/writeback_backup_ctx_test.go
// version: 1.2.0
// guid: f164b316-5969-47b6-ba84-173a926a536b
// last-edited: 2026-10-10

package metafetch

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/stretchr/testify/require"
)

// writeFileTagsSafe is the per-file write every write-back reaches, the bulk
// write-back ops included (runBulkWriteBack -> WriteBackMetadataForBookContext
// -> writeBackForBook -> here). With create_backups on, a ctx wrapped with
// tagger.WithoutBackup must leave no .bak-* sibling and a plain ctx exactly
// one: create_backups is the only backup switch, so nothing else adds a
// sibling.
func TestWriteFileTagsSafe_BackupFollowsContext(t *testing.T) {
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.CreateBackups = true })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })

	cases := []struct {
		name     string
		ctx      context.Context
		wantBaks int
	}{
		{"bulk write-back ctx", tagger.WithoutBackup(context.Background()), 0},
		{"single-book ctx", context.Background(), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			audio, _ := writeEmbedFixture(t, t.TempDir())
			// fileTagWrite is nil, so the real fileops.WriteTagsSafe runs.
			svc := &Service{}
			if err := svc.writeFileTagsSafe(tc.ctx, audio, map[string]any{"title": "Synthetic Title"},
				fileops.WriteTagsSafeOptions{}, fileops.OperationConfig{}); err != nil {
				t.Fatalf("writeFileTagsSafe: %v", err)
			}
			baks, err := filepath.Glob(audio + ".bak-*")
			if err != nil {
				t.Fatal(err)
			}
			if len(baks) != tc.wantBaks {
				t.Errorf("backup siblings = %v, want %d", baks, tc.wantBaks)
			}
		})
	}
}

// Auto-fetch's file work runs later on the file-I/O pool, through the
// scheduler closure FetchMetadataForBook hands it. A bulk caller (the iTunes
// import's enrichment) wraps its ctx with tagger.WithoutBackup; that opt-out
// must survive the hop into FinishAutoFetchFileWork and reach the tag write,
// while the per-book Fetch button's plain ctx keeps its backup.
func TestFetchMetadataForBook_BackupOptOutReachesPooledTagWrite(t *testing.T) {
	cases := []struct {
		name       string
		ctx        context.Context
		wantBackup bool
	}{
		{"bulk caller (WithoutBackup)", tagger.WithoutBackup(context.Background()), false},
		{"per-book Fetch", context.Background(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := fileWorkHarness(t, "/lib", false, true, nil)
			config.AppConfig.WriteBackMetadata = true
			config.AppConfig.CreateBackups = true
			var wanted []bool
			svc.tagWriter = func(ctx context.Context, _ string) (int, error) {
				wanted = append(wanted, tagger.BackupWanted(ctx))
				return 1, nil
			}
			svc.fileWorkScheduler = func(_ string, work func()) { work() }
			svc.overrideSources = []metadata.MetadataSource{fakeSource{
				name:    "Audible",
				results: []metadata.BookMetadata{{Title: "A Book", Author: "Some Author"}},
			}}

			_, err := svc.FetchMetadataForBook(tc.ctx, "b1")
			require.NoError(t, err)
			require.Equal(t, []bool{tc.wantBackup}, wanted, "BackupWanted at the pooled tag write")
		})
	}
}
