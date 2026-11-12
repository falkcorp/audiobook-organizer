// file: core/kernel/cutover_blockers.go
// version: 1.0.0
// guid: 7b1f4c0e-6a39-4d52-8e17-c3a05d9b2f64
// last-edited: 2026-11-12

package kernel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// `storage cutover-blockers` (05-storage §5S.3.13 "Cutover blockers (D13)";
// F4-06): the operator's way out of a cutover the preflight refused. The
// offline open below is the explicit exception to ErrOfflinePendingSteps
// (§5S.4 "Offline open" rule 2): it opens a store whose cutover steps are all
// still pending, runs none of them and never prepares the store, and only
// when the cutover has not started (storage.ErrCutoverStarted otherwise).
// list reads; fix and delete each commit one storage batch (see
// storage.(*DB).EditCutoverBlocker for the retired-claim seam and for why a
// refused edit writes nothing).

// cutoverBlockersCommand is the command's name as a step's Blocker.Fix names
// it.
const cutoverBlockersCommand = "storage cutover-blockers"

// blockerOffers reports whether a blocker's Fix text offers action ("fix" or
// "delete") on this command: the Fix text names `storage cutover-blockers`
// followed by the actions it offers, separated by "|" ("storage
// cutover-blockers fix|delete", "storage cutover-blockers delete"). A Fix
// text that does not (a merge in progress says to finish or undo it on the
// pre-freeze release) makes the row not fixable or deletable here. This is
// the contract a step's Preflight honours: core knows no step's family, so
// the step says what the command may do with each row it reports.
func blockerOffers(fix, action string) bool {
	rest := fix
	for {
		_, after, ok := strings.Cut(rest, cutoverBlockersCommand+" ")
		if !ok {
			return false
		}
		word := after
		if i := strings.IndexFunc(after, func(r rune) bool { return !unicode.IsLetter(r) && r != '|' }); i >= 0 {
			word = after[:i]
		}
		if slices.Contains(strings.Split(word, "|"), action) {
			return true
		}
		rest = after
	}
}

// CutoverBlockersOptions are OpenCutoverBlockers's seams.
type CutoverBlockersOptions struct {
	// Bind receives the store after the open, so a cutover step's Preflight,
	// FixBlocker and DeleteBlocker bind the handles they read the rows
	// through (the step's importer is built over this store, never the
	// boot's). It runs before any step is called. nil: the steps reach no
	// handle of their own. The wiring is F3-07's, as DryRunOptions.Bind's.
	Bind func(db *storage.DB) error
}

// CutoverBlockers is one offline open for `storage cutover-blockers`.
type CutoverBlockers struct {
	k        *Kernel
	o        *storage.Opened
	db       *storage.DB
	cutover  []datamigrate.DataMigration
	readOnly bool
	once     sync.Once
	err      error
}

// OpenCutoverBlockers opens the store for the command: OpenOffline's guards
// (the state directory's owner for a read-write open, the swap lock, a staged
// or in-flight swap refused, a running service refused by the store's lock)
// and storage's open steps, then storage.Opened.BeginCutoverBlockers instead
// of Opened.Offline: no migration step runs, nothing is written, and a store
// whose cutover has started is refused (errors.Is storage.ErrCutoverStarted)
// or an empty one. readOnly (list) writes nothing at all; fix and delete
// open read-write.
func OpenCutoverBlockers(ctx context.Context, c Config, readOnly bool, opt CutoverBlockersOptions) (_ *CutoverBlockers, err error) {
	k, o, err := openOfflineStore(ctx, c, readOnly)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, k.closeOffline(ctx))
		}
	}()
	var cutover []datamigrate.DataMigration
	if k.dataMig != nil {
		for _, m := range k.dataMig.runner.Migrations() {
			if m.Cutover {
				cutover = append(cutover, m)
			}
		}
	}
	steps := make([]storage.CutoverStep, 0, len(cutover))
	for _, m := range cutover {
		steps = append(steps, storage.CutoverStep{Owner: m.Owner, ID: m.ID, RaisesLevel: m.RaisesLevel})
	}
	db, err := o.BeginCutoverBlockers(ctx, steps)
	if err != nil {
		return nil, fmt.Errorf("kernel: cutover-blockers open: %w", err)
	}
	if opt.Bind != nil {
		if err := opt.Bind(db); err != nil {
			return nil, fmt.Errorf("kernel: cutover-blockers open: bind the store: %w", err)
		}
	}
	return &CutoverBlockers{k: k, o: o, db: db, cutover: cutover, readOnly: readOnly}, nil
}

