// file: internal/database/pebble_store_narrator_fold_index.go
// version: 1.0.0
// guid: 5f764ea0-8d25-4eae-9970-4bfcdacfce3d
// last-edited: 2026-10-30

package database

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

var narratorFoldLog = logger.New("database.narrator-fold-index")

// The narrator fold index answers "which narrator records spell this name,
// give or take case, accents, punctuation and spacing" with a prefix scan.
//
//	narrator_fold:<fold>:<narrator id>  ->  (empty value)
//
// fold is util.FoldKey of the record's name: letters and digits only, so the
// ':' after it ends it unambiguously and the range [narrator_fold:abc:,
// narrator_fold:abc;) cannot hold "abcd"'s keys. One key per (fold, id) rather
// than one key per fold holding an id list means no read-modify-write, a
// delete that can only remove its own key (so no ownership check, unlike
// narrator_name:), and a reconcile that is just "set the keys that are absent".
// A name whose fold is empty (no letter or digit) is not indexed and matches
// nothing by fold.
//
// WHY. narrator_name: is keyed by util.NormalizeAuthor, which folds case and
// whitespace only. A rescan that reads an unaccented tag back ("Renee Montoya")
// found no "Renée Montoya" under it and CreateNarrator minted a second record.
// resolveNarratorFolded (called by resolveNarratorCredit) consults this index
// before it creates one.
//
// WRITERS. Every writer of a narrator: row, in the same batch as the row:
// CreateNarrator (Set) and DeleteNarrator (Delete). The store has no narrator
// rename. A merge (credits_backfill, narrator_split_joined, narrator_purge_empty)
// repoints book_narrators rows and ends in DeleteNarrator, so it needs no
// index work of its own. Both writers hold nameIdx.narrator.
//
// READ CONTRACT. A missing key hides a record from the fold lookup (the caller
// then creates a duplicate, the pre-index behaviour); an extra key is harmless,
// because every candidate is point-verified (the row exists and its name still
// folds to the key). So only a missing key can be wrong, and the index is
// trusted only once this process has finished a reconcile (narratorFoldTrusted,
// cleared by Reset). Until then, and for any caller that cannot rely on the
// index, the lookup scans ListNarrators, which is thousands of small rows.
// The trust is per process rather than a persisted sentinel alone because a
// binary that predates the index writes narrators with no fold key while an
// old sentinel would still vouch for the index.
//
// RECONCILE runs on every boot (ReconcileNarratorFoldIndex), writes only keys
// that are absent, and sets narratorFoldSentinel when it completes. The
// sentinel records that a full pass has run on this database; it is not what
// readers trust.
const (
	narratorFoldPrefix = "narrator_fold:"

	// narratorFoldSentinel is set when a reconcile finishes. Bump the version if
	// the key format or the set of indexed names changes.
	narratorFoldSentinel = "system:backfill:narrator_fold_index_v1_done"

	narratorFoldChunk      = 256
	narratorFoldMaxWorkers = 8
	// narratorFoldResolveAttempts bounds resolveNarratorFolded's retries when a
	// concurrent writer keeps changing the answer between its lock-free read and
	// its locked re-check.
	narratorFoldResolveAttempts = 4
)

// narratorFoldKey is the index key for (fold, id).
func narratorFoldKey(fold string, id int) []byte {
	return []byte(narratorFoldPrefix + fold + ":" + strconv.Itoa(id))
}

// narratorFoldBounds is the scan range holding exactly one fold's keys.
func narratorFoldBounds(fold string) (lower, upper []byte) {
	lower = []byte(narratorFoldPrefix + fold + ":")
	return lower, prefixEnd(lower)
}

// stageNarratorFoldSet stages the fold key of n on batch. A name that folds to
// nothing is not indexed.
func stageNarratorFoldSet(batch *GatedBatch, n *Narrator) error {
	fold := util.FoldKey(n.Name)
	if fold == "" {
		return nil
	}
	return batch.Set(narratorFoldKey(fold, n.ID), []byte{}, nil)
}

// stageNarratorFoldDelete stages the removal of n's fold key on batch.
func stageNarratorFoldDelete(batch *GatedBatch, n *Narrator) error {
	fold := util.FoldKey(n.Name)
	if fold == "" {
		return nil
	}
	return batch.Delete(narratorFoldKey(fold, n.ID), nil)
}

