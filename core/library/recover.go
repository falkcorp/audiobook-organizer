// file: core/library/recover.go
// version: 1.0.0
// guid: 806040d6-fa0f-435b-bd89-14287546fc22
// last-edited: 2026-11-11

package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// Recoverer completes or rolls back every interrupted transfer and quarantine
// step from its last durable journal step (05-library 5L.3.3, boot recovery).
// The value New returns implements it; TransferRecoverTask runs it.
type Recoverer interface {
	Recover(ctx context.Context) (RecoverReport, error)
}

// RecoverReport counts what one Recover pass did.
type RecoverReport struct {
	Completed  int // rolled forward: the transfer finished
	RolledBack int // rolled back: the transfer left nothing behind
	Left       int // done TransferIn rows, left for Scope.PendingTransfers
	Blocked    int // a root is unmounted, disabled or gone: tried again next run
	Conflicts  int // reality matches no journal state; nothing was deleted, the row was dropped and logged
	Failed     int // a step failed; the row stays for the next run
}

func (r RecoverReport) String() string {
	return fmt.Sprintf("completed %d, rolled back %d, left %d, blocked %d, conflicts %d, failed %d",
		r.Completed, r.RolledBack, r.Left, r.Blocked, r.Conflicts, r.Failed)
}

type outcome uint8

const (
	outCompleted outcome = iota
	outRolledBack
	outLeft
	outBlocked
	outConflict
)

// Recover walks the journal and the quarantine records. Rows a live call owns
// are skipped (they are in flight, not interrupted). Each row is decided by its
// state AND by what is on disk, never by the state alone: a crash can land
// between any journal write and the operation it announces.
func (m *manager) Recover(ctx context.Context) (RecoverReport, error) {
	var rep RecoverReport
	var errs []error
	count := func(o outcome, err error) {
		switch {
		case err != nil:
			rep.Failed++
			errs = append(errs, err)
		case o == outCompleted:
			rep.Completed++
		case o == outRolledBack:
			rep.RolledBack++
		case o == outLeft:
			rep.Left++
		case o == outBlocked:
			rep.Blocked++
		case o == outConflict:
			rep.Conflicts++
		}
	}
	// The journal is small (interrupted transfers only), and rows of one
	// directory must not race each other, so the pass is sequential.
	rows, lerr := m.listTransfers(ctx)
	errs = append(errs, lerr)
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return rep, errors.Join(append(errs, err)...)
		}
		if _, live := m.inflight.Load(row.ID); live {
			continue
		}
		count(m.recoverTransfer(ctx, row))
	}
	var qrows []quarantineRow
	qerr := m.qrn.all(ctx, func(id string, raw []byte) error {
		var q quarantineRow
		if err := json.Unmarshal(raw, &q); err != nil || string(q.ID) != id {
			errs = append(errs, fmt.Errorf("library: quarantine row %q does not decode", id))
			return nil
		}
		qrows = append(qrows, q)
		return nil
	})
	errs = append(errs, qerr)
	for _, q := range qrows {
		if err := ctx.Err(); err != nil {
			return rep, errors.Join(append(errs, err)...)
		}
		if _, live := m.inflight.Load(string(q.ID)); live || q.State == qHeld {
			continue
		}
		count(m.recoverQuarantine(ctx, q))
	}
	m.ctr.recordRecovery(rep)
	return rep, errors.Join(errs...)
}

// peek is Lstat as (info, exists); any error but not-exist is reported.
func peek(fsys fs.StatFS, rel string) (fs.FileInfo, bool, error) {
	if rel == "" {
		return nil, false, nil
	}
	st, err := lstatOf(fsys, rel)
	switch {
	case err == nil:
		return st, true, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	}
	return nil, false, err
}

func lstatOf(fsys fs.StatFS, rel string) (fs.FileInfo, error) {
	if l, ok := fsys.(interface {
		Lstat(string) (fs.FileInfo, error)
	}); ok {
		return l.Lstat(rel)
	}
	return fsys.Stat(rel)
}

// recoverState is what a recovery sees of one row's files.
type recoverState struct {
	m        *manager
	row      transferRow
	sfs, dfs fsOps
	tmp      string
	srcSt    fs.FileInfo
	srcOK    bool
	dstSt    fs.FileInfo
	dstOK    bool
	tmpSt    fs.FileInfo
	tmpOK    bool
	asideOK  bool
}

