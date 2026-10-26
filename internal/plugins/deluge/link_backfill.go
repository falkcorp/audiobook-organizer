// file: internal/plugins/deluge/link_backfill.go
// version: 1.5.0
// guid: 7101d385-7f6e-4a95-bd2e-11760d0fed02
// last-edited: 2026-10-26

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// linkBackfillOpID is the op's canonical ID (internal/server/testdata/op_ids.golden).
const linkBackfillOpID = "deluge.link-backfill"

// linkMaxWorkers caps the worker pool. Each worker hashes at most 20 MB of a
// file at a time (filehash.BookFileHash reads head and tail above 100 MB, the
// whole file below it), so the cap bounds disk pressure on the download volume.
const linkMaxWorkers = 8

// linkHashesPerLimit and linkNoLimitHashBudget are the default hash budget
// (max_hashed_files): limit x 50 torrent files for a limited run, 100000 for
// an unlimited one. See linkHashBudget.
const (
	linkHashesPerLimit    = 50
	linkNoLimitHashBudget = 100000
)

// linkRoundHashFiles is how many torrent files one admission round may cost
// to hash (cross-seed checks included). Rounds are what the deadline stop
// works between: a round is never cut short (a window torrent whose
// cross-seed checks were skipped could link a row two torrents hold), so the
// run checks the clock only before admitting the next one, and a run with no
// limit hashes in many bounded rounds instead of one that lasts the whole
// listing. The first torrent of a round is admitted whatever it costs.
const linkRoundHashFiles = 2000

// linkDeadlineReserveMax caps the time a run keeps back from its deadline
// for deciding and writing: admitting stops once the next round would end
// inside the reserve. The reserve is a quarter of the time left at the start,
// up to this cap (4h timeout: 15 minutes), so a short deadline still examines
// something.
const linkDeadlineReserveMax = 15 * time.Minute

// linkCutoff is the time after which a run admits no more torrents: the
// context's deadline less the reserve. Zero when ctx has no deadline.
func linkCutoff(ctx context.Context, now time.Time) time.Time {
	deadline, ok := ctx.Deadline()
	if !ok {
		return time.Time{}
	}
	return deadline.Add(-min(linkDeadlineReserveMax, deadline.Sub(now)/4))
}

// linkStop is why a run stopped admitting torrents before the end of order.
type linkStop int

const (
	linkStopNone     linkStop = iota
	linkStopBudget            // max_hashed_files spent
	linkStopDeadline          // the op's timeout near (linkCutoff)
)

// linkHashBudget resolves max_hashed_files: the number of torrent files a run
// may hash before it stops admitting torrents. An explicit request wins. The
// default for a limited run is limit x linkHashesPerLimit, enough for tens of
// files per written torrent and for the torrents held back or unmatched that
// sit in front of it, yet far below the whole listing. Without a limit the
// run is meant to cover the listing, so the default (linkNoLimitHashBudget)
// only keeps a pathological listing inside the op's timeout.
func linkHashBudget(limit, requested int) int64 {
	switch {
	case requested > 0:
		return int64(requested)
	case limit > 0:
		return min(int64(limit), math.MaxInt64/linkHashesPerLimit) * linkHashesPerLimit
	}
	return linkNoLimitHashBudget
}

// linkBackfillStore is the slice of the store this op reads and writes. It is
// embedded in pluginStore rather than listed there method by method, so
// pluginStore stays within the interfacebloat limit (an embed counts as one
// declared entry).
type linkBackfillStore interface {
	// GetAllBookFilesCoreComplete, not GetAllBookFilesCore: the index it
	// feeds decides the relink hold-back by ABSENCE (a version no row names
	// is unsettled), so a memdb missing rows must not answer. A row it did
	// omit could only make the op hold more, never write more.
	GetAllBookFilesCoreComplete() ([]database.BookFileCore, error)
	BookFilesWithHash(hash string) ([]database.BookFile, error)
	BookFilesAtPath(path string) ([]database.BookFile, error)
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	// GetBooksByIDs reads the books of every verified row in one batch, so a
	// row of a soft-deleted (or missing) book is dropped before the
	// one-book-per-torrent-file check without a point read per row.
	GetBooksByIDs(ids []string) ([]database.Book, error)
}

// linkVersionReader is the read the relink hold-back makes (one per book of a
// planned row that names no version). pluginStore already declares it, so it is not repeated
// in linkBackfillStore.
type linkVersionReader interface {
	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
}

func (p *Plugin) linkBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          linkBackfillOpID,
		Preview:     sdk.PreviewSpec{Params: linkBackfillParams{}},
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "deluge",
		DisplayName: "Link Deluge torrents to library files",
		Description: "For each torrent under the discovery label, finds the library book file holding the same audio, " +
			"verifies it by content hash, and records the torrent hash on that file. Writes only BookFile.DelugeHash; " +
			"removes, moves and repoints nothing. A torrent file whose verified files belong to more than one book, " +
			"and a file two torrents both verify, are left unlinked as ambiguous. Defaults to a preview; " +
			"dry_run=false applies. Files of a book maintenance.relink-pre-fix-imports has not settled (the file " +
			"names no version and the book has an active or alt version with no torrent hash that no file names) " +
			"are held back and counted held_for_relink, in the preview too: setting their DelugeHash first would " +
			"turn them into that fixer's report-only hash_mismatch class. Apply the fixer, then run this op; " +
			"include_unsettled_relink=true writes them anyway (relink_fixer_would_link then counts the ones the " +
			"fixer would have linked). Params: dry_run, label (default: the discovery label), limit (0 = all: " +
			"caps the torrents written, counted after the hold-back; torrents are hashed in batches, torrents no " +
			"file carries yet first, until that many have writes, and the rest of the listing only for files " +
			"whose size equals one in the batches, for the cross-seed check. The batches grow past held, " +
			"unmatched and already-linked torrents; deferred_by_limit counts the torrents left for a later " +
			"run), max_hashed_files (the most torrent files a run hashes, cross-seed checks included: no more " +
			"torrents are admitted once the files of the next one would pass it, so a limited run cannot hash " +
			"most of the listing while torrents are held; default limit x 50, 100000 with no limit; the first " +
			"torrent is always examined; deferred_by_hash_budget counts the torrents left), start_after " +
			"(a torrent hash: begin the torrent order after it, wrapping round; a run that left torrents " +
			"unexamined reports next_start_after, so held or unmatched torrents at the front of the order " +
			"cannot starve the rest), include_unsettled_relink, workers (default: CPUs, max 8). Torrents are " +
			"hashed in bounded rounds; as the op's timeout nears a run admits no more, decides and writes what " +
			"it examined, and reports deferred_by_deadline and next_start_after to continue from.",
		ResumePolicy:    sdk.ResumeRestart, // idempotent: a linked row is skipped as already_linked
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  linkBackfillOpID,
		Writes:          []sdk.Resource{sdk.ResBookFiles},
		Permissions:     []auth.Permission{auth.PermIntegrationsManage},
		Cancellable:     true,
		Isolate:         false,
		Timeout:         4 * time.Hour,
		Run:             p.runLinkBackfill,
		// Manual only: no Schedule, no Triggers.
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
			sdk.CapFilesRead,
		},
	}
}

