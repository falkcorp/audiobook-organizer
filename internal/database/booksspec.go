// file: internal/database/booksspec.go
// version: 1.0.2
// guid: 8d814793-7285-4757-ae8a-aabc89f7a006
// last-edited: 2026-11-09

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// Books' storage declaration (05-storage §5S.9.1-§5S.9.3).
//
// From release R1 core storage opens the one store, and books declares here
// what core needs to run it: its claims (booksclaims.go), its HEAD-style
// migrations 1..LegacyCap as LegacyMigrations, every later books step as a
// versioned Migration numbered from LegacyCap+1, the version source that
// keeps HEAD's migration-version rows books' bookkeeping, and the legacy
// guard projection that keeps HEAD's storage-format stamp, marker and
// sidecar meaning what HEAD reads them as.
//
// The spec is built fresh on every call: the bookkeeping adapter keeps
// per-batch state (see booksBookkeeping), and one store's boot must never
// share it with another's.

// BooksSpec is books' storage.Spec at release R1. Every migration step,
// legacy or versioned, calls ran before it runs (nil: none). An opener
// passes ran to learn that a step wrote the store in this boot: the steps
// write through their own step stores and batches, so nothing the
// opener's store counts sees them, and the HNSW clean-stop marker the
// opener took before them no longer vouches (adoptCleanStop). Books'
// manifest and OpenStandalone pass one.
func BooksSpec(ran func()) storage.Spec {
	bk := &booksBookkeeping{}
	return storage.Spec{
		Claims:        BooksClaims(),
		Legacy:        observeLegacySteps(booksLegacySteps(), ran),
		Migrations:    observeMigrations(booksMigrations(), ran),
		VersionSource: bk,
		LegacyGuard: &storage.LegacyGuardProjection{
			StampKey:        "preference:" + storageFormatPreferenceKey,
			MarkerKey:       "preference:" + storageMigrationPreferenceKey,
			SidecarSuffix:   StorageFormatSidecarSuffix,
			LegacyMaxFormat: legacyMaxFormat,
			Stage:           bk.stageProjection,
		},
	}
}

// observeLegacySteps makes each step call ran before its Up (nil: as is).
func observeLegacySteps(steps []storage.LegacyMigration, ran func()) []storage.LegacyMigration {
	if ran == nil {
		return steps
	}
	for i := range steps {
		up := steps[i].Up
		steps[i].Up = func(ctx context.Context, r storage.LegacyRange) error {
			ran()
			return up(ctx, r)
		}
	}
	return steps
}

// observeMigrations makes each step call ran before its Up (nil: as is).
func observeMigrations(steps []storage.Migration, ran func()) []storage.Migration {
	if ran == nil {
		return steps
	}
	for i := range steps {
		up := steps[i].Up
		steps[i].Up = func(ctx context.Context, tx *storage.MigrationTx) error {
			ran()
			return up(ctx, tx)
		}
	}
	return steps
}

// legacyMaxFormat is the highest SupportedStorageFormat any release before
// the storage flip shipped (05-storage §5S.3.7): every one shipped 1. A
// store whose books level is raised projects legacyMaxFormat+1, which every
// such release refuses.
const legacyMaxFormat = 1

// booksLegacySteps wraps HEAD's migrations 1..LegacyCap as core
// LegacyMigrations. Each runs HEAD's MigrationFunc over a step store built on
// the LegacyRange core hands the step (books' own claims, §5S.3.4): its store
// methods commit their own batches, so a step is not atomic, which is why
// every Up is idempotent (TestMigrationUpFunctionsAreIdempotent,
// TestLegacyMigrationsReplayIdempotent).
func booksLegacySteps() []storage.LegacyMigration {
	out := make([]storage.LegacyMigration, 0, len(migrations))
	for _, m := range migrations {
		out = append(out, storage.LegacyMigration{Version: m.Version, Name: m.Description, Up: legacyStepUp(m)})
	}
	return out
}

