// file: internal/books/cutover/m4/step.go
// version: 1.0.0
// guid: a9d5f13e-7c02-4b68-8e41-35b0c6d7e2f8
// last-edited: 2026-11-18

// Package m4 is books' cutover step M4, Tasks (03 §3.8 row M4; 05-tasks
// §5T.8.3; F5-16): HEAD's operation families into core's task store through
// tasks.Importer, each run's rows imported in one batch with their source rows
// deleted, the HEAD def IDs mapped to task kinds by internal/books/cutover/
// kindrename.
//
// The step is dark until F5-18 registers it: nothing here is reached from
// internal/books/cutover's step list or from main. It takes the Importer, the
// legacy reader and HEAD's reconcile as parameters, and neither imports
// internal/books/store (a cutover step is a leaf: layering) nor
// internal/books/cutover (which imports this package once the step list exists),
// so the step ID comes in as a parameter too.
package m4

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// Outcomes the step counts beside the runner's "quarantined".
const (
	OutcomeRun         = "run-imported"         // a run and every row it owned, imported and deleted
	OutcomeDerived     = "derived-dropped"      // an index, definition or census row, deleted by rule
	OutcomeDeferred    = "dependent-deferred"   // a run-owned row visited before its run's driver; the driver pulls it
	OutcomeOrphan      = "orphan-quarantined"   // a run-owned row of no run record
	OutcomeRunQuarRows = "run-rows-quarantined" // the rows of a quarantined run, quarantined with it
)

// Config is what the step needs besides its declaration.
type Config struct {
	// Importer is the native task store's importer (module.Importers' tasks
	// member): tasks.NativeImporter over c/tasks/.
	Importer tasks.Importer
	// Legacy is books' legacy range: where Step reads the rows a run owns. The
	// runner binds it after the store is open, so it is a function.
	Legacy func() storage.Reader
	// Reconcile is HEAD's ReconcileOpsV3WithV2 (internal/books/store), the
	// every-boot repair that makes every v3 record agree with its v2 row. The
	// step runs it once, before it decodes anything (5T.8.3 order step 1). It is
	// a parameter because this package cannot import the store.
	Reconcile func(ctx context.Context) error
	// Logger receives one line per quarantined run (its ID and a fixed class,
	// never a value). nil discards.
	Logger *slog.Logger
}

// Migration is the step's state: the importer and the step's own accounting. It
// holds no per-run state: the runner steps its shards concurrently, so every
// Step call is independent.
type Migration struct {
	imp    tasks.Importer
	legacy func() storage.Reader
	recon  func(ctx context.Context) error
	logger *slog.Logger

	reconMu   sync.Mutex
	reconDone bool

	runs, runsQuarantined, rowsQuarantined, derived, deferred, orphans atomic.Int64
}

