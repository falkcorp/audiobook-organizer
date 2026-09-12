// file: internal/organizer/inplace_collision.go
// version: 1.0.1
// guid: df0b8ccd-c8b3-4b73-b9ab-89836b0d4c37
// last-edited: 2026-09-12

// Destination-conflict resolution for ReOrganizeInPlace.
//
// # The bug this closes
//
// ReOrganizeInPlace moves a book that already lives under RootDir to the path
// its current metadata computes. DL-2 (eade00f72) replaced a rename that
// silently OVERWROTE an occupied destination with moveExclusive, which refuses
// — correct, and deliberately not changed here. What DL-2 did not add was any
// resolution: every occupied destination became a hard "destination already
// exists — refusing to overwrite" error with no record, so the same pairs were
// retried on every scan (18,121 failure lines over 9,259 pairs in 72h of
// production logs), invisibly, because organize runs as a hook inside
// library.scan.
//
// # What happens now
//
// The occupant is classified with classifyDestination — the same helper the
// folder landing's adoptExistingDestination uses, so the in-place path and the
// out-of-root paths cannot disagree about what "the same file" means:
//
//   - same inode, or whole-file SHA-256 equal → ADOPT. The book is linked as a
//     non-primary version in the occupant book's version group. Nothing moves,
//     no file or row is deleted, and the source file stays where it is.
//   - same or near size (within 1%) but different bytes → the same recording
//     with rewritten tags is the usual cause. Adopt only when it is CONFIRMED:
//     both files carry a raw Chromaprint that matches AND durations agree
//     within max(2%, 60s). A fingerprint mismatch is a different recording
//     (suffix). No fingerprint on either side, or durations that disagree, is
//     "same_audio_unverified": both files and rows are left alone and a
//     durable skip is recorded, to be picked up once fingerprint coverage
//     improves.
//   - anything else → a genuinely different file. Organize's _copyN ladder
//     (nextAvailableTargetPath — the one scheme organize's single-file path and
//     the apply resolver in collision.go already share).
//
// Unlike collision.go's quarantine branch this never moves the losing source
// aside: the in-place source is a live library file with live book_file rows,
// and the version link keeps both reachable.
package organizer

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	ulid "github.com/oklog/ulid/v2"
	"golang.org/x/sync/errgroup"
)

// Outcome categories for an organize that met an occupied destination, or was
// held back before trying. They are the keys of Stats.Collisions and of the
// scan op result's organize_outcomes map.
const (
	OutcomeAdopted              = "adopted"
	OutcomeAdoptedSameRecording = "adopted_same_recording"
	OutcomeSuffixed             = "suffixed"
	OutcomeSameAudioUnverified  = "same_audio_unverified"
	OutcomeFragmentCollapse     = "fragment_collapse"
	OutcomePlaceholderSkipped   = "placeholder_skipped"
	OutcomeSkippedDurable       = "skipped_durable"
	// OutcomeUnresolvedConflict covers occupied destinations this code does
	// not decide: a directory book meeting a non-empty directory, a byte-equal
	// occupant no other book row owns (nothing to link to), and two books that
	// already sit in DIFFERENT version groups (merging groups is its own
	// decision).
	OutcomeUnresolvedConflict = "unresolved_conflict"
)

// sameRecordingMinSimilarity is the WholeFileSimilarity floor for treating two
// fingerprints as the same recording. It is the identity threshold
// reconcile/itunes_heal.go uses (0.9), not dedup's fuzzy candidate threshold
// (fingerprint.FuzzyMinSimilarity, 0.80): a match here LINKS two rows, so it
// has to mean "same audio", not "worth a human's look".
const sameRecordingMinSimilarity = 0.90

// ErrDestinationConflictUnresolved is wrapped by every DestinationConflictError,
// so a caller that only needs "declined, not failed" (duplicates_ops counts
// ErrAuthorUnresolved the same way) can errors.Is on it.
var ErrDestinationConflictUnresolved = errors.New("organize: destination conflict left unresolved")

// DestinationConflictError is a deliberate refusal to move a book, with the
// outcome category it is counted under. It is not a failure: the files and
// rows are exactly as they were.
type DestinationConflictError struct {
	Category string
	Source   string
	Target   string
	Reason   string
}

func (e *DestinationConflictError) Error() string {
	return fmt.Sprintf("organize: %s: %s -> %s: %s", e.Category, e.Source, e.Target, e.Reason)
}

func (e *DestinationConflictError) Unwrap() error { return ErrDestinationConflictUnresolved }