// linkBackfillParams is the op's request body. DryRun is a *bool pair resolved
// by opmode.ResolveDryRun, so an omitted mode is a preview.
type linkBackfillParams struct {
	DryRun      *bool  `json:"dry_run,omitempty"`
	DryRunCamel *bool  `json:"dryRun,omitempty"`
	Label       string `json:"label,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Workers     int    `json:"workers,omitempty"`
	// MaxHashedFiles bounds the torrent files one run hashes (linkHashBudget).
	MaxHashedFiles int `json:"max_hashed_files,omitempty"`
	// StartAfter is a torrent hash: the window order begins after it
	// (linkOrder). A run that leaves torrents unexamined reports the hash to
	// pass next as next_start_after.
	StartAfter string `json:"start_after,omitempty"`
	// IncludeUnsettledRelink writes the rows the relink hold-back would hold
	// (held_for_relink). Opt-in: the default holds them.
	IncludeUnsettledRelink bool `json:"include_unsettled_relink,omitempty"`
}

// linkTally counts outcomes across the run.
//
// Every field is atomic: the resolve, relink and write workers update them, and the
// RunItems Label closure reads them from inside each worker goroutine
// (run_items.go invokes Label there), so a plain int would be both a data race
// and a lost update.
//
// Units: torrents counts listed torrents and deferred_by_limit the ones left
// outside the limit window; files (audio files resolved), non_audio,
// empty_files, deselected, unmatched and outside_library count files of
// torrents inside the window, and beyond_limit_hashed the files outside it
// hashed for the cross-seed check; unverified counts candidate rows that
// failed verification; linked, already_linked, ambiguous, conflict,
// book_deleted, held_for_relink and relink_fixer_would_link count book_file
// rows of torrents inside the window; errors counts failed lookups, hashes,
// version reads and writes, inside the window or not.
type linkTally struct {
	torrents       atomic.Int64
	files          atomic.Int64
	nonAudio       atomic.Int64
	emptyFiles     atomic.Int64
	deselected     atomic.Int64
	unmatched      atomic.Int64
	outsideLibrary atomic.Int64
	unverified     atomic.Int64
	ambiguous      atomic.Int64
	alreadyLinked  atomic.Int64
	conflict       atomic.Int64
	bookDeleted    atomic.Int64
	deferred       atomic.Int64
	heldForRelink  atomic.Int64
	relinkPending  atomic.Int64
	// beyondLimit counts the files of torrents outside the limit window that
	// were hashed for the cross-seed check.
	beyondLimit atomic.Int64
	// hashedTotal counts every torrent file hashed so far this run, in the
	// window or not: what the hash budget is spent from. hashed (below) is
	// the window's share, counted once the run ends.
	hashedTotal atomic.Int64
	// hashBudget is the run's max_hashed_files, for the result.
	hashBudget atomic.Int64
	// deferredByBudget counts the torrents left outside the window because
	// the hash budget was spent (deferred counts the ones left by limit).
	deferredByBudget atomic.Int64
	// deferredByDeadline counts the torrents left outside the window because
	// the op's timeout was near (linkCutoff).
	deferredByDeadline atomic.Int64
	linked             atomic.Int64
	errors             atomic.Int64
	// hashed and hashMissing count torrent-file hash attempts and the ones
	// that failed because the file was not there, for the path-view note.
	hashed      atomic.Int64
	hashMissing atomic.Int64
	// provenanceUnavailable is 1 when the store does not expose
	// FileProvenanceStore, so rule (c) and the provenance verification arm
	// were skipped for the whole run.
	provenanceUnavailable atomic.Int64
}

func (t *linkTally) summary(dry bool) string {
	linkKey := "linked"
	if dry {
		linkKey = "would_link"
	}
	return fmt.Sprintf("%s=%d already_linked=%d unverified=%d ambiguous=%d conflict=%d book_deleted=%d held_for_relink=%d deferred_by_limit=%d deferred_by_hash_budget=%d deferred_by_deadline=%d errors=%d",
		linkKey, t.linked.Load(), t.alreadyLinked.Load(), t.unverified.Load(),
		t.ambiguous.Load(), t.conflict.Load(), t.bookDeleted.Load(), t.heldForRelink.Load(), t.deferred.Load(),
		t.deferredByBudget.Load(), t.deferredByDeadline.Load(), t.errors.Load())
}

// linkBackfillResult is the op's persisted result. Exactly one of Linked and
// WouldLink is set: Linked on a live run, WouldLink on a preview.
type linkBackfillResult struct {
	DryRun            bool   `json:"dry_run"`
	Label             string `json:"label"`
	Torrents          int64  `json:"torrents"`
	Files             int64  `json:"files"`
	Linked            *int64 `json:"linked,omitempty"`
	WouldLink         *int64 `json:"would_link,omitempty"`
	AlreadyLinked     int64  `json:"already_linked"`
	Unverified        int64  `json:"unverified"`
	Ambiguous         int64  `json:"ambiguous"`
	Conflict          int64  `json:"conflict"`
	BookDeleted       int64  `json:"book_deleted"`
	DeferredByLimit   int64  `json:"deferred_by_limit"`
	BeyondLimitHashed int64  `json:"beyond_limit_hashed"`
	// HashedFiles is every torrent file hashed (cross-seed checks included),
	// HashBudget the max_hashed_files it was held to, DeferredByHashBudget
	// the torrents left unexamined for want of it, and NextStartAfter the
	// start_after that continues past the last torrent examined ("" when no
	// torrent that no row carries yet is left).
	HashedFiles          int64 `json:"hashed_files"`
	HashBudget           int64 `json:"hash_budget"`
	DeferredByHashBudget int64 `json:"deferred_by_hash_budget"`
	// DeferredByDeadline is the torrents left unexamined because the op's
	// timeout was near: the run stopped admitting and wrote what it had.
	DeferredByDeadline    int64    `json:"deferred_by_deadline"`
	NextStartAfter        string   `json:"next_start_after,omitempty"`
	HeldForRelink         int64    `json:"held_for_relink"`
	RelinkFixerWouldLink  int64    `json:"relink_fixer_would_link"`
	NonAudio              int64    `json:"non_audio"`
	EmptyFiles            int64    `json:"empty_files"`
	Deselected            int64    `json:"deselected"`
	Errors                int64    `json:"errors"`
	ProvenanceUnavailable int64    `json:"provenance_unavailable"`
	Unmatched             int64    `json:"unmatched"`
	OutsideLibrary        int64    `json:"outside_library"`
	Notes                 []string `json:"notes,omitempty"`
}

func (t *linkTally) result(dry bool, label string, notes []string) linkBackfillResult {
	n := t.linked.Load()
	res := linkBackfillResult{
		DryRun:                dry,
		Label:                 label,
		Torrents:              t.torrents.Load(),
		Files:                 t.files.Load(),
		AlreadyLinked:         t.alreadyLinked.Load(),
		Unverified:            t.unverified.Load(),
		Ambiguous:             t.ambiguous.Load(),
		Conflict:              t.conflict.Load(),
		BookDeleted:           t.bookDeleted.Load(),
		DeferredByLimit:       t.deferred.Load(),
		BeyondLimitHashed:     t.beyondLimit.Load(),
		HashedFiles:           t.hashedTotal.Load(),
		HashBudget:            t.hashBudget.Load(),
		DeferredByHashBudget:  t.deferredByBudget.Load(),
		DeferredByDeadline:    t.deferredByDeadline.Load(),
		HeldForRelink:         t.heldForRelink.Load(),
		RelinkFixerWouldLink:  t.relinkPending.Load(),
		NonAudio:              t.nonAudio.Load(),
		EmptyFiles:            t.emptyFiles.Load(),
		Deselected:            t.deselected.Load(),
		Errors:                t.errors.Load(),
		ProvenanceUnavailable: t.provenanceUnavailable.Load(),
		Unmatched:             t.unmatched.Load(),
		OutsideLibrary:        t.outsideLibrary.Load(),
		Notes:                 notes,
	}
	if dry {
		res.WouldLink = &n
	} else {
		res.Linked = &n
	}
	return res
}

// linkDefaultWorkers is the pool size when the request names none:
// runtime.NumCPU(), capped at linkMaxWorkers.
func linkDefaultWorkers() int {
	return min(max(runtime.NumCPU(), 1), linkMaxWorkers)
}

// linkWorkers resolves the requested pool size: below 1 is the default, above
// linkMaxWorkers is clamped.
func linkWorkers(requested int) int {
	if requested < 1 {
		return linkDefaultWorkers()
	}
	return min(requested, linkMaxWorkers)
}

// linkRunOptions builds the RunItems options for both worker phases.
//
// A named function so a test runs the real options rather than a copy: an
// omitted Concurrency is clamped to 1 by run_items.go, which is how other
// full-library ops ended up sequential behind a worker-pool API.
func linkRunOptions(workers int, total int, tally *linkTally) registry.RunItemsOptions {
	return registry.RunItemsOptions{
		Concurrency:   linkWorkers(workers),
		ProgressTotal: total,
		Label: func(i, t int) string {
			// Runs inside a worker goroutine: read the tally through atomics only.
			return fmt.Sprintf("Torrents %d/%d (files=%d linked=%d already_linked=%d unverified=%d ambiguous=%d conflict=%d errors=%d)",
				i+1, t, tally.files.Load(), tally.linked.Load(), tally.alreadyLinked.Load(),
				tally.unverified.Load(), tally.ambiguous.Load(), tally.conflict.Load(), tally.errors.Load())
		},
	}
}

// linkCandidate is the part of a book_file row the op reads, as it stood when
// the candidate was found. The write re-checks it under the row's lock.
type linkCandidate struct {
	ID                   string
	BookID               string
	VersionID            string
	FilePath             string
	FileHash             string
	OriginalFileHash     string
	OriginalFileHashKind string
	DelugeHash           string
}

func candidateFromCore(c *database.BookFileCore) linkCandidate {
	return linkCandidate{
		ID: c.ID, BookID: c.BookID, VersionID: c.VersionID, FilePath: c.FilePath,
		FileHash: c.FileHash, OriginalFileHash: c.OriginalFileHash,
		OriginalFileHashKind: c.OriginalFileHashKind, DelugeHash: c.DelugeHash,
	}
}

func candidateFromRow(r *database.BookFile) linkCandidate {
	return linkCandidate{
		ID: r.ID, BookID: r.BookID, VersionID: r.VersionID, FilePath: r.FilePath,
		FileHash: r.FileHash, OriginalFileHash: r.OriginalFileHash,
		OriginalFileHashKind: r.OriginalFileHashKind, DelugeHash: r.DelugeHash,
	}
}

type baseSizeKey struct {
	base string
	size int64
}

// linkLibraryIndex is built once per run from GetAllBookFilesCoreComplete and
// only read afterwards, so workers share it without a lock.
type linkLibraryIndex struct {
	root       string
	byOrigPath map[string][]linkCandidate // rule (a): DelugeOriginalPath
	byBaseSize map[baseSizeKey][]linkCandidate
	// For the relink hold-back (relinkStates, holdBack): per book, the
	// version IDs its rows name, and how many of its rows name no version and
	// carry no torrent hash.
	namedVersions map[string]map[string]bool
	hashlessRows  map[string]int
	// rowHashes holds every DelugeHash some row carries (lower case), for
	// the limit window's order (linkOrder).
	rowHashes map[string]bool
}

func buildLinkLibraryIndex(root string, rows []database.BookFileCore) *linkLibraryIndex {
	idx := &linkLibraryIndex{
		root:          root,
		byOrigPath:    make(map[string][]linkCandidate),
		byBaseSize:    make(map[baseSizeKey][]linkCandidate),
		namedVersions: make(map[string]map[string]bool),
		hashlessRows:  make(map[string]int),
		rowHashes:     make(map[string]bool),
	}
	for i := range rows {
		r := &rows[i]
		c := candidateFromCore(r)
		if r.DelugeHash != "" {
			idx.rowHashes[strings.ToLower(r.DelugeHash)] = true
		}
		// A retired row is a version that left the library: it is never a link
		// candidate (rules a and d), and its version id and torrent-less state are
		// not the holder's, so they stay out of the relink hold-back counts too.
		// GetAllBookFilesCoreComplete already omits retired rows; this keeps the
		// index right if a caller hands it a list that does not. Its DelugeHash
		// still counts above: that torrent is already linked to something.
		if r.Retired {
			continue
		}
		switch {
		case r.VersionID != "":
			if idx.namedVersions[r.BookID] == nil {
				idx.namedVersions[r.BookID] = make(map[string]bool, 1)
			}
			idx.namedVersions[r.BookID][r.VersionID] = true
		case r.DelugeHash == "":
			idx.hashlessRows[r.BookID]++
		}
		if r.DelugeOriginalPath != "" {
			p := filepath.Clean(r.DelugeOriginalPath)
			idx.byOrigPath[p] = append(idx.byOrigPath[p], c)
		}
		// Rule (d) feeds verification only, and only from library rows.
		if r.FilePath != "" && r.FileSize > 0 && underLibraryRoot(root, r.FilePath) {
			k := baseSizeKey{base: filepath.Base(r.FilePath), size: r.FileSize}
			idx.byBaseSize[k] = append(idx.byBaseSize[k], c)
		}
	}
	return idx
}

// underLibraryRoot reports whether path lies strictly inside root. Spec 10
// §3.3.1: a link is made only to a row whose file is in the library. A row at
// the seeding path (never imported) also matches its own content hash; linking
// it would put it on GetBookFilesNeedingDelugeImportCore with a path outside
// the library, and the bulk importers would then copy it in.
//
// It is pathutil.IsUnderRoot, the containment test the importers and
// deluge.centralize make (pathutil.RelEscapesBase), so a row this op calls "in
// the library" is one they resolve to its own path: a directory named
// "..Extras" is inside the root for all of them.
func underLibraryRoot(root, path string) bool {
	return pathutil.IsUnderRoot(root, path)
}

// linkFile is one torrent file resolution will hash: audio, selected for
// download, not empty.
type linkFile struct {
	abs  string
	size int64 // Deluge's size for the file
}

// fileResult is one torrent file's resolution. It is computed at most once
// per run (done), by the one worker that owns its torrent.
type fileResult struct {
	done bool
	// failed: the hash or a lookup failed. missing: the hash failed because
	// the file was not on disk.
	failed, missing bool
	err             error // why it failed, for the log
	verified        []linkCandidate
	unverified      int64
	// outside: rules found rows, all outside the library root. unmatched: no
	// rule found any row.
	outside, unmatched bool
	// counted: its outcome is in the tally (set once its torrent is inside
	// the limit window).
	counted bool
}

// linkTorrent is one listed torrent: prepared without I/O, then resolved file
// by file. inWindow torrents are resolved in full and may be written; the
// others only have the files whose size equals a size inside the window
// resolved, for the cross-seed check, and are never written.
type linkTorrent struct {
	hash    string // "" when Deluge's id is invalid: nothing is resolved
	files   []linkFile
	results []fileResult // parallel to files
	// Counted into the tally when the torrent enters the window.
	nonAudio, deselected, emptyFiles int64
	inWindow                         bool
}

// torrentResolution is one torrent's input to decide.
type torrentResolution struct {
	hash string
	// inWindow: inside the limit window, so its rows may be written and are
	// counted. A torrent outside it only makes rows ambiguous.
	inWindow bool
	// failed is set when any lookup for the torrent errored. Such a torrent
	// links nothing: an incomplete lookup could hide a second row or a second
	// torrent, and must never read as "no candidates". The rows it did verify
	// still count toward the ambiguity partition in decide.
	failed bool
	// groups holds, per torrent file, the rows that verified against it. A
	// group is kept whole because decide judges it whole: a torrent file
	// whose rows belong to more than one live book links none of them.
	groups [][]linkCandidate
}

// relinkState is what one book's versions say about
// maintenance.relink-pre-fix-imports, for the hold-back.
type relinkState struct {
	// stranded counts the book's active or alt versions with an empty
	// TorrentHash that no book_file row names.
	stranded int
	// readErr: the version read failed, so the book's state is unknown.
	readErr bool
}

// linkRun holds one run's shared state plus the tally. idx, store, versions,
// prov, audio and hash are read-only after setup; the caches (live, asked,
// relink) are touched only between worker phases, by the run goroutine.
type linkRun struct {
	store    linkBackfillStore
	versions linkVersionReader
	prov     database.FileProvenanceStore // nil: rule (c) and the provenance arm are skipped
	idx      *linkLibraryIndex
	audio    interface{ MatchPath(string) bool }
	hash     func(path string) (string, error)
	tally    *linkTally
	log      *slog.Logger

	live   map[string]bool // book ID -> exists and is not soft-deleted
	asked  map[string]bool // book IDs liveBooks has read
	relink map[string]relinkState
}

func (p *Plugin) runLinkBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	log := reporter.Logger()

	var params linkBackfillParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("%s: decode params: %w", linkBackfillOpID, err)
		}
	}
	dry, modeErr := opmode.ResolveDryRun(linkBackfillOpID, params.DryRun, params.DryRunCamel)
	if modeErr != nil {
		// ResolveDryRun pairs the error with true: the run previews, and the
		// result's notes carry the error so the caller sees why.
		log.Error("dry_run and dryRun disagree; previewing", "error", modeErr)
	}

	label := strings.TrimSpace(params.Label)
	if label == "" {
		label = config.AppConfig.DelugeDiscoveryLabel
	}
	root := config.AppConfig.RootDir
	workers := linkWorkers(params.Workers)
	limit := max(params.Limit, 0)
	budget := linkHashBudget(limit, params.MaxHashedFiles)
	startAfter := ""
	if s := strings.TrimSpace(params.StartAfter); s != "" {
		norm, err := delugeclient.NormalizeTorrentID(s)
		if err != nil {
			return fmt.Errorf("%s: start_after %q is not a torrent hash: %w; nothing was written", linkBackfillOpID, s, err)
		}
		startAfter = norm
	}

	if p.client == nil {
		return fmt.Errorf("%s: deluge is not configured; nothing was written", linkBackfillOpID)
	}
	if root == "" {
		return fmt.Errorf("%s: root_dir is not set, so no row can be shown to be in the library; nothing was written", linkBackfillOpID)
	}
	if p.store == nil {
		return fmt.Errorf("%s: store not available; nothing was written", linkBackfillOpID)
	}

	sdk.NewProgress(reporter, 0).Start(fmt.Sprintf("Listing Deluge torrents (label %q)...", label))
	details, err := p.client.ListTorrentDetailsByLabel(label)
	if err != nil {
		return fmt.Errorf("%s: list torrents from deluge (label %q): %w; nothing was written", linkBackfillOpID, label, err)
	}

	tally := &linkTally{}
	tally.torrents.Store(int64(len(details)))
	tally.hashBudget.Store(budget)

	var notes []string
	if modeErr != nil {
		notes = append(notes, "previewed: "+modeErr.Error())
	}

	// Nothing listed: there is nothing to match, so skip the full-library
	// book_file scan below and say why the run did nothing.
	if len(details) == 0 {
		notes = append(notes, fmt.Sprintf("no_torrents: Deluge lists no torrent under label %q; nothing to link", label))
		res := tally.result(dry, label, notes)
		res.NextStartAfter = startAfter
		if err := registry.ReporterSetResult(reporter, res); err != nil {
			return fmt.Errorf("%s: persist result: %w", linkBackfillOpID, err)
		}
		log.Info("finished: no torrents under the label", "dry_run", dry, "label", label)
		sdk.NewProgress(reporter, 0).Done(fmt.Sprintf("No torrents under label %q", label))
		return nil
	}

	rows, err := p.store.GetAllBookFilesCoreComplete()
	if err != nil {
		return fmt.Errorf("%s: load book files: %w; nothing was written", linkBackfillOpID, err)
	}
	run := &linkRun{
		store:    p.store,
		versions: p.store,
		idx:      buildLinkLibraryIndex(root, rows),
		audio:    delugeclient.AudioExtensions(),
		hash:     p.linkHash,
		tally:    tally,
		log:      log,
		live:     make(map[string]bool),
		asked:    make(map[string]bool),
		relink:   make(map[string]relinkState),
	}
	if run.hash == nil {
		run.hash = filehash.BookFileHash
	}
	now := p.linkNow
	if now == nil {
		now = time.Now
	}
	roundFiles := int64(p.linkRoundFiles)
	if roundFiles <= 0 {
		roundFiles = linkRoundHashFiles
	}
	cutoff := linkCutoff(ctx, now())

	if prov, ok := database.AsCapability[database.FileProvenanceStore](p.store); ok {
		run.prov = prov
	} else {
		tally.provenanceUnavailable.Store(1)
		notes = append(notes, "provenance_unavailable: the store does not expose the file provenance chain; "+
			"rule (c) and the provenance verification arm were skipped")
		log.Warn("file provenance store unavailable; skipping provenance lookup and verification")
	}

	log.Info("starting", "dry_run", dry, "label", label, "torrents", len(details),
		"workers", workers, "limit", limit, "max_hashed_files", budget, "start_after", startAfter,
		"include_unsettled_relink", params.IncludeUnsettledRelink)

	torrents := make([]*linkTorrent, len(details))
	for i := range details {
		torrents[i] = run.prepare(&details[i])
	}
	order := linkOrder(torrents, run.idx, startAfter)

	// Phases 1 and 2, repeated while limit is unmet. Nothing is written here.
	//
	// limit bounds the torrents written, counted after the hold-back. Each
	// round moves the next torrents of order into the window, as many as
	// limit still lacks (all of them with no limit) within the round's hash
	// allowance (linkRoundHashFiles), and hashes their files in full. So the
	// hashing stops once limit torrents have writes, but a window that keeps
	// meeting torrents with nothing to write (held for the relink fixer,
	// unmatched; those carry no row's hash and stay at the front of order on
	// every run) keeps growing. The hash budget (max_hashed_files) is what
	// stops that: admitWithinBudget takes torrents in order only while the
	// files they and their cross-seed checks will hash fit in what is left of
	// it, and the first torrent that does not fit ends the run's examining
	// (the later ones are not skipped to, so the order stays the order).
	// Torrents it leaves are deferred_by_hash_budget, and the next run
	// continues past the last one examined (start_after / next_start_after).
	//
	// The op's timeout stops it the same way. Before each round after the
	// first, a run whose clock plus the last round's duration passes
	// linkCutoff (the deadline less a reserve) admits nothing more, decides
	// and writes what its window holds, and reports next_start_after:
	// deferred_by_deadline. Until 2026-10-11 a run with no limit hashed the
	// whole listing in one round and wrote only after it, so a listing that
	// outlasted the 4h timeout wrote nothing and persisted no cursor, and
	// every rerun hashed the same torrents again. A round is never cut short
	// (below): the window it leaves would hold torrents whose cross-seed
	// checks had not run.
	//
	// A torrent outside the window has only the files whose
	// Deluge size equals the size of a window file hashed: a cross-seed holds
	// identical bytes, so a size no window file has cannot make a window row
	// ambiguous (filehash.BookFileHash digests the size with a sampled file,
	// and a whole file's size is its own). That check is never skipped to save
	// budget -- a skipped cross-seed could link a row two torrents hold -- so
	// its files are charged to the budget in advance instead. Each torrent
	// file is hashed at most once per run, however many rounds there are.
	// Torrents some row already carries go to the back of order, so a later
	// limited run reaches new torrents instead of re-hashing linked ones.
	//
	// Phase 2 runs after every round of a limited run, since limit is counted
	// on the plan; a run with no limit decides once, after the last round.
	var (
		plan      []torrentWrites
		stats     decideStats
		decided   bool
		window    int
		stop      linkStop
		lastRound time.Duration
	)
	decideWindow := func() error {
		resolutions := make([]torrentResolution, len(torrents))
		for i, t := range torrents {
			resolutions[i] = t.resolution()
		}
		// Phase 2: decide, single-threaded, from every torrent's result at
		// once. The books of every verified row are read in batches first,
		// so a row of a soft-deleted book is dropped before the
		// one-book-per-file check.
		live, err := run.liveBooks(resolutions)
		if err != nil {
			return fmt.Errorf("%s: read the books of verified files: %w; nothing was written", linkBackfillOpID, err)
		}
		plan, stats = run.decide(resolutions, live)

		// Phase 2b: hold back the rows of books the relink fixer has not
		// settled, before limit counts the plan.
		if err := run.relinkStates(ctx, reporter, plan, workers); err != nil {
			return err
		}
		plan, stats.held, stats.fixerWouldLink = run.holdBack(plan, params.IncludeUnsettledRelink)
		decided = true
		return nil
	}
	for {
		grow := len(order) - window
		if limit > 0 {
			grow = min(grow, limit-len(plan))
		}
		if grow <= 0 {
			break
		}
		if window > 0 && !cutoff.IsZero() && !now().Add(lastRound).Before(cutoff) {
			stop = linkStopDeadline
			break
		}
		cand := order[window : window+grow]
		fits := admitWithinBudget(torrents, cand, budget-tally.hashedTotal.Load(), window == 0)
		if fits == 0 {
			// Second guard on termination: whatever else holds, a round
			// that admits nothing ends the loop (it would only repeat).
			stop = linkStopBudget
			break
		}
		admitted := admitWithinBudget(torrents, cand[:fits], roundFiles, true)
		for _, i := range cand[:admitted] {
			run.enterWindow(torrents[i])
		}
		window += admitted

		started := now()
		if err := run.resolveRound(ctx, reporter, torrents, workers); err != nil {
			return err
		}
		decided = false
		if limit > 0 {
			if err := decideWindow(); err != nil {
				return err
			}
		}
		// The whole round, decide included, is what the next one is
		// estimated to take.
		lastRound = now().Sub(started)
		if limit > 0 && len(plan) >= limit {
			break
		}
		if admitted == fits && fits < grow {
			// The budget, not the round allowance, ended this round's
			// admitting: the next torrent does not fit what is left.
			stop = linkStopBudget
			break
		}
	}
	if !decided {
		if err := decideWindow(); err != nil {
			return err
		}
	}
	run.countWindow(torrents)
	stats.addTo(tally)
	limitMet := limit > 0 && len(plan) >= limit
	if limit > 0 && len(plan) > limit {
		// Not expected (each window torrent adds at most one plan entry),
		// kept so limit holds whatever decide does.
		tally.deferred.Add(int64(len(plan) - limit))
		plan = plan[:limit]
	}
	// The torrents left outside the window are the limit's when the plan
	// reached it, whatever else also stopped admitting (a round can meet
	// limit and spend the budget at once: those torrents would have been
	// left anyway); otherwise the hash budget's or the deadline's, whichever
	// stopped admitting.
	if rest := int64(len(order) - window); rest > 0 {
		switch {
		case limitMet:
			tally.deferred.Add(rest)
		case stop == linkStopBudget:
			tally.deferredByBudget.Add(rest)
		case stop == linkStopDeadline:
			tally.deferredByDeadline.Add(rest)
		default:
			tally.deferred.Add(rest)
		}
	}
	nextAfter := run.nextStartAfter(torrents, order, window, startAfter)
	for _, t := range torrents {
		if t.inWindow {
			continue
		}
		for i := range t.results {
			if t.results[i].done {
				tally.beyondLimit.Add(1)
			}
		}
	}
	if n := tally.deferred.Load(); n > 0 {
		notes = append(notes, fmt.Sprintf("limit: %d torrent(s) written; deferred_by_limit counts the %d torrent(s) "+
			"left for a later run, and beyond_limit_hashed the %d file(s) of those hashed only for the cross-seed check",
			len(plan), n, tally.beyondLimit.Load()))
	}
	if n := tally.deferredByBudget.Load(); n > 0 {
		notes = append(notes, fmt.Sprintf("hash_budget: %d file(s) hashed of the max_hashed_files budget of %d, so %d torrent(s) "+
			"were not examined (deferred_by_hash_budget); raise max_hashed_files or run again with start_after=%q "+
			"(next_start_after) to continue past the last torrent examined", tally.hashedTotal.Load(), budget, n, nextAfter))
	}
	if n := tally.deferredByDeadline.Load(); n > 0 {
		notes = append(notes, fmt.Sprintf("deadline: the op's timeout was near, so %d torrent(s) were not examined "+
			"(deferred_by_deadline); what was examined was decided and written. Run again with start_after=%q "+
			"(next_start_after) to continue past the last torrent examined", n, nextAfter))
	}
	if n, missing := tally.hashed.Load(), tally.hashMissing.Load(); n > 0 && missing == n {
		notes = append(notes, fmt.Sprintf("path_views_differ: none of the %d torrent file(s) hashed was found on disk; "+
			"Deluge's save paths are likely mounted at a different path on this server than in Deluge", n))
	}
	if n := tally.heldForRelink.Load(); n > 0 {
		notes = append(notes, fmt.Sprintf("held_for_relink: %d file(s) were held back: their books have a version "+
			"maintenance.relink-pre-fix-imports has not settled (active or alt, no torrent hash, named by no file). "+
			"Setting DelugeHash first would turn them into that fixer's report-only hash_mismatch class. Apply "+
			"maintenance.relink-pre-fix-imports, then run this op again; include_unsettled_relink=true writes them anyway", n))
	}
	if n := tally.relinkPending.Load(); n > 0 {
		verb := "would set"
		if !dry {
			verb = "set"
		}
		notes = append(notes, fmt.Sprintf("relink_fixer_would_link: include_unsettled_relink is on, and this run %s "+
			"DelugeHash on %d file(s) that maintenance.relink-pre-fix-imports plans as would_link; once set, that "+
			"fixer classes them hash_mismatch (report-only) and cannot link them. Apply maintenance.relink-pre-fix-imports "+
			"first, then run this op", verb, n))
	}

	// Phase 3: write. Workers partition by torrent, and phase 2 already gave
	// each book_file row to at most one torrent (a row two torrents resolve to
	// is skipped for both as ambiguous), so no two workers can target one row.
	// Only window torrents are in plan. ModifyBookFile's under-lock re-check
	// guards against everything else that writes book_file rows.
	if dry {
		for _, w := range plan {
			tally.linked.Add(int64(len(w.rows)))
		}
	} else if len(plan) > 0 {
		sdk.NewProgress(reporter, len(plan)).Start(fmt.Sprintf("Linking %d torrent(s)...", len(plan)))
		if err := registry.RunItems(ctx, reporter, plan, func(ctx context.Context, w torrentWrites) error {
			for _, c := range w.rows {
				if err := ctx.Err(); err != nil {
					return err
				}
				run.link(w.hash, c)
			}
			return nil
		}, linkRunOptions(workers, len(plan), tally)); err != nil {
			// Interrupted while writing: the rows linked so far are written,
			// so persist what was done (and the cursor) before failing.
			// next_start_after stays the one this run computed: a rerun from
			// it skips the torrents examined here, and their unwritten rows
			// are picked up when the order wraps round (already-linked rows
			// are skipped).
			notes = append(notes, fmt.Sprintf("interrupted: writing stopped (%v) after %d row(s) linked", err, tally.linked.Load()))
			res := tally.result(dry, label, notes)
			res.NextStartAfter = nextAfter
			if setErr := registry.ReporterSetResult(reporter, res); setErr != nil {
				log.Error("persist the partial result failed", "error", setErr)
			}
			return err
		}
	}

	res := tally.result(dry, label, notes)
	res.NextStartAfter = nextAfter
	if err := registry.ReporterSetResult(reporter, res); err != nil {
		return fmt.Errorf("%s: persist result: %w", linkBackfillOpID, err)
	}
	log.Info("finished", "dry_run", dry, "summary", tally.summary(dry),
		"torrents", res.Torrents, "files", res.Files, "non_audio", res.NonAudio,
		"empty_files", res.EmptyFiles, "deselected", res.Deselected,
		"unmatched", res.Unmatched, "outside_library", res.OutsideLibrary,
		"beyond_limit_hashed", res.BeyondLimitHashed,
		"hashed_files", res.HashedFiles, "hash_budget", res.HashBudget,
		"deferred_by_hash_budget", res.DeferredByHashBudget, "deferred_by_deadline", res.DeferredByDeadline,
		"next_start_after", res.NextStartAfter,
		"relink_fixer_would_link", res.RelinkFixerWouldLink,
		"provenance_unavailable", res.ProvenanceUnavailable)
	sdk.NewProgress(reporter, len(details)).Done(tally.summary(dry))
	return nil
}

// prepare reads one listed torrent without I/O: its normalized hash and the
// files resolution will hash (D62: audio only; Deluge-deselected and empty
// files are skipped and counted).
func (r *linkRun) prepare(d *delugeclient.TorrentDetail) *linkTorrent {
	hash, err := delugeclient.NormalizeTorrentID(d.Hash)
	if err != nil {
		r.tally.errors.Add(1)
		r.log.Warn("skipping torrent with an invalid id", "error", err)
		return &linkTorrent{}
	}
	t := &linkTorrent{hash: hash}
	for _, f := range d.Files {
		switch {
		case !r.audio.MatchPath(f.Path):
			t.nonAudio++
		case deselected(d, f):
			// Priority 0: Deluge was told not to download it, so it is
			// usually absent or partial. Skip the file, not the torrent.
			t.deselected++
		case f.Size <= 0:
			// Every empty file has the same digest, so an empty file
			// "verifies" against every empty library row of every book.
			t.emptyFiles++
		default:
			t.files = append(t.files, linkFile{abs: filepath.Clean(d.AbsPath(f)), size: f.Size})
		}
	}
	t.results = make([]fileResult, len(t.files))
	return t
}

// linkRank groups torrents for the window order: 0 for a torrent no
// book_file row carries the hash of yet (work to do, or held or unmatched
// forever), 1 for one some row already carries (linked or imported before),
// 2 for an invalid id.
func linkRank(t *linkTorrent, idx *linkLibraryIndex) int {
	switch {
	case t.hash == "":
		return 2
	case idx.rowHashes[t.hash]:
		return 1
	}
	return 0
}

// linkOrder is the order torrents enter the limit window: torrents whose
// hash no book_file row carries yet first, then the ones some row already
// carries (linked or imported before), and invalid ids last. So a limited run
// that linked a batch reaches new torrents the next time instead of
// re-hashing the same ones.
//
// Within each group torrents are by hash, rotated to begin after startAfter
// (those with a greater hash first, then the rest, wrapping round). Torrents
// held for the relink fixer or matching no row write nothing, so no row ever
// carries their hash and they keep their place at the front of the first
// group; under the hash budget a fixed order would examine the same few of
// them on every run and never reach the rest. A run reports where it stopped
// (nextStartAfter) and the next one is given that, so successive runs
// cover the whole group before any torrent is examined twice. Empty
// startAfter is the plain hash order.
func linkOrder(ts []*linkTorrent, idx *linkLibraryIndex, startAfter string) []int {
	order := make([]int, len(ts))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ta, tb := ts[order[a]], ts[order[b]]
		if ra, rb := linkRank(ta, idx), linkRank(tb, idx); ra != rb {
			return ra < rb
		}
		if wa, wb := ta.hash > startAfter, tb.hash > startAfter; wa != wb {
			return wa
		}
		return ta.hash < tb.hash
	})
	return order
}

// nextStartAfter is the start_after that continues past the torrents this
// run examined: the hash of the last window torrent no row carries yet, when
// such a torrent is left outside the window. When none is left (the whole
// group was examined) it is "" and a later run starts over from the
// beginning. When the window took none, the cursor stays where it was.
func (r *linkRun) nextStartAfter(ts []*linkTorrent, order []int, window int, startAfter string) string {
	if window >= len(order) || linkRank(ts[order[window]], r.idx) != 0 {
		return ""
	}
	for k := window - 1; k >= 0; k-- {
		if t := ts[order[k]]; linkRank(t, r.idx) == 0 {
			return t.hash
		}
	}
	return startAfter
}

// admitWithinBudget returns how many of the leading torrents of cand (indexes
// into ts, in window order) can enter the window with the files they will
// cost to hash within remaining. The cost of a torrent is its own unhashed
// files plus the unhashed files of torrents outside the window whose size
// equals one of its file sizes the window did not have before (the
// cross-seed check resolveRound will run), each file counted once however
// many admitted torrents pull it in. It stops at the first torrent that does
// not fit rather than skipping to a cheaper one. A first admission (first:
// the window is empty) takes the first torrent whatever it costs, or a
// budget below one torrent's size would examine nothing on every run.
func admitWithinBudget(ts []*linkTorrent, cand []int, remaining int64, first bool) int {
	// Sizes the window already holds need no entry: resolveRound hashes every
	// file of such a size the moment it enters, so none is left unhashed. So
	// sizes holds only those this pass admits, and outside the unhashed files
	// of torrents outside the window, by size.
	sizes := make(map[int64]bool)
	outside := make(map[int64]int64)
	for _, t := range ts {
		if t.inWindow {
			continue
		}
		for i, f := range t.files {
			if !t.results[i].done {
				outside[f.size]++
			}
		}
	}
	n := 0
	for _, idx := range cand {
		t := ts[idx]
		fresh := make(map[int64]bool) // sizes of t's files the window lacks
		for _, f := range t.files {
			if !sizes[f.size] {
				fresh[f.size] = true
			}
		}
		// Every unhashed file of a fresh size is hashed once t is in: t's
		// own (outside[s] counts them while t is outside the window) and
		// those of the other torrents outside it.
		var cost int64
		for s := range fresh {
			cost += outside[s]
		}
		if cost > remaining && !(first && n == 0) {
			break
		}
		n++
		remaining -= cost
		for s := range fresh {
			sizes[s] = true
		}
	}
	return n
}

// enterWindow moves t into the limit window and counts its torrent-level
// outcomes.
func (r *linkRun) enterWindow(t *linkTorrent) {
	t.inWindow = true
	r.tally.nonAudio.Add(t.nonAudio)
	r.tally.deselected.Add(t.deselected)
	r.tally.emptyFiles.Add(t.emptyFiles)
}

// resolveRound resolves, on the worker pool, every file not yet resolved of
// every window torrent, and the files of the other torrents whose size
// equals a window file's size. Each worker writes only its own torrent's
// results.
func (r *linkRun) resolveRound(ctx context.Context, reporter sdk.Reporter, ts []*linkTorrent, workers int) error {
	sizes := make(map[int64]bool)
	for _, t := range ts {
		if t.inWindow {
			for _, f := range t.files {
				sizes[f.size] = true
			}
		}
	}
	var work []*linkTorrent
	for _, t := range ts {
		for i, f := range t.files {
			if !t.results[i].done && (t.inWindow || sizes[f.size]) {
				work = append(work, t)
				break
			}
		}
	}
	if len(work) == 0 {
		return nil
	}
	sdk.NewProgress(reporter, len(work)).Start(fmt.Sprintf("Resolving %d torrent(s)...", len(work)))
	return registry.RunItems(ctx, reporter, work, func(ctx context.Context, t *linkTorrent) error {
		for i, f := range t.files {
			if t.results[i].done || (!t.inWindow && !sizes[f.size]) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			t.results[i] = r.resolveFile(f.abs, f.size)
			r.tally.hashedTotal.Add(1)
			if t.results[i].failed {
				r.tally.errors.Add(1)
				r.log.Warn("torrent file lookup failed; this torrent links nothing", "torrent", t.hash,
					"in_window", t.inWindow, "error", t.results[i].err)
			}
		}
		return nil
	}, linkRunOptions(workers, len(work), r.tally))
}

// countWindow puts the per-file outcomes of every window torrent into the
// tally, once each.
func (r *linkRun) countWindow(ts []*linkTorrent) {
	for _, t := range ts {
		if !t.inWindow {
			continue
		}
		for i := range t.results {
			fr := &t.results[i]
			if !fr.done || fr.counted {
				continue
			}
			fr.counted = true
			r.tally.files.Add(1)
			r.tally.hashed.Add(1)
			if fr.missing {
				r.tally.hashMissing.Add(1)
			}
			r.tally.unverified.Add(fr.unverified)
			switch {
			case fr.outside:
				r.tally.outsideLibrary.Add(1)
			case fr.unmatched:
				r.tally.unmatched.Add(1)
			}
		}
	}
}

// resolution is t's input to decide: every resolved file's verified rows.
func (t *linkTorrent) resolution() torrentResolution {
	res := torrentResolution{hash: t.hash, inWindow: t.inWindow, failed: t.hash == ""}
	for i := range t.results {
		fr := &t.results[i]
		if !fr.done {
			continue
		}
		if fr.failed {
			res.failed = true
		}
		if len(fr.verified) > 0 {
			res.groups = append(res.groups, fr.verified)
		}
	}
	return res
}

// deselected reports whether Deluge's file_priorities marks f as not to be
// downloaded (priority 0). FilePriorities is indexed by the file's Index; a
// missing or short list means every file is selected.
func deselected(d *delugeclient.TorrentDetail, f delugeclient.TorrentFile) bool {
	return f.Index >= 0 && f.Index < len(d.FilePriorities) && d.FilePriorities[f.Index] == 0
}

// resolveFile finds the library candidates for one torrent file and returns
// the verified ones. It touches no shared state but the read-only index.
func (r *linkRun) resolveFile(abs string, size int64) fileResult {
	// filehash.BookFileHash is the only digest the op compares: head and tail
	// above 100 MB, the whole file below, no audio decoding.
	out := fileResult{done: true}
	h, err := r.hash(abs)
	if err != nil {
		out.failed, out.err = true, fmt.Errorf("hash torrent file: %w", err)
		out.missing = errors.Is(err, fs.ErrNotExist)
		return out
	}
	cands, sawOutside, err := r.candidates(abs, size, h)
	if err != nil {
		out.failed, out.err = true, err
		return out
	}
	if len(cands) == 0 {
		out.outside = sawOutside
		out.unmatched = !sawOutside
		return out
	}
	for _, c := range cands {
		ok, err := r.verify(c, h, size)
		if err != nil {
			out.failed, out.err = true, err
			out.verified = nil
			return out
		}
		if !ok {
			out.unverified++
			continue
		}
		out.verified = append(out.verified, c)
	}
	return out
}

// candidates tries rules (a) to (d) in order and returns the in-library
// candidates of the first rule that yields any. Path equality alone never
// writes: every candidate still goes through verify. sawOutside reports that
// some rule found rows, all of them outside the library root.
func (r *linkRun) candidates(abs string, size int64, h string) ([]linkCandidate, bool, error) {
	sawOutside := false
	pick := func(in []linkCandidate) []linkCandidate {
		seen := make(map[string]bool, len(in))
		var keep []linkCandidate
		for _, c := range in {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			if !underLibraryRoot(r.idx.root, c.FilePath) {
				sawOutside = true
				continue
			}
			keep = append(keep, c)
		}
		return keep
	}

	// (a) DelugeOriginalPath equals the torrent file's path.
	if got := pick(r.idx.byOrigPath[abs]); len(got) > 0 {
		return got, false, nil
	}

	// (b) Rows whose file_hash or original_file_hash is the content hash.
	byHash, err := r.store.BookFilesWithHash(h)
	if err != nil {
		return nil, false, fmt.Errorf("rows with hash: %w", err)
	}
	if got := pick(rowsToCandidates(byHash)); len(got) > 0 {
		return got, false, nil
	}

	// (c) Provenance: every path that ever carried the hash.
	if r.prov != nil {
		events, err := r.prov.FindFileEventsByHash(h)
		if err != nil {
			return nil, false, fmt.Errorf("provenance events for hash: %w", err)
		}
		var viaProv []linkCandidate
		seenPath := make(map[string]bool, len(events))
		for _, e := range events {
			if e.Path == "" || seenPath[e.Path] {
				continue
			}
			seenPath[e.Path] = true
			at, err := r.store.BookFilesAtPath(e.Path)
			if err != nil {
				return nil, false, fmt.Errorf("rows at provenance path: %w", err)
			}
			viaProv = append(viaProv, rowsToCandidates(at)...)
		}
		if got := pick(viaProv); len(got) > 0 {
			return got, false, nil
		}
	}

	// (d) Same basename and size under the library root: a candidate source
	// only, never a reason to write on its own.
	if got := pick(r.idx.byBaseSize[baseSizeKey{base: filepath.Base(abs), size: size}]); len(got) > 0 {
		return got, false, nil
	}
	return nil, sawOutside, nil
}

// rowsToCandidates turns store rows into link candidates, leaving out retired
// rows: BookFilesWithHash and BookFilesAtPath return them by design (a retired
// row still owns its hash and path for claim purposes), but a retired row is a
// version that left the library, so linking a torrent to it would attach the
// torrent to a file the holder no longer plays.
func rowsToCandidates(rows []database.BookFile) []linkCandidate {
	out := make([]linkCandidate, 0, len(rows))
	for i := range rows {
		if rows[i].Retired {
			continue
		}
		out = append(out, candidateFromRow(&rows[i]))
	}
	return out
}

// verify reports whether candidate c holds the torrent file's content: h
// equals c's FileHash, or c's sampled OriginalFileHash (a legacy original of
// unknown kind is not accepted), or the digest of the earliest provenance
// event for c with a matching size.
//
// The provenance writers (file_provenance_capture.go, write_tags_safe.go)
// record SHA256Full, the whole-file digest, and not SHA256Chunk. h is
// filehash.BookFileHash, which is the whole-file SHA-256 for a file at or
// below filehash.Threshold, so SHA256Full is accepted there; above it the two
// digests differ and only SHA256Chunk can match.
func (r *linkRun) verify(c linkCandidate, h string, size int64) (bool, error) {
	if c.FileHash != "" && c.FileHash == h {
		return true, nil
	}
	if c.OriginalFileHash != "" && c.OriginalFileHash == h && c.OriginalFileHashKind == database.FileHashKindSampled {
		return true, nil
	}
	if r.prov == nil {
		return false, nil
	}
	hist, err := r.prov.GetFileHistory(c.ID)
	if err != nil {
		return false, fmt.Errorf("provenance history: %w", err)
	}
	if len(hist) == 0 {
		return false, nil
	}
	first := hist[0].Digest
	if first.SizeBytes != size {
		return false, nil
	}
	return first.SHA256Chunk == h || (size <= filehash.Threshold && first.SHA256Full == h), nil
}

// torrentWrites is one torrent's share of the write plan.
type torrentWrites struct {
	hash string
	rows []linkCandidate
}

// liveBooksChunk bounds one GetBooksByIDs call: it reads full Book rows.
const liveBooksChunk = 500

// liveBooks reads, in batches, the book of every verified row not read in an
// earlier round, and returns the IDs of those that exist and are not
// soft-deleted (r.live, which it extends).
func (r *linkRun) liveBooks(resolutions []torrentResolution) (map[string]bool, error) {
	var ids []string
	for _, res := range resolutions {
		for _, g := range res.groups {
			for _, c := range g {
				if !r.asked[c.BookID] {
					r.asked[c.BookID] = true
					ids = append(ids, c.BookID)
				}
			}
		}
	}
	sort.Strings(ids)
	for start := 0; start < len(ids); start += liveBooksChunk {
		books, err := r.store.GetBooksByIDs(ids[start:min(start+liveBooksChunk, len(ids))])
		if err != nil {
			return nil, err
		}
		for i := range books {
			if !books[i].IsSoftDeleted() {
				r.live[books[i].ID] = true
			}
		}
	}
	return r.live, nil
}

// decideStats is decide's and holdBack's count of one round, put into the
// tally once, for the final round only.
type decideStats struct {
	ambiguous, deleted, alreadyLinked, conflict int64
	held, fixerWouldLink                        int64
}

func (d decideStats) addTo(t *linkTally) {
	t.ambiguous.Add(d.ambiguous)
	t.bookDeleted.Add(d.deleted)
	t.alreadyLinked.Add(d.alreadyLinked)
	t.conflict.Add(d.conflict)
	t.heldForRelink.Add(d.held)
	t.relinkPending.Add(d.fixerWouldLink)
}

// decide partitions the verified rows by book_file ID across every torrent
// and returns the rows to link, grouped by torrent and sorted for a stable
// order. In this order:
//
//  1. A row whose book is soft-deleted or missing is dropped (book_deleted).
//  2. A torrent file whose remaining rows belong to more than one book links
//     none of them (ambiguous): identical bytes on unrelated books -- a shared
//     intro, an unmerged duplicate -- say nothing about which book the
//     torrent is.
//  3. A row two or more torrents resolve to (cross-seeded copies) is skipped
//     for all of them (ambiguous): there is no arbitrary winner. A torrent
//     outside the limit window counts here: its size-matched files were
//     resolved for exactly this.
//  4. A row already holding this torrent's hash is already_linked; one
//     holding another hash is a conflict and is left as it is.
//
// Only window torrents' rows are planned or counted: a row only torrents
// outside the window reached belongs to a later run. ambiguous and
// book_deleted count each such row once, whatever the number of files or
// torrents that reached it. decide writes nothing to the tally; the caller
// adds the final round's stats.
func (r *linkRun) decide(resolutions []torrentResolution, live map[string]bool) ([]torrentWrites, decideStats) {
	var stats decideStats
	byRow := make(map[string]map[string]linkCandidate) // row ID -> torrent hash -> candidate
	failed := make(map[string]bool)
	window := make(map[string]bool)
	inScope := make(map[string]bool) // rows a window torrent verified
	ambiguous := make(map[string]bool)
	deleted := make(map[string]bool)
	for _, res := range resolutions {
		if res.hash == "" {
			continue // invalid torrent id: nothing was resolved
		}
		if res.inWindow {
			window[res.hash] = true
		}
		// A failed torrent still takes part in the ambiguity partition with
		// the rows it did verify: if it and another torrent both verified a
		// row, the row is ambiguous whatever else went wrong with it. It only
		// loses its own writes, below.
		if res.failed {
			failed[res.hash] = true
		}
		for _, g := range res.groups {
			kept := make([]linkCandidate, 0, len(g))
			books := make(map[string]bool, 1)
			for _, c := range g {
				if res.inWindow {
					inScope[c.ID] = true
				}
				if !live[c.BookID] {
					deleted[c.ID] = true
					continue
				}
				kept = append(kept, c)
				books[c.BookID] = true
			}
			if len(books) > 1 {
				for _, c := range kept {
					ambiguous[c.ID] = true
				}
				continue
			}
			for _, c := range kept {
				if byRow[c.ID] == nil {
					byRow[c.ID] = make(map[string]linkCandidate, 1)
				}
				byRow[c.ID][res.hash] = c
			}
		}
	}

	perTorrent := make(map[string][]linkCandidate)
	for id, torrents := range byRow {
		if ambiguous[id] {
			continue
		}
		if len(torrents) > 1 {
			ambiguous[id] = true
			continue
		}
		for hash, c := range torrents {
			switch {
			case !window[hash]:
				// Outside the limit window: a later run's row.
			case failed[hash]:
				// Counted in errors when the lookup failed; no write.
			case strings.EqualFold(c.DelugeHash, hash):
				stats.alreadyLinked++
			case c.DelugeHash != "":
				stats.conflict++
			default:
				perTorrent[hash] = append(perTorrent[hash], c)
			}
		}
	}
	for id := range ambiguous {
		if inScope[id] {
			stats.ambiguous++
		}
	}
	for id := range deleted {
		if inScope[id] {
			stats.deleted++
		}
	}

	plan := make([]torrentWrites, 0, len(perTorrent))
	for hash, rows := range perTorrent {
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		plan = append(plan, torrentWrites{hash: hash, rows: rows})
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].hash < plan[j].hash })
	return plan, stats
}

// relinkStates reads, on the worker pool, the versions of every book with a
// planned row that names no version, once per run (r.relink), and records
// its relinkState. A failed read is counted in errors and recorded as
// readErr, which holdBack treats as unsettled.
func (r *linkRun) relinkStates(ctx context.Context, reporter sdk.Reporter, plan []torrentWrites, workers int) error {
	var books []string
	seen := make(map[string]bool)
	for _, w := range plan {
		for _, c := range w.rows {
			if c.VersionID == "" && !seen[c.BookID] {
				seen[c.BookID] = true
				if _, done := r.relink[c.BookID]; !done {
					books = append(books, c.BookID)
				}
			}
		}
	}
	if len(books) == 0 {
		return nil
	}
	sort.Strings(books)
	states := make([]relinkState, len(books))
	pos := make(map[string]int, len(books))
	for i, id := range books {
		pos[id] = i
	}
	opts := linkRunOptions(workers, len(books), r.tally)
	opts.Label = func(i, t int) string { return fmt.Sprintf("Checking relink order: book %d/%d", i+1, t) }
	err := registry.RunItems(ctx, reporter, books, func(_ context.Context, bookID string) error {
		st := &states[pos[bookID]] // each worker owns one slot
		vers, err := r.versions.GetBookVersionsByBookID(bookID)
		if err != nil {
			r.tally.errors.Add(1)
			r.log.Warn("relink hold-back: read versions failed; holding the book's files", "book_id", bookID, "error", err)
			st.readErr = true
			return nil
		}
		for i := range vers {
			v := &vers[i]
			if (v.Status == database.BookVersionStatusActive || v.Status == database.BookVersionStatusAlt) &&
				v.TorrentHash == "" && !r.idx.namedVersions[bookID][v.ID] {
				st.stranded++
			}
		}
		return nil
	}, opts)
	if err != nil {
		return err
	}
	for i, id := range books {
		r.relink[id] = states[i]
	}
	return nil
}

// holdBack drops from plan the rows of books maintenance.relink-pre-fix-imports
// has not settled, unless include (include_unsettled_relink) is set, and
// returns the rest with the count it held. relinkStates must have run on
// plan.
//
// Unsettled: the row names no version, and its book has at least one active
// or alt version with an empty TorrentHash that no row names, or its versions
// could not be read. That fixer links such a row to such a version when both
// carry no torrent hash. Once this op sets the row's DelugeHash the pair no
// longer matches: the fixer's doc lists "the row carries one and the version
// none" as hash_mismatch, report-only, and versions.CreateIngestVersion's
// adopt path refuses a version whose TorrentHash differs from the row's
// (WhyNotLinkable, strandedVersion). Treating "" as compatible there would
// plan links its apply then refuses, so this op waits for the fixer instead.
//
// The hold is wider than the fixer's own would_link on purpose: a book with
// two such versions, or two hashless unlinked rows, is the fixer's ambiguous
// class today and can become would_link once its owner settles one of them,
// and linking the row first would rule that out. Any version of the book no
// row names is enough; the index is GetAllBookFilesCoreComplete's, so a row
// the store failed to list can only widen the hold.
//
// fixerWouldLink counts the rows written (include set) that the fixer plans
// as would_link today: exactly one such version and at most one hashless
// unlinked row on the book (more is its hashless ambiguous class).
func (r *linkRun) holdBack(plan []torrentWrites, include bool) (kept []torrentWrites, held, fixerWouldLink int64) {
	kept = make([]torrentWrites, 0, len(plan))
	for _, w := range plan {
		rows := make([]linkCandidate, 0, len(w.rows))
		for _, c := range w.rows {
			if c.VersionID == "" {
				st := r.relink[c.BookID]
				if st.readErr || st.stranded > 0 {
					if !include {
						held++
						continue
					}
					if !st.readErr && st.stranded == 1 && r.idx.hashlessRows[c.BookID] <= 1 {
						fixerWouldLink++
					}
				}
			}
			rows = append(rows, c)
		}
		if len(rows) > 0 {
			kept = append(kept, torrentWrites{hash: w.hash, rows: rows})
		}
	}
	return kept, held, fixerWouldLink
}

// link writes torrentHash to one row's DelugeHash and nothing else. Under the
// row's lock it re-checks that DelugeHash is still empty and that FilePath,
// FileHash, OriginalFileHash and OriginalFileHashKind -- everything verify
// read -- are the values that were verified; otherwise it writes nothing. It
// never sets ImportedFromDelugeAt, DownloadHash or DelugeOriginalPath.
func (r *linkRun) link(torrentHash string, c linkCandidate) {
	type outcome int
	const (
		outLinked outcome = iota
		outAlready
		outConflict
		outMoved
	)
	var got outcome
	row, err := r.store.ModifyBookFile(c.BookID, c.ID, func(bf *database.BookFile) error {
		switch {
		case bf.FilePath != c.FilePath, bf.FileHash != c.FileHash,
			bf.OriginalFileHash != c.OriginalFileHash, bf.OriginalFileHashKind != c.OriginalFileHashKind:
			got = outMoved
			return database.ErrSkipBookFileWrite
		case strings.EqualFold(bf.DelugeHash, torrentHash):
			got = outAlready
			return database.ErrSkipBookFileWrite
		case bf.DelugeHash != "":
			got = outConflict
			return database.ErrSkipBookFileWrite
		}
		bf.DelugeHash = torrentHash
		got = outLinked
		return nil
	})
	switch {
	case errors.Is(err, database.ErrBookFileDurabilityUnknown):
		// The write was applied; only its fsync failed.
		r.log.Warn("link written but its fsync failed: durability unknown", "book_file_id", c.ID, "error", err)
	case err != nil:
		r.tally.errors.Add(1)
		r.log.Warn("link write failed", "book_file_id", c.ID, "error", err)
		return
	case row == nil:
		r.tally.errors.Add(1)
		r.log.Warn("book file vanished before the link write", "book_file_id", c.ID)
		return
	}
	switch got {
	case outLinked:
		r.tally.linked.Add(1)
	case outAlready:
		r.tally.alreadyLinked.Add(1)
	case outConflict:
		r.tally.conflict.Add(1)
	case outMoved:
		// The row's path or content hashes changed since it was verified:
		// the verification no longer holds.
		r.tally.unverified.Add(1)
	}
}
