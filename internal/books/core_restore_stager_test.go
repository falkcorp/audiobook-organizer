// file: internal/books/core_restore_stager_test.go
// version: 0.1.0
// guid: 7d2c9e41-5a38-4b06-8f1d-e04a63b95c78
// last-edited: 2026-11-22

package books

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
	"github.com/falkcorp/mediacore/app"
	corebackup "github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// These are the restore stager's tests (restore_stager_test.go,
// swap_slot_test.go, the staging half of legacy_restore_test.go) run through
// coreRestoreStager over core's backup Service, F8-04a G2. The Service is the
// real one; what the kernel wires at F8-04b and books cannot import (it is
// core-internal) is stood in for: checkedSlotStager is restoreswap.Swapper's
// staging as far as books can observe it, the same public restorejournal calls
// restoreStager's backup.StageRestore makes (ValidateTargets, the rename
// pre-check, CreateIntent), and the Service's own audit and journal are left
// out (the keyring is empty, the markers are fixed).

// checkedSlotStager is a corebackup.CheckedStager over the one swap slot: its
// CheckStage is the refusals a stage makes before it writes, and its Stage is
// the write.
type checkedSlotStager struct {
	stateDir string
	probe    restorejournal.Probe // nil is the OS
	suffixes []string
	staged   *restorejournal.Intent // when set, the last intent Stage was handed
}

func (c checkedSlotStager) CheckStage(in restorejournal.Intent) error {
	probe := c.probe
	if probe == nil {
		probe = restorejournal.OSProbe()
	}
	if err := restorejournal.ValidateTargets(c.stateDir, in, c.suffixes); err != nil {
		return err
	}
	moves := append(restorejournal.SetAsides(c.stateDir, in, c.suffixes), in.Targets...)
	return restorejournal.Validate(probe, moves)
}

func (c checkedSlotStager) Stage(_ context.Context, in restorejournal.Intent) error {
	if c.staged != nil {
		*c.staged = in
	}
	return restorejournal.CreateIntent(c.stateDir, in)
}

// plainSlotStager is a Stager that cannot check: the Service refuses a hook.
type plainSlotStager struct{ stateDir string }

func (p plainSlotStager) Stage(_ context.Context, in restorejournal.Intent) error {
	return restorejournal.CreateIntent(p.stateDir, in)
}

// ownerProof is what the handler establishes after its guards, for a caller
// who is the owner and an admin.
func ownerProof(ctx context.Context) context.Context {
	return corebackup.WithRestoreProof(ctx, corebackup.RestoreProof{Owner: true, Admin: true})
}

// flipOwners are the storage owners the real restore opens an archive's store
// with (restoreStager's VerifyStore: StandaloneOptions): books' spec with the
// flip's level step, and core's level owner. A store HEAD's own offline open
// raised to the flip's level is "from a newer build" to books' bare R1 spec.
func flipOwners() []storage.OwnerSpec {
	o := StandaloneOptions(database.StandaloneOptions{})
	return append([]storage.OwnerSpec{{Owner: database.BooksOwner, Enabled: true, Spec: o.Spec(database.BooksSpec(nil))}}, o.ExtraOwners...)
}

// coreStager is the books stager over a Service on h's stopped store, with a
// restart that only counts.
func (h *offlineABO) coreStager(restarts *int, stager corebackup.Stager) coreRestoreStager {
	h.t.Helper()
	build, err := app.BinaryID(app.Options{Version: telemetry.Version()})
	require.NoError(h.t, err)
	owners := flipOwners()
	dir := backup.ResolveDir("", h.path)
	if stager == nil {
		stager = checkedSlotStager{stateDir: h.state, suffixes: legacySidecarSuffixes()}
	}
	svc, err := corebackup.New(corebackup.Options{
		AppID: AppID, AppVersion: telemetry.Version(), CoreVersion: "test", BuildID: build,
		Prefix: archivePrefix, Dir: dir, StateDir: h.state, DBPath: h.path,
		Owners: owners, Keyring: legacyKeys{}, Markers: legacyMarkers{}, Stager: stager,
		Settings:  legacySettings,
		Inspector: &corebackup.StorageInspector{Base: storage.OpenOptions{Owners: owners, Build: build}},
	})
	require.NoError(h.t, err)
	return coreRestoreStager{svc: svc, dir: dir, dbPath: h.path, restart: func(string) { *restarts++ }}
}