// InPlaceResolution reports how ReOrganizeInPlace resolved an occupied
// destination. It rides Landing.Resolution so CommitLanding — the step that
// knows the operation id — can write the change-ledger rows.
type InPlaceResolution struct {
	Outcome        string
	OriginalTarget string
	FinalTarget    string

	// Adopt only: the occupant book this one was linked to, the group it
	// joined, and the prior values the ledger records.
	OccupantBookID      string
	VersionGroupID      string
	PriorVersionGroupID string
	PriorPrimary        string
	// OccupantGroupSet is true when the occupant had no group and was given
	// VersionGroupID (and OccupantMadePrimary when it also became primary).
	OccupantGroupSet    bool
	OccupantMadePrimary bool
}

func (r *InPlaceResolution) adopted() bool {
	return r != nil && (r.Outcome == OutcomeAdopted || r.Outcome == OutcomeAdoptedSameRecording)
}

// inPlaceDestLocks serializes destination-conflict decisions per destination
// DIRECTORY. The race it closes is stat → classify → pick _copyN → move: two
// workers moving different books onto one occupied target would otherwise both
// see "_copy1 free" and one would fail, or both would adopt against a group
// they each read before the other wrote. Keying on the directory rather than
// the file means every _copyN candidate of a target is covered by the same
// lock, so two workers can never choose the same suffixed name. moveExclusive
// (link(2)/EEXIST) stays the backstop against writers outside organize.
//
// Striped rather than a map of per-path mutexes so memory stays fixed over a
// library-scale run. This is a NEW lock, deliberately not organizeBooks'
// pathMu, which guards the detection-only DEC-11 claim map and must not start
// serializing file I/O.
const inPlaceDestStripes = 256

var inPlaceDestLocks [inPlaceDestStripes]sync.Mutex

func lockInPlaceDestination(target string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(filepath.Clean(filepath.Dir(target))))
	mu := &inPlaceDestLocks[h.Sum32()%inPlaceDestStripes]
	mu.Lock()
	return mu.Unlock
}

// narratorPlaceholderTitles are titles that are really a narrator placeholder
// that leaked into the title field. "read by narrator" is the tail the old
// default pattern wrote into filenames (see the NOTE beside defaultTitle);
// maintenance's junk-title repair counts 1,595 such books.
var narratorPlaceholderTitles = map[string]struct{}{
	"narrator":         {},
	"read by narrator": {},
	"unknown narrator": {},
}

// IsPlaceholderTitle reports whether title is empty or one of the system's own
// placeholders rather than a real title. Organizing such a book bakes the
// placeholder into a path that every other placeholder book computes too — the
// 2026-08-11 "848 books onto one path" collapse.
func IsPlaceholderTitle(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" || strings.EqualFold(t, defaultTitle) || authorname.IsPlaceholder(t) {
		return true
	}
	_, ok := narratorPlaceholderTitles[strings.ToLower(t)]
	return ok
}

// resolveOccupiedInPlace decides what to do about a destination that already
// exists. Callers hold lockInPlaceDestination(target). It returns the
// resolution for an adopt or a suffix, or a *DestinationConflictError (with a
// durable skip already recorded) for everything it declines.
func (orgSvc *Service) resolveOccupiedInPlace(book *database.Book, src, target string, srcInfo, dstLink os.FileInfo, log logger.Logger) (*InPlaceResolution, error) {
	res := &InPlaceResolution{OriginalTarget: target, FinalTarget: target}
	decline := func(category, reason string) error {
		orgSvc.recordOrganizeSkip(book.ID, category, reason, src, srcInfo, target, dstLink)
		return &DestinationConflictError{Category: category, Source: src, Target: target, Reason: reason}
	}

	if srcInfo.IsDir() || dstLink.IsDir() {
		return nil, decline(OutcomeUnresolvedConflict,
			"directory book or directory occupant: which tracks belong to which book is not decided here")
	}
	// Identity is about bytes, so follow a symlinked occupant.
	dstInfo, err := os.Stat(target)
	if err != nil || !dstInfo.Mode().IsRegular() {
		return nil, decline(OutcomeUnresolvedConflict, "occupant is not a readable regular file")
	}

	switch classifyDestination(src, target, srcInfo, dstInfo) {
	case destinationSame:
		return orgSvc.adoptIntoOccupantGroup(book, src, target, res, OutcomeAdopted, decline)
	case destinationNearSize:
		verdict, reason := orgSvc.sameRecording(book, src, target, srcInfo, dstInfo)
		switch verdict {
		case recordingConfirmed:
			return orgSvc.adoptIntoOccupantGroup(book, src, target, res, OutcomeAdoptedSameRecording, decline)
		case recordingUnverified:
			return nil, decline(OutcomeSameAudioUnverified, reason)
		}
		log.Debug("organize: %s vs %s: %s — suffixing", src, target, reason)
	}

	next, err := nextAvailableTargetPath(target)
	if err != nil {
		return nil, fmt.Errorf("cannot move %s -> %s: destination occupied by a different file and no free _copyN name: %w", src, target, err)
	}
	res.Outcome = OutcomeSuffixed
	res.FinalTarget = next
	return res, nil
}

