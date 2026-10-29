// file: internal/scanner/file_ownership.go
// version: 1.6.1
// guid: f938af2f-e090-48ab-b6b0-c89267a7adbf
// last-edited: 2026-10-29

package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/oklog/ulid/v2"
)

// errAppendFrozenITunes: a staged arrival whose owner (or whose new files) are
// in the hands-off iTunes tree. iTunes owns that tree and the book's track
// list; the scanner does not grow it.
var errAppendFrozenITunes = errors.New("the book's files are in the iTunes-managed tree; nothing is appended there")

// errFileOwnedByOtherBook is what saveBookToDatabase returns, beside an
// *ownershipSkip, when the scanned book was NOT saved because its files belong
// to another book. It is not a failure: callers test for it with errors.Is and
// then skip everything that would otherwise run against the saved row --
// book_file creation, chapters, the scan-cache stamp and AI nomination. Until
// 2026-09-28 the skip returned nil, and every one of those steps then ran
// against whatever book the path lookups resolved to, which was the OWNER: its
// NeedsRescan was cleared, its chapters were rewritten and an AI parse was
// queued for it on every scan.
//
// The error is only the signal. The per-book record (owners, reason, the files
// to append) travels in the ownershipSkip returned beside it, never inside the
// error: a file list unpacked out of a wrapped error with errors.As carried the
// error's book-id text with it, and CodeQL (go/path-injection, alerts #2022 and
// #1651) took the whole list as tainted by the time it reached hashing.
var errFileOwnedByOtherBook = errors.New("scanned files already belong to another book")

// ownershipSkip is the per-book record of an ownership skip.
type ownershipSkip struct {
	Path   string
	Owners []string
	Reason string
	// AppendTo, when set, is the one book the scanned files belong to, and
	// Unowned are the scanned files it does not have rows for yet: the caller
	// appends them (appendScannedFilesToOwner) instead of creating a book.
	AppendTo string
	Unowned  []string
}

// fileOwnershipVerdict is checkFileOwnership's answer.
type fileOwnershipVerdict struct {
	// skip is true when saving this scanned book would mint a NEW book over
	// files another book already owns, or overlay another book.
	skip bool
	// owners are the live books that own at least one scanned file, sorted,
	// for the log line.
	owners []string
	// reason says which shape was refused.
	reason string
	// appendTo / unowned: a staged arrival. See checkFileOwnership step 3.
	appendTo string
	unowned  []string
	// sameBook is the live book that owns every scanned file, all of its
	// present rows among them (step 3's rescan of the same book), on a
	// proceed verdict; "" otherwise. A segment list whose first file changed
	// (a re-sorted chapter order, or a first file whose row is marked
	// Missing) is not at that book's path, and
	// saveBookToDatabase holds the folder parse against this row
	// (lookupScanExisting).
	sameBook string
}

// asSkip is the record of the skip this verdict asks for; the caller returns it
// beside errFileOwnedByOtherBook.
func (v fileOwnershipVerdict) asSkip(path string) *ownershipSkip {
	return &ownershipSkip{Path: path, Owners: v.owners, Reason: v.reason, AppendTo: v.appendTo, Unowned: v.unowned}
}

// scannedFilesOf returns the files a scanned Book would claim: its segment
// list, its own path for a single-file book, or -- for a directory-shaped book
// (no segment list, FilePath is a folder) -- the audio files directly in that
// folder, enumerated the way createBookFilesForBookWithAudio will enumerate them when it
// mints the book's rows.
func scannedFilesOf(book *Book) ([]string, error) {
	if len(book.SegmentFiles) > 0 {
		return book.SegmentFiles, nil
	}
	if book.FilePath == "" {
		return nil, nil
	}
	info, err := os.Stat(book.FilePath)
	if err != nil || !info.IsDir() {
		return []string{book.FilePath}, nil
	}
	entries, err := os.ReadDir(book.FilePath)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", book.FilePath, err)
	}
	audioExts := make(map[string]bool, len(config.AppConfig.SupportedExtensions))
	for _, ext := range config.AppConfig.SupportedExtensions {
		audioExts[ext] = true
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if audioExts[strings.ToLower(filepath.Ext(e.Name()))] {
			files = append(files, filepath.Join(book.FilePath, e.Name()))
		}
	}
	return files, nil
}