// makeArchive is HEAD's own archive of h's store (a legacy-abo/0 file with its
// checksum sidecar).
func (h *offlineABO) makeArchive() *backup.BackupInfo {
	h.t.Helper()
	info, err := backup.CreateBackup(h.path, "pebble", backup.BackupConfig{BackupDir: backup.ResolveDir("", h.path), MaxBackups: 5})
	require.NoError(h.t, err)
	return info
}

// TestCoreRestoreStager_StagesALegacyArchive: the happy path through the
// Service, and what the handler reads off the result: the intent's ID (the
// slot's), the archive's path, the catalogue ID (legacy-<stem>, not a
// digest) and that it was verified.
func TestCoreRestoreStager_StagesALegacyArchive(t *testing.T) {
	h := newOfflineABO(t)
	info := h.makeArchive()
	build, err := app.BinaryID(app.Options{Version: telemetry.Version()})
	require.NoError(t, err)

	restarts := 0
	var in restorejournal.Intent
	st := h.coreStager(&restarts, checkedSlotStager{stateDir: h.state, suffixes: legacySidecarSuffixes(), staged: &in})
	got, err := st.StageRestore(ownerProof(context.Background()), filepath.Dir(info.Path), info.Filename, true, "user:alfred", nil)
	require.NoError(t, err)

	pending := h.slot().Pending
	require.NotNil(t, pending, "nothing staged")
	assert.Equal(t, pending.ID, got.ID)
	assert.Equal(t, got.ID, in.ID, "the result names the intent the Stager was handed")
	assert.Equal(t, info.Path, got.Archive)
	assert.Equal(t, corebackup.LegacyIDPrefix+strings.TrimSuffix(info.Filename, ".tar.gz"), got.ArchiveID)
	assert.True(t, got.Verified)
	assert.Equal(t, restorejournal.KindArchive, pending.Kind)
	assert.Equal(t, build, in.StagedByBuild)
	assert.Equal(t, h.path, in.DBPath)
	assert.Equal(t, []string{got.ID}, h.stagingLeft())
	assert.Zero(t, restarts, "staging asked for the restart; that is the handler's call after its 202")
	st.RequestRestart("restore " + got.ID + " staged")
	assert.Equal(t, 1, restarts)
}

// TestCoreRestoreStager_RefusesAStoreThisBuildCannotOpen is
// TestRestoreStagerRefusesAStoreThisBuildCannotOpen through the Service: the
// Inspector opens the extracted store with this build's guard before it
// stages, so a store the boot after the swap would refuse is refused now, the
// slot left empty, nothing left extracted and neither HNSW step run.
func TestCoreRestoreStager_RefusesAStoreThisBuildCannotOpen(t *testing.T) {
	h := newOfflineABO(t)
	src := filepath.Join(tempDir(t), "joker.pebble")
	headStoreAt(t, src, nil)
	db, err := pebble.Open(src, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte("c/rogue"), []byte("x"), pebble.Sync))
	require.NoError(t, db.Close())
	backups := backup.ResolveDir("", h.path)
	info, err := backup.CreateBackup(src, "pebble", backup.BackupConfig{BackupDir: backups, MaxBackups: 5})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(h.marker), 0o700))
	require.NoError(t, os.WriteFile(h.marker, []byte(`{"version":1,"model":""}`+"\n"), 0o644))

	restarts, marked := 0, 0
	_, err = h.coreStager(&restarts, nil).StageRestore(ownerProof(context.Background()), backups, info.Filename, true, "user:alfred", func() { marked++ })
	require.ErrorIs(t, err, storage.ErrForeignKeys)
	require.False(t, h.slot().Busy(), "a refused restore was staged: %+v", h.slot())
	assert.Empty(t, h.stagingLeft(), "the refused restore left its extraction")
	assert.Zero(t, restarts)
	assert.Zero(t, marked, "a refused restore marked the HNSW store diverged")
	assert.True(t, exists(h.marker), "a refused restore discarded the clean-stop marker")
}