// New returns the data migration for the step with ID id.
//
// raisesLevel and after are the flip's: F3's books level and the StepRefs of
// the steps that must run first (books' level step), the same two M2, M3 and M5
// take.
//
// Parallelism is CPU(): the work is decoding and one batch per chunk, and the
// partition is disjoint by construction. A run's rows are written by exactly
// one worker, the one that steps the run's driver row (opv3:run:<id>): the
// driver reads every row the run owns, imports them and stages their deletes in
// its own chunk's batch. Another worker that reaches one of those rows first
// finds the run's record still present and leaves the row alone (the driver
// pulls it), or, when the runner already recorded the driver's write of it in
// this boot, never steps it at all (RewritesInPlace: a write into another
// shard's unvisited part is recorded and skipped). A run's driver commits the
// import and every delete of the run together, so the record's absence means
// its rows are gone too, and a row of no run record is an orphan that nothing
// else will ever claim. Two workers therefore never write one row, and no run
// is imported twice (the importer's create-only guard would refuse it).
func New(cfg Config, id string, raisesLevel int, after []storage.StepRef) (datamigrate.DataMigration, error) {
	switch {
	case cfg.Importer == nil:
		return datamigrate.DataMigration{}, errors.New("m4: Config.Importer is required")
	case cfg.Legacy == nil:
		return datamigrate.DataMigration{}, errors.New("m4: Config.Legacy is books' legacy range")
	case cfg.Reconcile == nil:
		return datamigrate.DataMigration{}, errors.New("m4: Config.Reconcile is HEAD's ReconcileOpsV3WithV2 (5T.8.3 order step 1)")
	case id == "":
		return datamigrate.DataMigration{}, errors.New("m4: the step ID is required")
	case raisesLevel <= 0:
		return datamigrate.DataMigration{}, errors.New("m4: raisesLevel is the flip's books level, above 0")
	case len(after) == 0:
		return datamigrate.DataMigration{}, errors.New("m4: after is books' level step")
	}
	m := &Migration{imp: cfg.Importer, legacy: cfg.Legacy, recon: cfg.Reconcile, logger: cfg.Logger}
	if m.logger == nil {
		m.logger = slog.New(slog.DiscardHandler)
	}
	return datamigrate.DataMigration{
		ID:          id,
		Owner:       "books",
		Description: "the operation families (runs, trails, rows, marks) into core's task store",
		Source:      SourceRanges(),
		Parallelism: tasks.CPU(),
		Mode:        datamigrate.BootBlocking,
		Compat:      storage.Breaking,
		RaisesLevel: raisesLevel,
		After:       append([]storage.StepRef(nil), after...),
		Cutover:     true,
		// A run's driver writes (deletes) rows in other shards' unvisited key
		// ranges: see New.
		RewritesInPlace: true,
		Step:            m.Step,
		Verify:          m.Verify,
	}, nil
}

// reconcile runs HEAD's reconcile once, before the first row is decoded. A
// failure fails every Step: the boot stops with the checkpoint open.
func (m *Migration) reconcile(ctx context.Context) error {
	m.reconMu.Lock()
	defer m.reconMu.Unlock()
	if m.reconDone {
		return nil
	}
	if err := m.recon(ctx); err != nil {
		return fmt.Errorf("m4: reconcile the v3 records with their v2 rows: %w", err)
	}
	m.reconDone = true
	return nil
}

// Step handles one source row.
func (m *Migration) Step(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	if err := m.reconcile(ctx); err != nil {
		return err
	}
	key := string(kv.Key)
	family, id, ok := familyOf(key)
	switch {
	case !ok:
		return fmt.Errorf("m4: the row %q is outside the step's source", key)
	case isDerived(family):
		m.derived.Add(1)
		w.Count(OutcomeDerived)
		return w.Delete(kv.Key)
	case family == prefixRun:
		return m.stepRun(ctx, kv, w, id)
	}
	// A row a run owns, visited before (or without) its run's driver.
	_, err := m.legacy().Get(ctx, []byte(prefixRun+id))
	switch {
	case err == nil:
		m.deferred.Add(1)
		w.Count(OutcomeDeferred)
		return nil // the run's driver pulls it
	case errors.Is(err, storage.ErrNotFound):
		m.logger.Warn("m4: a run-owned row of no run record is quarantined", "key", key)
		m.orphans.Add(1)
		return w.Quarantine(kv.Key, kv.Value, reasonOrphan)
	}
	return err
}

// stepRun imports one run: the run record, then every row the run owns, in one
// batch, with the deletes of exactly those rows.
func (m *Migration) stepRun(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter, id string) error {
	rr, err := m.collect(ctx, id, kv)
	if err != nil {
		return err
	}
	c, reason := convert(rr)
	if reason != "" {
		return m.quarantineRun(rr, w, reason)
	}
	b := w.Batch()
	if err := m.imp.Run(ctx, b, c.rec); err != nil {
		return fmt.Errorf("m4: import run %q: %w", id, err)
	}
	if err := m.imp.RunData(ctx, b, c.rec.ID, c.data); err != nil {
		return fmt.Errorf("m4: import the data of run %q: %w", id, err)
	}
	for _, row := range rr.all() {
		if err := w.Delete(row.Key); err != nil {
			return err
		}
	}
	m.runs.Add(1)
	w.Count(OutcomeRun)
	return nil
}