func (m *manager) recoverTransfer(ctx context.Context, row transferRow) (outcome, error) {
	if row.State == stDone {
		return outLeft, nil
	}
	sr, serr := m.rootForRecovery(ctx, row.Src.Root)
	dr, derr := m.rootForRecovery(ctx, row.Dst.Root)
	if serr != nil || derr != nil {
		m.log.Warn("library: transfer recovery waits for its roots", "id", row.ID, "err", errors.Join(serr, derr))
		return outBlocked, nil
	}
	unlock := m.lockPaths(row.Src, row.Dst)
	defer unlock()
	rs := &recoverState{m: m, row: row, sfs: sr.rw, dfs: dr.rw, tmp: path.Join(row.Dst.Dir().Rel, tmpPrefix+row.ID)}
	if row.Dst.Dir().Rel == "" {
		rs.tmp = tmpPrefix + row.ID
	}
	var err error
	if rs.srcSt, rs.srcOK, err = peek(rs.sfs, row.Src.Rel); err != nil {
		return outBlocked, err
	}
	if rs.dstSt, rs.dstOK, err = peek(rs.dfs, row.Dst.Rel); err != nil {
		return outBlocked, err
	}
	if rs.tmpSt, rs.tmpOK, err = peek(rs.dfs, rs.tmp); err != nil {
		return outBlocked, err
	}
	if _, rs.asideOK, err = peek(rs.dfs, row.Aside); err != nil {
		return outBlocked, err
	}
	var out outcome
	switch row.State {
	case stBegun:
		out, err = rs.fromBegun(ctx)
	case stCopied:
		out, err = rs.fromCopied(ctx)
	case stReplacing:
		out, err = rs.fromReplacing(ctx)
	case stPublished:
		out, err = rs.forward(ctx)
	default:
		return outConflict, m.dropRow(ctx, row, fmt.Errorf("unknown state %q", row.State))
	}
	return out, err
}

// rootForRecovery is a root that is registered, live and mounted.
func (m *manager) rootForRecovery(ctx context.Context, id RootID) (*Root, error) {
	r, err := m.need(Path{Root: id})
	if err != nil {
		return nil, err
	}
	return r, r.Mounted(ctx)
}

// dropRow ends a row that cannot be decided, loudly: nothing was deleted.
func (m *manager) dropRow(ctx context.Context, row transferRow, why error) error {
	m.log.Error("library: transfer recovery found a state it cannot decide; the row is dropped and no file was deleted",
		"id", row.ID, "state", row.State, "src", row.Src.String(), "dst", row.Dst.String(), "why", why)
	return m.jrn.remove(ctx, row.ID)
}

func (rs *recoverState) del(ctx context.Context) error { return rs.m.jrn.remove(ctx, rs.row.ID) }

