// file: core/kernel/dryrun.go
// version: 1.0.1
// guid: f63ab3a5-e55e-4498-a2d2-ffd73ac392be
// last-edited: 2026-11-10

package kernel

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/internal/storagemigrate"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// DryRunOptions configure DryRun, `storage migrate --dry-run` (05-storage
// §5S.3.6 "Dry run"; 09 §9.2.3 "Dry run"). Name coined by F3-04.
type DryRunOptions struct {
	// ID limits the run to one data migration and the data migrations it
	// runs after (--id).
	ID string
	// FromArchive copies the store from an archive through
	// Config.ArchiveExtract instead of the live store (--from-archive).
	FromArchive string
	// Estimate counts over the live store, opened ReadOnly, with every
	// write discarded and no scratch copy (--estimate): the old in-batch
	// discard mode, labelled an estimate.
	Estimate bool
	// Migrations are the build's data migrations; Rewrite its M8. Until
	// F2-16 and F3-07 wire the boot's list, the command passes none.
	Migrations []datamigrate.DataMigration
	Rewrite    *storage.RewriteStep
	// Sealer builds the throw-away keyring's quarantine sealer in the
	// scratch directory (05-config §2.4 step 1: secrets.Open with a KeyFile
	// there; F3-07 wires it). nil: a sensitive row fails its step.
	Sealer func(scratch string) (storage.QuarantineSealer, error)
	// Bind receives the scratch store between Prepare and Migrate, so a
	// cutover step's importer binds the scratch store's handles (the batch
	// it stages into is the scratch store's), never the live boot's. nil: the
	// steps reach no handle of their own.
	Bind func(db *storage.DB) error
	// FirstN bounds the keys listed per outcome; 0 = 10.
	FirstN int
}

// DryRun is `storage migrate --dry-run`: it refuses while another process
// holds the store (ErrStoreHeld) or a swap is staged or in flight; opens the
// live store ReadOnly (no WAL flush, no ratchet, no write) and copies it into
// a new directory of the checkpoint area's scratch/, or extracts
// --from-archive there; opens the copy as a full store whose checkpoints and
// sidecars stay in the scratch directory; runs the cutover preflight first,
// then every pending step in DAG order (Verify included, M8 copying into a
// second scratch directory and swapping nothing); and reports blockers,
// counts per outcome, held items, would-quarantine rows, bytes written, the
// first keys per outcome and the duration. The scratch is deleted whatever
// the outcome. The live directory and the state directory's keyring are
// never written. A blocked cutover returns the report with its
// *storage.CutoverBlockedError (ExitCode 78). Name coined by F3-04.
func DryRun(ctx context.Context, c Config, opt DryRunOptions) (rep *storage.DryRunReport, err error) {
	start := time.Now()
	k, err := offlineKernel(c)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, k.closeOffline(ctx)) }()
	if err := k.lockSwap(ctx, false); err != nil {
		return nil, err
	}
	if err := k.refuseBusySlot(ctx); err != nil {
		return nil, err
	}
	opts, err := k.storageOptions(ctx)
	if err != nil {
		return nil, err
	}
	migs, err := dryRunList(opt.Migrations, opt.ID)
	if err != nil {
		return nil, err
	}
	rewrite := opt.Rewrite
	if opt.ID != "" && (rewrite == nil || rewrite.ID != opt.ID) {
		rewrite = nil
	}
	rep = &storage.DryRunReport{Estimate: opt.Estimate, Source: opts.Path}
	if opt.FromArchive != "" {
		rep.Source = opt.FromArchive
	}
	defer func() {
		rep.Took = time.Since(start)
		if err != nil && rep.Err == "" {
			rep.Err = err.Error()
		}
	}()
	first := opt.FirstN
	if first <= 0 {
		first = 10
	}
	fs := bootseam.FS(ctx)
	if opt.Estimate {
		return rep, k.estimate(ctx, opts, migs, first, rep)
	}
	scratch, err := k.dryRunScratch(ctx, opts, opt.FromArchive != "")
	if err != nil {
		return rep, err
	}
	defer func() {
		if rerr := fs.RemoveAll(scratch); rerr != nil {
			err = errors.Join(err, fmt.Errorf("kernel: delete the dry run's scratch %s: %w", scratch, rerr))
		}
	}()
	dbDir := filepath.Join(scratch, "db")
	if err := k.dryRunCopy(ctx, opts, opt.FromArchive, dbDir, rewrite != nil); err != nil {
		return rep, err
	}
	return rep, k.dryRunMigrate(ctx, opts, scratch, dbDir, migs, rewrite, opt, first, rep)
}

