// file: core/library/quarantine.go
// version: 1.1.3
// guid: dbac77ed-fee6-451d-9dbc-7065c15ea39f
// last-edited: 2026-11-11

package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// quarantineState is a quarantine record's last durable step.
type quarantineState string

const (
	qMoving    quarantineState = "moving"    // the record exists; the rename may or may not have happened
	qHeld      quarantineState = "held"      // the item is under <Quarantine>/<id>/
	qReleasing quarantineState = "releasing" // a Release is under way; To names where it goes
	qPurging   quarantineState = "purging"   // a Purge is under way
)

// quarantineRow is one c/library/quarantine/<id> record.
type quarantineRow struct {
	ID       QuarantineID    `json:"id"`
	State    quarantineState `json:"state"`
	Original Path            `json:"original"`
	Held     Path            `json:"held"`
	Owner    storage.Owner   `json:"owner"`
	Reason   string          `json:"reason,omitempty"`
	At       time.Time       `json:"at"`
	To       *Path           `json:"to,omitempty"`
}

func (r quarantineRow) record() QuarantineRecord {
	return QuarantineRecord{ID: r.ID, Original: r.Original, Held: r.Held, Owner: r.Owner, Reason: r.Reason, At: r.At}
}

// Quarantine moves p (a file or a directory) into its root's quarantine area
// with a no-clobber rename (the same root is the same filesystem, so it is
// atomic) and records it in c/library/quarantine/<id>, the record first.
func (m *manager) Quarantine(ctx context.Context, caller storage.Owner, p Path, reason string) (_ QuarantineID, err error) {
	defer func() { m.ctr.refused(err) }()
	if p.IsRoot() {
		return "", fmt.Errorf("%w: a root directory cannot be quarantined", ErrBadTransfer)
	}
	r, err := m.need(p)
	if err != nil {
		return "", err
	}
	if !m.canWrite(r, caller) {
		return "", fmt.Errorf("%w: %s", ErrReadOnlyRoot, p.Root)
	}
	if !m.enabled(r.spec.Owner) {
		return "", fmt.Errorf("%w: %s", ErrOwnerDisabled, r.spec.Owner)
	}
	// A directory that holds the AppDir or the Quarantine area carries it into
	// the quarantine, so it is refused like the areas themselves.
	if err := m.refuseReserved(caller, r, p, true); err != nil {
		return "", err
	}
	unlock := m.lockPaths(p)
	defer unlock()
	if err := r.Mounted(ctx); err != nil {
		return "", err
	}
	st, err := r.rw.Lstat(p.Rel)
	if err != nil {
		return "", err
	}
	// Asked under the lock, with the type that will move: a directory takes its
	// whole subtree, so every oracle must answer for the subtree.
	if protected, perr := m.protectedForMove(ctx, p, st.IsDir()); protected {
		return "", errors.Join(fmt.Errorf("%w: %s", ErrSourceProtected, p), perr)
	}
	id := QuarantineID(m.seam.newID())
	held := Path{Root: p.Root, Rel: path.Join(r.spec.Quarantine, string(id), p.Rel)}
	row := quarantineRow{ID: id, State: qMoving, Original: p, Held: held, Owner: caller, Reason: reason, At: m.seam.now().UTC()}
	m.inflight.Store(string(id), struct{}{})
	defer m.inflight.Delete(string(id))
	if err := m.qrn.put(ctx, string(id), row); err != nil {
		return "", fmt.Errorf("%w: %w", ErrJournal, err)
	}
	bg := context.WithoutCancel(ctx)
	if err := r.rw.MkdirAll(path.Dir(held.Rel), DirMode); err != nil {
		return "", withRollback(err, "the quarantine record", m.qrn.remove(bg, string(id)))
	}
	// The caller's side of the rename is sealed (judged from the descriptor of the
	// directory holding p, not from its name); the held side is core's own area.
	if err := r.engineFS(caller).renameNoReplaceInto(r.rw, p.Rel, held.Rel); err != nil {
		return "", withRollback(err, "the quarantine record", m.qrn.remove(bg, string(id)))
	}
	row.State = qHeld
	if err := m.qrn.put(bg, string(id), row); err != nil {
		return id, fmt.Errorf("%w: %w", ErrJournal, err)
	}
	if err := errors.Join(syncDir(r.rw, p.Dir().Rel), syncDir(r.rw, path.Dir(held.Rel))); err != nil {
		return id, err
	}
	return id, nil
}