// ownerState is what checkFileOwnership learned about one owning book ID.
type ownerState int

const (
	ownerGone        ownerState = iota // dangling row: the book no longer exists
	ownerLive                          // a live book
	ownerSoftDeleted                   // soft-deleted (MarkedForDeletion), not yet purged
)

// checkFileOwnership decides whether saveBookToDatabase may treat book as a
// book at all, by asking who already owns its files.
//
// Until 2026-09-28 the only "already known?" question the scan asked was
// GetBookByFilePath, which reads the book-path index and nothing else. A
// chapter file of a multi-file book is NOT at any book's path -- it is one of
// that book's book_file rows -- so a scan that emitted the chapter on its own
// (a flat directory over the size cap, a leading-number-only consolidation
// miss) imported it as a brand-new book. Auto-organize then moved the file to
// the new book's target path, and the owning book's row was left pointing at
// a file that no longer existed. Prod held ~26,600 such fragment books.
//
// The decision, in order:
//
//  1. A single-file or directory-shaped book already at book.FilePath is an
//     update of that book: proceed, the upsert below handles it exactly as
//     before. A segment-list book gets no such shortcut, because its FilePath
//     is only its first file: the hit can be fragment #1 of a folder whose
//     chapters were imported as separate books, and the new grouping must not
//     be overlaid onto it.
//  2. Collect the owners of every scanned file. A row whose book no longer
//     exists is dangling and owns nothing. A soft-deleted owner is ignored
//     when any scanned file has a LIVE owner (the live book is the claim that
//     matters; the soft-deleted one is on its way out), and counts otherwise
//     (so a scan does not resurrect, as a new book, files the user deleted).
//  3. Proceed when no file is owned (a genuinely new book) or exactly one book
//     owns every scanned file and all of its present (non-Missing) rows are
//     among them -- the rescan of the same book. In both cases the book the
//     save would UPDATE, the one at book.FilePath, must be nil or that owner:
//     otherwise the save would overlay the scanned grouping onto a different
//     book (fragment #1 of a folder whose fragments never got rows).
//  4. A staged arrival: exactly one live owner, every one of its present rows
//     is among the scanned files, and the remaining scanned files have no
//     rows at all. That is the owner, grown -- a download that was scanned
//     half-written. The verdict names the owner and the new files, and the
//     caller appends rows for them instead of creating a book.
//     A RETIRED row (a version another book holds, database.BookFile.Retired)
//     is not an owner and never reaches this step: it is collected per file,
//     apart from the claim tally, and a scanned file whose only claims are
//     retired rows skips the whole scanned book (see retiredOnlyVerdict). It is
//     never read as "owned by the holder", which is what would otherwise make
//     the scan the holder's rescan (sameBook) or grow the holder with the
//     retired file (appendTo).
//  5. Everything else is a fragment and is skipped: files owned by a
//     different book, owned files mixed with unowned ones in any other shape,
//     files owned by several books.
//
// A store error is returned, never read as "unowned": the caller fails closed.
func checkFileOwnership(book *Book) (fileOwnershipVerdict, error) {
	store := getStore()
	if store == nil || book == nil {
		return fileOwnershipVerdict{}, nil
	}
	segmentList := len(book.SegmentFiles) > 1
	atPath, err := store.GetBookByFilePath(book.FilePath)
	if err != nil {
		return fileOwnershipVerdict{}, fmt.Errorf("book lookup for %s: %w", book.FilePath, err)
	}
	// A segment list is the one shape whose FilePath is merely its first file;
	// every other shape's FilePath names the whole book.
	if !segmentList && atPath != nil {
		// A retired shell at the path is not "this same book": its files live
		// on as retired rows under another book, so a rescan of the shell's own
		// path must still be judged by who claims the scanned files. The
		// full check below skips a file only retired rows claim, where the
		// shortcut would let the scan mint a second, live row on it.
		shell, serr := isRetiredShell(store, atPath.ID)
		if serr != nil {
			return fileOwnershipVerdict{}, fmt.Errorf("retired versions of book %s: %w", atPath.ID, serr)
		}
		if !shell {
			return directoryBookArrivals(store, book, atPath)
		}
	}
	files, err := scannedFilesOf(book)
	if err != nil {
		return fileOwnershipVerdict{}, err
	}
	if len(files) == 0 {
		return fileOwnershipVerdict{}, nil
	}

	scanned := make(map[string]struct{}, len(files))
	for _, f := range files {
		scanned[f] = struct{}{}
	}

	state := make(map[string]ownerState)    // bookID -> what it is
	fileOwners := make(map[string][]string) // scanned file -> owning book IDs (live or soft-deleted)
	retired := make(map[string][]retiredClaim)
	anyLive := false
	for f := range scanned {
		rows, err := database.BookFileRowsAtPath(store, f)
		if err != nil {
			return fileOwnershipVerdict{}, fmt.Errorf("book_file lookup for %s: %w", f, err)
		}
		seen := make(map[string]struct{}, len(rows))
		for _, r := range rows {
			if r.Retired {
				// BookFileRowsAtPath is the complete lookup and returns a
				// retired version under its holder. It is a claim on the file
				// but not an owner: kept apart so it can neither make the
				// holder the owner nor count as a second owning book.
				retired[f] = append(retired[f], retiredClaim{holder: r.BookID, versionID: r.VersionID})
				continue
			}
			if _, dup := seen[r.BookID]; dup {
				continue
			}
			seen[r.BookID] = struct{}{}
			st, known := state[r.BookID]
			if !known {
				b, err := store.GetBookByID(r.BookID)
				if err != nil {
					return fileOwnershipVerdict{}, fmt.Errorf("owner lookup %s for %s: %w", r.BookID, f, err)
				}
				switch {
				case b == nil:
					st = ownerGone
				case b.IsSoftDeleted():
					st = ownerSoftDeleted
				default:
					st = ownerLive
				}
				state[r.BookID] = st
			}
			if st == ownerGone {
				continue // dangling row: its book is gone, so it owns nothing
			}
			if st == ownerLive {
				anyLive = true
			}
			fileOwners[f] = append(fileOwners[f], r.BookID)
		}
	}

	if v, ok := retiredOnlyVerdict(scanned, fileOwners, retired); ok {
		return v, nil
	}

	owned := make(map[string]int)
	var unowned []string
	softOnly := 0 // files whose only owners are soft-deleted books that are being ignored
	for f := range scanned {
		counted := 0
		for _, id := range fileOwners[f] {
			if anyLive && state[id] == ownerSoftDeleted {
				continue
			}
			owned[id]++
			counted++
		}
		if counted == 0 {
			if len(fileOwners[f]) > 0 {
				softOnly++
			}
			unowned = append(unowned, f)
		}
	}
	slices.SortFunc(unowned, util.CompareNatural)

	overlays := func(ownerID string) bool {
		return segmentList && atPath != nil && atPath.ID != ownerID
	}

	if len(owned) == 0 {
		if overlays("") {
			return fileOwnershipVerdict{skip: true, owners: []string{atPath.ID},
				reason: "another book already sits at this group's first file; the group would overwrite it"}, nil
		}
		return fileOwnershipVerdict{}, nil
	}
	owners := make([]string, 0, len(owned))
	for id := range owned {
		owners = append(owners, id)
	}
	slices.Sort(owners)

	if len(owners) > 1 {
		return fileOwnershipVerdict{skip: true, owners: owners, reason: "files are owned by several books"}, nil
	}

	// One owner. It is this same book only when every one of its PRESENT rows
	// is among the scanned files: a row flagged Missing may be a file that has
	// just reappeared, and one the scan did not emit at all is expected to be
	// missing. Any present row outside the scanned set means the scanned files
	// are a piece of a larger book.
	ownerID := owners[0]
	rows, err := store.GetBookFiles(ownerID)
	if err != nil {
		return fileOwnershipVerdict{}, fmt.Errorf("book_files of owner %s: %w", ownerID, err)
	}
	for _, r := range rows {
		if r.Missing {
			continue
		}
		if _, ok := scanned[r.FilePath]; !ok {
			return fileOwnershipVerdict{skip: true, owners: owners,
				reason: "the files are part of a larger book that owns other files too"}, nil
		}
	}

	if len(unowned) == 0 {
		if overlays(ownerID) {
			return fileOwnershipVerdict{skip: true, owners: []string{ownerID, atPath.ID},
				reason: "a different book sits at this group's first file than the one owning its files"}, nil
		}
		if state[ownerID] == ownerLive {
			return fileOwnershipVerdict{sameBook: ownerID}, nil
		}
		return fileOwnershipVerdict{}, nil
	}

	// Staged arrival: the owner is whole inside the scanned set and the rest
	// of the scanned files are new. Only files with NO rows qualify: a file
	// held by a soft-deleted book is left to that book, not reassigned by an
	// upsert, and a soft-deleted owner is never grown.
	if softOnly == 0 && state[ownerID] == ownerLive {
		return fileOwnershipVerdict{skip: true, owners: owners, appendTo: ownerID, unowned: unowned,
			reason: fmt.Sprintf("%d new file(s) arrived for an already-imported book; appending them to it", len(unowned))}, nil
	}
	return fileOwnershipVerdict{skip: true, owners: owners,
		reason: "some files are owned by another book and some are not"}, nil
}