// legacyStepUp runs one HEAD migration over the step's LegacyRange.
func legacyStepUp(m Migration) func(ctx context.Context, r storage.LegacyRange) error {
	return func(ctx context.Context, r storage.LegacyRange) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		s, err := newStepStore(r)
		if err != nil {
			return fmt.Errorf("migration %d: %w", m.Version, err)
		}
		if err := m.Up(s); err != nil {
			return fmt.Errorf("migration %d failed: %w", m.Version, err)
		}
		return nil
	}
}

// booksMigrations are books' versioned steps, numbered from LegacyCap+1
// (DATA7-7): HEAD's two open-time data writers that §5S.9.3 makes books
// Migrations. Both are Additive: HEAD itself performs each at open, so a
// build that does not know the step reads and writes the result unchanged.
func booksMigrations() []storage.Migration {
	return []storage.Migration{
		{
			Version: LegacyCap + 1,
			Name:    "Rename the legacy library: keys and counter to import_path:",
			Compat:  storage.Additive,
			Up:      migrateImportPathKeysStep,
		},
		{
			Version: LegacyCap + 2,
			Name:    "Backfill the opchange_by_list journal index",
			Compat:  storage.Additive,
			Up:      backfillOpChangeListIndexStep,
		},
	}
}

// migrateImportPathKeysStep is HEAD's migrateImportPathKeys as a versioned
// step: every library: key moves to its import_path: name and counter:library
// to counter:import_path (an existing import_path counter wins), in the
// step's one batch. Migration 007 still calls the store method for stores
// that ran it before this step existed; both are idempotent.
func migrateImportPathKeysStep(ctx context.Context, tx *storage.MigrationTx) error {
	r := tx.Legacy()
	var legacy []storage.KV
	for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte("library:"), End: []byte("library;")}) {
		if err != nil {
			return fmt.Errorf("scan legacy library: keys: %w", err)
		}
		legacy = append(legacy, kv.Clone())
	}
	for _, kv := range legacy {
		newKey, ok := importPathKeyFor(string(kv.Key))
		if !ok {
			continue
		}
		if err := r.Set(ctx, []byte(newKey), kv.Value); err != nil {
			return fmt.Errorf("write migrated key %s: %w", newKey, err)
		}
		if err := r.Delete(ctx, kv.Key); err != nil {
			return fmt.Errorf("delete legacy key %s: %w", kv.Key, err)
		}
	}
	old, err := r.Get(ctx, []byte("counter:library"))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("read legacy library counter: %w", err)
	}
	switch _, err := r.Get(ctx, []byte("counter:import_path")); {
	case errors.Is(err, storage.ErrNotFound):
		if err := r.Set(ctx, []byte("counter:import_path"), old); err != nil {
			return fmt.Errorf("migrate import path counter: %w", err)
		}
	case err != nil:
		return fmt.Errorf("read import path counter: %w", err)
	}
	if err := r.Delete(ctx, []byte("counter:library")); err != nil {
		return fmt.Errorf("remove legacy library counter: %w", err)
	}
	return nil
}

// importPathKeyFor is the import_path: name of a legacy library: key, and
// false when the key has no other name.
func importPathKeyFor(oldKey string) (string, bool) {
	newKey := strings.Replace(oldKey, "library:path:", "import_path:path:", 1)
	if newKey == oldKey {
		newKey = strings.Replace(oldKey, "library:", "import_path:", 1)
	}
	return newKey, newKey != oldKey
}

