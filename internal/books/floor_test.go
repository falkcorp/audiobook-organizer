// file: internal/books/floor_test.go
// version: 0.1.0
// guid: 8e1b4d27-60c3-4a95-b7f2-5d09c3a1e648
// last-edited: 2026-11-22

package books

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// R3-02d: books' upgrade floor (floor.go) and what it does to a store on
// each side of it. The R2 release tag is not known until OWNER-05, so the
// declaration carries floorPlaceholderTag; TestUpgradeFloorTagShape accepts
// it or a release tag, and TestUpgradeFloorTagIsNotPlaceholder
// (floor_release_test.go, build tag release) refuses it.

// floorPlaceholderTag is the value of InstallFirst until OWNER-05 records
// the R2 production release. It cannot be mistaken for a tag a release cut.
const floorPlaceholderTag = "v0.0.0-r2-placeholder"

// floorTagPattern is a production release tag: plain semver, no pre-release
// suffix (OWNER-05 cuts the production tag from its deployed RC tag).
var floorTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// TestUpgradeFloorDeclaration pins the declaration: books' floor is LegacyCap
// (66) with M2-M6 required, in the manifest's storage spec and in the spec
// of the openers that plan no manifest (the restore stager's verify, the
// op-runner child), so every open of books' store applies it.
func TestUpgradeFloorDeclaration(t *testing.T) {
	f := upgradeFloor()
	assert.Equal(t, storage.Owner("books"), f.Owner)
	assert.Equal(t, 66, f.Version, "books' floor is LegacyCap as the spec fixes it (05-storage 5S.3.5)")
	assert.Equal(t, database.LegacyCap, f.Version)
	assert.Equal(t, []storage.StepRef{
		{Owner: "books", DataMigration: "books.config-cutover"},
		{Owner: "books", DataMigration: "books.identity-cutover"},
		{Owner: "books", DataMigration: "books.tasks-cutover"},
		{Owner: "books", DataMigration: "books.log-cutover"},
		{Owner: "books", DataMigration: "books.integrations-cutover"},
	}, f.Requires, "M2, M3, M4, M5 and M6, in that order")
	assert.NotEmpty(t, f.InstallFirst)

	// The copies in floor.go are what outlive internal/books/cutover (R3-02e
	// deletes the package and this cell with it): until then they must be the
	// IDs the steps record.
	assert.Equal(t, cutover.ConfigCutoverID, floorConfigCutoverID)
	assert.Equal(t, cutover.IdentityCutoverID, floorIdentityCutoverID)
	assert.Equal(t, cutover.TasksCutoverID, floorTasksCutoverID)
	assert.Equal(t, cutover.LogCutoverID, floorLogCutoverID)
	assert.Equal(t, cutover.IntegrationsCutoverID, floorIntegrationsCutoverID)

	for name, spec := range map[string]storage.Spec{
		"manifest":   Module().Manifest().Storage,
		"flip":       flipOn().Manifest().Storage,
		"standalone": StandaloneSpec(storage.Spec{}),
	} {
		assert.Equal(t, f, spec.UpgradeFloor, "the %s storage spec does not declare books' floor", name)
	}
}

// TestUpgradeFloorTagShape: until OWNER-05 the tag is the placeholder; after
// it, a plain release tag. Anything else (a typo, an RC tag, "main") is a
// mistake in the one line OWNER-05's record is copied into.
func TestUpgradeFloorTagShape(t *testing.T) {
	tag := upgradeFloor().InstallFirst
	if tag == floorPlaceholderTag {
		t.Skipf("InstallFirst is still the placeholder %q; floor_release_test.go (-tags release) fails the release gate while it is", tag)
	}
	assert.Regexp(t, floorTagPattern, tag, "InstallFirst must be the R2 production release tag from OWNER-05's record")
}

// bootOnDisk boots ABO's composition over the store at path and returns
// app.New's error; a boot that succeeds is closed. stateDir is shared between
// boots of one store so they see one key and swap slot.
func bootOnDisk(t *testing.T, path, stateDir string, wrap ...func(module.Module) module.Module) error {
	t.Helper()
	edit := func(c *config.Config) { c.DatabasePath, c.RootDir = path, filepath.Dir(path) }
	mod, opts, _ := composedOptions(t, false, stateDir, edit, nil)
	opts.StorageFS = storage.FS{}
	// A refused or merely closed boot must not leave the process defaults taken
	// for the next test (bootAt's reason).
	opts.ProcessDefaults = false
	var m module.Module = mod
	for _, w := range wrap {
		m = w(m)
	}
	a, err := app.New(t.Context(), opts, m)
	if err == nil {
		require.NoError(t, a.Close(t.Context()))
	}
	Release(mod)
	return err
}