// isRetiredShell reports whether bookID is a book some retired version was
// retired out of: a trashed or merged shell whose files now live as retired
// rows under another book. A store that cannot say fails the check, and the
// caller fails closed.
func isRetiredShell(store any, bookID string) (bool, error) {
	versions, err := database.RetiredVersionsFromBook(store, bookID)
	if err != nil {
		return false, err
	}
	return len(versions) > 0, nil
}

// retiredClaim is one retired version's claim on a scanned file: the live
// book holding it and the version row.
type retiredClaim struct {
	holder    string
	versionID string
}

// retiredOnlyVerdict skips the scanned book when any of its files is claimed
// only by retired rows. Such a file is a former file of a trashed or merged
// book that now lives on as a retired version of another book: importing it
// again would mint a new book for content the library already holds, and
// treating it as the holder's own file would grow or rescan the holder with a
// file it deliberately keeps out of its live track list. A file that ALSO has
// a non-retired claim (fileOwners) ignores its retired rows, since the live
// claim is the one the ownership rules below judge. The files are examined in
// natural order so the logged holder is stable. ok is false when no file is
// retired-only.
func retiredOnlyVerdict(scanned map[string]struct{}, fileOwners map[string][]string, retired map[string][]retiredClaim) (fileOwnershipVerdict, bool) {
	var only []string
	for f := range scanned {
		if len(fileOwners[f]) == 0 && len(retired[f]) > 0 {
			only = append(only, f)
		}
	}
	if len(only) == 0 {
		return fileOwnershipVerdict{}, false
	}
	slices.SortFunc(only, util.CompareNatural)
	first := retired[only[0]][0]
	holders := make([]string, 0, len(only))
	for _, f := range only {
		for _, c := range retired[f] {
			if !slices.Contains(holders, c.holder) {
				holders = append(holders, c.holder)
			}
		}
	}
	slices.Sort(holders)
	reason := fmt.Sprintf("retired version of book %s (version %s)", first.holder, first.versionID)
	if len(only) > 1 {
		reason = fmt.Sprintf("%s; %d of the scanned files are retired versions", reason, len(only))
	}
	return fileOwnershipVerdict{skip: true, owners: holders, reason: reason}, true
}