// backfillOpChangeListIndexStep is HEAD's open-time list-index backfill as a
// versioned step: every opchange_by_list: entry a journal row lacks, then the
// done key, in the step's one batch. A store whose done key is set (every
// store a HEAD build has opened since the index landed) reads that one key.
// A journal too large for one step's batch fails it with
// ErrMigrationTooLarge, the documented signal to make the step a data
// migration; no store HEAD has opened can reach it.
func backfillOpChangeListIndexStep(ctx context.Context, tx *storage.MigrationTx) error {
	r := tx.Legacy()
	switch _, err := r.Get(ctx, []byte(opChangeByListBackfillKey)); {
	case err == nil:
		return nil
	case !errors.Is(err, storage.ErrNotFound):
		return fmt.Errorf("read %s: %w", opChangeByListBackfillKey, err)
	}
	var missing [][]byte
	rows := 0
	for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte(opChangeKeyPrefix), End: []byte("opchange;")}) {
		if err != nil {
			return fmt.Errorf("scan the journal: %w", err)
		}
		var c journalScanRow
		if json.Unmarshal(kv.Value, &c) != nil {
			continue // a row that does not decode is skipped, as HEAD's scan did
		}
		rows++
		kind, id, ok := opChangeListRef(&OperationChange{ChangeType: c.ChangeType, FieldName: c.FieldName})
		if !ok || !opChangeListIndexable(id) {
			continue
		}
		key := append(opChangeByListListPrefix(kind, id), kv.Key[len(opChangeKeyPrefix):]...)
		switch _, err := r.Get(ctx, key); {
		case err == nil:
			continue
		case !errors.Is(err, storage.ErrNotFound):
			return fmt.Errorf("read list entry: %w", err)
		}
		missing = append(missing, key)
	}
	for _, key := range missing {
		if err := r.Set(ctx, key, nil); err != nil {
			return fmt.Errorf("stage opchange_by_list entry: %w", err)
		}
	}
	if err := r.Set(ctx, []byte(opChangeByListBackfillKey), []byte("1")); err != nil {
		return fmt.Errorf("stage list index backfill marker: %w", err)
	}
	if rows > 0 {
		tx.Log().Info("journal list index backfilled", "rows", rows, "entries", len(missing))
	}
	return nil
}

// booksBookkeeping is books' storage.VersionSource over HEAD's
// migration-version rows (the db_version and migration_<n> preferences,
// §5S.9.1's ABO-migration table) and its LegacyGuardProjection.Stage over
// HEAD's storage_format stamp and storage_migration marker preferences.
//
// Rows are written in HEAD's format: a UserPreference whose value is the
// DatabaseVersion or MigrationRecord JSON, the preference keeping its ID on
// a rewrite and taking a new one from counter:preference when absent
// (DATA2-22). Core stages several records into ONE non-indexed batch (a
// fresh install stages every step's record; a level step stages its record
// and the projection), and a non-indexed batch cannot read its own writes,
// so the IDs and the counter value staged into a batch are kept here for that
// batch. The state is keyed by the batch itself and holds a reference to it,
// so no later batch can be mistaken for it, and a new batch starts from the
// store's committed rows. Core boots are sequential; mu makes a misuse safe
// rather than racy.
type booksBookkeeping struct {
	mu       sync.Mutex
	b        *storage.Batch // the batch the cached state describes
	nextPref int            // the next preference ID; 0 = not read for b yet
	ids      map[string]int // preference key -> ID staged or read for b
}

var _ storage.VersionSource = (*booksBookkeeping)(nil)

// migrationRecordRow is HEAD's MigrationRecord plus the fields core adds
// (HEAD ignores unknown JSON fields). A HEAD record has no compat, which
// counts as Breaking (§5S.3.5).
type migrationRecordRow struct {
	Version     int       `json:"version"`
	Description string    `json:"description"`
	AppliedAt   time.Time `json:"applied_at"`
	Compat      string    `json:"compat,omitempty"`
	RaisesLevel int       `json:"raises_level,omitempty"`
	Build       string    `json:"build,omitempty"`
}

// migrationRecordPrefix is the key prefix of HEAD's applied-migration rows.
const migrationRecordPrefix = "preference:migration_"