// refusedByFloor asserts err is the floor's refusal as the process reports it
// (I-floor-1): *storage.ErrUpgradeFloor for books, exit 78, and a last stderr
// line of exactly "install <tag> first". It returns the refusal.
func refusedByFloor(t *testing.T, err error) *storage.ErrUpgradeFloor {
	t.Helper()
	require.Error(t, err, "a store below the floor booted")
	floor, ok := errors.AsType[*storage.ErrUpgradeFloor](err)
	require.True(t, ok, "boot error = %v, want *storage.ErrUpgradeFloor", err)
	assert.Equal(t, storage.Owner("books"), floor.Owner)
	assert.Equal(t, database.LegacyCap, floor.Floor)
	assert.Equal(t, upgradeFloor().InstallFirst, floor.InstallFirst)
	require.Equal(t, kernel.ExitConfig, kernel.ExitCode(err))

	var stderr bytes.Buffer
	code := app.ExitStatus(err, &stderr, func(string) string { return "" }, func(time.Duration) { t.Error("slept without launchd") })
	require.Equal(t, 78, code)
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	require.Equal(t, "install "+upgradeFloor().InstallFirst+" first", lines[len(lines)-1])
	return floor
}

// untouched is what a refused boot must leave: every row of the store, and
// every file beside it (sidecars, checkpoints), as they were.
type untouched struct {
	rows  map[string]string
	files map[string]string
}

func snapshotStore(t *testing.T, path string) untouched {
	t.Helper()
	return untouched{rows: rawKVs(t, path), files: dirImage(t, filepath.Dir(path))}
}

func (u untouched) assertUnchanged(t *testing.T, path string) {
	t.Helper()
	assert.Equal(t, u.rows, rawKVs(t, path), "the refused boot wrote rows")
	assert.Empty(t, imageDiff(u.files, dirImage(t, filepath.Dir(path))), "the refused boot wrote files")
}

// dmDone reports whether the store at path holds a done record for each of
// ids' data migrations.
func dmDone(t *testing.T, path string, ids ...string) map[string]bool {
	t.Helper()
	kv := rawKVs(t, path)
	out := map[string]bool{}
	for _, id := range ids {
		raw, ok := kv[dmStateKey(id)]
		out[id] = ok && strings.Contains(raw, `"state":"done"`)
	}
	return out
}

func floorStepIDs() []string {
	return []string{floorConfigCutoverID, floorIdentityCutoverID, floorTasksCutoverID, floorLogCutoverID, floorIntegrationsCutoverID}
}

// deleteRow removes one row from the closed store at path.
func deleteRow(t *testing.T, path, key string) {
	t.Helper()
	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	require.NoError(t, err)
	require.NoError(t, db.Delete([]byte(key), pebble.Sync))
	require.NoError(t, db.Close())
}

// TestPreR2StoreRefusedByR3 (R3-02 I-floor-1; 09 section 9.2.2): a build with
// books' floor refuses a store that skipped R2 before it checkpoints or writes
// anything: ErrUpgradeFloor naming the release to install first, exit 78, the
// last stderr line "install <tag> first", and the store byte-identical. Four
// stores: the R0-written corpus, books at a version below LegacyCap (and one
// below it by one), and an R2 store whose cutover did not finish (a fresh
// install with M4's record taken away, M7's level step applied).
// The control is the same boot of a store at the floor, which succeeds.
func TestPreR2StoreRefusedByR3(t *testing.T) {
	allSteps := func() []storage.StepRef { return floorRequires() }

	t.Run("the R0 corpus", func(t *testing.T) {
		work := copyCorpus(t, r0Corpus(t))
		before := snapshotStore(t, storeDB(work))
		err := bootAt(t, work, func(o *app.Options) { o.Env = bootEnv(absEnv()...) })
		floor := refusedByFloor(t, err)
		assert.Equal(t, allSteps(), floor.Missing, "no cutover step is done in an R0 store")
		assert.LessOrEqual(t, floor.Have, database.LegacyCap)
		before.assertUnchanged(t, storeDB(work))
	})

	for _, version := range []int{40, database.LegacyCap - 1, database.LegacyCap} {
		t.Run(fmt.Sprintf("books at version %d", version), func(t *testing.T) {
			path := filepath.Join(tempDir(t), "gotham.pebble")
			headStoreAtVersion(t, path, version, nil)
			before := snapshotStore(t, path)
			err := bootOnDisk(t, path, "")
			floor := refusedByFloor(t, err)
			assert.Equal(t, version, floor.Have, "books' applied version")
			assert.Equal(t, allSteps(), floor.Missing)
			before.assertUnchanged(t, path)
		})
	}

	t.Run("an R2 store whose M4 did not finish", func(t *testing.T) {
		path := filepath.Join(tempDir(t), "gotham.pebble")
		state := t.TempDir()
		require.NoError(t, bootOnDisk(t, path, state), "the fresh install")
		require.Equal(t, map[string]bool{
			floorConfigCutoverID: true, floorIdentityCutoverID: true, floorTasksCutoverID: true,
			floorLogCutoverID: true, floorIntegrationsCutoverID: true,
		}, dmDone(t, path, floorStepIDs()...), "the fresh install must have recorded every required step")

		deleteRow(t, path, dmStateKey(floorTasksCutoverID))
		before := snapshotStore(t, path)
		err := bootOnDisk(t, path, state)
		floor := refusedByFloor(t, err)
		assert.GreaterOrEqual(t, floor.Have, database.LegacyCap, "the version is fine; only M4 is missing")
		assert.Equal(t, []storage.StepRef{{Owner: "books", DataMigration: floorTasksCutoverID}}, floor.Missing)
		before.assertUnchanged(t, path)
	})

	t.Run("control: a store at the floor boots", func(t *testing.T) {
		path := filepath.Join(tempDir(t), "gotham.pebble")
		state := t.TempDir()
		require.NoError(t, bootOnDisk(t, path, state))
		require.NoError(t, bootOnDisk(t, path, state), "a store that passed the floor once must pass it again")
	})
}