// loadQuarantine reads one record.
func (m *manager) loadQuarantine(ctx context.Context, id QuarantineID) (quarantineRow, error) {
	var row quarantineRow
	if !validULID(string(id)) {
		return row, fmt.Errorf("%w: %q", ErrUnknownQuarantine, id)
	}
	ok, err := m.qrn.get(ctx, string(id), &row)
	if err != nil {
		return row, err
	}
	if !ok {
		return row, fmt.Errorf("%w: %s", ErrUnknownQuarantine, id)
	}
	return row, nil
}

// Release moves a held item back to its Original path (or *to), never
// clobbering, and ends its record. Only the owner that quarantined it may.
func (m *manager) Release(ctx context.Context, caller storage.Owner, id QuarantineID, to *Path) (_ Path, err error) {
	defer func() { m.ctr.refused(err) }()
	row, err := m.loadQuarantine(ctx, id)
	if err != nil {
		return Path{}, err
	}
	if row.Owner != caller {
		return Path{}, fmt.Errorf("%w: quarantine record %s belongs to another owner", ErrReadOnlyRoot, id)
	}
	if row.State != qHeld && row.State != qReleasing {
		return Path{}, fmt.Errorf("%w: quarantine record %s is %s", ErrUnknownQuarantine, id, row.State)
	}
	target := row.Original
	if to != nil {
		target = *to
	}
	sr, err := m.need(row.Held)
	if err != nil {
		return Path{}, err
	}
	dr, err := m.need(target)
	if err != nil {
		return Path{}, err
	}
	if !m.canWrite(dr, caller) {
		return Path{}, fmt.Errorf("%w: %s", ErrReadOnlyRoot, target.Root)
	}
	if !m.enabled(dr.spec.Owner) {
		return Path{}, fmt.Errorf("%w: %s", ErrOwnerDisabled, dr.spec.Owner)
	}
	if target.IsRoot() {
		return Path{}, fmt.Errorf("%w: a root directory is not a release target", ErrBadTransfer)
	}
	// A release goes back to the library, never into the AppDir or the
	// quarantine area itself.
	if err := m.refuseReserved(caller, dr, target, false); err != nil {
		return Path{}, err
	}
	unlock := m.lockPaths(row.Held, target)
	defer unlock()
	if err := errors.Join(sr.Mounted(ctx), dr.Mounted(ctx)); err != nil {
		return Path{}, err
	}
	m.inflight.Store(string(id), struct{}{})
	defer m.inflight.Delete(string(id))
	row.State, row.To = qReleasing, &target
	if err := m.qrn.put(ctx, string(id), row); err != nil {
		return Path{}, fmt.Errorf("%w: %w", ErrJournal, err)
	}
	bg := context.WithoutCancel(ctx)
	if target.Dir().Rel != "" {
		if err := dr.engineFS(caller).MkdirAll(target.Dir().Rel, DirMode); err != nil {
			m.revertRelease(bg, row)
			return Path{}, err
		}
	}
	// The held side is core's own area; the target the caller named is sealed.
	if err := sr.rw.renameNoReplaceInto(dr.engineFS(caller), row.Held.Rel, target.Rel); err != nil {
		m.revertRelease(bg, row)
		return Path{}, err
	}
	if err := m.qrn.remove(bg, string(id)); err != nil {
		return target, fmt.Errorf("%w: %w", ErrJournal, err)
	}
	// Sync the directories the rename changed while both still exist, then tidy
	// the emptied holding directories and sync the quarantine area they were
	// removed from (a directory that tidy removed cannot be synced).
	serr := errors.Join(syncDir(dr.rw, target.Dir().Rel), syncDir(sr.rw, path.Dir(row.Held.Rel)))
	m.tidyQuarantineDir(sr, row)
	if err := syncDir(sr.rw, sr.spec.Quarantine); err != nil && !errors.Is(err, fs.ErrNotExist) {
		serr = errors.Join(serr, err)
	}
	return target, serr
}