// Applied reads books' applied version and records. The version is HEAD's
// db_version, advanced past every contiguous record above it: a record
// without its version bump means the step's Up completed and the bump was
// lost, which HEAD's runner treats as applied (DB-02), and so does this one.
// An absent db_version is version 0 (a store no migration ran on); a row that
// does not decode is an error, never 0 (§5S.3.5 "Unreadable version").
func (k *booksBookkeeping) Applied(ctx context.Context, r storage.LegacyRange) (int, map[int]storage.AppliedStep, error) {
	version := 0
	raw, err := r.Get(ctx, []byte("preference:"+dbVersionPreferenceKey))
	switch {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return 0, nil, fmt.Errorf("read db_version: %w", err)
	default:
		var dv DatabaseVersion
		if err := decodePreferenceValue(raw, &dv); err != nil {
			return 0, nil, fmt.Errorf("db_version: %w", err)
		}
		if dv.Version < 0 {
			return 0, nil, fmt.Errorf("db_version is %d", dv.Version)
		}
		version = dv.Version
	}
	records := map[int]storage.AppliedStep{}
	prefix := []byte(migrationRecordPrefix)
	for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: prefix, End: prefixUpperBound(prefix)}) {
		if err != nil {
			return 0, nil, fmt.Errorf("scan migration records: %w", err)
		}
		n, err := strconv.Atoi(string(kv.Key[len(prefix):]))
		if err != nil || n < 1 {
			return 0, nil, fmt.Errorf("migration record key %q does not name a version", kv.Key)
		}
		var rec migrationRecordRow
		if err := decodePreferenceValue(kv.Value, &rec); err != nil {
			return 0, nil, fmt.Errorf("migration record %d: %w", n, err)
		}
		step := storage.AppliedStep{Version: n, Name: rec.Description, RaisesLevel: rec.RaisesLevel, AppliedAt: rec.AppliedAt, Build: rec.Build}
		if rec.Compat != "" {
			switch rec.Compat {
			case storage.Additive.String():
				step.Compat = storage.Additive
			case storage.Breaking.String():
				step.Compat = storage.Breaking
			default:
				return 0, nil, fmt.Errorf("migration record %d has compat %q", n, rec.Compat)
			}
			step.HasCompat = true
		}
		records[n] = step
	}
	for {
		if _, ok := records[version+1]; !ok {
			break
		}
		version++
	}
	return version, records, nil
}

// Stage stages step's applied record and raises db_version to it, into b
// through r (bound to b by the runner).
func (k *booksBookkeeping) Stage(ctx context.Context, r storage.LegacyRange, b *storage.Batch, step storage.AppliedStep) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	rec := migrationRecordRow{Version: step.Version, Description: step.Name, AppliedAt: step.AppliedAt,
		RaisesLevel: step.RaisesLevel, Build: step.Build}
	if step.HasCompat {
		rec.Compat = step.Compat.String()
	}
	recJSON, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode migration record %d: %w", step.Version, err)
	}
	if err := k.setPreference(ctx, r, b, migrationRecordKey(step.Version), string(recJSON), step.AppliedAt); err != nil {
		return err
	}
	dv, err := json.Marshal(DatabaseVersion{Version: step.Version, UpdatedAt: step.AppliedAt})
	if err != nil {
		return fmt.Errorf("encode db_version: %w", err)
	}
	return k.setPreference(ctx, r, b, dbVersionPreferenceKey, string(dv), step.AppliedAt)
}

// storageMigrationMarker is the value of the storage_migration marker core
// sets while a books level step's conversion is unfinished. HEAD refuses to
// serve a store that carries the row whatever it holds.
type storageMigrationMarker struct {
	Projection int       `json:"projection"`
	At         time.Time `json:"at"`
}