// TestFreshInstallPostFloorBootsTwice (R3-02 I-floor-2; IMPL-7): an empty
// store is exempt from the floor, and its one fresh-install batch records
// books' versions up to the floor as applied and M2-M6 as done whether or not
// the build registers the steps, so the second boot (now a non-empty store)
// passes. The second build here is the pre-flip module, which registers none of
// the cutover steps: the records come from the floor's Requires, not from the
// steps.
func TestFreshInstallPostFloorBootsTwice(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap []func(module.Module) module.Module
	}{
		{"the flipped build", nil},
		{"a build that registers none of M2-M6", []func(module.Module) module.Module{func(m module.Module) module.Module { return preFlip{m} }}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(tempDir(t), "gotham.pebble")
			state := t.TempDir()
			require.NoError(t, bootOnDisk(t, path, state, tc.wrap...), "first boot (fresh install)")
			assert.Equal(t, map[string]bool{
				floorConfigCutoverID: true, floorIdentityCutoverID: true, floorTasksCutoverID: true,
				floorLogCutoverID: true, floorIntegrationsCutoverID: true,
			}, dmDone(t, path, floorStepIDs()...))
			require.NoError(t, bootOnDisk(t, path, state, tc.wrap...), "second boot")
			require.NoError(t, bootOnDisk(t, path, state, tc.wrap...), "third boot")
		})
	}
}

// TestFactoryResetPostFloorBoots (R3-02 I-floor-2; 05-storage 5S.3.15; DATA7-7):
// after a factory reset on a build with the floor, the next boot is a fresh
// install whose one batch records books' versions up to the floor and every
// M2-M6 step as done, so it passes the floor at that boot and at every later
// one. The reset swaps the store for an empty one and the old one is set aside;
// nothing of the floor is carried in the old store.
func TestFactoryResetPostFloorBoots(t *testing.T) {
	h := newOfflineABO(t)
	require.Equal(t, map[string]bool{
		floorConfigCutoverID: true, floorIdentityCutoverID: true, floorTasksCutoverID: true,
		floorLogCutoverID: true, floorIntegrationsCutoverID: true,
	}, dmDone(t, h.path, floorStepIDs()...), "the first install must be at the floor before the reset")

	out, err := h.run("storage reset", "--kind", "factory", "--confirm", "RESET")
	require.NoError(t, err, out)
	st := h.slot()
	require.NotNil(t, st.Pending)
	id := st.Pending.ID

	h.boot() // applies the reset: the old store is set aside and this boot is a fresh install
	require.False(t, h.slot().Busy(), "slot after the reset boot = %+v", h.slot())
	assert.True(t, exists(restorejournal.SetAsideDest(h.state, h.path, id, h.path)), "the old store was not set aside: the reset did not swap")
	assert.Equal(t, map[string]bool{
		floorConfigCutoverID: true, floorIdentityCutoverID: true, floorTasksCutoverID: true,
		floorLogCutoverID: true, floorIntegrationsCutoverID: true,
	}, dmDone(t, h.path, floorStepIDs()...), "the reset's fresh install did not record the floor's steps")

	h.boot() // the boot after: a non-empty store, the floor is checked and passes
	h.boot()
}