// TestCoreRestoreStager_MovesTheBooksSidecar is TestRestoreStagerMovesTheBooksSidecar
// through the Service: the swap sets books' storage-format sidecar aside with
// the store, so the stager's check pre-checks that rename too, and one the swap
// could not move refuses the restore at staging, before anything is extracted
// and before either HNSW step.
func TestCoreRestoreStager_MovesTheBooksSidecar(t *testing.T) {
	h := newOfflineABO(t)
	sidecar := database.StorageFormatSidecarPath(h.path)
	require.FileExists(t, sidecar, "the booted store has no storage-format sidecar to move")
	info := h.makeArchive()
	require.NoError(t, os.MkdirAll(filepath.Dir(h.marker), 0o700))
	require.NoError(t, os.WriteFile(h.marker, []byte(`{"version":1,"model":""}`+"\n"), 0o644))

	restarts, marked := 0, 0
	st := h.coreStager(&restarts, checkedSlotStager{
		stateDir: h.state, suffixes: legacySidecarSuffixes(),
		probe: itemElsewhere{restorejournal.OSProbe(), sidecar},
	})
	_, err := st.StageRestore(ownerProof(context.Background()), filepath.Dir(info.Path), info.Filename, true, "user:alfred", func() { marked++ })
	require.ErrorIs(t, err, restorejournal.ErrRenameRefused)
	require.False(t, h.slot().Busy(), "a refused restore was staged: %+v", h.slot())
	assert.Empty(t, h.stagingLeft(), "the refused restore left its extraction")
	assert.Zero(t, marked, "a restore the swap would refuse marked the HNSW store diverged")
	assert.True(t, exists(h.marker), "a restore the swap would refuse discarded the clean-stop marker")
}

// TestCoreRestoreStager_MarksDivergedBeforeTheMarkerAndTheIntent is
// TestRestoreStagerMarksDivergedBeforeTheMarkerAndTheIntent through the
// Service: the divergence mark runs once, while the clean-stop marker is still
// on disk and before the intent is written, then the marker is discarded. A
// restore of a file that is not there marks nothing.
func TestCoreRestoreStager_MarksDivergedBeforeTheMarkerAndTheIntent(t *testing.T) {
	h := newOfflineABO(t)
	info := h.makeArchive()
	require.NoError(t, os.MkdirAll(filepath.Dir(h.marker), 0o700))
	require.NoError(t, os.WriteFile(h.marker, []byte(`{"version":1,"model":""}`+"\n"), 0o644))

	restarts := 0
	type seen struct{ marker, staged bool }
	var marks []seen
	diverged := func() { marks = append(marks, seen{marker: exists(h.marker), staged: h.slot().Busy()}) }
	ctx := ownerProof(context.Background())
	st := h.coreStager(&restarts, nil)

	_, err := st.StageRestore(ctx, filepath.Dir(info.Path), "riddler.tar.gz", true, "user:alfred", diverged)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Empty(t, marks, "a refused restore marked the HNSW store diverged")
	require.True(t, exists(h.marker), "a refused restore discarded the marker")

	_, err = st.StageRestore(ctx, filepath.Dir(info.Path), info.Filename, true, "user:alfred", diverged)
	require.NoError(t, err)
	require.Equal(t, []seen{{marker: true, staged: false}}, marks, "the mark: once, with the marker still on disk and nothing staged")
	require.False(t, exists(h.marker), "the marker survived the stage")
	require.True(t, h.slot().Busy(), "nothing staged")
}

// TestCoreRestoreStager_NeedsTheOwnerProof: core refuses a restore whose
// context carries no owner proof, and a foreign one (every legacy archive)
// without admin; the stager neither mints a proof nor reaches either HNSW step
// for the refusal. This is what keeps the handler's guards the only way in.
func TestCoreRestoreStager_NeedsTheOwnerProof(t *testing.T) {
	h := newOfflineABO(t)
	info := h.makeArchive()
	require.NoError(t, os.MkdirAll(filepath.Dir(h.marker), 0o700))
	require.NoError(t, os.WriteFile(h.marker, []byte(`{"version":1,"model":""}`+"\n"), 0o644))

	restarts, marked := 0, 0
	st := h.coreStager(&restarts, nil)
	for name, ctx := range map[string]context.Context{
		"no proof":         context.Background(),
		"owner, not admin": corebackup.WithRestoreProof(context.Background(), corebackup.RestoreProof{Owner: true}),
		"admin, not owner": corebackup.WithRestoreProof(context.Background(), corebackup.RestoreProof{Admin: true}),
		"the zero proof":   corebackup.WithRestoreProof(context.Background(), corebackup.RestoreProof{}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := st.StageRestore(ctx, filepath.Dir(info.Path), info.Filename, true, "user:alfred", func() { marked++ })
			require.ErrorIs(t, err, corebackup.ErrRestoreNotAuthorized)
			require.False(t, h.slot().Busy(), "an unauthorized restore was staged")
			assert.Empty(t, h.stagingLeft())
			assert.Zero(t, marked, "an unauthorized restore marked the HNSW store diverged")
			assert.True(t, exists(h.marker), "an unauthorized restore discarded the clean-stop marker")
		})
	}
}