type recordingVerdict int

const (
	recordingUnverified recordingVerdict = iota
	recordingConfirmed
	recordingDifferent
)

// sameRecording decides whether a near-size, different-bytes occupant is the
// same recording as src. Confirmation needs BOTH a raw Chromaprint match and
// durations within max(2%, 60s); duration alone is never enough.
func (orgSvc *Service) sameRecording(book *database.Book, src, target string, srcInfo, dstInfo os.FileInfo) (recordingVerdict, string) {
	occupant, _ := orgSvc.db.GetBookByFilePath(target)
	if occupant == nil || occupant.ID == book.ID {
		return recordingUnverified, "no other book row owns the occupant, so its fingerprint cannot be read"
	}
	srcRow := orgSvc.bookFileAt(book.ID, src)
	occRow := orgSvc.bookFileAt(occupant.ID, target)
	if srcRow == nil || occRow == nil || len(srcRow.AcoustIDFingerprint) == 0 || len(occRow.AcoustIDFingerprint) == 0 {
		return recordingUnverified, "no Chromaprint fingerprint on one or both files; duration alone does not prove the same recording"
	}
	sim, err := fingerprint.WholeFileSimilarity(srcRow.AcoustIDFingerprint, occRow.AcoustIDFingerprint)
	if err != nil {
		return recordingUnverified, "fingerprint comparison failed: " + err.Error()
	}
	if sim < sameRecordingMinSimilarity {
		return recordingDifferent, fmt.Sprintf("fingerprints differ (similarity %.3f < %.2f)", sim, sameRecordingMinSimilarity)
	}
	a := fileDurationSec(srcRow, book, srcInfo.Size())
	b := fileDurationSec(occRow, occupant, dstInfo.Size())
	if a <= 0 || b <= 0 {
		return recordingUnverified, "fingerprints match but a duration is unknown"
	}
	if !durationsAgree(a, b) {
		return recordingUnverified, fmt.Sprintf("fingerprints match but durations disagree (%.0fs vs %.0fs)", a, b)
	}
	return recordingConfirmed, fmt.Sprintf("fingerprint similarity %.3f, durations %.0fs / %.0fs", sim, a, b)
}

// durationsAgree is the max(2%, 60s) tolerance.
func durationsAgree(a, b float64) bool {
	tol := math.Max(0.02*math.Max(a, b), 60)
	return math.Abs(a-b) <= tol
}

// fileDurationSec prefers the duration fpcalc measured while decoding (it is a
// probe of the actual audio), then the row's stored duration, then the book's.
// Stored durations go through NormalizeDurationSec because some rows hold
// milliseconds.
func fileDurationSec(bf *database.BookFile, b *database.Book, size int64) float64 {
	if bf.AcoustIDFingerprintDurationSec > 0 {
		return bf.AcoustIDFingerprintDurationSec
	}
	if bf.Duration > 0 {
		return float64(database.NormalizeDurationSec(size, bf.Duration))
	}
	if b != nil && b.Duration != nil && *b.Duration > 0 {
		return float64(database.NormalizeDurationSec(size, *b.Duration))
	}
	return 0
}

// bookFileAt returns bookID's row for path. GetBookFiles reads the full rows,
// including the fingerprint memdb strips from Core projections.
func (orgSvc *Service) bookFileAt(bookID, path string) *database.BookFile {
	files, err := orgSvc.db.GetBookFiles(bookID)
	if err != nil {
		return nil
	}
	for i := range files {
		if files[i].FilePath == path {
			return &files[i]
		}
	}
	return nil
}