// directoryBookArrivals handles a single-file or directory-shaped scanned book
// that is already at its path: an update of that book, which proceeds -- except
// that for a DIRECTORY book, audio files in its folder that no book has a row
// for are a staged arrival (checkFileOwnership step 4, directory shape).
// createBookFilesForBookWithAudio returns as soon as the book has any rows, so without
// this those files never got rows. Files another book claims are left to it.
func directoryBookArrivals(store scannerStore, book *Book, atPath *database.Book) (fileOwnershipVerdict, error) {
	if atPath.IsSoftDeleted() {
		return fileOwnershipVerdict{}, nil
	}
	info, err := os.Stat(book.FilePath)
	if err != nil || !info.IsDir() {
		return fileOwnershipVerdict{}, nil
	}
	rows, err := store.GetBookFiles(atPath.ID)
	if err != nil {
		return fileOwnershipVerdict{}, fmt.Errorf("book_files of %s: %w", atPath.ID, err)
	}
	if len(rows) == 0 {
		return fileOwnershipVerdict{}, nil // createBookFilesForBookWithAudio mints them all
	}
	have := make(map[string]bool, len(rows))
	for _, r := range rows {
		have[r.FilePath] = true
	}
	files, err := scannedFilesOf(book)
	if err != nil {
		return fileOwnershipVerdict{}, err
	}
	var unowned []string
	for _, f := range files {
		if have[f] {
			continue
		}
		owners, err := database.BookFileRowsAtPath(store, f)
		if err != nil {
			return fileOwnershipVerdict{}, fmt.Errorf("book_file lookup for %s: %w", f, err)
		}
		if len(owners) == 0 {
			unowned = append(unowned, f)
		}
	}
	if len(unowned) == 0 {
		return fileOwnershipVerdict{}, nil
	}
	slices.SortFunc(unowned, util.CompareNatural)
	return fileOwnershipVerdict{skip: true, owners: []string{atPath.ID}, appendTo: atPath.ID, unowned: unowned,
		reason: fmt.Sprintf("%d new file(s) arrived in the folder of an already-imported book; appending them to it", len(unowned))}, nil
}