// quarantineRun sets the run record and everything the run owns aside, with the
// reason class, so none of it is left to read as an orphan.
func (m *Migration) quarantineRun(rr *runRows, w *datamigrate.StepWriter, reason string) error {
	m.logger.Warn("m4: a run is quarantined", "run", rr.id, "reason", reason)
	for _, row := range rr.all() {
		why := reasonOfRunQuar
		if string(row.Key) == prefixRun+rr.id {
			why = reason
		}
		if err := w.Quarantine(row.Key, row.Value, why); err != nil {
			return err
		}
		if string(row.Key) == prefixRun+rr.id {
			m.runsQuarantined.Add(1)
		} else {
			m.rowsQuarantined.Add(1)
		}
	}
	return nil
}

// collect reads every row HEAD stores for run id, cloned. run is the driver row
// the step was handed.
func (m *Migration) collect(ctx context.Context, id string, run storage.KV) (*runRows, error) {
	r := m.legacy()
	rr := &runRows{id: id}
	rr.run = &run
	one := func(prefix string) (*storage.KV, error) {
		raw, err := r.Get(ctx, []byte(prefix+id))
		switch {
		case errors.Is(err, storage.ErrNotFound):
			return nil, nil
		case err != nil:
			return nil, err
		}
		return &storage.KV{Key: []byte(prefix + id), Value: raw}, nil
	}
	var err error
	if rr.prog, err = one(prefixProg); err != nil {
		return nil, err
	}
	if rr.ckpt, err = one(prefixCkpt); err != nil {
		return nil, err
	}
	if rr.ledger, err = one(prefixLedger); err != nil {
		return nil, err
	}
	for _, f := range []struct {
		prefix string
		dst    *[]storage.KV
	}{
		{prefixEvt, &rr.evt}, {prefixIntent, &rr.intent}, {prefixItem, &rr.item}, {prefixLog, &rr.log},
		{prefixSnap, &rr.snap}, {prefixMark, &rr.mark}, {prefixRowMark, &rr.rowmark},
		{prefixRow, &rr.row}, {prefixRowIx, &rr.rowix},
	} {
		if *f.dst, err = scanRows(ctx, r, f.prefix+id+":"); err != nil {
			return nil, err
		}
	}
	return rr, nil
}

// scanRows reads every row under prefix through r, cloned.
func scanRows(ctx context.Context, r storage.Reader, prefix string) ([]storage.KV, error) {
	var out []storage.KV
	for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte(prefix), End: prefixEnd(prefix)}) {
		if err != nil {
			return nil, err
		}
		out = append(out, kv.Clone())
	}
	return out, nil
}

// Verify is the step's invariant gate: no row of any source family is left, and
// this boot's accounting is reported. The counts are this boot's: a boot that
// resumed after a crash reports only what it stepped.
func (m *Migration) Verify(ctx context.Context, r storage.Reader) (datamigrate.VerifyReport, error) {
	rep := datamigrate.VerifyReport{
		Read:        m.runs.Load() + m.runsQuarantined.Load(),
		Written:     m.runs.Load(),
		Quarantined: m.runsQuarantined.Load() + m.rowsQuarantined.Load() + m.orphans.Load(),
	}
	for _, rg := range SourceRanges() {
		// One probe per family is enough: any key in the range is a row the
		// step missed.
		it, err := r.NewIterator(ctx, storage.ScanOptions{Start: rg.Start, End: rg.End, KeysOnly: true})
		if err != nil {
			return rep, err
		}
		if it.First() {
			rep.Failures = append(rep.Failures, fmt.Sprintf("a row of %s is still present (%s)", strings.TrimSuffix(string(rg.Start), ":"), it.Key()))
		}
		if err := errors.Join(it.Err(), it.Close()); err != nil {
			return rep, err
		}
	}
	return rep, nil
}