// adoptIntoOccupantGroup links book as a non-primary version in the version
// group of the book that owns target, the way CreateOrganizedVersion links an
// organized copy (that function refuses in-place landings, so the demote is
// done here). The incumbent keeps or takes primary: it is the copy already at
// the organized path. Both rows are written through hydrateAndUpdateBook so a
// Core projection never wipes Author/Series.
func (orgSvc *Service) adoptIntoOccupantGroup(book *database.Book, src, target string, res *InPlaceResolution, outcome string, decline func(string, string) error) (*InPlaceResolution, error) {
	occupant, err := orgSvc.db.GetBookByFilePath(target)
	if err != nil || occupant == nil || occupant.ID == book.ID {
		return nil, decline(OutcomeUnresolvedConflict, "occupant holds this book's audio but no other book row owns it, so there is no version group to join")
	}
	occGroup := derefString(occupant.VersionGroupID)
	bookGroup := derefString(book.VersionGroupID)
	if occGroup != "" && bookGroup != "" && occGroup != bookGroup {
		return nil, decline(OutcomeUnresolvedConflict,
			fmt.Sprintf("book is already in version group %s and the occupant in %s; merging groups is a separate decision", bookGroup, occGroup))
	}
	group := occGroup
	if group == "" {
		group = bookGroup
	}
	if group == "" {
		group = ulid.Make().String()
	}

	res.Outcome = outcome
	res.OccupantBookID = occupant.ID
	res.VersionGroupID = group
	res.PriorVersionGroupID = bookGroup
	res.PriorPrimary = boolPtrString(book.IsPrimaryVersion)
	res.FinalTarget = src // nothing moved

	// Does the group already have a primary other than this book?
	hasPrimary := false
	if members, merr := orgSvc.db.GetBooksByVersionGroup(group); merr == nil {
		for i := range members {
			if members[i].ID != book.ID && members[i].IsPrimaryVersion != nil && *members[i].IsPrimaryVersion {
				hasPrimary = true
				break
			}
		}
	}
	if occGroup == "" || !hasPrimary {
		makePrimary := !hasPrimary
		res.OccupantGroupSet = occGroup == ""
		res.OccupantMadePrimary = makePrimary
		if err := orgSvc.hydrateAndUpdateBook(occupant.ID, func(b *database.Book) {
			b.VersionGroupID = &group
			if makePrimary {
				t := true
				b.IsPrimaryVersion = &t
			}
		}); err != nil {
			return nil, fmt.Errorf("adopt %s into version group of %s: update occupant: %w", book.ID, occupant.ID, err)
		}
	}

	notPrimary := false
	if err := orgSvc.hydrateAndUpdateBook(book.ID, func(b *database.Book) {
		b.VersionGroupID = &group
		b.IsPrimaryVersion = &notPrimary
	}); err != nil {
		return nil, fmt.Errorf("adopt %s into version group %s: %w", book.ID, group, err)
	}
	book.VersionGroupID = &group
	book.IsPrimaryVersion = &notPrimary
	return res, nil
}

// recordOrganizeSkip persists the durable skip for a declined pair, keyed on
// the target and fingerprinted by BOTH files' size and mtime, so the pair is
// not re-attempted until one of them changes (see OrganizeCollisionBlocked).
func (orgSvc *Service) recordOrganizeSkip(bookID, category, reason, src string, srcInfo os.FileInfo, target string, dstLink os.FileInfo) {
	rec := ApplyRenameFailure{
		BookID:       bookID,
		Category:     category,
		TargetPath:   target,
		OccupantPath: target,
		SourcePath:   src,
		Reason:       reason,
	}
	if dstLink != nil {
		rec.OccupantSize = dstLink.Size()
		rec.OccupantModUnix = dstLink.ModTime().Unix()
	}
	if srcInfo != nil {
		rec.SourceSize = srcInfo.Size()
		rec.SourceModUnix = srcInfo.ModTime().Unix()
	}
	RecordOrganizeCollisionSkip(orgSvc.db, rec)
}