// dryRunList is migs limited to id and the data migrations it runs after,
// transitively; all of migs when id is "".
func dryRunList(migs []datamigrate.DataMigration, id string) ([]datamigrate.DataMigration, error) {
	if id == "" {
		return slices.Clone(migs), nil
	}
	byID := map[string]datamigrate.DataMigration{}
	for _, m := range migs {
		byID[m.ID] = m
	}
	want := map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		m, ok := byID[id]
		if !ok {
			return false
		}
		if want[id] {
			return true
		}
		want[id] = true
		for _, a := range m.After {
			if a.DataMigration != "" {
				visit(a.DataMigration)
			}
		}
		return true
	}
	if !visit(id) {
		return nil, nil // M8's ID, or one this build does not register
	}
	var out []datamigrate.DataMigration
	for _, m := range migs {
		if want[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}

// dryRunScratch creates the run's scratch directory in the checkpoint
// area's scratch/ (0700). With no live store to open (an archive), it is
// found from the configured paths alone.
func (k *Kernel) dryRunScratch(ctx context.Context, opts storage.OpenOptions, archive bool) (string, error) {
	if archive {
		return storage.NewDryRunScratchAt(opts.FS, opts.Path, opts.CheckpointDir, time.Now())
	}
	if err := refuseHeldStore(bootseam.FS(ctx), opts.Path); err != nil {
		return "", err
	}
	ro := opts
	ro.ReadOnly, ro.MustExist = true, true
	o, err := storage.Open(ctx, ro)
	if err != nil {
		return "", fmt.Errorf("kernel: dry run: open the live store read-only: %w", err)
	}
	dir, err := o.NewDryRunScratch()
	return dir, errors.Join(err, o.Close())
}

// dryRunCopy fills dbDir: the archive's store through Config.ArchiveExtract,
// or the live store's read-only copy (CopyForDryRun).
func (k *Kernel) dryRunCopy(ctx context.Context, opts storage.OpenOptions, archive, dbDir string, rewrite bool) error {
	if archive != "" {
		if k.cfg.ArchiveExtract == nil {
			return errors.New("kernel: dry run: --from-archive needs the app's archive extractor (Config.ArchiveExtract)")
		}
		if err := mkdirPrivate(bootseam.FS(ctx), dbDir); err != nil {
			return err
		}
		if _, err := k.cfg.ArchiveExtract(ctx, archive, dbDir); err != nil {
			return fmt.Errorf("kernel: dry run: extract %s: %w", archive, err)
		}
		return chmodTree(bootseam.FS(ctx), dbDir)
	}
	if err := refuseHeldStore(bootseam.FS(ctx), opts.Path); err != nil {
		return err
	}
	ro := opts
	ro.ReadOnly, ro.MustExist = true, true
	o, err := storage.Open(ctx, ro)
	if err != nil {
		return fmt.Errorf("kernel: dry run: open the live store read-only: %w", err)
	}
	return errors.Join(o.CopyForDryRun(ctx, dbDir, rewrite), o.Close())
}

// dryRunMigrate opens the scratch copy as a full store and runs the boot's
// P4 over it: the preflight, Prepare (its checkpoint in the scratch
// directory), Migrate, then the report from the runner's stats and the
// scratch store's quarantine.
func (k *Kernel) dryRunMigrate(ctx context.Context, opts storage.OpenOptions, scratch, dbDir string,
	migs []datamigrate.DataMigration, rewrite *storage.RewriteStep, opt DryRunOptions,
	first int, rep *storage.DryRunReport) error {
	var sealer storage.QuarantineSealer
	if opt.Sealer != nil {
		s, err := opt.Sealer(scratch)
		if err != nil {
			return fmt.Errorf("kernel: dry run: the throw-away keyring: %w", err)
		}
		sealer = s
	}
	rec := &dryRunRecorder{first: first, steps: map[string]*storage.DryRunStep{}}
	log := k.coreLog("storage")
	dm, err := NewDataMigrations(DataMigrationsConfig{
		Build: opts.Build, Logger: log, Clock: opts.Clock,
		Quarantine: storage.QuarantineWriter(sealer), LevelStep: LevelStep, Rewrite: rewrite,
	}, opts.Owners, rec.wrap(migs)...)
	if err != nil {
		return err
	}
	if rewrite != nil {
		dir := filepath.Join(scratch, "rewrite")
		dm.rewriteRun = func(ctx context.Context, db *storage.DB) error {
			st, err := restoreswap.RewriteDry(ctx, db, *rewrite, dir)
			rep.Rewrite = &st
			return err
		}
	}
	sopts := opts
	sopts.Path, sopts.CheckpointDir = dbDir, scratch
	sopts.ReadOnly, sopts.MustExist = false, true
	sopts.StateFiles = nil // the state directory's files (the keyring) are never read into the scratch
	sopts.DataSteps = dm.Steps()
	o, err := storage.Open(ctx, sopts)
	if err != nil {
		return fmt.Errorf("kernel: dry run: open the scratch copy: %w", err)
	}
	var cutover []datamigrate.DataMigration
	for _, m := range migs {
		if m.Cutover {
			cutover = append(cutover, m)
		}
	}
	blockers, _, err := cutoverBlockers(ctx, o, cutover)
	if err != nil {
		return errors.Join(err, o.Close())
	}
	if len(blockers) > 0 {
		rep.Blockers = blockers
		return errors.Join(&storage.CutoverBlockedError{Blockers: blockers}, o.Close())
	}
	db, err := o.Prepare(ctx)
	if err != nil {
		return errors.Join(fmt.Errorf("kernel: dry run: prepare the scratch copy: %w", err), o.Close())
	}
	if opt.Bind != nil {
		if err := opt.Bind(db); err != nil {
			return errors.Join(fmt.Errorf("kernel: dry run: bind the scratch store: %w", err), db.Close())
		}
	}
	runErr := db.Migrate(ctx)
	if runErr != nil {
		rep.Err = runErr.Error()
	}
	stats, err := dm.Stats(ctx, db)
	if err == nil {
		err = rec.fill(ctx, db, stats, rep)
	}
	return errors.Join(runErr, err, db.Close())
}

// estimate is --estimate: the preflight and each migration's Step over its
// Source on the live store opened ReadOnly, every write discarded.
func (k *Kernel) estimate(ctx context.Context, opts storage.OpenOptions, migs []datamigrate.DataMigration, first int, rep *storage.DryRunReport) (err error) {
	if err := refuseHeldStore(bootseam.FS(ctx), opts.Path); err != nil {
		return err
	}
	ro := opts
	ro.ReadOnly, ro.MustExist = true, true
	o, err := storage.Open(ctx, ro)
	if err != nil {
		return fmt.Errorf("kernel: dry run estimate: open the live store read-only: %w", err)
	}
	defer func() { err = errors.Join(err, o.Close()) }()
	var cutover []datamigrate.DataMigration
	for _, m := range migs {
		if m.Cutover {
			cutover = append(cutover, m)
		}
	}
	blockers, _, err := cutoverBlockers(ctx, o, cutover)
	if err != nil {
		return err
	}
	rep.Blockers = blockers
	for _, m := range migs {
		start := time.Now()
		sink := &discardSink{step: &storage.DryRunStep{ID: m.ID, State: "estimate", Items: map[string]int64{}, First: map[string][]string{}}, first: first}
		for kv, err := range o.EstimateSource(ctx, m.Owner, m.Source) {
			if err != nil {
				return err
			}
			sink.key = kv.Key
			if err := m.Step(ctx, kv, datamigrate.NewStepWriter(sink, sink)); err != nil {
				sink.step.State = "estimate-failed"
				rep.Err = fmt.Sprintf("%s: %v", m.ID, err)
				break
			}
		}
		sink.step.Took = time.Since(start)
		rep.Steps = append(rep.Steps, *sink.step)
	}
	return nil
}

// dryRunRecorder wraps each migration's Step so the dry run sees the first
// keys per outcome, the held items and the bytes staged; the outcomes still
// reach the runner unchanged, so its counts are a live run's.
type dryRunRecorder struct {
	mu    sync.Mutex
	first int
	steps map[string]*storage.DryRunStep
}

func (r *dryRunRecorder) step(id string) *storage.DryRunStep {
	s := r.steps[id]
	if s == nil {
		s = &storage.DryRunStep{ID: id, Items: map[string]int64{}, First: map[string][]string{}}
		r.steps[id] = s
	}
	return s
}

func (r *dryRunRecorder) wrap(migs []datamigrate.DataMigration) []datamigrate.DataMigration {
	out := slices.Clone(migs)
	for i := range out {
		id, step := out[i].ID, out[i].Step
		out[i].Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			s := &recordingSink{w: w, r: r, id: id, key: kv.Key}
			return step(ctx, kv, datamigrate.NewStepWriter(s, s))
		}
	}
	return out
}