// revertRelease puts the record back to held after a release that moved nothing.
func (m *manager) revertRelease(ctx context.Context, row quarantineRow) {
	row.State, row.To = qHeld, nil
	if err := m.qrn.put(ctx, string(row.ID), row); err != nil {
		m.log.Warn("library: could not restore a quarantine record after a refused release; recovery will", "id", row.ID, "err", err)
	}
}

// tidyQuarantineDir removes the emptied <Quarantine>/<id>/... directories, up
// to the quarantine area itself.
func (m *manager) tidyQuarantineDir(r *Root, row quarantineRow) {
	area := r.spec.Quarantine
	for d := path.Dir(row.Held.Rel); d != area && strings.HasPrefix(d, area+"/"); d = path.Dir(d) {
		if err := r.rw.Remove(d); err != nil {
			return
		}
	}
}

// Purge deletes a held item for good. It is a separate, permissioned action: the
// core:library.manage check is the caller's (the HTTP route).
func (m *manager) Purge(ctx context.Context, id QuarantineID) error {
	row, err := m.loadQuarantine(ctx, id)
	if err != nil {
		return err
	}
	if row.State != qHeld && row.State != qPurging {
		return fmt.Errorf("%w: quarantine record %s is %s", ErrUnknownQuarantine, id, row.State)
	}
	r, err := m.need(row.Held)
	if err != nil {
		return err
	}
	if !m.enabled(r.spec.Owner) {
		return fmt.Errorf("%w: %s", ErrOwnerDisabled, r.spec.Owner)
	}
	if err := r.Mounted(ctx); err != nil {
		return err
	}
	m.inflight.Store(string(id), struct{}{})
	defer m.inflight.Delete(string(id))
	row.State = qPurging
	if err := m.qrn.put(ctx, string(id), row); err != nil {
		return fmt.Errorf("%w: %w", ErrJournal, err)
	}
	return m.finishPurge(context.WithoutCancel(ctx), r, row)
}

func (m *manager) finishPurge(ctx context.Context, r *Root, row quarantineRow) error {
	dir := path.Join(r.spec.Quarantine, string(row.ID))
	if err := r.os.RemoveAll(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := m.qrn.remove(ctx, string(row.ID)); err != nil {
		return fmt.Errorf("%w: %w", ErrJournal, err)
	}
	return syncDir(r.rw, r.spec.Quarantine)
}

// Quarantined lists owner's held records, oldest first.
func (m *manager) Quarantined(ctx context.Context, owner storage.Owner) ([]QuarantineRecord, error) {
	var out []QuarantineRecord
	var bad []error
	err := m.qrn.all(ctx, func(id string, raw []byte) error {
		var row quarantineRow
		if err := json.Unmarshal(raw, &row); err != nil || string(row.ID) != id {
			bad = append(bad, fmt.Errorf("library: quarantine row %q does not decode", id))
			return nil
		}
		if row.Owner == owner && row.State == qHeld {
			out = append(out, row.record())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b QuarantineRecord) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out, errors.Join(bad...)
}

// heldByOwner counts the held records of every owner.
func (m *manager) heldByOwner(ctx context.Context) (map[storage.Owner]int64, error) {
	out := map[storage.Owner]int64{}
	err := m.qrn.all(ctx, func(id string, raw []byte) error {
		var row quarantineRow
		if json.Unmarshal(raw, &row) == nil && row.State == qHeld {
			out[row.Owner]++
		}
		return nil
	})
	return out, err
}
