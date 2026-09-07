// file: internal/organizer/collision.go
// version: 1.1.0
// guid: 5b1f7c2a-9d34-4e18-8f60-c7a2b4d91e03
// last-edited: 2026-09-07

// Pre-flight collision resolution for RenameFiles.
//
// # The bug this closes
//
// RenameFiles publishes each parked temp with finalizeExclusive, which refuses
// (EEXIST) rather than replacing an occupant. That refusal is CORRECT and is
// deliberately not changed here: saferename.go:22-23 and fileops/reflink.go:27-36
// put the obligation on the CALLER — "a caller that wants replacement must
// remove the destination itself". Six callers depend on that primitive.
//
// What was missing is the caller-side resolver. RenameFiles only ever detected
// collisions WITHIN one book's own plan (planPass's `seen` map); a target
// already owned on disk by a DIFFERENT file was never looked at, so
// metadata.batch-apply-cached failed permanently with
// `rename files: ... link <tmp-rename-nonce> <dest>: file already exists`
// on every run, forever, for the same books.
//
// # Why the pass runs BEFORE phase 1
//
// rollbackRenameTemps (pipeline.go) can return a parked temp to its source, but
// it cannot resurrect an occupant that was removed. So every collision for a
// book is decided BEFORE any of that book's files is parked as a temp. A
// resolution that fails mid-book therefore never leaves a half-published book
// with a destroyed occupant.
//
// The pass is itself journalled (collisionJournal): quarantine moves and row
// repoints are recorded with their inverse and undone on any later failure in
// phase 1 or phase 2, alongside rollbackRenameTemps. Ordering alone is not
// enough — a pre-flight mutation is still a mutation.
//
// # Non-destructive by construction
//
//   - No code path here calls os.Remove/unlink on a library audio file. The
//     losing file is MOVED to a quarantine tree (quarantineCollisionSource),
//     which keeps the action reversible.
//   - No code path here deletes a book_file row. The surviving row is REPOINTED
//     at the kept file; a row whose file was quarantined is tombstoned by
//     setting Missing=true (book_file has no deleted_at/Active column — see
//     database/store.go's BookFile — so Missing is the tombstone this schema
//     has, and it is TRUE: the row's path no longer has bytes).
package organizer

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// CollisionStore is the narrow store surface the resolver needs. It is
// deliberately four methods rather than database.Store: the resolver reads the
// row that owns an occupied target, rehydrates its own row, and writes both
// back. Nothing else.
type CollisionStore interface {
	// GetBookFileByPath finds the row that claims filePath, or (nil, nil).
	GetBookFileByPath(filePath string) (*database.BookFile, error)
	// GetBookFileByID rehydrates the FULL record. UpdateBookFile is a
	// full-record replacement, so a partial record would wipe the
	// fingerprint/transcript/tags — the same idiom recover_missing_files.go
	// and missing_file_repoint.go use.
	GetBookFileByID(bookID, fileID string) (*database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// CollisionPolicy switches the pre-flight pass on and supplies everything it
// needs. A nil *CollisionPolicy passed to RenameFiles means "no pre-flight
// resolution" — an occupied target then fails the batch exactly as it did
// before this file existed. That is the explicit opt-out; it is not the default
// for the write-back callers, which all had the permanent-failure bug.
type CollisionPolicy struct {
	// RootDir is the library boundary. It is the SAME notion organize uses
	// (organizer.go's o.config.RootDir), not a second one. Empty RootDir
	// disables quarantine (there is nowhere safe to put a loser) and makes an
	// unresolvable collision a reported failure instead.
	RootDir string

	// BookID owns these entries; used for the quarantine layout and logging.
	BookID string

	// Store enables the stored-FileHash rung and all row repointing. nil
	// leaves the resolver filesystem-only: it can still decide same-inode,
	// size-mismatch and live-SHA identity, and can still retarget, but it will
	// not quarantine (quarantining without repointing the row would strand the
	// row at a path with no bytes).
	Store CollisionStore

	// hashFile is the identity digest. Defaults to filehash.BookFileHash — the
	// one algorithm that fills book_files.file_hash, so a live hash is
	// comparable with a stored one. Overridable in tests only.
	hashFile func(string) (string, error)
}

func (p *CollisionPolicy) hasher() func(string) (string, error) {
	if p != nil && p.hashFile != nil {
		return p.hashFile
	}
	return filehash.BookFileHash
}

// collisionHashSem bounds live-SHA work across the WHOLE process, not per book.
//
// CLAUDE.md's concurrency mandate names this exact hotspot shape: hashing
// multi-GB .m4b files over a library-scale collection. The per-book errgroup
// below is bounded too, but metadata.batch-apply-cached runs books in parallel
// at writeBackWorkers(), so a per-book limit alone multiplies. This package
// semaphore is the real ceiling; the errgroup keeps one book from queueing the
// whole pool.
var collisionHashSem = make(chan struct{}, max(2, runtime.NumCPU()))

// CollisionAction names what the resolver did for one entry.
type CollisionAction string

const (
	// CollisionQuarantinedSource: the occupant is byte-identical to our source
	// AND is already sitting at the organized target, so the occupant is by
	// definition the better-organized copy. Our source is quarantined and the
	// row follows the kept file.
	CollisionQuarantinedSource CollisionAction = "quarantined-source"
	// CollisionAlreadyLinked: the occupant resolves OUTSIDE the library root
	// (a symlink out of the tree, from the `symlink` organization strategy) and
	// passes the size + re-stat proxy against our source. Nothing is moved.
	CollisionAlreadyLinked CollisionAction = "already-linked"
	// CollisionRetargeted: the occupant is a different file. The entry falls
	// back to organize's own _copyN ladder (nextAvailableTargetPath) so apply
	// and organize resolve a genuine name clash the same way.
	CollisionRetargeted CollisionAction = "retargeted"
)

// CollisionResolution is one resolved collision, reported so a caller can log
// or persist what happened. Every field is filled for every action.
type CollisionResolution struct {
	SegmentID      string          `json:"segment_id"`
	SourcePath     string          `json:"source_path"`
	OccupantPath   string          `json:"occupant_path"`
	OriginalTarget string          `json:"original_target"`
	FinalTarget    string          `json:"final_target"`
	QuarantinePath string          `json:"quarantine_path,omitempty"`
	Action         CollisionAction `json:"action"`
	Detail         string          `json:"detail"`
}

// CollisionFailure is a collision the resolver could NOT resolve. It carries
// the occupant fingerprint the durable-failure record needs to self-heal: when
// the occupant is gone, or its size/mtime changed, the block has cleared and
// the book is retried without any flag.
type CollisionFailure struct {
	SegmentID       string `json:"segment_id"`
	SourcePath      string `json:"source_path"`
	TargetPath      string `json:"target_path"`
	OccupantPath    string `json:"occupant_path"`
	OccupantSize    int64  `json:"occupant_size"`
	OccupantModUnix int64  `json:"occupant_mod_unix"`
	Reason          string `json:"reason"`
}

// ErrUnresolvedCollision wraps a batch that stopped because at least one
// collision could not be resolved. Callers test with errors.As on
// *CollisionError to reach the per-entry detail.
type CollisionError struct {
	Failures []CollisionFailure
}

func (e *CollisionError) Error() string {
	parts := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		parts = append(parts, fmt.Sprintf("%s occupied by %s: %s", f.TargetPath, f.OccupantPath, f.Reason))
	}
	return "unresolved path collision: " + strings.Join(parts, "; ")
}

// ---------------------------------------------------------------------------
// journal
// ---------------------------------------------------------------------------

type quarantineMove struct {
	original   string
	quarantine string
}

type rowRestore struct {
	fileID string
	prior  *database.BookFile
}

// collisionJournal records the inverse of every mutation the pre-flight pass
// made, so a failure LATER in RenameFiles (phase 1 or phase 2) can put the tree
// and the rows back. rollbackRenameTemps knows nothing about these mutations;
// without the journal a book that failed on its second file would be left with
// its first file's source in quarantine and its row repointed, which is not
// data loss but is not a clean rollback either.
type collisionJournal struct {
	moves []quarantineMove
	rows  []rowRestore
	store CollisionStore
}

func (j *collisionJournal) empty() bool {
	return j == nil || (len(j.moves) == 0 && len(j.rows) == 0)
}

// rollback undoes the journal newest-first. Every failure is loud and recorded
// in result.Errors — the same posture rollbackRenameTemps takes, and for the
// same reason: a file left in quarantine whose row points elsewhere is
// invisible to the library and must not be dropped silently.
func (j *collisionJournal) rollback(result *RenameFilesResult) {
	if j == nil {
		return
	}
	for i := len(j.rows) - 1; i >= 0; i-- {
		r := j.rows[i]
		if j.store == nil || r.prior == nil {
			continue
		}
		if err := j.store.UpdateBookFile(r.fileID, r.prior); err != nil {
			slog.Error("collision rollback failed — book_file row left in its resolved state",
				"file_id", r.fileID, "restore_path", r.prior.FilePath, "error", err)
			result.Errors = append(result.Errors, fmt.Sprintf(
				"collision rollback: could not restore book_file %s to %s: %v", r.fileID, r.prior.FilePath, err))
		}
	}
	for i := len(j.moves) - 1; i >= 0; i-- {
		m := j.moves[i]
		// moveExclusive, not os.Rename: the original name was vacated moments
		// ago and a concurrent worker can have landed its own file there.
		// Refusing leaves the file in quarantine (reported); replacing would
		// destroy the other one silently.
		if err := moveExclusive(m.quarantine, m.original); err != nil {
			slog.Error("collision rollback failed — file left in quarantine",
				"quarantine_path", m.quarantine, "original_path", m.original, "error", err)
			result.Errors = append(result.Errors, fmt.Sprintf(
				"collision rollback: file stranded in quarantine at %s (original %s): %v",
				m.quarantine, m.original, err))
		}
	}
	j.moves = nil
	j.rows = nil

	// Resolutions describe decisions that have just been undone. Leaving them
	// on the result would report a quarantine whose file is back at its source
	// and a repoint that has been reverted. Anything the rollback could NOT
	// undo was appended to result.Errors above, which is the accurate record.
	result.Resolutions = nil
}

// ---------------------------------------------------------------------------
// identity ladder
// ---------------------------------------------------------------------------

type identityVerdict int

const (
	// identityUnknown: nothing decided yet; the next rung must run.
	identityUnknown identityVerdict = iota
	// identityIdentical: the two paths hold the same bytes (or literally the
	// same inode).
	identityIdentical
	// identityDifferent: the two paths hold different bytes.
	identityDifferent
)

// classifyCheap runs every rung that costs no file reads, in cost order. It
// NEVER hashes. A verdict of identityUnknown means the caller must run the SHA
// rung for this pair.
//
// Rungs, cheapest first:
//  1. os.SameFile — inode identity. Catches an already-hardlinked pair for
//     free. NOTE it does NOT catch a reflink: a reflink is a different inode
//     sharing extents (see fileops/reflink.go), so a CoW clone falls through
//     to the size/hash rungs like any other pair.
//  2. size mismatch — a fast, certain reject.
//  3. stored FileHash on both rows — the same value organize's
//     GetBookByFileHash dedup reads, produced by filehash.BookFileHash. Two
//     stored hashes that disagree is a certain reject; two that agree is a
//     certain match, at the cost of two index reads and no file I/O.
func classifyCheap(srcInfo, dstInfo os.FileInfo, srcHash, dstHash string) identityVerdict {
	if os.SameFile(srcInfo, dstInfo) {
		return identityIdentical
	}
	if srcInfo.Size() != dstInfo.Size() {
		return identityDifferent
	}
	if srcHash != "" && dstHash != "" {
		if srcHash == dstHash {
			return identityIdentical
		}
		return identityDifferent
	}
	return identityUnknown
}

// sameByReflinkProxy is the "already linked" test for an occupant that lives
// OUTSIDE the library root.
//
// ⚠️ THIS IS A PROXY, AND IT IS WEAKER THAN TRUE EXTENT IDENTITY. It answers
// "these two files have the same length, twice in a row" — not "these two files
// share extents". A true test would read the ZFS block-cloning / FIEMAP extent
// map, and this deliberately does not: it reuses the interlock the one shipped
// precedent uses, recover_missing_files.go:829-830 (Branch B), which matches an
// outside candidate on size alone and re-stats immediately before acting.
// Consistency with the shipped precedent was chosen over a stronger, unshipped
// probe. os.SameFile is not a substitute — it catches only hardlinks (one
// inode), and a reflink is a different inode.
//
// The re-stat is the interlock: sizes are compared, then BOTH files are stat-ed
// again and required to be unchanged, so a file being written underneath us
// fails the test rather than passing it.
func sameByReflinkProxy(srcPath, dstPath string, srcInfo, dstInfo os.FileInfo) (bool, error) {
	if srcInfo.Size() != dstInfo.Size() {
		return false, nil
	}
	srcAgain, err := os.Stat(srcPath)
	if err != nil {
		return false, fmt.Errorf("re-stat %s: %w", srcPath, err)
	}
	dstAgain, err := os.Stat(dstPath)
	if err != nil {
		return false, fmt.Errorf("re-stat %s: %w", dstPath, err)
	}
	if srcAgain.Size() != srcInfo.Size() || dstAgain.Size() != dstInfo.Size() {
		return false, nil
	}
	if !srcAgain.ModTime().Equal(srcInfo.ModTime()) || !dstAgain.ModTime().Equal(dstInfo.ModTime()) {
		return false, nil
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// the pass
// ---------------------------------------------------------------------------

// collisionCandidate is one entry whose target is occupied, carried between the
// cheap pass and the (bounded, parallel) hash pass.
type collisionCandidate struct {
	idx      int // index into the entries slice
	entry    FileRenameEntry
	srcInfo  os.FileInfo
	dstInfo  os.FileInfo
	occupant *database.BookFile // row that claims the target, or nil
	outside  bool               // occupant resolves outside RootDir
	verdict  identityVerdict
}

// resolveTargetCollisions is the pre-flight pass. It returns the entries that
// should still be renamed (possibly with a rewritten TargetPath), the journal
// of what it changed, and the resolutions it performed.
//
// It runs to completion or fails whole: on any error every mutation it already
// made is rolled back before returning, so the caller never sees a half-applied
// pass.
func resolveTargetCollisions(entries []FileRenameEntry, policy *CollisionPolicy, result *RenameFilesResult) ([]FileRenameEntry, *collisionJournal, error) {
	journal := &collisionJournal{}
	if policy == nil || len(entries) == 0 {
		return entries, journal, nil
	}
	journal.store = policy.Store

	// --- Pass 1: find occupied targets and decide everything that is free. ---
	var candidates []collisionCandidate
	var failures []CollisionFailure
	for i, entry := range entries {
		dstLink, err := os.Lstat(entry.TargetPath)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // target free — the overwhelmingly common case
			}
			journal.rollback(result)
			return nil, journal, fmt.Errorf("stat rename target %s: %w", entry.TargetPath, err)
		}
		srcInfo, err := os.Stat(entry.SourcePath)
		if err != nil {
			journal.rollback(result)
			return nil, journal, fmt.Errorf("stat rename source %s: %w", entry.SourcePath, err)
		}
		// Our own source sitting at the target is not a collision.
		if entry.SourcePath == entry.TargetPath {
			continue
		}

		c := collisionCandidate{idx: i, entry: entry, srcInfo: srcInfo, dstInfo: dstLink}

		// A directory at the target can never be "the better-organized copy"
		// and must never be crowned. Retarget.
		if dstLink.IsDir() {
			c.verdict = identityDifferent
			candidates = append(candidates, c)
			continue
		}
		// Identity is about BYTES, so from here on the occupant is followed
		// through a symlink: an Lstat of a symlink reports the length of the
		// link text, which would make every symlinked occupant look like a
		// tiny file with different content. A dangling symlink has no bytes to
		// compare, and os.Link would refuse the target anyway, so it takes the
		// retarget branch.
		dstInfo, serr := os.Stat(entry.TargetPath)
		if serr != nil || !dstInfo.Mode().IsRegular() {
			c.verdict = identityDifferent
			candidates = append(candidates, c)
			continue
		}
		c.dstInfo = dstInfo

		outside, oerr := occupantOutsideRoot(entry.TargetPath, policy.RootDir)
		if oerr != nil {
			// We cannot tell which side of the library boundary the occupant
			// is on, so we cannot pick a branch. Record it as unresolvable
			// rather than guessing.
			failures = append(failures, *collisionFailure(c, "cannot determine whether the occupant is inside the library: "+oerr.Error()))
			continue
		}
		c.outside = outside
		if c.outside {
			// Outside the library root: the size + re-stat proxy stands in for
			// extent identity. See sameByReflinkProxy's comment — it is a PROXY.
			same, perr := sameByReflinkProxy(entry.SourcePath, entry.TargetPath, srcInfo, dstInfo)
			if perr != nil {
				journal.rollback(result)
				return nil, journal, perr
			}
			if same {
				c.verdict = identityIdentical
			} else {
				c.verdict = identityDifferent
			}
			candidates = append(candidates, c)
			continue
		}

		// In-library. Look up the row that owns the target so the stored-hash
		// rung has something to compare against.
		if policy.Store != nil {
			row, lerr := policy.Store.GetBookFileByPath(entry.TargetPath)
			if lerr == nil {
				c.occupant = row
			}
		}
		occHash := ""
		if c.occupant != nil {
			occHash = c.occupant.FileHash
		}
		c.verdict = classifyCheap(srcInfo, dstInfo, entry.SourceHash, occHash)
		candidates = append(candidates, c)
	}

	// A pass-1 failure is decided before anything has been touched, so the
	// journal is still empty — but it is reported through exactly the same
	// CollisionError the later passes use, so the caller's durable-failure
	// record does not have to know which pass gave up.
	if len(failures) > 0 {
		result.Collisions = append(result.Collisions, failures...)
		journal.rollback(result)
		return nil, journal, &CollisionError{Failures: failures}
	}

	if len(candidates) == 0 {
		return entries, journal, nil
	}

	// --- Pass 2: the SHA rung, for candidates the cheap rungs could not
	// decide. Bounded twice over: a per-book errgroup so one book cannot
	// queue the whole pool, and a package-wide semaphore so parallel books
	// cannot multiply past NumCPU. ---
	if err := hashUndecided(candidates, policy); err != nil {
		journal.rollback(result)
		return nil, journal, err
	}

	// --- Pass 3: act. ---
	out := make([]FileRenameEntry, len(entries))
	copy(out, entries)
	dropped := make(map[int]bool, len(candidates))

	for _, c := range candidates {
		res, drop, ferr := applyCollisionDecision(c, policy, journal)
		if ferr != nil {
			failures = append(failures, *ferr)
			continue
		}
		if drop {
			// Dropped entries need nothing from the caller: the quarantine
			// branch has already repointed the row, and the already-linked
			// branch leaves a row that was correct to begin with. They are
			// reported through result.Resolutions below, not through a
			// separate list the caller would have to act on.
			dropped[c.idx] = true
		} else {
			out[c.idx] = res.entry
		}
		result.Resolutions = append(result.Resolutions, res.resolution)
		slog.Warn("apply rename: resolved a path collision",
			"book_id", policy.BookID,
			"action", res.resolution.Action,
			"source", res.resolution.SourcePath,
			"occupant", res.resolution.OccupantPath,
			"original_target", res.resolution.OriginalTarget,
			"final_target", res.resolution.FinalTarget,
			"quarantine", res.resolution.QuarantinePath,
			"detail", res.resolution.Detail)
	}

	if len(failures) > 0 {
		result.Collisions = append(result.Collisions, failures...)
		journal.rollback(result)
		return nil, journal, &CollisionError{Failures: failures}
	}

	kept := out[:0]
	for i, e := range out {
		if !dropped[i] {
			kept = append(kept, e)
		}
	}
	return kept, journal, nil
}

// hashUndecided fills in the verdict for every candidate the cheap rungs left
// at identityUnknown, by hashing both files with filehash.BookFileHash — the
// digest that fills book_files.file_hash, so the live value is comparable with
// a stored one.
func hashUndecided(candidates []collisionCandidate, policy *CollisionPolicy) error {
	hasher := policy.hasher()
	var g errgroup.Group
	g.SetLimit(max(1, runtime.NumCPU()))
	for i := range candidates {
		if candidates[i].verdict != identityUnknown {
			continue
		}
		c := &candidates[i]
		g.Go(func() error {
			collisionHashSem <- struct{}{}
			defer func() { <-collisionHashSem }()

			srcHash, err := hasher(c.entry.SourcePath)
			if err != nil {
				return fmt.Errorf("hash rename source %s: %w", c.entry.SourcePath, err)
			}
			dstHash, err := hasher(c.entry.TargetPath)
			if err != nil {
				return fmt.Errorf("hash collision occupant %s: %w", c.entry.TargetPath, err)
			}
			if srcHash == dstHash {
				c.verdict = identityIdentical
			} else {
				c.verdict = identityDifferent
			}
			return nil
		})
	}
	return g.Wait()
}

type collisionDecision struct {
	entry      FileRenameEntry
	resolution CollisionResolution
}

// applyCollisionDecision performs the branch chosen for one candidate. It
// returns (decision, drop, failure): drop=true means the entry needs no rename
// at all and must leave the batch.
func applyCollisionDecision(c collisionCandidate, policy *CollisionPolicy, journal *collisionJournal) (collisionDecision, bool, *CollisionFailure) {
	base := CollisionResolution{
		SegmentID:      c.entry.SegmentID,
		SourcePath:     c.entry.SourcePath,
		OccupantPath:   c.entry.TargetPath,
		OriginalTarget: c.entry.TargetPath,
		FinalTarget:    c.entry.TargetPath,
	}

	// --- Different content, same computed target. Fall back to organize's own
	// _copyN ladder rather than inventing a third policy for the same
	// situation: nextAvailableTargetPath is what OrganizeBook picks at
	// organizer.go:192-197 when a different file owns the target. ---
	if c.verdict == identityDifferent {
		next, err := nextAvailableTargetPath(c.entry.TargetPath)
		if err != nil {
			return collisionDecision{}, false, collisionFailure(c, fmt.Sprintf("no free _copyN name beside the occupant: %v", err))
		}
		e := c.entry
		e.TargetPath = next
		base.FinalTarget = next
		base.Action = CollisionRetargeted
		base.Detail = "occupant holds different content; using organize's _copyN ladder"
		return collisionDecision{entry: e, resolution: base}, false, nil
	}

	// --- Identical, occupant outside the library root. Nothing to move: the
	// library slot already resolves to our bytes. ---
	if c.outside {
		base.Action = CollisionAlreadyLinked
		base.Detail = "occupant resolves outside the library root and passes the size + re-stat proxy (see sameByReflinkProxy — this is a PROXY, not extent identity)"
		return collisionDecision{entry: c.entry, resolution: base}, true, nil
	}

	// --- Identical, occupant in-library. The occupant is ALREADY at the
	// organized target, so it is by definition the better-organized copy; our
	// source is the loser. Quarantine it (never unlink) and make the rows
	// follow the kept file. ---
	if policy.Store == nil {
		return collisionDecision{}, false, collisionFailure(c,
			"occupant is byte-identical but no store is wired, so the losing row cannot be repointed; refusing to quarantine a file whose row would be left pointing at nothing")
	}
	if strings.TrimSpace(policy.RootDir) == "" {
		return collisionDecision{}, false, collisionFailure(c,
			"occupant is byte-identical but RootDir is unset, so there is no quarantine tree to move the loser into")
	}

	// Rehydrate our own row FIRST: it is both the value we mutate and the
	// value the journal restores.
	prior, err := policy.Store.GetBookFileByID(policy.BookID, c.entry.SegmentID)
	if err != nil || prior == nil {
		return collisionDecision{}, false, collisionFailure(c,
			fmt.Sprintf("occupant is byte-identical but book_file %s could not be rehydrated: %v", c.entry.SegmentID, err))
	}

	qPath, err := quarantineCollisionSource(policy.RootDir, policy.BookID, c.entry.SourcePath)
	if err != nil {
		return collisionDecision{}, false, collisionFailure(c, fmt.Sprintf("quarantine the losing copy: %v", err))
	}
	journal.moves = append(journal.moves, quarantineMove{original: c.entry.SourcePath, quarantine: qPath})

	// Snapshot for rollback BEFORE mutating. UpdateBookFile is a full-record
	// replacement, so the snapshot must be the full record.
	snapshot := *prior
	journal.rows = append(journal.rows, rowRestore{fileID: prior.ID, prior: &snapshot})

	updated := *prior
	sameBookOccupantRow := c.occupant != nil && c.occupant.ID != prior.ID && c.occupant.BookID == prior.BookID
	if sameBookOccupantRow {
		// Another row of THIS book already points at the kept file. Repointing
		// ours there too would manufacture a duplicate row for one path.
		// Instead the occupant row survives untouched and ours is tombstoned:
		// its FilePath genuinely has no bytes any more, so Missing=true is a
		// true statement, not a marker (book_file has no deleted_at column).
		// mark-missing-files reconciles the flag in both directions, so this
		// self-corrects if the file ever comes back.
		updated.Missing = true
		base.Detail = "occupant is byte-identical and already organized; source quarantined, this row tombstoned (Missing=true) because a sibling row of the same book already points at the kept file"
	} else {
		// Repoint — never delete. The surviving row follows the kept file.
		updated.FilePath = c.entry.TargetPath
		updated.Missing = false
		base.Detail = "occupant is byte-identical and already organized; source quarantined and this row repointed at the kept file"
	}
	if err := policy.Store.UpdateBookFile(prior.ID, &updated); err != nil {
		return collisionDecision{}, false, collisionFailure(c, fmt.Sprintf("repoint book_file %s: %v", prior.ID, err))
	}

	base.Action = CollisionQuarantinedSource
	base.QuarantinePath = qPath
	return collisionDecision{entry: c.entry, resolution: base}, true, nil
}

func collisionFailure(c collisionCandidate, reason string) *CollisionFailure {
	f := &CollisionFailure{
		SegmentID:    c.entry.SegmentID,
		SourcePath:   c.entry.SourcePath,
		TargetPath:   c.entry.TargetPath,
		OccupantPath: c.entry.TargetPath,
		Reason:       reason,
	}
	if c.dstInfo != nil {
		f.OccupantSize = c.dstInfo.Size()
		f.OccupantModUnix = c.dstInfo.ModTime().Unix()
	}
	return f
}

// occupantOutsideRoot reports whether the file that occupies target actually
// lives outside the library root.
//
// target itself is always under RootDir (ComputeTargetPaths builds it with
// filepath.Join(rootDir, ...)), so this can only be true when the occupant is a
// SYMLINK pointing out of the tree — which is exactly what the `symlink`
// organization strategy leaves in the library.
//
// Only a NOT-EXIST resolution failure is treated as in-library: a broken link
// really is not evidence of an outside file. Every other error — a permission
// denial on some component of the link's path being the realistic one — is
// returned. Swallowing it would be the worse bug of the two: os.Stat follows
// the link and succeeds, so the pair would be compared on the OUTSIDE file's
// bytes while being classified as in-library, and a match would quarantine our
// source and repoint the row at a symlink leaving the tree. An undecidable
// boundary is an unresolvable collision, which the durable failure record is
// there to absorb.
func occupantOutsideRoot(target, rootDir string) (bool, error) {
	if strings.TrimSpace(rootDir) == "" {
		return false, nil
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("resolve occupant %s: %w", target, err)
	}
	root, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		root = rootDir
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return true, nil
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)), nil
}