// TestRestoreBelowUpgradeFloorRefusedAtStaging (R3-02 I-floor-3; 05-backup
// 5B.3.4 step 4): books' restore stager opens an archive's store with this
// build's spec before it stages (VerifyStoreOpensWith over StandaloneOptions),
// so an archive of a store below the floor is refused now with
// ErrUpgradeFloor naming the release to install first: the swap slot stays
// empty, nothing is extracted and nothing restarts. The same stager stages an
// archive of a store at the floor. (Core's own check of a manifest-bearing
// archive is core/backup/compat_test.go's TestRestoreBelowUpgradeFloorRefusedAtStaging.)
func TestRestoreBelowUpgradeFloorRefusedAtStaging(t *testing.T) {
	ctx := context.Background()
	archive := func(t *testing.T, h *offlineABO, store string) *backup.BackupInfo {
		t.Helper()
		info, err := backup.CreateBackup(store, "pebble", backup.BackupConfig{BackupDir: backup.ResolveDir("", h.path), MaxBackups: 5})
		require.NoError(t, err)
		return info
	}

	for name, version := range map[string]int{"a pre-R2 store (books at LegacyCap, no cutover step done)": database.LegacyCap, "books below LegacyCap": 40} {
		t.Run(name, func(t *testing.T) {
			h := newOfflineABO(t)
			src := filepath.Join(tempDir(t), "joker.pebble")
			headStoreAtVersion(t, src, version, nil)
			info := archive(t, h, src)
			liveBefore := snapshotStore(t, h.path)

			restarts := 0
			_, err := h.liveStager(&restarts).StageRestore(ctx, filepath.Dir(info.Path), info.Filename, true, "user:alfred", nil)
			floor, ok := errors.AsType[*storage.ErrUpgradeFloor](err)
			require.True(t, ok, "StageRestore = %v, want *storage.ErrUpgradeFloor", err)
			assert.Equal(t, storage.Owner("books"), floor.Owner)
			assert.Equal(t, database.LegacyCap, floor.Floor)
			assert.Equal(t, upgradeFloor().InstallFirst, floor.InstallFirst)
			assert.True(t, strings.HasSuffix(err.Error(), "install "+upgradeFloor().InstallFirst+" first"), "%v", err)
			require.False(t, h.slot().Busy(), "a refused restore was staged: %+v", h.slot())
			assert.Empty(t, h.stagingLeft(), "the refused restore left its extraction")
			assert.Zero(t, restarts)
			liveBefore.assertUnchanged(t, h.path)
		})
	}

	t.Run("control: an archive of a store at the floor stages", func(t *testing.T) {
		h := newOfflineABO(t)
		info := archive(t, h, h.path)
		restarts := 0
		staged, err := h.liveStager(&restarts).StageRestore(ctx, filepath.Dir(info.Path), info.Filename, true, "user:alfred", nil)
		require.NoError(t, err)
		require.Equal(t, staged.ID, h.slot().Pending.ID)
	})
}

// noFloor is the R2 build: the flipped module with books' floor undeclared (R2
// ships the cutover steps and no floor). It is the build that leaves a store
// behind for the R3 build to refuse or accept.
type noFloor struct{ module.Module }

func (n noFloor) Manifest() module.Manifest {
	m := n.Module.Manifest()
	m.Storage.UpgradeFloor = nil
	return m
}

// putRows writes rows into the closed store at path.
func putRows(t *testing.T, path string, rows map[string]string) {
	t.Helper()
	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	require.NoError(t, err)
	for k, v := range rows {
		require.NoError(t, db.Set([]byte(k), []byte(v), pebble.Sync))
	}
	require.NoError(t, db.Close())
}

// freshInstallRecords boots a fresh install of this build in its own directory
// and returns the done records it wrote for the steps in ids, keyed by row key.
func freshInstallRecords(t *testing.T, ids ...string) map[string]string {
	t.Helper()
	src := filepath.Join(tempDir(t), "src", "gotham.pebble")
	require.NoError(t, bootOnDisk(t, src, t.TempDir()))
	kv := rawKVs(t, src)
	out := map[string]string{}
	for _, id := range ids {
		raw, ok := kv[dmStateKey(id)]
		require.True(t, ok, "the fresh install wrote no record for %s", id)
		out[dmStateKey(id)] = raw
	}
	return out
}