// appendScannedFilesToOwner writes book_file rows for files that arrived after
// ownerID was imported. createBookFilesForBookWithAudio cannot do this: it returns as
// soon as the book has ANY rows (a rescan must not rebuild rows whose tag
// positions were judged at import), so without this a book first scanned
// mid-download kept the first batch of files forever and every later scan
// skipped the rest as fragments.
//
// Track numbering: the new files are judged on their own tags exactly as an
// import is (applyTagPositionsIfTrusted). Tag positions are kept only when
// none collides with a (disc, track) an existing row already holds; otherwise
// every new file gets a positional number after the owner's highest track, in
// natural filename order. Existing rows are never renumbered or rewritten.
//
// The caller then re-synthesizes the owner's chapters from all its rows
// (resynthesizeChaptersAfterAppend) and re-arms its NeedsRescan so the next
// scan re-reads the grown book. The totals are recomputed by the upsert.
func appendScannedFilesToOwner(ctx context.Context, ownerID string, files []string, knownHashes map[string]string, scanLog logger.Logger) (int, error) {
	store := getStore()
	if store == nil || len(files) == 0 {
		return 0, nil
	}
	existing, err := store.GetBookFiles(ownerID)
	if err != nil {
		return 0, fmt.Errorf("book_files of %s: %w", ownerID, err)
	}
	if slices.ContainsFunc(files, pathutil.UnderFrozenITunesTree) {
		return 0, errAppendFrozenITunes
	}
	for _, r := range existing {
		if pathutil.UnderFrozenITunesTree(r.FilePath) {
			return 0, errAppendFrozenITunes
		}
	}
	type slot struct{ disc, track int }
	taken := make(map[slot]bool, len(existing))
	maxTrack := 0
	for _, r := range existing {
		taken[slot{r.DiscNumber, r.TrackNumber}] = true
		maxTrack = max(maxTrack, r.TrackNumber)
	}

	bfs := make([]*database.BookFile, 0, len(files))
	placements := make([]metadata.TagPlacement, 0, len(files))
	present := make([]bool, 0, len(files))
	for i, f := range files {
		var size int64
		fi, statErr := os.Stat(f)
		if statErr == nil {
			size = fi.Size()
		}
		bf := &database.BookFile{
			ID:               ulid.Make().String(),
			BookID:           ownerID,
			FilePath:         f,
			OriginalFilename: filepath.Base(f),
			Format:           strings.TrimPrefix(strings.ToLower(filepath.Ext(f)), "."),
			FileSize:         size,
			TrackNumber:      maxTrack + i + 1,
		}
		placements = append(placements, readScannedFileTagsAndHash(bf, f, knownHashes[f], scanLog))
		bfs = append(bfs, bf)
		present = append(present, statErr == nil)
	}
	applyTagPositionsIfTrusted(bfs, placements, ownerID, scanLog)
	collides := false
	for _, bf := range bfs {
		if taken[slot{bf.DiscNumber, bf.TrackNumber}] {
			collides = true
			break
		}
	}
	if collides {
		for i, bf := range bfs {
			bf.TrackNumber, bf.DiscNumber, bf.TrackCount, bf.DiscCount = maxTrack+i+1, 0, 0, 0
		}
	}
	rows := make([]database.ScannedBookFile, len(bfs))
	for i, bf := range bfs {
		rows[i] = database.ScannedBookFile{File: bf, Present: present[i]}
	}
	if err := store.BatchUpsertScannedBookFiles(rows); err != nil {
		return 0, fmt.Errorf("append %d file(s) to %s: %w", len(rows), ownerID, err)
	}
	scanRunCountersFrom(ctx).stagedAppended.Add(int64(len(rows)))
	return len(rows), nil
}

