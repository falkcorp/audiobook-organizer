// file: internal/books/legacy_restore_test.go
// version: 1.0.0
// guid: 5d0b8a63-1e47-4c92-b7f5-3a6c9e2d8f14
// last-edited: 2026-11-12

package books

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
	"github.com/falkcorp/mediacore/app"
	corebackup "github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The staging half of TestLegacyArchiveRestoreThenMigrate (05-backup §5B.9;
// F8-01 invariant I6; F8-04 owns the test and extends it to the migrated
// store and the corpus' per-family counts).

// legacyKeys is a keyring that holds nothing: a legacy archive carries no
// provenance and no secret records for core's keyring to cover.
type legacyKeys struct{}

func (legacyKeys) ActiveDeriveID() string { return "d1" }
func (legacyKeys) DeriveKeyFor(string, string) ([]byte, error) {
	return []byte("0123456789abcdef0123456789abcdef"), nil
}
func (legacyKeys) KeyIDs() []string { return nil }
func (legacyKeys) KeyIDsReferenced(context.Context, storage.Reader) ([]string, error) {
	return nil, nil
}

// slotStager creates the intent in the one swap slot, as the kernel's swapper
// does for the staged restore (restorejournal.CreateIntent is its write).
type slotStager struct{ stateDir string }

func (s slotStager) Stage(_ context.Context, in restorejournal.Intent) error {
	return restorejournal.CreateIntent(s.stateDir, in)
}

// archiveName splits a legacy archive's file name into core's catalogue prefix
// and the rest (<yyyymmdd>_<hhmmss>.tar.gz).
var archiveName = regexp.MustCompile(`^(.*_)(\d{8}_\d{6}\.tar(\.gz|\.zst)?)$`)

// restoreOfflineWithCoreExtractor is restoreOffline with the app's own
// archive extractor left unset, so storage restore-offline --archive reads the
// archive through core's backup.ExtractStore (the R2 behaviour), not HEAD's.
func restoreOfflineWithCoreExtractor(t *testing.T, work string, args ...string) string {
	t.Helper()
	stateDir := filepath.Join(work, "state")
	opts := app.Options{
		AppID:             AppID,
		Version:           telemetry.Version(),
		StateDir:          stateDir,
		R1StoragePath:     storeDB(work),
		StorageStateFiles: StateFiles(stateDir, storeDB(work)),
		R1BeforeSwap:      database.DiscardHNSWCleanStop,
		StorageFS:         storage.FS{},
	}
	for _, cmd := range app.OfflineCommands(opts, Module()) {
		if cmd.Name != "storage restore-offline" {
			continue
		}
		var out bytes.Buffer
		require.NoError(t, cmd.Run(t.Context(), args, &out), "storage restore-offline %v: %s", args, out.String())
		return out.String()
	}
	t.Fatal("app.OfflineCommands has no storage restore-offline")
	return ""
}

// stageWithCore stages archive (a legacy-abo/0 file) in work's state directory
// through core's backup Service, as a foreign restore, and returns the intent's
// ID. Everything a kernel will wire at F8-04 is stood in for here: the keyring
// is empty, the stager writes the slot, and the HNSW clean-stop marker of the
// store being replaced is discarded first, as books' own stager does.
func stageWithCore(t *testing.T, work, archive string) string {
	t.Helper()
	m := archiveName.FindStringSubmatch(filepath.Base(archive))
	require.NotNil(t, m, "archive name %q", filepath.Base(archive))
	stateDir := filepath.Join(work, "state")
	build, err := app.BinaryID(app.Options{Version: telemetry.Version()})
	require.NoError(t, err)
	owners := []storage.OwnerSpec{{Owner: database.BooksOwner, Enabled: true, Spec: database.BooksSpec(nil)}}
	svc, err := corebackup.New(corebackup.Options{
		AppID: AppID, AppVersion: telemetry.Version(), CoreVersion: "test", BuildID: build,
		Prefix: m[1], Dir: filepath.Dir(archive), StateDir: stateDir, DBPath: storeDB(work),
		Owners: owners, Keyring: legacyKeys{}, Stager: slotStager{stateDir},
		Inspector: &corebackup.StorageInspector{Base: storage.OpenOptions{Owners: owners, Build: build}},
	})
	require.NoError(t, err)

	list, err := svc.List(t.Context())
	require.NoError(t, err)
	require.Len(t, list, 1, "the backup directory holds the one archive")
	require.Equal(t, corebackup.FormatLegacyABO, list[0].Format)
	require.Equal(t, corebackup.PreProvenance, list[0].Provenance)
	require.True(t, strings.HasPrefix(string(list[0].ID), corebackup.LegacyIDPrefix))

	// A legacy archive predates provenance: restoring one is a foreign restore.
	ctx := corebackup.WithRestoreProof(t.Context(), corebackup.RestoreProof{Owner: true, Admin: true})
	_, err = svc.StageRestore(ctx, list[0].ID, corebackup.RestoreOptions{})
	require.ErrorIs(t, err, corebackup.ErrForeignArchive, "a legacy archive staged without Foreign")
	require.NoError(t, database.DiscardHNSWCleanStop(storeDB(work)))
	pr, err := svc.StageRestore(ctx, list[0].ID, corebackup.RestoreOptions{Foreign: true})
	require.NoError(t, err)
	require.Equal(t, list[0].ID, pr.ArchiveID)
	require.Equal(t, build, pr.StagedByBuild)
	require.NotEmpty(t, pr.Warnings, "a legacy archive restored without --gap-fill's file warns (DATA-15)")

	st, err := restorejournal.ReadSlot(vfs.Default, stateDir)
	require.NoError(t, err)
	require.True(t, st.Busy(), "nothing staged in the one slot")
	require.NotNil(t, st.Pending)
	return st.Pending.ID
}