// TestCoreRestoreStager_NeedsAServiceThatRunsTheHook: a Service whose Stager
// cannot check an intent before it writes cannot run the stager's before-stage
// step between the checks and the create, so it refuses before anything is
// read, rather than stage a restore whose clean-stop marker was never
// discarded.
func TestCoreRestoreStager_NeedsAServiceThatRunsTheHook(t *testing.T) {
	h := newOfflineABO(t)
	info := h.makeArchive()
	require.NoError(t, os.MkdirAll(filepath.Dir(h.marker), 0o700))
	require.NoError(t, os.WriteFile(h.marker, []byte(`{"version":1,"model":""}`+"\n"), 0o644))

	restarts, marked := 0, 0
	st := h.coreStager(&restarts, plainSlotStager{stateDir: h.state})
	_, err := st.StageRestore(ownerProof(context.Background()), filepath.Dir(info.Path), info.Filename, true, "user:alfred", func() { marked++ })
	require.ErrorIs(t, err, corebackup.ErrBeforeStageUnsupported)
	require.False(t, h.slot().Busy(), "a restore was staged without its before-stage step")
	assert.Empty(t, h.stagingLeft())
	assert.Zero(t, marked)
	assert.True(t, exists(h.marker))
}

// TestCoreRestoreStager_ReadsOnlyItsOwnDirectoryAndCatalogue: the stager
// stages a file the Service's catalogue lists, from the directory the Service
// serves; another directory, a path that climbs, and a file the catalogue does
// not know are all refused with nothing staged.
func TestCoreRestoreStager_ReadsOnlyItsOwnDirectoryAndCatalogue(t *testing.T) {
	h := newOfflineABO(t)
	info := h.makeArchive()
	restarts := 0
	st := h.coreStager(&restarts, nil)
	ctx := ownerProof(context.Background())

	_, err := st.StageRestore(ctx, tempDir(t), info.Filename, true, "user:alfred", nil)
	require.Error(t, err, "a restore from a directory the Service does not serve")

	for _, name := range []string{"../" + info.Filename, filepath.Join(filepath.Dir(info.Path), info.Filename), "", "riddler.tar.gz"} {
		_, err := st.StageRestore(ctx, filepath.Dir(info.Path), name, true, "user:alfred", nil)
		require.ErrorIs(t, err, os.ErrNotExist, "file %q", name)
	}
	require.False(t, h.slot().Busy(), "a refused restore was staged: %+v", h.slot())
	assert.Empty(t, h.stagingLeft())
}

