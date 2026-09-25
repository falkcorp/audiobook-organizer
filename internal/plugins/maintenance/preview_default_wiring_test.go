// file: internal/plugins/maintenance/preview_default_wiring_test.go
// version: 1.0.1
// guid: 6f1a9c3d-2b84-4e57-a0d9-3c5e7b8f1a26
// last-edited: 2026-09-25

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// TestOps_DryRunRoutesThroughOpmode proves each op's mode goes through
// opmode.ResolveDryRun and that BOTH spellings reach it: a body sending
// dry_run and dryRun with different values must be refused before the op
// touches the store. opmode's own tests pin what ResolveDryRun returns for {}
// (preview) and for an explicit false (live); this test pins that these ops
// actually call it with both fields.
//
// Before 2026-09-25 the first seven decoded only camelCase dryRun into a
// plain bool pre-filled with true, so a caller sending dry_run=false got a
// silent preview, and the pre-fill was one refactor away from Go's zero value
// (LIVE). A nil store is deliberate: an op that got past the mode check would
// fail on it, which this test reports as "not at the mode check".
func TestOps_DryRunRoutesThroughOpmode(t *testing.T) {
	p := New(&fakeDeps{})
	ops := []struct {
		id  string
		run func(context.Context, json.RawMessage, sdk.Reporter) error
	}{
		{"itunes.regroup", p.runITunesRegroup},
		{"itunes.playlist-import", p.runITunesPlaylistImport},
		{"maintenance.tag-backfill", p.runTagBackfill},
		{"maintenance.booksig-sidecar-migrate", p.runBookSigSidecarMigrate},
		{"maintenance.fs-regroup-xml", p.runFSRegroupXML},
		{"maintenance.booksig-recovery-audit", p.runBookSigRecoveryAudit},
		{"maintenance.title-backfill", p.runTitleBackfill},
		{"maintenance.author-id-repair", p.runAuthorIDRepair},
		{"maintenance.author-path-link", p.runAuthorPathLink},
		{"maintenance.author-split-scan", p.runAuthorSplitScan},
		{"maintenance.resolve-production-authors", p.runResolveProductionAuthors},
	}
	for _, op := range ops {
		for _, body := range []string{`{"dry_run":false,"dryRun":true}`, `{"dry_run":true,"dryRun":false}`} {
			t.Run(op.id+" "+body, func(t *testing.T) {
				var err error
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("%s ran past the mode check on a conflicting body (panic: %v)", op.id, r)
						}
					}()
					err = op.run(context.Background(), json.RawMessage(body), &fakeReporter{})
				}()
				if err == nil || !strings.Contains(err.Error(), "disagree") {
					t.Fatalf("%s must refuse conflicting dry_run/dryRun at the mode check, got: %v", op.id, err)
				}
			})
		}
	}
}

// repoint-missing-to-folder-audio checks the store before its mode, so it is
// pinned at its resolver instead of through Run.
func TestRepointMissingToFolderAudio_DryRunResolver(t *testing.T) {
	f, tr := false, true
	if got, err := (rfParams{}).dryRun(); err != nil || !got {
		t.Fatalf("{} must preview; got (%v, %v)", got, err)
	}
	if got, err := (rfParams{DryRunSnake: &f}).dryRun(); err != nil || got {
		t.Fatalf("dry_run=false must be live; got (%v, %v)", got, err)
	}
	if got, err := (rfParams{DryRun: &f}).dryRun(); err != nil || got {
		t.Fatalf("dryRun=false must be live; got (%v, %v)", got, err)
	}
	if got, err := (rfParams{DryRun: &tr, DryRunSnake: &f}).dryRun(); err == nil || !got {
		t.Fatalf("conflict must be refused toward preview; got (%v, %v)", got, err)
	}
}