// stageProjection is the LegacyGuardProjection's Stage: the storage_format
// stamp rewritten to value (keeping its preference ID; an absent stamp takes
// one from counter:preference), and the storage_migration marker set or
// deleted, into b through r.
func (k *booksBookkeeping) stageProjection(ctx context.Context, r storage.LegacyRange, b *storage.Batch, value int, marker bool) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now().UTC()
	dv, err := json.Marshal(DatabaseVersion{Version: value, UpdatedAt: now})
	if err != nil {
		return fmt.Errorf("encode storage format stamp: %w", err)
	}
	if err := k.setPreference(ctx, r, b, storageFormatPreferenceKey, string(dv), now); err != nil {
		return err
	}
	if !marker {
		if err := b.In(r).Delete([]byte("preference:" + storageMigrationPreferenceKey)); err != nil {
			return fmt.Errorf("stage storage migration marker delete: %w", err)
		}
		return nil
	}
	mk, err := json.Marshal(storageMigrationMarker{Projection: value, At: now})
	if err != nil {
		return fmt.Errorf("encode storage migration marker: %w", err)
	}
	return k.setPreference(ctx, r, b, storageMigrationPreferenceKey, string(mk), now)
}

// setPreference stages preference key = value in HEAD's row format.
// Caller holds k.mu.
func (k *booksBookkeeping) setPreference(ctx context.Context, r storage.LegacyRange, b *storage.Batch, key, value string, at time.Time) error {
	id, err := k.preferenceID(ctx, r, b, key)
	if err != nil {
		return err
	}
	row, err := json.Marshal(UserPreference{ID: id, Key: key, Value: &value, UpdatedAt: at})
	if err != nil {
		return fmt.Errorf("encode preference %q: %w", key, err)
	}
	if err := b.In(r).Set([]byte("preference:"+key), row); err != nil {
		return fmt.Errorf("stage preference %q: %w", key, err)
	}
	return nil
}

// preferenceID is key's preference ID for b: the one already staged or read
// for b, else the stored row's, else the next ID from counter:preference,
// whose new value is staged into b. Caller holds k.mu.
func (k *booksBookkeeping) preferenceID(ctx context.Context, r storage.LegacyRange, b *storage.Batch, key string) (int, error) {
	if k.b != b {
		k.b, k.nextPref, k.ids = b, 0, map[string]int{}
	}
	if id, ok := k.ids[key]; ok {
		return id, nil
	}
	raw, err := r.Get(ctx, []byte("preference:"+key))
	switch {
	case err == nil:
		var pref UserPreference
		if err := json.Unmarshal(raw, &pref); err != nil {
			return 0, fmt.Errorf("decode preference %q: %w", key, err)
		}
		k.ids[key] = pref.ID
		return pref.ID, nil
	case !errors.Is(err, storage.ErrNotFound):
		return 0, fmt.Errorf("read preference %q: %w", key, err)
	}
	if k.nextPref == 0 {
		n, err := readCounter(ctx, r, "preference")
		if err != nil {
			return 0, err
		}
		k.nextPref = n
	}
	id := k.nextPref
	k.nextPref++
	if err := b.In(r).Set([]byte("counter:preference"), []byte(strconv.Itoa(k.nextPref))); err != nil {
		return 0, fmt.Errorf("stage preference counter: %w", err)
	}
	k.ids[key] = id
	return id, nil
}

// readCounter is counter:<name>'s value: the next ID it hands out. An absent
// counter is 1 (05-storage §5S.9.3: no seeding write); one that does not
// parse is an error.
func readCounter(ctx context.Context, r storage.LegacyRange, name string) (int, error) {
	raw, err := r.Get(ctx, []byte("counter:"+name))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return 1, nil
	case err != nil:
		return 0, fmt.Errorf("read counter %s: %w", name, err)
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, fmt.Errorf("counter %s holds %q: %w", name, raw, err)
	}
	return n, nil
}

// decodePreferenceValue decodes a preference row and then its value as v.
func decodePreferenceValue(raw []byte, v any) error {
	var pref UserPreference
	if err := json.Unmarshal(raw, &pref); err != nil {
		return fmt.Errorf("decode preference row: %w", err)
	}
	if pref.Value == nil || *pref.Value == "" {
		return errors.New("preference row has no value")
	}
	if err := json.Unmarshal([]byte(*pref.Value), v); err != nil {
		return fmt.Errorf("decode preference value: %w", err)
	}
	return nil
}