// Close closes the store and the offline kernel's logging, once.
func (c *CutoverBlockers) Close() error {
	c.once.Do(func() { c.err = c.k.closeOffline(context.Background()) })
	return c.err
}

// List runs every pending cutover step's Preflight over the store, read-only,
// and returns the blockers in step order. It is the report fix and delete
// match their key against.
func (c *CutoverBlockers) List(ctx context.Context) ([]storage.CutoverBlocker, error) {
	return runPreflights(ctx, c.cutover, c.o.PreflightSource)
}

// BlockerRow is one blocking row's raw key and value, for `list --out`.
type BlockerRow struct {
	Key   []byte
	Value []byte
}

// Rows reads the raw rows of blockers, in order. Its values are credential
// material (password and key hashes): the caller writes them only to a file
// only the operator can read.
func (c *CutoverBlockers) Rows(ctx context.Context, blockers []storage.CutoverBlocker) ([]BlockerRow, error) {
	byOwner := map[string]storage.Owner{}
	for _, m := range c.cutover {
		byOwner[m.ID] = m.Owner
	}
	rows := make([]BlockerRow, 0, len(blockers))
	for _, b := range blockers {
		owner, ok := byOwner[b.Step]
		if !ok {
			return nil, fmt.Errorf("kernel: blocker of unknown step %s", b.Step)
		}
		src, err := c.o.PreflightSource(owner)
		if err != nil {
			return nil, err
		}
		v, err := src.Get(ctx, b.Key)
		if err != nil {
			return nil, fmt.Errorf("kernel: read the row at %s: %w", storage.PrintableKey(b.Key), err)
		}
		rows = append(rows, BlockerRow{Key: slices.Clone(b.Key), Value: v})
	}
	return rows, nil
}

// CutoverBlockerResult is what a committed fix or delete reports: the step,
// the number of its record at c/storage/cutover/blockers/<step>/<n> and the
// IDs the delete removed.
type CutoverBlockerResult struct {
	Step string
	N    uint64
	IDs  []string
}

// Fix replaces the row at key with value through its owning step's
// FixBlocker, committed with its record and its core.storage.cutover_blocker_fix
// audit event in one batch, only if the step's Preflight, re-run over the
// store with the change laid over it, no longer names the row and names no
// row it did not name before. It refuses a key the current report does not
// name, a row whose Fix text does not offer fix on this command
// (blockerOffers) and a step with no FixBlocker. by names the operator for
// the audit event. The value is never logged, echoed or audited.
func (c *CutoverBlockers) Fix(ctx context.Context, key, value []byte, by string) (CutoverBlockerResult, error) {
	return c.edit(ctx, storage.CutoverBlockerFix, key, by, func(m datamigrate.DataMigration) (func(context.Context, *storage.Batch) ([]string, error), error) {
		if m.FixBlocker == nil {
			return nil, fmt.Errorf("step %s has no FixBlocker", m.ID)
		}
		return func(ctx context.Context, b *storage.Batch) ([]string, error) {
			return nil, m.FixBlocker(ctx, b, key, value)
		}, nil
	})
}

// Delete removes the row at key and the rows its owning step says go with it
// through DeleteBlocker, with the same record, audit event
// (core.storage.cutover_blocker_delete), recheck and refusals as Fix. The
// result carries the IDs DeleteBlocker returned.
func (c *CutoverBlockers) Delete(ctx context.Context, key []byte, by string) (CutoverBlockerResult, error) {
	return c.edit(ctx, storage.CutoverBlockerDelete, key, by, func(m datamigrate.DataMigration) (func(context.Context, *storage.Batch) ([]string, error), error) {
		if m.DeleteBlocker == nil {
			return nil, fmt.Errorf("step %s has no DeleteBlocker", m.ID)
		}
		return func(ctx context.Context, b *storage.Batch) ([]string, error) {
			return m.DeleteBlocker(ctx, b, key)
		}, nil
	})
}

