// file: core/storage/preflight.go
// version: 1.0.1
// guid: 17127bd3-ef79-4077-aaf5-aac159db016d
// last-edited: 2026-11-11

package storage

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	"github.com/falkcorp/mediacore/storage/keys"
)

// The cutover preflight (05-storage §5S.3.13 "Cutover blockers"; 06 §6.5.1
// P4 (a); §5S.4 step 5a): at P4a, before the headroom check and the
// checkpoint, and only on a store whose cutover has not started, every
// pending cutover step's Preflight reads its source and returns the rows
// that must stop the upgrade instead of being quarantined. Any blocker
// refuses the boot with ErrCutoverBlocked (exit 78) and nothing written: no
// checkpoint, no marker, no level, no step, so the pre-freeze release still
// opens the store. The kernel drives it (kernel.CutoverPreflight); storage
// provides the predicate, the read-only source and the report.

// CutoverStep is one registered cutover step as the preflight sees it: its
// owner, its data migration ID and the level its RaisesLevel names (M7's
// target for its owner).
type CutoverStep struct {
	Owner       Owner
	ID          string
	RaisesLevel int
}

// CutoverBlocker is one source row that stops the cutover: the step, the
// key (absolute), its §5S.9.1 family, the reason and the operator's way
// out. It never carries a value.
type CutoverBlocker struct {
	Step   string
	Key    []byte
	Family string
	Reason string
	Fix    string
}

// CutoverBlockedError is ErrCutoverBlocked with every blocker the preflight
// found, in step order. Compared with errors.Is(err, ErrCutoverBlocked).
type CutoverBlockedError struct {
	Blockers []CutoverBlocker
}

func (e *CutoverBlockedError) Error() string {
	return fmt.Sprintf("%v: %d blocking row(s)", ErrCutoverBlocked, len(e.Blockers))
}

// Is matches ErrCutoverBlocked.
func (e *CutoverBlockedError) Is(target error) bool { return target == ErrCutoverBlocked }

// Report is the blocker report for the boot log (and the dry run's): one
// line per blocker with its step, key, family, reason and fix, never a
// value, and a last line naming `storage cutover-blockers`.
func (e *CutoverBlockedError) Report() []string {
	return CutoverBlockerReport(e.Blockers)
}

// CutoverBlockerReport renders blockers as the report's lines: one per
// blocker, then the line naming `storage cutover-blockers`, the command
// that lists, fixes or deletes them (F4-06). An empty list renders nothing.
func CutoverBlockerReport(blockers []CutoverBlocker) []string {
	if len(blockers) == 0 {
		return nil
	}
	out := make([]string, 0, len(blockers)+1)
	for _, b := range blockers {
		out = append(out, fmt.Sprintf("blocker: step=%s key=%s family=%s reason=%q fix=%q",
			b.Step, printableKey(b.Key), b.Family, b.Reason, b.Fix))
	}
	out = append(out, fmt.Sprintf("the cutover is blocked by %d row(s): run `storage cutover-blockers` on the pre-freeze release to list, fix or delete them, then upgrade again", len(blockers)))
	return out
}

// printableKey renders a key for a report: its tuple segments when it is
// one, else the bytes quoted.
func printableKey(k []byte) string {
	if segs, err := keys.Split(k); err == nil && len(segs) > 0 {
		parts := make([]string, len(segs))
		for i, s := range segs {
			parts[i] = fmt.Sprintf("%q", []byte(s))
		}
		return strings.Join(parts, "/")
	}
	return fmt.Sprintf("%q", k)
}

// CutoverStarted is the preflight's predicate: the cutover has started on
// o's store when an owner's committed level is at or above M7's target for
// it (the highest RaisesLevel of its cutover steps: the level step that
// guards them committed), or when any cutover step has a record in
// c/storage/dm/ (its plan was committed, whatever its state). A resuming
// boot, and every boot after the cutover, skips the preflight. It reads
// only.
func (o *Opened) CutoverStarted(ctx context.Context, steps []CutoverStep) (bool, error) {
	db := o.db
	target := map[Owner]int{}
	for _, s := range steps {
		lo := s.Owner
		if info, ok := db.owners[s.Owner]; ok {
			lo = info.levelOwner()
		}
		target[lo] = max(target[lo], s.RaisesLevel)
	}
	stamps := db.boot.stamps
	for lo, t := range target {
		if t > 0 && stamps[lo] >= t {
			return true, nil
		}
	}
	ks := db.storageKS()
	for _, s := range steps {
		switch _, err := ks.Get(ctx, DataMigrationKey(s.ID)); {
		case err == nil:
			return true, nil
		case !errors.Is(err, ErrNotFound):
			return false, fmt.Errorf("storage: cutover step %s's record: %w", s.ID, err)
		}
	}
	return false, nil
}

// PreflightSource is owner's cutover source as a Preflight reads it: its
// legacy range (a cutover step's Source is retired claims), through a
// Reader that has no write method behind it, so a Preflight cannot write
// even by a type assertion. It refuses an owner with no legacy range, and
// refuses once Prepare (or Offline) ran: the preflight is P4a's first act,
// before the headroom check, the checkpoint and the marker (§5S.4 step 5a),
// so a refusal writes nothing, and a boot that ran it after Prepare would
// refuse with the checkpoint already taken.
func (o *Opened) PreflightSource(owner Owner) (Reader, error) {
	if o.db.boot.prepared {
		return nil, errors.New("storage: the cutover preflight runs before Prepare (§5S.4 step 5a, 06 §6.5.1 P4 (a)); this boot already prepared the store")
	}
	lr, ok := o.db.Legacy(owner)
	if !ok {
		return nil, fmt.Errorf("storage: owner %s has no legacy range for a cutover preflight", owner)
	}
	return readOnlyReader{r: lr}, nil
}

// readOnlyReader exposes only Reader's methods of r.
type readOnlyReader struct{ r Reader }

func (r readOnlyReader) Get(ctx context.Context, key []byte) ([]byte, error) {
	return r.r.Get(ctx, key)
}

func (r readOnlyReader) Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error] {
	return r.r.Scan(ctx, opts)
}

func (r readOnlyReader) NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error) {
	return r.r.NewIterator(ctx, opts)
}