func (rs *recoverState) removeTmp() error {
	if !rs.tmpOK {
		return nil
	}
	if err := rs.dfs.Remove(rs.tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	rs.tmpOK = false
	return nil
}

func (rs *recoverState) rollBack(ctx context.Context) (outcome, error) {
	if err := rs.removeTmp(); err != nil {
		return outRolledBack, err
	}
	if err := syncDir(rs.dfs, rs.row.Dst.Dir().Rel); err != nil {
		return outRolledBack, err
	}
	return outRolledBack, rs.del(ctx)
}

func (rs *recoverState) sameFile(a fs.FileInfo, aOK bool, b fs.FileInfo, bOK bool) bool {
	return aOK && bOK && libfs.SameFile(a, b)
}

// dstHasDigest reports whether the destination's bytes hash to sha.
func (rs *recoverState) dstHasDigest(ctx context.Context, sha string) bool {
	if sha == "" || !rs.dstOK {
		return false
	}
	d, err := rs.m.hashVia(ctx, rs.dfs, rs.row.Dst.Rel, false)
	return err == nil && d.SHA256 == sha
}

// fromBegun: the row was written and nothing else is promised. Each mode's
// possible partial result is told from reality: a rename or link that finished
// completes; anything else rolls back with its temp removed.
func (rs *recoverState) fromBegun(ctx context.Context) (outcome, error) {
	row := rs.row
	switch row.Mode {
	case Move, Auto, Hardlink:
		if row.Mode == Move && rs.dstOK && !rs.srcOK {
			// The rename finished.
			return rs.forward(ctx)
		}
		if rs.sameFile(rs.srcSt, rs.srcOK, rs.dstSt, rs.dstOK) {
			// One inode under two names: a hard link was made, or a rename's
			// link-then-unlink fallback stopped between its two steps.
			if row.Mode == Move {
				if err := rs.sfs.Remove(row.Src.Rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return outCompleted, err
				}
			}
			return rs.forward(ctx)
		}
	case Symlink:
		if rs.dstOK && rs.dstSt.Mode()&fs.ModeSymlink != 0 {
			return rs.forward(ctx)
		}
	}
	return rs.rollBack(ctx)
}

// fromCopied: the temp is complete and verified, the final name may or may not
// have been published.
func (rs *recoverState) fromCopied(ctx context.Context) (outcome, error) {
	published := rs.sameFile(rs.tmpSt, rs.tmpOK, rs.dstSt, rs.dstOK) ||
		(!rs.tmpOK && rs.dstHasDigest(ctx, rs.row.SrcSHA))
	if published {
		return rs.forward(ctx)
	}
	return rs.rollBack(ctx)
}

// fromReplacing: the old destination is (about to be) set aside. Roll forward
// when the new bytes are in place, otherwise put the old file back.
func (rs *recoverState) fromReplacing(ctx context.Context) (outcome, error) {
	row := rs.row
	newInPlace := rs.sameFile(rs.tmpSt, rs.tmpOK, rs.dstSt, rs.dstOK) ||
		(rs.dstOK && rs.dstHasDigest(ctx, row.SrcSHA)) ||
		(row.Tmp == "" && row.Mode == Move && rs.dstOK && !rs.srcOK)
	switch {
	case newInPlace:
		return rs.forward(ctx)
	case rs.asideOK && !rs.dstOK:
		if err := rs.dfs.RenameNoReplace(row.Aside, row.Dst.Rel); err != nil {
			return outRolledBack, err
		}
		return rs.rollBack(ctx)
	case !rs.asideOK && rs.dstOK && rs.dstHasDigest(ctx, row.Expect):
		// The old file never moved: nothing happened.
		return rs.rollBack(ctx)
	}
	return outConflict, rs.m.dropRow(ctx, row, errors.New("the destination matches neither the old nor the new bytes"))
}

// forward finishes a transfer whose final name holds the new bytes: the temp
// goes, the directory is fsynced, a kept old file is removed, and (cross-device
// Move) the source is removed only if it still has the digest that was copied.
func (rs *recoverState) forward(ctx context.Context) (outcome, error) {
	row, m := rs.row, rs.m
	if err := rs.removeTmp(); err != nil {
		return outCompleted, err
	}
	if err := syncDir(rs.dfs, row.Dst.Dir().Rel); err != nil {
		return outCompleted, err
	}
	if rs.asideOK {
		if err := rs.dfs.Remove(row.Aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return outCompleted, err
		}
		if err := syncDir(rs.dfs, row.Dst.Dir().Rel); err != nil {
			return outCompleted, err
		}
	}
	if row.RemoveSrc && rs.srcOK {
		if !rs.dstHasDigest(ctx, row.SrcSHA) {
			return outConflict, m.dropRow(ctx, row, errors.New("the destination does not hold the copied bytes; the source is kept"))
		}
		d, err := m.hashVia(ctx, rs.sfs, row.Src.Rel, false)
		if err != nil || d.SHA256 != row.SrcSHA {
			return outConflict, m.dropRow(ctx, row, errors.New("the source changed after it was copied; it is kept"))
		}
		if err := rs.sfs.Remove(row.Src.Rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return outCompleted, err
		}
		if err := syncDir(rs.sfs, row.Src.Dir().Rel); err != nil {
			return outCompleted, err
		}
	}
	return outCompleted, rs.del(ctx)
}

// ---- quarantine ----

// recoverQuarantine settles a record stuck mid-step.
func (m *manager) recoverQuarantine(ctx context.Context, q quarantineRow) (outcome, error) {
	r, err := m.rootForRecovery(ctx, q.Held.Root)
	if err != nil {
		return outBlocked, nil
	}
	unlock := m.lockPaths(q.Original, q.Held)
	defer unlock()
	_, heldOK, err := peek(r.rw, q.Held.Rel)
	if err != nil {
		return outBlocked, err
	}
	switch q.State {
	case qMoving:
		if heldOK {
			q.State = qHeld
			return outCompleted, m.qrn.put(ctx, string(q.ID), q)
		}
		return outRolledBack, m.qrn.remove(ctx, string(q.ID))
	case qReleasing:
		if heldOK {
			q.State, q.To = qHeld, nil
			return outRolledBack, m.qrn.put(ctx, string(q.ID), q)
		}
		m.tidyQuarantineDir(r, q)
		return outCompleted, m.qrn.remove(ctx, string(q.ID))
	case qPurging:
		return outCompleted, m.finishPurge(ctx, r, q)
	}
	return outConflict, m.qrn.remove(ctx, string(q.ID))
}