// narratorsByFold returns every narrator record whose name folds to the same
// key as name, ordered by id. The index answers once this process has
// reconciled it; before that a ListNarrators scan does. Every candidate is
// point-verified.
func (p *PebbleStore) narratorsByFold(name string) ([]Narrator, error) {
	fold := util.FoldKey(name)
	if fold == "" {
		return nil, nil
	}
	if !p.nameIdx.narratorFoldTrusted.Load() {
		all, err := p.ListNarrators()
		if err != nil {
			return nil, err
		}
		var out []Narrator
		for _, n := range all {
			if util.FoldKey(n.Name) == fold {
				out = append(out, n)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	lower, upper := narratorFoldBounds(fold)
	var ids []int
	if err := forEachKeyInRange(p.db, lower, upper, func(k, _ []byte) error {
		id, err := strconv.Atoi(strings.TrimPrefix(string(k), string(lower)))
		if err != nil {
			// Not a key this code wrote; it names no record.
			narratorFoldLog.Warn("ignoring malformed key %q", logger.SanitizeLogValue(string(k)))
			return nil
		}
		ids = append(ids, id)
		return nil
	}); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	var out []Narrator
	for _, id := range ids {
		n, err := p.GetNarratorByID(id)
		if err != nil {
			return nil, err
		}
		if n == nil || util.FoldKey(n.Name) != fold {
			continue
		}
		out = append(out, *n)
	}
	return out, nil
}

// matchNarrator resolves name to an existing record, or (nil, nil) when none
// matches. An exact normalized-name match wins; otherwise the single fold
// match; otherwise, among several fold matches, the record with the most
// credits (lowest id on a tie), which is logged at Warn with every candidate.
func (p *PebbleStore) matchNarrator(name string) (*Narrator, error) {
	exact, err := p.GetNarratorByName(name)
	if err != nil {
		return nil, err
	}
	if exact != nil {
		return exact, nil
	}
	cands, err := p.narratorsByFold(name)
	if err != nil {
		return nil, err
	}
	switch len(cands) {
	case 0:
		return nil, nil
	case 1:
		return &cands[0], nil
	}
	return p.pickNarratorByCredits(name, cands)
}

// pickNarratorByCredits chooses among several fold-matching records: most
// books credited, lowest id on a tie. One pass over the junction counts them
// all.
func (p *PebbleStore) pickNarratorByCredits(name string, cands []Narrator) (*Narrator, error) {
	want := make(map[int]bool, len(cands))
	for _, c := range cands {
		want[c.ID] = true
	}
	linking, err := p.BookNarratorsLinking(want)
	if err != nil {
		return nil, fmt.Errorf("count credits of %d narrators folding with %q: %w", len(cands), name, err)
	}
	credits := make(map[int]int, len(cands))
	for _, rows := range linking {
		counted := make(map[int]bool, len(rows))
		for _, r := range rows {
			if want[r.NarratorID] && !counted[r.NarratorID] {
				counted[r.NarratorID] = true
				credits[r.NarratorID]++
			}
		}
	}
	best := 0
	for i := 1; i < len(cands); i++ {
		if credits[cands[i].ID] > credits[cands[best].ID] {
			best = i
		}
	}
	parts := make([]string, 0, len(cands))
	for _, c := range cands {
		parts = append(parts, fmt.Sprintf("%d (%d books)", c.ID, credits[c.ID]))
	}
	narratorFoldLog.Warn("narrator %q matches %d records by fold key [%s]; linking to %d, merge the others",
		logger.SanitizeLogValue(name), len(cands), strings.Join(parts, ", "), cands[best].ID)
	chosen := cands[best]
	return &chosen, nil
}

// resolveNarratorFolded is resolve-or-create for a credit name that also
// matches records spelled differently (accents, hyphens, case, spacing). It is
// what resolveNarratorCredit calls; CreateNarrator keeps its exact-name
// semantics for its other callers.
//
// The check and the create are one step against every other narrator writer:
// the lock-free read misses, then nameIdx.narrator is taken and the match is
// re-checked before the record is minted, so two goroutines resolving
// "Renée Montoya" and "Renee Montoya" on an empty store mint ONE record. The
// re-check only asks whether anything matches; if something does, the lock is
// dropped and the match is resolved (and a tie broken, which can scan the
// junction) outside it.
func (p *PebbleStore) resolveNarratorFolded(name string) (*Narrator, error) {
	for range narratorFoldResolveAttempts {
		n, err := p.matchNarrator(name)
		if err != nil || n != nil {
			return n, err
		}
		created, done, err := p.createNarratorIfUnmatched(name)
		if err != nil || done {
			return created, err
		}
	}
	return nil, fmt.Errorf("resolve narrator %q: matching records kept changing", name)
}

// createNarratorIfUnmatched takes nameIdx.narrator and, when nothing matches
// name by exact name or fold key, mints the record (done true). When something
// matches it returns done false and the caller re-resolves.
func (p *PebbleStore) createNarratorIfUnmatched(name string) (*Narrator, bool, error) {
	p.nameIdx.narrator.Lock()
	defer p.nameIdx.narrator.Unlock()
	exact, err := p.GetNarratorByName(name)
	if err != nil {
		return nil, false, err
	}
	if exact != nil {
		return nil, false, nil
	}
	cands, err := p.narratorsByFold(name)
	if err != nil {
		return nil, false, err
	}
	if len(cands) > 0 {
		return nil, false, nil
	}
	n, err := p.mintNarratorLocked(name)
	if err != nil {
		return nil, false, err
	}
	return n, true, nil
}

// NarratorFoldIndexTrusted reports whether this process has finished a
// reconcile, which is what lets narratorsByFold read the index instead of
// scanning the records.
func (p *PebbleStore) NarratorFoldIndexTrusted() bool {
	return p.nameIdx.narratorFoldTrusted.Load()
}

// NarratorFoldIndexReport summarises one ReconcileNarratorFoldIndex run.
type NarratorFoldIndexReport struct {
	// Scanned is how many narrator records the pass looked at.
	Scanned int `json:"scanned"`
	// Written is how many absent fold keys it wrote.
	Written int `json:"written"`
	// FirstBuild is true when the sentinel was not set before the pass.
	FirstBuild bool          `json:"first_build"`
	Duration   time.Duration `json:"duration"`
}

// ReconcileNarratorFoldIndex writes every narrator's fold key that is absent,
// then sets the sentinel and marks the index trusted for this process. It is
// the boot repair: the first boot after the index shipped builds it, every
// later boot is a read-mostly pass that heals what a binary without the index
// left. It never deletes a key (extras are harmless, see the read contract).
//
// CONCURRENCY. The records are read once, split into narratorFoldChunk-sized
// chunks, and fanned out over a bounded pool (NumCPU, capped at
// narratorFoldMaxWorkers). A worker folds its chunk's names and probes for the
// absent keys without a lock, then takes nameIdx.narrator only for the commit,
// re-verifying each row there so a concurrent DeleteNarrator cannot be undone
// by a stale Set. Chunks are disjoint, so no two workers write the same key.
// Each commit is pebble.Sync and the sentinel goes in only after every worker
// has returned, so an interrupted pass loses nothing: the next one rescans.
func (p *PebbleStore) ReconcileNarratorFoldIndex(ctx context.Context) (NarratorFoldIndexReport, error) {
	var rep NarratorFoldIndexReport
	start := time.Now()
	built, err := p.narratorFoldSentinelSet()
	if err != nil {
		return rep, err
	}
	rep.FirstBuild = !built

	all, err := p.ListNarrators()
	if err != nil {
		return rep, fmt.Errorf("narrator fold index: list narrators: %w", err)
	}
	rep.Scanned = len(all)

	var written atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, min(runtime.NumCPU(), narratorFoldMaxWorkers)))
	for lo := 0; lo < len(all); lo += narratorFoldChunk {
		chunk := all[lo:min(lo+narratorFoldChunk, len(all))]
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			n, err := p.reconcileNarratorFoldChunk(chunk)
			written.Add(int64(n))
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return rep, fmt.Errorf("narrator fold index: %w", err)
	}
	rep.Written = int(written.Load())
	if err := p.db.Set([]byte(narratorFoldSentinel), []byte("1"), pebble.Sync); err != nil {
		return rep, fmt.Errorf("narrator fold index: set sentinel: %w", err)
	}
	p.nameIdx.narratorFoldTrusted.Store(true)
	rep.Duration = time.Since(start)
	narratorFoldLog.Info("reconciled: narrators=%d keys_written=%d first_build=%t duration=%s",
		rep.Scanned, rep.Written, rep.FirstBuild, rep.Duration.Round(time.Millisecond))
	return rep, nil
}

// narratorFoldSentinelSet reports whether a reconcile has ever completed.
func (p *PebbleStore) narratorFoldSentinelSet() (bool, error) {
	_, closer, err := p.db.Get([]byte(narratorFoldSentinel))
	switch {
	case err == nil:
		closer.Close()
		return true, nil
	case errors.Is(err, pebble.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("read %s: %w", narratorFoldSentinel, err)
	}
}

// reconcileNarratorFoldChunk writes the absent fold keys of one chunk and
// returns how many it wrote.
func (p *PebbleStore) reconcileNarratorFoldChunk(chunk []Narrator) (int, error) {
	var missing []Narrator
	for _, n := range chunk {
		fold := util.FoldKey(n.Name)
		if fold == "" {
			continue
		}
		_, closer, err := p.db.Get(narratorFoldKey(fold, n.ID))
		if err == nil {
			closer.Close()
			continue
		}
		if !errors.Is(err, pebble.ErrNotFound) {
			return 0, err
		}
		missing = append(missing, n)
	}
	if len(missing) == 0 {
		return 0, nil
	}
	p.nameIdx.narrator.Lock()
	defer p.nameIdx.narrator.Unlock()
	batch := p.db.NewBatch()
	staged := 0
	for _, n := range missing {
		cur, err := p.GetNarratorByID(n.ID)
		if err != nil {
			batch.Close()
			return 0, err
		}
		if cur == nil || util.FoldKey(cur.Name) != util.FoldKey(n.Name) {
			continue // deleted since the list was read
		}
		if err := stageNarratorFoldSet(batch, cur); err != nil {
			batch.Close()
			return 0, err
		}
		staged++
	}
	if staged == 0 {
		batch.Close()
		return 0, nil
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, err
	}
	return staged, nil
}