// lowerBooksVersion drops books' applied-version records above version from the
// closed store at path, so books' version (read from them) is version and
// nothing else about the store changes: the install marker and every step
// record stay. db_version is rewritten with the records, as headStoreAtVersion
// does: books' version source reads both.
func lowerBooksVersion(t *testing.T, path string, version int) {
	t.Helper()
	const recordPrefix = "preference:migration_"
	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	require.NoError(t, err)
	it, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte(recordPrefix), UpperBound: []byte("preference:migration`")})
	require.NoError(t, err)
	b := db.NewBatch()
	for ok := it.First(); ok; ok = it.Next() {
		n, err := strconv.Atoi(strings.TrimPrefix(string(it.Key()), recordPrefix))
		require.NoError(t, err, "record key %q", it.Key())
		if n > version {
			require.NoError(t, b.Delete(append([]byte(nil), it.Key()...), nil))
		}
	}
	require.NoError(t, it.Close())
	key := []byte("preference:db_version")
	raw, closer, err := db.Get(key)
	require.NoError(t, err)
	var pref database.UserPreference
	require.NoError(t, json.Unmarshal(raw, &pref))
	require.NoError(t, closer.Close())
	dv, err := json.Marshal(database.DatabaseVersion{Version: version, UpdatedAt: time.Now().UTC()})
	require.NoError(t, err)
	v := string(dv)
	pref.Value = &v
	row, err := json.Marshal(pref)
	require.NoError(t, err)
	require.NoError(t, b.Set(key, row, nil))
	require.NoError(t, b.Commit(pebble.Sync))
	require.NoError(t, db.Close())
}

// TestStoreLeftByR2PassesTheFloor (R3-02 I-floor-1, the converse): the floor
// must not refuse a store R2 would leave behind. The R0-written corpus goes
// through the R2 build (the flipped module, floor undeclared); the R3 build,
// with the floor, then boots it, and boots it again.
//
// At the base this was written against only M2 is live: M3-M6 are F4-08's and
// F5-18's flips, and the R2 build records none of them. For each step the R2
// build did not record, the test writes the record a fresh install writes in its
// place and logs it, so the converse is proved now for what is live and
// becomes the real thing, with no top-up, when the last of those flips is on
// main.
func TestStoreLeftByR2PassesTheFloor(t *testing.T) {
	work := copyCorpus(t, r0Corpus(t))
	repointCorpusPaths(t, work)
	env := func(o *app.Options) { o.Env = bootEnv(absEnv()...) }

	require.NoError(t, bootAt(t, work, env, func(m module.Module) module.Module { return noFloor{m} }), "the R2 build")
	done := dmDone(t, storeDB(work), floorStepIDs()...)
	require.True(t, done[floorConfigCutoverID], "the R2 build did not run M2: this is not an R2 store")

	var missing []string
	for _, id := range floorStepIDs() {
		if !done[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		t.Logf("steps the R2 build at this base did not record (stood in by a fresh install's record): %v", missing)
		putRows(t, storeDB(work), freshInstallRecords(t, missing...))
	}
	require.Empty(t, func() []string {
		var left []string
		for id, ok := range dmDone(t, storeDB(work), floorStepIDs()...) {
			if !ok {
				left = append(left, id)
			}
		}
		return left
	}(), "a required step is still not recorded")

	require.NoError(t, bootAt(t, work, env), "the R3 build refused a store the R2 build left behind")
	require.NoError(t, bootAt(t, work, env), "the second R3 boot")
}

// TestUpgradeFloorVersionBoundary pins the version half of the floor apart from
// the steps half. Both cases are a fresh install (install marker, every step
// record) with only books' applied version lowered, so nothing but the version
// can refuse: one below LegacyCap is refused with nothing missing; exactly
// LegacyCap boots.
func TestUpgradeFloorVersionBoundary(t *testing.T) {
	for _, tc := range []struct {
		version int
		refused bool
	}{
		{database.LegacyCap - 1, true},
		{database.LegacyCap, false},
	} {
		t.Run(fmt.Sprintf("books at version %d with every step recorded", tc.version), func(t *testing.T) {
			path := filepath.Join(tempDir(t), "gotham.pebble")
			state := t.TempDir()
			require.NoError(t, bootOnDisk(t, path, state), "the fresh install")
			lowerBooksVersion(t, path, tc.version)
			before := snapshotStore(t, path)
			err := bootOnDisk(t, path, state)
			if !tc.refused {
				require.NoError(t, err, "a store at exactly the floor, every step done, was refused")
				return
			}
			floor := refusedByFloor(t, err)
			assert.Equal(t, tc.version, floor.Have)
			assert.Empty(t, floor.Missing, "every step was recorded: only the version can refuse")
			before.assertUnchanged(t, path)
		})
	}
}