// TestCoreRestoreStager_VerifyFalse is the legacy stager's verify flag through
// the Service: asked to verify, an archive with no checksum sidecar is refused;
// not asked, one with a sidecar is still checked against it (and reports
// verified) and one without is staged unverified.
func TestCoreRestoreStager_VerifyFalse(t *testing.T) {
	ctx := ownerProof(context.Background())
	t.Run("sidecar present", func(t *testing.T) {
		h := newOfflineABO(t)
		info := h.makeArchive()
		restarts := 0
		got, err := h.coreStager(&restarts, nil).StageRestore(ctx, filepath.Dir(info.Path), info.Filename, false, "user:alfred", nil)
		require.NoError(t, err)
		assert.True(t, got.Verified, "an archive with a sidecar reports it was checked against it")
	})
	t.Run("a corrupted archive with a sidecar is refused without verify too", func(t *testing.T) {
		h := newOfflineABO(t)
		info := h.makeArchive()
		f, err := os.OpenFile(info.Path, os.O_WRONLY|os.O_APPEND, 0)
		require.NoError(t, err)
		_, err = f.WriteString("joker")
		require.NoError(t, err)
		require.NoError(t, f.Close())
		restarts := 0
		_, err = h.coreStager(&restarts, nil).StageRestore(ctx, filepath.Dir(info.Path), info.Filename, false, "user:alfred", nil)
		require.ErrorIs(t, err, corebackup.ErrChecksumMismatch)
		require.False(t, h.slot().Busy())
	})
	t.Run("sidecar absent", func(t *testing.T) {
		h := newOfflineABO(t)
		info := h.makeArchive()
		require.NoError(t, os.Remove(info.Path+corebackup.ChecksumSidecar))
		restarts := 0
		st := h.coreStager(&restarts, nil)
		_, err := st.StageRestore(ctx, filepath.Dir(info.Path), info.Filename, true, "user:alfred", nil)
		require.Error(t, err, "verify=true staged an archive with no checksum sidecar")
		require.False(t, h.slot().Busy(), "a refused restore was staged")
		got, err := st.StageRestore(ctx, filepath.Dir(info.Path), info.Filename, false, "user:alfred", nil)
		require.NoError(t, err)
		assert.False(t, got.Verified, "an archive staged with no sidecar reported it was verified")
	})
}

// TestCoreRestoreStager_SwapSlotExclusive is TestSwapSlotExclusive through the
// Service: the one swap slot holds one intent, whoever staged it. A restore
// staged here refuses `storage reset` with ErrSwapPending and the in-place
// offline restore supersedes it; a reset staged first refuses the restore
// before it extracts anything; and of several restores racing for the slot
// exactly one is staged, whether they share a Service (the Service's own
// exclusion refuses the others, ErrBackupInProgress, or the slot does) or each
// has its own (the slot's one create decides, ErrSwapPending).
func TestCoreRestoreStager_SwapSlotExclusive(t *testing.T) {
	ctx := ownerProof(context.Background())

	t.Run("restore first", func(t *testing.T) {
		h := newOfflineABO(t)
		info := h.makeArchive()
		restarts := 0
		staged, err := h.coreStager(&restarts, nil).StageRestore(ctx, filepath.Dir(info.Path), info.Filename, true, "user:alfred", nil)
		require.NoError(t, err)
		require.Equal(t, staged.ID, h.slot().Pending.ID)

		out, err := h.run("storage reset", "--kind", "store", "--confirm", "RESET")
		require.ErrorIs(t, err, restorejournal.ErrSwapPending, out)
		require.Equal(t, staged.ID, h.slot().Pending.ID, "a refused stager changed the slot")
		assert.Equal(t, restorejournal.KindArchive, h.slot().Pending.Kind)
		assert.Equal(t, []string{staged.ID}, h.stagingLeft())

		out, err = h.run("storage restore-offline", "--archive", info.Path, "--accept-loss")
		require.NoError(t, err, out)
		assert.Contains(t, out, staged.ID)
		var steps []string
		for _, r := range journalOf(t, h.state, staged.ID) {
			steps = append(steps, r.Step)
		}
		assert.Equal(t, "rejected", steps[len(steps)-1], "the superseded restore's journal: %v", steps)
		require.False(t, h.slot().Busy())
	})

	t.Run("reset first", func(t *testing.T) {
		h := newOfflineABO(t)
		info := h.makeArchive()
		out, err := h.run("storage reset", "--kind", "store", "--confirm", "RESET")
		require.NoError(t, err, out)
		reset := h.slot().Pending
		require.NotNil(t, reset)
		require.Equal(t, restorejournal.KindReset, reset.Kind)

		restarts, marked := 0, 0
		_, err = h.coreStager(&restarts, nil).StageRestore(ctx, filepath.Dir(info.Path), info.Filename, true, "user:alfred", func() { marked++ })
		require.ErrorIs(t, err, restorejournal.ErrSwapPending)
		require.Equal(t, reset.ID, h.slot().Pending.ID, "a refused stager changed the slot")
		assert.Empty(t, h.stagingLeft(), "the refused restore extracted the archive")
		assert.Zero(t, restarts)
		assert.Zero(t, marked, "a restore refused for a taken slot marked the HNSW store diverged")
	})

	race := func(t *testing.T, stagerFor func(h *offlineABO, restarts *int) coreRestoreStager, refused func(error) bool) {
		h := newOfflineABO(t)
		info := h.makeArchive()
		const n = 6
		restarts := 0
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			won  []string
			lost int
			errs []error
		)
		stagers := make([]coreRestoreStager, n)
		for i := range stagers {
			stagers[i] = stagerFor(h, &restarts)
		}
		for i := range n {
			wg.Go(func() {
				got, err := stagers[i].StageRestore(ctx, filepath.Dir(info.Path), info.Filename, true, "user:alfred", nil)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					won = append(won, got.ID)
				case refused(err):
					lost++
				default:
					errs = append(errs, err)
				}
			})
		}
		wg.Wait()
		require.Empty(t, errs)
		require.Len(t, won, 1, "stagers that won the slot")
		require.Equal(t, n-1, lost)
		require.Equal(t, won[0], h.slot().Pending.ID)
		assert.Equal(t, won, h.stagingLeft(), "a losing stager left its extraction")
	}
	t.Run("racing restores, one Service each", func(t *testing.T) {
		race(t, func(h *offlineABO, restarts *int) coreRestoreStager { return h.coreStager(restarts, nil) },
			func(err error) bool { return errors.Is(err, restorejournal.ErrSwapPending) })
	})
	t.Run("racing restores, one shared Service", func(t *testing.T) {
		var shared coreRestoreStager
		var once sync.Once
		race(t, func(h *offlineABO, restarts *int) coreRestoreStager {
			once.Do(func() { shared = h.coreStager(restarts, nil) })
			return shared
		}, func(err error) bool {
			return errors.Is(err, restorejournal.ErrSwapPending) || errors.Is(err, corebackup.ErrBackupInProgress)
		})
	})
}