func (r *dryRunRecorder) note(id, outcome string, key []byte, bytes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.step(id)
	s.Bytes += int64(bytes)
	if outcome != "" && len(s.First[outcome]) < r.first {
		s.First[outcome] = append(s.First[outcome], storage.PrintableKey(key))
	}
}

func (r *dryRunRecorder) hold(id string, key []byte, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.step(id)
	s.Held = append(s.Held, storage.DryRunItem{Key: storage.PrintableKey(key), Reason: reason})
	if len(s.First[datamigrate.OutcomeHeld]) < r.first {
		s.First[datamigrate.OutcomeHeld] = append(s.First[datamigrate.OutcomeHeld], storage.PrintableKey(key))
	}
}

// fill completes rep from the runner's stats and the scratch store's
// quarantine records.
func (r *dryRunRecorder) fill(ctx context.Context, db *storage.DB, stats []storagemigrate.Stats, rep *storage.DryRunReport) error {
	q, err := db.Quarantine(ctx, "")
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, st := range stats {
		s := r.step(st.ID)
		s.State, s.Took = st.State, st.Took
		maps.Copy(s.Items, st.Items)
		for _, e := range q {
			if e.Record.Step == st.ID {
				s.Quarantine = append(s.Quarantine, storage.DryRunItem{Key: storage.PrintableKey(e.Record.Key), Reason: e.Record.Reason, Sensitive: e.Record.Sensitive})
			}
		}
		rep.Steps = append(rep.Steps, *s)
	}
	return nil
}