func (c *CutoverBlockers) edit(ctx context.Context, action string, key []byte, by string,
	stage func(datamigrate.DataMigration) (func(context.Context, *storage.Batch) ([]string, error), error)) (CutoverBlockerResult, error) {
	if c.readOnly {
		return CutoverBlockerResult{}, fmt.Errorf("kernel: cutover-blockers %s needs a read-write open", action)
	}
	printable := storage.PrintableKey(key)
	current, err := c.List(ctx)
	if err != nil {
		return CutoverBlockerResult{}, err
	}
	i := slices.IndexFunc(current, func(b storage.CutoverBlocker) bool { return bytes.Equal(b.Key, key) })
	if i < 0 {
		return CutoverBlockerResult{}, fmt.Errorf("kernel: the row at %s is not named by the current blocker report; run storage cutover-blockers list", printable)
	}
	blocker := current[i]
	if !blockerOffers(blocker.Fix, action) {
		return CutoverBlockerResult{}, fmt.Errorf("kernel: step %s does not offer %s for the row at %s here (its way out: %s)", blocker.Step, action, printable, blocker.Fix)
	}
	mi := slices.IndexFunc(c.cutover, func(m datamigrate.DataMigration) bool { return m.ID == blocker.Step })
	if mi < 0 {
		return CutoverBlockerResult{}, fmt.Errorf("kernel: the row at %s names step %s, which this build does not register", printable, blocker.Step)
	}
	m := c.cutover[mi]
	stageFn, err := stage(m)
	if err != nil {
		return CutoverBlockerResult{}, fmt.Errorf("kernel: cutover-blockers %s: %w", action, err)
	}
	as, err := buildAudit(ctx, c.db, c.k.coreLog("audit"), time.Now)
	if err != nil {
		return CutoverBlockerResult{}, fmt.Errorf("kernel: cutover-blockers %s is audited: %w", action, err)
	}
	build, err := c.k.binaryID(ctx)
	if err != nil {
		return CutoverBlockerResult{}, err
	}
	// What the step already reported: the edit may remove blockers, never
	// add one.
	before := map[string]bool{}
	for _, b := range current {
		if b.Step == blocker.Step {
			before[string(b.Key)] = true
		}
	}
	var removed []string
	n, err := c.db.EditCutoverBlocker(ctx, storage.CutoverBlockerEdit{
		Step: blocker.Step, Owner: m.Owner, Source: m.Source, Key: key, Action: action,
		Reason: blocker.Reason, Build: build, At: time.Now(),
		Stage: func(ctx context.Context, b *storage.Batch) ([]string, error) {
			var err error
			removed, err = stageFn(ctx, b)
			return removed, err
		},
		Recheck: func(ctx context.Context, r storage.Reader) error {
			found, err := m.Preflight(ctx, r)
			if err != nil {
				return fmt.Errorf("preflight: %w", err)
			}
			for _, f := range found {
				switch {
				case bytes.Equal(f.Key, key):
					return fmt.Errorf("the row at %s still blocks the cutover: %s", printable, f.Reason)
				case !before[string(f.Key)]:
					return fmt.Errorf("the change would make the row at %s block the cutover: %s", storage.PrintableKey(f.Key), f.Reason)
				}
			}
			return nil
		},
		Audit: func(b *storage.Batch, ids []string) error {
			return as.RecordIn(ctx, b, audit.Event{
				Action:  "core.storage.cutover_blocker_" + action,
				Outcome: audit.Success,
				Actor:   audit.Actor{Username: by, Method: "offline"},
				Reason:  "operator_" + action,
				// Details never carry a value: the step, the row's key and
				// family, and the IDs a delete removed.
				Details: map[string]any{"step": blocker.Step, "row": printable, "family": blocker.Family, "ids": ids},
			})
		},
	})
	if err != nil {
		return CutoverBlockerResult{}, fmt.Errorf("kernel: cutover-blockers %s: %w", action, err)
	}
	res := CutoverBlockerResult{Step: blocker.Step, N: n, IDs: removed}
	c.k.coreLog("storage").Info("kernel: core.storage.cutover_blocker_"+action, "step", blocker.Step, "n", n, "row", printable, "by", by)
	return res, nil
}