// stageWithBooksCoreStager is stageWithCore through books' stager: it stages
// archive (a legacy-abo/0 file) in work's state directory as the handler would,
// after the handler's guards (an owner proof), and returns the intent's ID.
// The HNSW clean-stop marker of the store being replaced is discarded by the
// stager, not by the test.
func stageWithBooksCoreStager(t *testing.T, work, archive string) string {
	t.Helper()
	m := archiveName.FindStringSubmatch(filepath.Base(archive))
	require.NotNil(t, m, "archive name %q", filepath.Base(archive))
	stateDir := filepath.Join(work, "state")
	build, err := app.BinaryID(app.Options{Version: telemetry.Version()})
	require.NoError(t, err)
	owners := flipOwners()
	var in restorejournal.Intent
	dir := filepath.Dir(archive)
	svc, err := corebackup.New(corebackup.Options{
		AppID: AppID, AppVersion: telemetry.Version(), CoreVersion: "test", BuildID: build,
		Prefix: m[1], Dir: dir, StateDir: stateDir, DBPath: storeDB(work),
		Owners: owners, Keyring: legacyKeys{}, Markers: legacyMarkers{},
		Stager:    checkedSlotStager{stateDir: stateDir, suffixes: legacySidecarSuffixes(), staged: &in},
		Settings:  legacySettings,
		Inspector: &corebackup.StorageInspector{Base: storage.OpenOptions{Owners: owners, Build: build}},
	})
	require.NoError(t, err)
	st := coreRestoreStager{svc: svc, dir: dir, dbPath: storeDB(work), restart: func(string) {}}

	marked := 0
	got, err := st.StageRestore(ownerProof(t.Context()), dir, filepath.Base(archive), true, "user:alfred", func() { marked++ })
	require.NoError(t, err)
	require.Equal(t, 1, marked, "the divergence mark")
	require.True(t, strings.HasPrefix(got.ArchiveID, corebackup.LegacyIDPrefix), "archive id %q", got.ArchiveID)
	require.True(t, got.Verified)

	slot, err := restorejournal.ReadSlot(vfs.Default, stateDir)
	require.NoError(t, err)
	require.True(t, slot.Busy(), "nothing staged in the one slot")
	require.NotNil(t, slot.Pending)
	require.Equal(t, got.ID, slot.Pending.ID, "the result names the intent in the slot")
	require.Equal(t, build, in.StagedByBuild)
	require.Equal(t, uint64(legacyJournalHead), in.OutgoingSeq, "the staged intent carries the live journal head")
	return slot.Pending.ID
}
