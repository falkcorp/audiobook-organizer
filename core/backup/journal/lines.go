// file: core/backup/journal/lines.go
// version: 1.0.0
// guid: 0b7f3c85-d2a1-4e96-8f40-5c8a1d6e3b72
// last-edited: 2026-11-11

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// The bookkeeping lines of the journal and their Detail layouts (05-backup
// §5B.3.2 Kind comments, §5B.3.6). The writers and the readers of a layout
// live here so the boot never parses a Detail by position on its own.

// Gap reasons: Detail[1] of a gap line. append_failed is the one the journal
// itself writes (File.Append); other_install is the boot's, for the line a new
// journal starts with after another install's was set aside.
const (
	GapOtherInstall = "other_install"
	GapAppendFailed = gapReasonAppend
)

// GapLine is a gap line for the interval from since: Detail = [since as
// RFC 3339, reason].
func GapLine(since time.Time, reason string) Revocation {
	return Revocation{Kind: Gap, At: since.UTC(), Detail: []string{since.UTC().Format(time.RFC3339Nano), reason}}
}

// The restore kinds of a restore line's Detail[0].
const (
	RestoreArchive    = "archive"
	RestoreRevert     = "revert"
	RestoreCheckpoint = "checkpoint"
	RestoreExternal   = "external"
)

// RestoreInfo is what a restore line records: Detail = [kind, archive or
// source ID, boundary seq, restore journal ID]. The fourth element is
// F4-11's: it lets a boot that crashed between appending the line and
// journaling `restore_line` find its own line instead of appending a second
// (05-backup §5B.3.6 (5), "never duplicated"); an external restore has no
// restore journal and an empty one.
type RestoreInfo struct {
	Kind      string
	Source    string
	Boundary  uint64
	RestoreID string
}

// RestoreLineOf is the restore line recording info.
func RestoreLineOf(info RestoreInfo, at time.Time) Revocation {
	return Revocation{Kind: Restore, At: at.UTC(),
		Detail: []string{info.Kind, info.Source, strconv.FormatUint(info.Boundary, 10), info.RestoreID}}
}

// RestoreInfo reads a restore line's Detail. ok is false for another kind of
// line or a Detail that does not hold the layout.
func (r Revocation) RestoreInfo() (info RestoreInfo, ok bool) {
	if r.Kind != Restore || len(r.Detail) < 3 {
		return info, false
	}
	b, err := strconv.ParseUint(r.Detail[2], 10, 64)
	if err != nil {
		return info, false
	}
	info = RestoreInfo{Kind: r.Detail[0], Source: r.Detail[1], Boundary: b}
	if len(r.Detail) > 3 {
		info.RestoreID = r.Detail[3]
	}
	return info, true
}

// AbortedLine is the restore_aborted line of the restore line at seq:
// Detail = [that line's seq].
func AbortedLine(restoreSeq uint64, at time.Time) Revocation {
	return Revocation{Kind: RestoreAborted, At: at.UTC(), Detail: []string{strconv.FormatUint(restoreSeq, 10)}}
}

// AbortedSeq reads a restore_aborted line's Detail: the seq of the restore
// line it closes.
func (r Revocation) AbortedSeq() (uint64, bool) {
	if r.Kind != RestoreAborted || len(r.Detail) < 1 {
		return 0, false
	}
	n, err := strconv.ParseUint(r.Detail[0], 10, 64)
	return n, err == nil
}

// AbortedRestores is the set of restore-line seqs that were aborted: those a
// restore_aborted line names, and those whose restore journal reads
// swapped_back (swappedBack answers by restore journal ID; nil means none
// did). Such a restore line is neither resumed nor evidence, and the hook lines
// carrying its seq are never re-applied (§5B.3.6 (3), DATA6-3, DATA8-7).
func (c *Chain) AbortedRestores(swappedBack func(restoreID string) bool) map[uint64]bool {
	out := map[uint64]bool{}
	for _, l := range c.Lines {
		switch l.Kind {
		case RestoreAborted:
			if n, ok := l.AbortedSeq(); ok {
				out[n] = true
			}
		case Restore:
			if info, ok := l.RestoreInfo(); ok && info.RestoreID != "" && swappedBack != nil && swappedBack(info.RestoreID) {
				out[l.Seq] = true
			}
		}
	}
	return out
}

// hookedKey is c/backup/restore_hooked/<seq> with the seq in fixed width so
// the keys sort by seq.
func hookedKey(seq uint64) []byte { return []byte(fmt.Sprintf("%s%020d", hookedPrefix, seq)) }

type hookedRecord struct {
	At     time.Time      `json:"at"`
	Counts map[string]int `json:"counts,omitempty"`
}

// MarkHooked records, synchronously, that the restore hooks of the restore line
// seq ran to completion (05-backup §5B.3.4 step h: the journal's hooks_done).
// A restore line without this record is resumed by the next boot.
func MarkHooked(ctx context.Context, ks storage.Keyspace, seq uint64, at time.Time, counts map[string]int) error {
	raw, err := json.Marshal(hookedRecord{At: at.UTC(), Counts: counts})
	if err != nil {
		return err
	}
	b := ks.NewBatch()
	defer func() { _ = b.Close() }()
	if err := b.In(ks).Set(hookedKey(seq), raw); err != nil {
		return err
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return fmt.Errorf("journal: record %s%d: %w", hookedPrefix, seq, err)
	}
	return nil
}

// Hooked reports whether MarkHooked recorded the restore line seq.
func Hooked(ctx context.Context, ks storage.Reader, seq uint64) (bool, error) {
	_, err := ks.Get(ctx, hookedKey(seq))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}
