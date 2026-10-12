// file: internal/plugins/deluge/link_backfill.go
// version: 1.1.0
// guid: 7101d385-7f6e-4a95-bd2e-11760d0fed02
// last-edited: 2026-10-11

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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

// linkBackfillStore is the slice of the store this op reads and writes. It is
// embedded in pluginStore rather than listed there method by method, so
// pluginStore stays within the interfacebloat limit (an embed counts as one
// declared entry).
type linkBackfillStore interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	BookFilesWithHash(hash string) ([]database.BookFile, error)
	BookFilesAtPath(path string) ([]database.BookFile, error)
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	// GetBooksByIDs reads the books of every verified row in one batch, so a
	// row of a soft-deleted (or missing) book is dropped before the
	// one-book-per-torrent-file check without a point read per row.
	GetBooksByIDs(ids []string) ([]database.Book, error)
}

// linkVersionReader is the read the relink-order report makes (one per book
// of a planned row). pluginStore already declares it, so it is not repeated
// in linkBackfillStore.
type linkVersionReader interface {
	GetBookVersionsByBookID(bookID string) ([]database.BookVersion, error)
}

func (p *Plugin) linkBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          linkBackfillOpID,
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "deluge",
		DisplayName: "Link Deluge torrents to library files",
		Description: "For each torrent under the discovery label, finds the library book file holding the same audio, " +
			"verifies it by content hash, and records the torrent hash on that file. Writes only BookFile.DelugeHash; " +
			"removes, moves and repoints nothing. A torrent file whose verified files belong to more than one book, " +
			"and a file two torrents both verify, are left unlinked as ambiguous. Defaults to a preview; " +
			"dry_run=false applies. Run maintenance.relink-pre-fix-imports' apply FIRST: a file it would link is " +
			"one this op turns into its report-only hash_mismatch class (the preview's relink_fixer_would_link " +
			"counts them). Params: dry_run, label (default: the discovery label), limit (torrents written, applied " +
			"after every torrent is checked; 0 = all), workers (default: CPUs, max 8).",
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
}

// linkTally counts outcomes across the run.
//
// Every field is atomic: phase-1 and phase-3 workers write them, and the
// RunItems Label closure reads them from inside each worker goroutine
// (run_items.go invokes Label there), so a plain int would be both a data race
// and a lost update.
//
// Units: torrents counts listed torrents; files (audio files resolved),
// non_audio, empty_files, deselected, unmatched and outside_library count
// torrent files; unverified counts candidate rows that failed verification;
// linked, already_linked, ambiguous, conflict, book_deleted,
// deferred_by_limit and relink_fixer_would_link count book_file rows; errors
// counts failed lookups, hashes and writes.
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
	relinkPending  atomic.Int64
	linked         atomic.Int64
	errors         atomic.Int64
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
	return fmt.Sprintf("%s=%d already_linked=%d unverified=%d ambiguous=%d conflict=%d book_deleted=%d deferred_by_limit=%d errors=%d",
		linkKey, t.linked.Load(), t.alreadyLinked.Load(), t.unverified.Load(),
		t.ambiguous.Load(), t.conflict.Load(), t.bookDeleted.Load(), t.deferred.Load(), t.errors.Load())
}