// OwnershipSkips accumulates the scanned books one scan run did not import
// because their files belong to another book. It is the durable per-book
// record the Info-level process log used to be: the first
// MaxFileFailureSamples are written to the OPERATION log at warn with the
// path, the owners and the reason as structured attrs, and a summary line at
// the end carries the total.
type OwnershipSkips struct {
	mu      sync.Mutex
	total   int
	samples []ownershipSkip
}

// Record counts e and lists it in scanLog while under the sample cap.
func (c *OwnershipSkips) Record(scanLog logger.Logger, e *ownershipSkip) {
	if scanLog == nil {
		scanLog = logger.New("scanner")
	}
	c.mu.Lock()
	c.total++
	n := c.total
	if n <= MaxFileFailureSamples {
		c.samples = append(c.samples, *e)
	}
	c.mu.Unlock()
	if n <= MaxFileFailureSamples {
		logger.LogWithAttrs(scanLog, slog.LevelWarn, "scan: book not imported, files belong to another book",
			slog.String("file_path", e.Path),
			slog.String("owners", strings.Join(e.Owners, ",")),
			slog.String("reason", e.Reason))
		return
	}
	scanLog.Debug("scan: book not imported (not listed in the op log) %s: %s (owners %s)",
		e.Path, e.Reason, strings.Join(e.Owners, ","))
}

// Total is the number of skips recorded, listed or not.
func (c *OwnershipSkips) Total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// ReportSummary writes one warn line when any book was skipped.
func (c *OwnershipSkips) ReportSummary(scanLog logger.Logger) {
	c.mu.Lock()
	total, listed := c.total, len(c.samples)
	c.mu.Unlock()
	if total == 0 {
		return
	}
	logger.LogWithAttrs(scanLog, slog.LevelWarn,
		fmt.Sprintf("scan summary: %d scanned book(s) not imported because their files already belong to another book; %d listed individually", total, listed),
		slog.Int("books_skipped_owned", total),
		slog.Int("books_listed", listed))
}

// handleOwnershipSkip is the one place a worker deals with a saveBook result
// that is an ownership skip: se is the record saveBook returned beside
// errFileOwnedByOtherBook. It returns false when se is nil (the caller handles
// err as a failure). For a skip it performs the staged-arrival append
// when the verdict asked for one, records the skip, and returns true: the
// caller must then run NOTHING else for this book.
//
// A failed append is a failure of this book, not a skip: it is counted for the
// run summary and recorded in failures (the op's file-failure list), because
// the new files stay unimported until a later scan manages it.
func handleOwnershipSkip(ctx context.Context, se *ownershipSkip, skips *OwnershipSkips, failures *FileFailures, knownHashes map[string]string, scanLog logger.Logger) bool {
	if se == nil {
		return false
	}
	if se.AppendTo != "" {
		n, aerr := appendScannedFilesToOwner(ctx, se.AppendTo, se.Unowned, knownHashes, scanLog)
		switch {
		case errors.Is(aerr, errAppendFrozenITunes):
			se.Reason = fmt.Sprintf("%d new file(s) arrived for an already-imported book in the iTunes-managed tree; they are not appended (iTunes owns that tree)", len(se.Unowned))
			if skips != nil {
				skips.Record(scanLog, se)
			}
		case aerr != nil:
			scanRunCountersFrom(ctx).appendFailed.Add(1)
			reason := fmt.Sprintf("could not append %d newly arrived file(s) to book %s: %v", len(se.Unowned), se.AppendTo, aerr)
			if failures != nil {
				failures.Record(scanLog, FileFailure{Path: se.Path, Stage: FileFailureStageSave, Reason: reason})
			} else {
				scanLog.Warn("scan: %s", reason)
			}
		default:
			scanLog.Info("scan: appended %d newly arrived file(s) to book %s (%s)", n, se.AppendTo, logger.SanitizeLogValue(se.Path))
			resynthesizeChaptersAfterAppend(ctx, se.AppendTo, se.Unowned, scanLog)
			rearmOwnerForRescan(se.AppendTo, scanLog)
		}
		return true
	}
	if skips != nil {
		skips.Record(scanLog, se)
	}
	return true
}