// recordingSink passes every write and outcome to the chunk's StepWriter
// and tells the recorder.
type recordingSink struct {
	w   *datamigrate.StepWriter
	r   *dryRunRecorder
	id  string
	key []byte
}

func (s *recordingSink) Set(key, value []byte) error {
	s.r.note(s.id, "", nil, len(key)+len(value))
	return s.w.Set(key, value)
}

func (s *recordingSink) Delete(key []byte) error {
	s.r.note(s.id, "", nil, len(key))
	return s.w.Delete(key)
}

func (s *recordingSink) SetIfAbsent(key, value []byte) error {
	s.r.note(s.id, "", nil, len(key)+len(value))
	return s.w.SetIfAbsent(key, value)
}

func (s *recordingSink) SetIfUnchanged(key, expect, value []byte) error {
	s.r.note(s.id, "", nil, len(key)+len(value))
	return s.w.SetIfUnchanged(key, expect, value)
}

func (s *recordingSink) DeleteIfUnchanged(key, expect []byte) error {
	s.r.note(s.id, "", nil, len(key))
	return s.w.DeleteIfUnchanged(key, expect)
}

func (s *recordingSink) Hold(key []byte, reason string) {
	s.r.hold(s.id, key, reason)
	s.w.Hold(key, reason)
}

func (s *recordingSink) Count(outcome string) {
	s.r.note(s.id, outcome, s.key, 0)
	s.w.Count(outcome)
}

func (s *recordingSink) Quarantine(key, raw []byte, reason string) error {
	s.r.note(s.id, datamigrate.OutcomeQuarantined, key, 0)
	return s.w.Quarantine(key, raw, reason)
}

func (s *recordingSink) Batch() *storage.Batch { return s.w.Batch() }

// discardSink is --estimate's StepWriter: it counts and discards.
type discardSink struct {
	step  *storage.DryRunStep
	first int
	key   []byte
}

func (d *discardSink) add(outcome string, key []byte) {
	d.step.Items[outcome]++
	if len(d.step.First[outcome]) < d.first {
		d.step.First[outcome] = append(d.step.First[outcome], storage.PrintableKey(key))
	}
}

func (d *discardSink) Set(key, value []byte) error {
	d.step.Bytes += int64(len(key) + len(value))
	return nil
}

func (d *discardSink) Delete(key []byte) error { d.step.Bytes += int64(len(key)); return nil }

func (d *discardSink) SetIfAbsent(key, value []byte) error { return d.Set(key, value) }

func (d *discardSink) SetIfUnchanged(key, _, value []byte) error { return d.Set(key, value) }

func (d *discardSink) DeleteIfUnchanged(key, _ []byte) error { return d.Delete(key) }

func (d *discardSink) Hold(key []byte, reason string) {
	d.step.Held = append(d.step.Held, storage.DryRunItem{Key: storage.PrintableKey(key), Reason: reason})
	d.add(datamigrate.OutcomeHeld, key)
}

func (d *discardSink) Count(outcome string) {
	if outcome != "" {
		d.add(outcome, d.key)
	}
}

func (d *discardSink) Quarantine(key, _ []byte, reason string) error {
	d.step.Quarantine = append(d.step.Quarantine, storage.DryRunItem{Key: storage.PrintableKey(key), Reason: reason})
	d.add(datamigrate.OutcomeQuarantined, key)
	return nil
}

// Batch has no batch to give: an importer's writes are not run in an
// estimate.
func (d *discardSink) Batch() *storage.Batch { return nil }
