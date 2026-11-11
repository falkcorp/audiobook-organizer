// file: core/auth/reapply.go
// version: 1.0.0
// guid: 7d4e2a91-c5b8-4f03-a6d1-3e9f8b20c4a7
// last-edited: 2026-11-11

package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/storage"
)

// StageReapply stages the effect of journaled revocations on the identity
// store into b, the caller's batch, and writes nothing itself (05-backup
// §5B.3.6 "Boot re-apply" (3)). It is the same-store re-apply: a store whose
// c/backup/journal_head lags the journal (a crash between a line's fsync and
// its batch commit, a failed commit) never lost a credential change, it only
// lacks the journal's last lines, so the boot re-applies them narrowing-only
// and idempotently, in the same batch that advances the head. Unlike
// PostRestore it runs no hook: it deletes no session or one-time token that a
// line did not name, bumps no epoch, derives no restriction of its own, and
// appends no journal line (the lines are already journaled). A line that names
// no row (a restore, gap or restore_aborted line, a role kind) is ignored; role
// kinds are rbac's StageReapply.
//
// b must be an indexed batch over the identity keyspace (RestoreDeps.NewBatch
// builds one); the caller commits it and, on storage.ErrConflict, calls
// StageReapply again with a fresh one, which re-reads everything it decides on.
// It returns the per-family counts of what it changed.
//
// The lines are applied in order, sequentially: the journal's order is the
// order the revocations were made in, and a later line may depend on an
// earlier one (a delete after a disable).
func StageReapply(ctx context.Context, d RestoreDeps, b *storage.Batch, lines []journal.Revocation) (RestoreCounts, error) {
	if d.Store == nil {
		return nil, errors.New("auth: reapply: missing deps [Store]")
	}
	if b == nil {
		return nil, errors.New("auth: reapply: no batch")
	}
	p, err := d.load(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: reapply: %w", err)
	}
	for _, r := range lines {
		if err := p.reapply(ctx, r); err != nil {
			return nil, fmt.Errorf("auth: reapply: %w", err)
		}
	}
	if err := p.stage(ctx, b); err != nil {
		return nil, fmt.Errorf("auth: reapply: %w", err)
	}
	return p.counts, nil
}