// TestLegacyArchiveRestoreThenMigrate, the staging half: HEAD's own archive of
// the corpus (a legacy-abo/0 file with its .sha256 sidecar, made by
// backup.CreateBackup), after an upgrade boot and a marker row changed the
// corpus copy, is restored both ways and the restore is undone: staged through
// core's Service and applied by the next boot's swap, and offline through
// storage restore-offline --archive with core's extractor. Each restores the
// corpus' rows byte for byte (the marker is gone), and the boot after it takes
// its own pre-migration checkpoint when any step runs (05-storage §5S.3.12),
// whose store copy is the restored rows. No cutover step is registered at this
// row's wave, so nothing is asserted about migrated families (F8-04).
func TestLegacyArchiveRestoreThenMigrate(t *testing.T) {
	if testing.Short() {
		t.Skip("boots the composition twice per corpus (extraction-gates r2-cutover-suite)")
	}
	forEachCorpus(t, func(t *testing.T, c corpus) {
		if c.name == "shaped" {
			t.Skip("the shaped store is a size fixture; the staging half reads r0 and prefreeze-written")
		}
		steps := registeredCutoverSteps(t)
		pristine := copyCorpus(t, c)
		archive := legacyArchive(t, pristine)
		pre := scanStore(t, storeDB(pristine), wholeStore())

		// migratingBoot is the boot that follows a restore: it applies any staged
		// swap before the store opens, then migrates the restored store behind its
		// own pre-migration checkpoint when a step runs (05-storage §5S.3.12),
		// whose store copy must be the corpus' rows.
		migratingBoot := func(t *testing.T, work string) {
			had := checkpoints(t, work)
			edit, opt := cutoverBootConfig(t, work)
			r := runComposedIn(t, filepath.Join(work, "state"), edit, nil, opt, nil)
			require.NoError(t, r.stop())
			cp, problem := upgradeCheckpoint(len(steps), had, checkpoints(t, work))
			require.Empty(t, problem)
			if cp == "" {
				t.Log("the boot took no checkpoint: no step or legacy migration to run on this corpus")
				return
			}
			copyOf := scanStore(t, filepath.Join(work, "db", ".mediacore-checkpoints", cp, "db"), wholeStore())
			requireNoProblems(t, "the pre-migration checkpoint of the restored store", drillProblems(pre, copyOf, c.wrote))
		}

		t.Run("offline", func(t *testing.T) {
			work := copyCorpus(t, c)
			upgradeAndMark(t, work)
			out := restoreOfflineWithCoreExtractor(t, work, "--archive", archive, "--accept-loss")
			require.Contains(t, out, "--gap-fill", "the offline restore of a legacy archive warns what migrates empty without it")
			restored := scanStore(t, storeDB(work), wholeStore())
			requireNoProblems(t, "the offline-restored store", drillProblems(pre, restored, c.wrote))
			migratingBoot(t, work)
		})
		t.Run("staged", func(t *testing.T) {
			work := copyCorpus(t, c)
			upgradeAndMark(t, work)
			id := stageWithCore(t, work, archive)

			// Extracted: the staged store is the corpus' rows, and the live store
			// was not written.
			staged := scanStore(t, filepath.Join(restorejournal.StagingDir(storeDB(work), id), "db"), wholeStore())
			requireNoProblems(t, "the staged store", drillProblems(pre, staged, c.wrote))

			// The next boot applies the swap before the store opens, then migrates.
			migratingBoot(t, work)
			var journalSteps []string
			for _, rec := range journalOf(t, filepath.Join(work, "state"), id) {
				journalSteps = append(journalSteps, rec.Step)
			}
			require.Contains(t, journalSteps, "swapped", "journal %v", journalSteps)
			require.NotContains(t, journalSteps, "rejected", "journal %v", journalSteps)
			st, err := restorejournal.ReadSlot(vfs.Default, filepath.Join(work, "state"))
			require.NoError(t, err)
			require.False(t, st.Busy(), "slot after the applying boot = %+v", st)
			// The boot's own writes differ from the corpus, so the live store is
			// not compared row for row; the marker row of the replaced store is gone.
			live := scanStore(t, storeDB(work), wholeStore())
			for k, v := range live.rows {
				require.NotContains(t, string(v), drillMarker, "the marker written after the upgrade survived the swap (%s)", k)
			}
		})
	})
}