// ---------------------------------------------------------------------------
// quarantine
// ---------------------------------------------------------------------------

// collisionQuarantineDir is the subtree collision losers are parked in.
//
// It sits under the SAME `<RootDir>/.failed` tree internal/quarantine already
// owns (quarantine/service.go:98) rather than under a new dot-directory,
// because `.failed` is already excluded everywhere it needs to be: the scanner
// and every sweep prune dot-dirs through pathutil.ShouldSkipDir, and
// server_middleware.go, metafetch/helpers.go and audiobooks/helpers.go all
// block `.failed` paths explicitly. A brand-new directory would need each of
// those carve-outs re-added, and the one that got missed would be the bug.
const collisionQuarantineDir = ".failed/_collisions"

// quarantineMu serializes the ENTIRE choose-a-name-and-move sequence, not just
// the MkdirAll. Two workers quarantining files with the same basename under the
// same book would otherwise both probe with Lstat, both find the name free, and
// both settle on it. moveExclusive would refuse the loser rather than clobber
// the winner — safe, but it would turn a resolvable collision into a durable
// failure for no reason. Quarantining is rare (it only happens on a real
// duplicate), so a global lock over it costs nothing worth measuring.
var quarantineMu sync.Mutex

// quarantineCollisionSource MOVES src into the quarantine tree and returns
// where it landed. It never removes anything.
//
// The move is moveExclusive (saferename.go): link-then-unlink, which refuses an
// existing destination and treats a failed source unlink as an ERROR rather
// than a warning — the right semantics for a real library file, where a
// surviving source name would mean the same audio under two names.
//
// The quarantined name deliberately keeps the source's basename and never ends
// in `.tmp`: cleanupTempFiles sweeps `*.tmp` and internal/sweep additionally
// matches `.tmp.m4b`-style names anywhere in a filename, so a scratch-looking
// name here would be deleted by a later sweep — which is precisely the
// destruction this whole design exists to avoid.
func quarantineCollisionSource(rootDir, bookID, src string) (string, error) {
	if strings.TrimSpace(rootDir) == "" {
		return "", errors.New("no library root configured")
	}
	qRoot := filepath.Clean(filepath.Join(rootDir, collisionQuarantineDir))
	dirName := strings.TrimSpace(bookID)
	if dirName == "" {
		dirName = "unattributed"
	}
	dir := filepath.Clean(filepath.Join(qRoot, sanitizePath(dirName)))
	if dir != qRoot && !strings.HasPrefix(dir, qRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("quarantine path %q escapes %s", dir, qRoot)
	}

	quarantineMu.Lock()
	defer quarantineMu.Unlock()

	if err := os.MkdirAll(dir, 0o775); err != nil {
		return "", fmt.Errorf("create quarantine dir %s: %w", dir, err)
	}

	dest := filepath.Join(dir, filepath.Base(src))
	if _, statErr := os.Lstat(dest); statErr == nil {
		// Same disambiguation convention as the library itself.
		var err error
		dest, err = nextAvailableTargetPath(dest)
		if err != nil {
			return "", err
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect quarantine destination %s: %w", dest, statErr)
	}

	if err := moveExclusive(src, dest); err != nil {
		return "", fmt.Errorf("move %s to quarantine %s: %w", src, dest, err)
	}
	return dest, nil
}