// rearmOwnerForRescan flags a book whose rows just grew so the next scan
// re-reads it instead of trusting a scan-cache stamp written before the new
// files existed.
func rearmOwnerForRescan(bookID string, scanLog logger.Logger) {
	store := getStore()
	if store == nil {
		return
	}
	if err := store.MarkNeedsRescan(bookID); err != nil {
		scanLog.Warn("scan: could not flag book %s for rescan after appending files: %v", bookID, err)
	}
}

type ownershipSkipsKey struct{}

// withOwnershipSkips attaches c to ctx so every ProcessBooksParallel call of a
// run records into one collector.
func withOwnershipSkips(ctx context.Context, c *OwnershipSkips) context.Context {
	return context.WithValue(ctx, ownershipSkipsKey{}, c)
}

// ownershipSkipsFrom returns the collector attached to ctx, or nil.
func ownershipSkipsFrom(ctx context.Context) *OwnershipSkips {
	c, _ := ctx.Value(ownershipSkipsKey{}).(*OwnershipSkips)
	return c
}

// resynthesizeChaptersAfterAppend rebuilds a grown book's chapters from ALL of
// its rows. PersistChaptersForBook is idempotent -- it returns as soon as a
// book has any chapters -- so without this a book first imported with 20 of
// its 40 files kept 20 files' worth of chapters after the other 20 were
// appended: the truncation moved from the rows into the chapter list.
//
// It only replaces a list that was itself synthesized from the files: one
// chapter per pre-append file, in order, each titled with that file's title or
// name (chaptersAreOnePerFile). A list from a metadata provider or edited by
// the user has other titles or another count, and is kept as it is -- the new
// files are then simply not in it, which is recoverable; overwriting someone's
// chapter list is not. If synthesis yields nothing the stale synthesized list
// is cleared rather than kept, so the next scan's PersistChaptersForBook
// derives it afresh.
func resynthesizeChaptersAfterAppend(ctx context.Context, bookID string, appended []string, scanLog logger.Logger) {
	store := getStore()
	if store == nil {
		return
	}
	ps := resolveChapterStore(store)
	if ps == nil {
		return
	}
	files, err := store.GetBookFiles(bookID)
	if err != nil {
		scanLog.Warn("scan: could not re-read book %s to rebuild its chapters after appending files: %v", bookID, err)
		return
	}
	if len(files) <= 1 {
		return
	}
	existing, err := ps.GetChaptersForBook(bookID)
	if err != nil {
		scanLog.Warn("scan: could not read the chapters of book %s after appending files; leaving them as they are: %v", bookID, err)
		return
	}
	if len(existing) > 0 {
		isNew := make(map[string]bool, len(appended))
		for _, f := range appended {
			isNew[f] = true
		}
		before := make([]database.BookFile, 0, len(files))
		for _, f := range files {
			if !isNew[f.FilePath] {
				before = append(before, f)
			}
		}
		if !chaptersAreOnePerFile(existing, before) {
			scanLog.Info("scan: kept the chapters of book %s after appending files: they were not synthesized from its files (provider- or user-supplied), so the new files are not in them", bookID)
			return
		}
	}
	chapters := synthesizeMultiFileChapters(ctx, files, scanLog)
	dbChapters := make([]database.Chapter, len(chapters))
	for i, c := range chapters {
		dbChapters[i] = database.Chapter{ID: c.ID, StartSec: c.StartSec, EndSec: c.EndSec, Title: c.Title}
	}
	if err := ps.SaveChaptersForBook(bookID, dbChapters); err != nil {
		scanLog.Warn("scan: could not rebuild chapters of book %s after appending files: %v", bookID, err)
	}
}

// chaptersAreOnePerFile reports whether chapters is the list
// synthesizeMultiFileChapters builds from files: one chapter per file, in the
// rows' order, titled with the row's title or the file's name (with or without
// its extension).
func chaptersAreOnePerFile(chapters []database.Chapter, files []database.BookFile) bool {
	if len(chapters) != len(files) {
		return false
	}
	for i, f := range files {
		base := filepath.Base(f.FilePath)
		t := chapters[i].Title
		if t != base && t != strings.TrimSuffix(base, filepath.Ext(base)) && (f.Title == "" || t != f.Title) {
			return false
		}
	}
	return true
}