// linkBackfillResult is the op's persisted result. Exactly one of Linked and
// WouldLink is set: Linked on a live run, WouldLink on a preview.
type linkBackfillResult struct {
	DryRun                bool     `json:"dry_run"`
	Label                 string   `json:"label"`
	Torrents              int64    `json:"torrents"`
	Files                 int64    `json:"files"`
	Linked                *int64   `json:"linked,omitempty"`
	WouldLink             *int64   `json:"would_link,omitempty"`
	AlreadyLinked         int64    `json:"already_linked"`
	Unverified            int64    `json:"unverified"`
	Ambiguous             int64    `json:"ambiguous"`
	Conflict              int64    `json:"conflict"`
	BookDeleted           int64    `json:"book_deleted"`
	DeferredByLimit       int64    `json:"deferred_by_limit"`
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

// linkLibraryIndex is built once per run from GetAllBookFilesCore and only
// read afterwards, so workers share it without a lock.
type linkLibraryIndex struct {
	root       string
	byOrigPath map[string][]linkCandidate // rule (a): DelugeOriginalPath
	byBaseSize map[baseSizeKey][]linkCandidate
	// For the relink-order report (relinkFixerWouldLink): per book, the
	// version IDs its rows name, and how many of its rows name no version and
	// carry no torrent hash.
	namedVersions map[string]map[string]bool
	hashlessRows  map[string]int
}

func buildLinkLibraryIndex(root string, rows []database.BookFileCore) *linkLibraryIndex {
	idx := &linkLibraryIndex{
		root:          root,
		byOrigPath:    make(map[string][]linkCandidate),
		byBaseSize:    make(map[baseSizeKey][]linkCandidate),
		namedVersions: make(map[string]map[string]bool),
		hashlessRows:  make(map[string]int),
	}
	for i := range rows {
		r := &rows[i]
		c := candidateFromCore(r)
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

// torrentResolution is phase 1's output for one torrent. Each worker writes
// only its own slot of a preallocated slice, so phase 1 shares no map.
type torrentResolution struct {
	hash string
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

// linkRun holds one run's shared, read-only state plus the tally.
type linkRun struct {
	store    linkBackfillStore
	versions linkVersionReader
	prov     database.FileProvenanceStore // nil: rule (c) and the provenance arm are skipped
	idx      *linkLibraryIndex
	audio    interface{ MatchPath(string) bool }
	tally    *linkTally
	log      *slog.Logger
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
	// limit is applied to the write plan after decide, never here: a torrent
	// cut from the listing could be the cross-seed twin that makes a row
	// ambiguous, and the survivor would then win the row arbitrarily.

	rows, err := p.store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("%s: load book files: %w; nothing was written", linkBackfillOpID, err)
	}

	tally := &linkTally{}
	tally.torrents.Store(int64(len(details)))
	run := &linkRun{
		store:    p.store,
		versions: p.store,
		idx:      buildLinkLibraryIndex(root, rows),
		audio:    delugeclient.AudioExtensions(),
		tally:    tally,
		log:      log,
	}

	var notes []string
	if modeErr != nil {
		notes = append(notes, "previewed: "+modeErr.Error())
	}
	if prov, ok := database.AsCapability[database.FileProvenanceStore](p.store); ok {
		run.prov = prov
	} else {
		tally.provenanceUnavailable.Store(1)
		notes = append(notes, "provenance_unavailable: the store does not expose the file provenance chain; "+
			"rule (c) and the provenance verification arm were skipped")
		log.Warn("file provenance store unavailable; skipping provenance lookup and verification")
	}

	log.Info("starting", "dry_run", dry, "label", label, "torrents", len(details),
		"workers", workers, "limit", params.Limit)

	// Phase 1: resolve and verify every torrent, in parallel. Nothing is
	// written here.
	resolutions := make([]torrentResolution, len(details))
	indices := make([]int, len(details))
	for i := range indices {
		indices[i] = i
	}
	sdk.NewProgress(reporter, len(details)).Start(fmt.Sprintf("Resolving %d torrent(s)...", len(details)))
	if err := registry.RunItems(ctx, reporter, indices, func(ctx context.Context, i int) error {
		resolutions[i] = run.resolveTorrent(ctx, &details[i])
		return ctx.Err()
	}, linkRunOptions(workers, len(details), tally)); err != nil {
		return err
	}
	if n, missing := tally.hashed.Load(), tally.hashMissing.Load(); n > 0 && missing == n {
		notes = append(notes, fmt.Sprintf("path_views_differ: none of the %d torrent file(s) hashed was found on disk; "+
			"Deluge's save paths are likely mounted at a different path on this server than in Deluge", n))
	}

	// Phase 2: decide, single-threaded, from every torrent's result at once.
	// The books of every verified row are read in one batch first, so a row
	// of a soft-deleted book is dropped before the one-book-per-file check.
	live, err := run.liveBooks(resolutions)
	if err != nil {
		return fmt.Errorf("%s: read the books of verified files: %w; nothing was written", linkBackfillOpID, err)
	}
	plan := run.decide(resolutions, live)
	if params.Limit > 0 && len(plan) > params.Limit {
		for _, w := range plan[params.Limit:] {
			tally.deferred.Add(int64(len(w.rows)))
		}
		plan = plan[:params.Limit]
		notes = append(notes, fmt.Sprintf("limit: %d torrent(s) written of the planned ones; deferred_by_limit "+
			"counts the files left for a later run", params.Limit))
	}

	// Phase 2b: report the planned rows maintenance.relink-pre-fix-imports
	// would link today. Setting their DelugeHash first moves them to that
	// fixer's report-only hash_mismatch class.
	if err := run.relinkFixerWouldLink(ctx, reporter, plan, workers); err != nil {
		return err
	}
	if n := tally.relinkPending.Load(); n > 0 {
		verb := "would set"
		if !dry {
			verb = "set"
		}
		notes = append(notes, fmt.Sprintf("relink_fixer_would_link: this run %s DelugeHash on %d file(s) that "+
			"maintenance.relink-pre-fix-imports plans as would_link; once set, that fixer classes them hash_mismatch "+
			"(report-only) and cannot link them. Apply maintenance.relink-pre-fix-imports first, then run this op", verb, n))
	}

	// Phase 3: write. Workers partition by torrent, and phase 2 already gave
	// each book_file row to at most one torrent (a row two torrents resolve to
	// is skipped for both as ambiguous), so no two workers can target one row.
	// ModifyBookFile's under-lock re-check guards against everything else that
	// writes book_file rows.
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
			return err
		}
	}

	res := tally.result(dry, label, notes)
	if err := registry.ReporterSetResult(reporter, res); err != nil {
		return fmt.Errorf("%s: persist result: %w", linkBackfillOpID, err)
	}
	log.Info("finished", "dry_run", dry, "summary", tally.summary(dry),
		"torrents", res.Torrents, "files", res.Files, "non_audio", res.NonAudio,
		"empty_files", res.EmptyFiles, "deselected", res.Deselected,
		"unmatched", res.Unmatched, "outside_library", res.OutsideLibrary,
		"relink_fixer_would_link", res.RelinkFixerWouldLink,
		"provenance_unavailable", res.ProvenanceUnavailable)
	sdk.NewProgress(reporter, len(details)).Done(tally.summary(dry))
	return nil
}

// resolveTorrent runs candidate resolution (§4 rules a-d) and verification for
// every audio file of one torrent.
func (r *linkRun) resolveTorrent(ctx context.Context, d *delugeclient.TorrentDetail) torrentResolution {
	hash, err := delugeclient.NormalizeTorrentID(d.Hash)
	if err != nil {
		r.tally.errors.Add(1)
		r.log.Warn("skipping torrent with an invalid id", "error", err)
		return torrentResolution{failed: true}
	}
	res := torrentResolution{hash: hash}
	for _, f := range d.Files {
		if ctx.Err() != nil {
			res.failed = true
			return res
		}
		if !r.audio.MatchPath(f.Path) {
			r.tally.nonAudio.Add(1) // D62: audio files only
			continue
		}
		if deselected(d, f) {
			// Priority 0: Deluge was told not to download it, so it is
			// usually absent or partial. Skip the file, not the torrent.
			r.tally.deselected.Add(1)
			continue
		}
		if f.Size <= 0 {
			// Every empty file has the same digest, so an empty file
			// "verifies" against every empty library row of every book.
			r.tally.emptyFiles.Add(1)
			continue
		}
		r.tally.files.Add(1)
		abs := filepath.Clean(d.AbsPath(f))
		verified, err := r.resolveFile(abs, f.Size)
		if err != nil {
			r.tally.errors.Add(1)
			r.log.Warn("torrent file lookup failed; this torrent links nothing", "torrent", hash, "error", err)
			res.failed = true
			continue
		}
		if len(verified) > 0 {
			res.groups = append(res.groups, verified)
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
// the verified ones. It returns an error only for a failed hash or lookup.
func (r *linkRun) resolveFile(abs string, size int64) ([]linkCandidate, error) {
	// filehash.BookFileHash is the only digest the op compares: head and tail
	// above 100 MB, the whole file below, no audio decoding.
	r.tally.hashed.Add(1)
	h, err := filehash.BookFileHash(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			r.tally.hashMissing.Add(1)
		}
		return nil, fmt.Errorf("hash torrent file: %w", err)
	}
	cands, sawOutside, err := r.candidates(abs, size, h)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		if sawOutside {
			r.tally.outsideLibrary.Add(1)
		} else {
			r.tally.unmatched.Add(1)
		}
		return nil, nil
	}
	var out []linkCandidate
	for _, c := range cands {
		ok, err := r.verify(c, h, size)
		if err != nil {
			return nil, err
		}
		if !ok {
			r.tally.unverified.Add(1)
			continue
		}
		out = append(out, c)
	}
	return out, nil
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

func rowsToCandidates(rows []database.BookFile) []linkCandidate {
	out := make([]linkCandidate, 0, len(rows))
	for i := range rows {
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

// liveBooks reads, in batches, the book of every verified row and returns the
// IDs of those that exist and are not soft-deleted.
func (r *linkRun) liveBooks(resolutions []torrentResolution) (map[string]bool, error) {
	seen := make(map[string]bool)
	var ids []string
	for _, res := range resolutions {
		for _, g := range res.groups {
			for _, c := range g {
				if !seen[c.BookID] {
					seen[c.BookID] = true
					ids = append(ids, c.BookID)
				}
			}
		}
	}
	sort.Strings(ids)
	live := make(map[string]bool, len(ids))
	for start := 0; start < len(ids); start += liveBooksChunk {
		books, err := r.store.GetBooksByIDs(ids[start:min(start+liveBooksChunk, len(ids))])
		if err != nil {
			return nil, err
		}
		for i := range books {
			if !books[i].IsSoftDeleted() {
				live[books[i].ID] = true
			}
		}
	}
	return live, nil
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
//     for all of them (ambiguous): there is no arbitrary winner.
//  4. A row already holding this torrent's hash is already_linked; one
//     holding another hash is a conflict and is left as it is.
//
// ambiguous and book_deleted count each row once, whatever the number of
// files or torrents that reached it.
func (r *linkRun) decide(resolutions []torrentResolution, live map[string]bool) []torrentWrites {
	byRow := make(map[string]map[string]linkCandidate) // row ID -> torrent hash -> candidate
	failed := make(map[string]bool)
	ambiguous := make(map[string]bool)
	deleted := make(map[string]bool)
	for _, res := range resolutions {
		if res.hash == "" {
			continue // invalid torrent id: nothing was resolved
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
			case failed[hash]:
				// Counted in errors when the lookup failed; no write.
			case strings.EqualFold(c.DelugeHash, hash):
				r.tally.alreadyLinked.Add(1)
			case c.DelugeHash != "":
				r.tally.conflict.Add(1)
			default:
				perTorrent[hash] = append(perTorrent[hash], c)
			}
		}
	}
	r.tally.ambiguous.Add(int64(len(ambiguous)))
	r.tally.bookDeleted.Add(int64(len(deleted)))

	plan := make([]torrentWrites, 0, len(perTorrent))
	for hash, rows := range perTorrent {
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		plan = append(plan, torrentWrites{hash: hash, rows: rows})
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].hash < plan[j].hash })
	return plan
}

// relinkFixerWouldLink counts the planned rows maintenance.relink-pre-fix-imports
// classes would_link today, into tally.relinkPending. It reports; it changes
// no plan.
//
// Why only report. That fixer links a pre-fix import's row to the one live
// version of its book no row names whose TorrentHash equals the row's
// DelugeHash (empty matches empty). Pre-fix imports recorded no torrent hash
// on either, so the pair matched on "". Once this op sets the row's DelugeHash
// the version (still "") no longer matches: the fixer's doc lists "the row
// carries one and the version none" as hash_mismatch, not inferred, and
// versions.CreateIngestVersion's adopt path refuses a version whose
// TorrentHash differs from the row's (WhyNotLinkable, strandedVersion).
// Treating "" as compatible there would plan links its apply then refuses, so
// the fixer is left as it is and the order is stated instead: apply the relink
// fixer first.
//
// The fixer's would_link, for a row this op links (hashless, unlinked, under
// the library root and so inside its allow-list, on a live book): exactly one
// active or alt version of the book with an empty TorrentHash that no row
// names, and at most one hashless unlinked row on the book (more is its
// hashless ambiguous class). One version read per book, on the worker pool.
func (r *linkRun) relinkFixerWouldLink(ctx context.Context, reporter sdk.Reporter, plan []torrentWrites, workers int) error {
	byBook := make(map[string]int)
	for _, w := range plan {
		for _, c := range w.rows {
			if c.VersionID == "" {
				byBook[c.BookID]++
			}
		}
	}
	if len(byBook) == 0 {
		return nil
	}
	books := make([]string, 0, len(byBook))
	for id := range byBook {
		books = append(books, id)
	}
	sort.Strings(books)
	opts := linkRunOptions(workers, len(books), r.tally)
	opts.Label = func(i, t int) string { return fmt.Sprintf("Checking relink order: book %d/%d", i+1, t) }
	return registry.RunItems(ctx, reporter, books, func(_ context.Context, bookID string) error {
		if r.idx.hashlessRows[bookID] > 1 {
			return nil
		}
		vers, err := r.versions.GetBookVersionsByBookID(bookID)
		if err != nil {
			r.tally.errors.Add(1)
			r.log.Warn("relink-order check: read versions failed; not counted", "book_id", bookID, "error", err)
			return nil
		}
		stranded := 0
		for i := range vers {
			v := &vers[i]
			if (v.Status == database.BookVersionStatusActive || v.Status == database.BookVersionStatusAlt) &&
				v.TorrentHash == "" && !r.idx.namedVersions[bookID][v.ID] {
				stranded++
			}
		}
		if stranded == 1 {
			r.tally.relinkPending.Add(int64(byBook[bookID]))
		}
		return nil
	}, opts)
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
