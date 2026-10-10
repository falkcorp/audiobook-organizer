// file: internal/metafetch/writeback_backup_ctx_test.go
// version: 1.0.0
// guid: f164b316-5969-47b6-ba84-173a926a536b
// last-edited: 2026-10-10

package metafetch

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// writeFileTagsSafe is the per-file write every write-back reaches, the bulk
// write-back ops included (runBulkWriteBack -> WriteBackMetadataForBookContext
// -> writeBackForBook -> here). With create_backups on, a ctx wrapped with
// tagger.WithoutBackup must leave no .bak-* sibling and a plain ctx exactly
// one. The older write_backup_before_tag_write setting is held off so its
// own dated backup does not count.
func TestWriteFileTagsSafe_BackupFollowsContext(t *testing.T) {
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) {
		c.CreateBackups = true
		c.MetadataScoring.WriteBackupBefore = false
	})
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