// dirIsEmpty reports whether path is a directory with no entries. An empty
// directory at the target is not a conflict: rename(2) replaces it, which is
// what the in-place move always did.
func dirIsEmpty(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	// Readdirnames(1) on an empty directory returns io.EOF; any other error
	// is "cannot tell", which must not be mistaken for empty (that would send a
	// directory book into rename(2) against an occupied directory).
	names, err := f.Readdirnames(1)
	return len(names) == 0 && errors.Is(err, io.EOF)
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func boolPtrString(b *bool) string {
	if b == nil {
		return ""
	}
	if *b {
		return "true"
	}
	return "false"
}

// recordAdoptChanges writes the change-ledger rows for an adopt: an
// organize_skipped row naming the pair and outcome, and metadata_update rows
// carrying the prior version_group_id / is_primary_version of every row the
// adopt wrote. The revert engine classifies metadata_update rows for those two
// fields as record-only (undo/restorable.go, kept that way by c5e7a2786), so
// they are the record a manual reversal works from, not a one-click revert.
// No file was moved and no row was created or deleted, so there is nothing
// else to reverse.
func (orgSvc *Service) recordAdoptChanges(book *database.Book, r *InPlaceResolution, operationID string) {
	if operationID == "" {
		return
	}
	add := func(bookID, changeType, field, oldV, newV string) {
		_ = orgSvc.db.CreateOperationChange(&database.OperationChange{
			ID:          ulid.Make().String(),
			OperationID: operationID,
			BookID:      bookID,
			ChangeType:  changeType,
			FieldName:   field,
			OldValue:    oldV,
			NewValue:    newV,
		})
	}
	add(book.ID, "organize_skipped", "file_path", r.OriginalTarget,
		fmt.Sprintf("%s: kept at %s as a non-primary version of book %s (version group %s)", r.Outcome, r.FinalTarget, r.OccupantBookID, r.VersionGroupID))
	add(book.ID, "metadata_update", "version_group_id", r.PriorVersionGroupID, r.VersionGroupID)
	add(book.ID, "metadata_update", "is_primary_version", r.PriorPrimary, "false")
	if r.OccupantGroupSet {
		add(r.OccupantBookID, "metadata_update", "version_group_id", "", r.VersionGroupID)
	}
	if r.OccupantMadePrimary {
		add(r.OccupantBookID, "metadata_update", "is_primary_version", "", "true")
	}
}

// detectFragmentCollapse returns bookID -> shared target for every in-library
// single-file book whose computed destination is also computed by another book
// from the SAME source directory: "Title - 1/12.mp3", "Title - 2/12.mp3", ...
// all carrying the title "Title". Moving the first and suffixing the rest would
// scatter one book's chapters as _copyN files; merging those rows is a separate
// decision (the fragment/consolidation ops), so none of them move.
//
// Target computation reads the store (author/series lookups), so it runs on a
// bounded pool sized to NumCPU. Each goroutine writes only its own index of
// targets, so no lock is needed; grouping happens after Wait.
func (orgSvc *Service) detectFragmentCollapse(ctx context.Context, books []database.Book) map[string]string {
	root := config.AppConfig.RootDir
	if root == "" || len(books) < 2 {
		return nil
	}
	exts := config.SupportedExtensionSet()
	targets := make([]string, len(books))
	var g errgroup.Group
	g.SetLimit(runtime.NumCPU())
	for i := range books {
		b := &books[i]
		if !strings.HasPrefix(b.FilePath, root) || !exts.MatchPath(b.FilePath) {
			continue
		}
		g.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			if t, err := orgSvc.newOrganizer().GenerateTargetPath(b); err == nil && t != b.FilePath {
				targets[i] = t
			}
			return nil
		})
	}
	_ = g.Wait()

	type key struct{ dir, target string }
	groups := make(map[key][]string)
	for i, t := range targets {
		if t != "" {
			k := key{filepath.Dir(books[i].FilePath), t}
			groups[k] = append(groups[k], books[i].ID)
		}
	}
	out := make(map[string]string)
	for k, ids := range groups {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			out[id] = k.target
		}
	}
	return out
}

// recordFragmentCollapse records the durable skip for a fragment_collapse
// book. The pre-pass re-detects fragments every run anyway; the record is what
// makes the skip visible and clearable like every other declined pair.
func (orgSvc *Service) recordFragmentCollapse(book *database.Book, target string) {
	srcInfo, _ := os.Stat(book.FilePath)
	dstLink, _ := os.Lstat(target)
	orgSvc.recordOrganizeSkip(book.ID, OutcomeFragmentCollapse,
		"several books from one source directory compute this destination; merging them is a separate decision",
		book.FilePath, srcInfo, target, dstLink)
}

// addCollision counts one outcome category. Callers hold the stats lock.
func (s *Stats) addCollision(category string, n int) {
	if n <= 0 || category == "" {
		return
	}
	if s.Collisions == nil {
		s.Collisions = make(map[string]int)
	}
	s.Collisions[category] += n
}

// formatCollisionTally renders "adopted=3 suffixed=1 ..." in a stable order.
func formatCollisionTally(c map[string]int) string {
	if len(c) == 0 {
		return ""
	}
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, c[k]))
	}
	return strings.Join(parts, " ")
}
