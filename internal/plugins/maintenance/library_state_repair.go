// file: internal/plugins/maintenance/library_state_repair.go
// version: 1.0.0
// guid: 7c41d9e2-8b06-4f53-9a17-2e5d0b3c68af
// last-edited: 2026-09-20

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- maintenance.repair-library-state ---
//
// WHY THIS EXISTS. Audiobookshelf lists only books that are BOTH primary AND
// library_state == "organized" (internal/server/handlers/abs/browse.go:223-230,
// absItemFilterBase). A book that was organized and then had its state stamped
// back to the scanner's creation default is therefore invisible in the app while
// its files sit exactly where they belong.
//
// The stamping was real: saveBookToDatabase's merge overlaid the bare "imported"
// default onto existing rows, and because the scan root and the organized tree
// are the same directory that meant every organized book on every scan. It is
// FIXED (internal/scanner/scanner.go:4021-4023 now refuses to let the bare
// default overwrite a state already on the row), so the rows this op repairs are
// residue from before that fix and will not regress.
//
// Census on prod 2026-09-20, every row paged rather than sampled (a 400-row
// sample read 98% under libroot where offset 2000 read 54% — sampling this
// population is actively misleading):
//
//	imported         6,484 total: 5,872 under libroot, 362 iTunes, 250 elsewhere
//	suspicious       6,126 total: 5,900 under libroot,  46 iTunes, 180 elsewhere
//	organized_source   135 total:    25 under libroot,  90 iTunes,  20 elsewhere
//
// # Why this is a STATE repair and not an organize
//
// library.organize moves files and has no dry-run mode. 93% of the population
// above is already in the canonical tree, so organize is the wrong instrument
// for it: there is nothing to move. This op writes one column and touches no
// file.
//
// # The evidence gate
//
// "Was under libroot" alone is not proof a book was organized — an unorganized
// import can sit there too. OrganizedFileHash is the corroborating witness: it
// is written by the organize path and by nothing else. On prod, 5,857 of the
// 5,872 imported-under-libroot rows carry it, and 5,900 of 5,900 suspicious
// ones do. The 15 that do not are left alone rather than guessed at.
//
// So a row is repaired only when ALL hold:
//   - FilePath is under RootDir, matched on a separator boundary;
//   - FilePath is NOT under any iTunes root (books/itunes/** is hands-off, and
//     those rows are excluded by the libroot test anyway — this is belt and
//     braces, because a misconfigured RootDir would otherwise sweep them in);
//   - OrganizedFileHash is set and non-empty;
//   - LibraryState is one of the ALLOWLISTED stale states below.
//
// # Which states are repairable, and which are deliberately not
//
//   - "imported" — the bare creation default. Stale by the fixed bug. Repaired.
//   - "suspicious" — DERIVED by the MinBookSizeBytes guard (scanner.go:1453),
//     not a default, so overriding it discards a real signal the scanner
//     recorded about a small file. Repaired ONLY with include_suspicious:true,
//     which is a separate decision from clearing a stale default.
//   - "organized_source" — the correct, current state of the demoted source of
//     an organized copy (organizer/service.go:2177). NEVER repaired: the
//     organized twin is the copy that should be visible.
//   - "organized" — already right; counted as already_organized.
//   - anything else, including a nil state — left alone and counted, never
//     guessed. An allowlist, not a blocklist: a state nobody has classified
//     must not be silently rewritten to "organized".
type libraryStateRepairParams struct {
	// DryRun defaults to TRUE and is a *bool for exactly that reason: a plain
	// bool cannot tell "absent" from "false", and the safe reading of an
	// omitted field on a writing op is "do not write".
	DryRun *bool `json:"dry_run"`
	// IncludeSuspicious widens the allowlist to the derived "suspicious" state.
	// Defaults false: see the note above on why that is a separate decision.
	IncludeSuspicious bool `json:"include_suspicious"`
	// BookIDs, when non-empty, scopes the run to exactly those books. Anything
	// outside the list is untouched however well it matches.
	BookIDs []string `json:"book_ids"`
}

func (p libraryStateRepairParams) dryRun() bool { return p.DryRun == nil || *p.DryRun }

const (
	libStateImported        = "imported"
	libStateSuspicious      = "suspicious"
	libStateOrganized       = "organized"
	libStateOrganizedSource = "organized_source"

	libraryStateRepairPageSize = 1000
)

// Outcome buckets. Every scanned row lands in exactly one, so a row that is not
// repaired is visible with a reason rather than silently absent.
const (
	lsrRepaired           = "repaired"
	lsrWouldRepair        = "would_repair"
	lsrAlreadyOrganized   = "already_organized"
	lsrNotUnderLibroot    = "not_under_libroot"
	lsrITunes             = "itunes_hands_off"
	lsrNoOrganizedHash    = "no_organized_file_hash"
	lsrOrganizedSource    = "organized_source_left_alone"
	lsrSuspiciousExcluded = "suspicious_excluded"
	lsrStateNotAllowed    = "state_not_in_allowlist"
	lsrEmptyPath          = "empty_file_path"
	lsrChangedSinceScan   = "changed_since_scan"
	lsrWriteFailed        = "write_failed"
	lsrRowGone            = "row_gone"
)

type libraryStateRepairChange struct {
	BookID   string `json:"book_id"`
	Path     string `json:"path"`
	OldState string `json:"old_state"`
	Outcome  string `json:"outcome"`
	Detail   string `json:"detail,omitempty"`
}

type libraryStateRepairResult struct {
	DryRun            bool                       `json:"dry_run"`
	IncludeSuspicious bool                       `json:"include_suspicious"`
	RootDir           string                     `json:"root_dir"`
	BooksScanned      int                        `json:"books_scanned"`
	SoftDeleted       int                        `json:"soft_deleted"`
	Outcomes          map[string]int             `json:"outcomes"`
	Changes           []libraryStateRepairChange `json:"changes"`
}

type libraryStateRepairStore interface {
	GetAllBooksCoreComplete(limit, offset int) ([]database.BookCore, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

func (p *Plugin) repairLibraryStateDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.repair-library-state",
		Plugin:      "maintenance",
		DisplayName: "Repair stale library_state on already-organized books",
		Description: "Writes library_state=\"organized\" on books that are already under RootDir and " +
			"carry organized_file_hash but whose state was stamped back to the scanner's creation " +
			"default. Moves NO files. Never touches organized_source, iTunes paths, or a state " +
			"outside its allowlist. DEFAULT DRY RUN: pass {\"dry_run\": false} to write.",
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.repair-library-state",
		// Only book rows; no book_file row is read or written.
		Writes: []sdk.Resource{sdk.ResBooks},
		// ResumeDrop: an apply interrupted midway must not pick itself back up
		// unattended. Re-running is cheap and safe — a repaired row now reads
		// "organized" and is simply not selected again, which is also what makes
		// "re-run after a partial apply finishes the rest" true.
		ResumePolicy: sdk.ResumeDrop,
		Liveness:     sdk.LivenessRunItems,
		Cancellable:  true,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
			return p.runRepairLibraryState(ctx, raw, reporter)
		},
	}
}

func (p *Plugin) runRepairLibraryState(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params libraryStateRepairParams
	if err := decodeStrictParams(rawParams, &params); err != nil {
		return fmt.Errorf("repair-library-state: decode params: %w", err)
	}

	rootDir := strings.TrimRight(p.deps.RootDir(), "/")
	if strings.TrimSpace(rootDir) == "" {
		// Fail closed. With an empty root every path fails the containment test
		// and the op would report a clean "nothing to do" run, which is the
		// worst possible answer: indistinguishable from "already repaired".
		return fmt.Errorf("repair-library-state: RootDir is empty; refusing to run")
	}
	itunesRoots, itErr := merge.ITunesProtectedRoots(config.Snapshot().ITunes)
	if itErr != nil {
		return fmt.Errorf("repair-library-state: resolve iTunes roots: %w", itErr)
	}

	store, ok := p.deps.OpsStore().(libraryStateRepairStore)
	if !ok {
		return fmt.Errorf("repair-library-state: store does not implement the required methods")
	}

	res := &libraryStateRepairResult{
		DryRun:            params.dryRun(),
		IncludeSuspicious: params.IncludeSuspicious,
		RootDir:           rootDir,
		Outcomes:          map[string]int{},
	}

	scoped, err := libraryStateRepairScope(ctx, store, params, rootDir, itunesRoots, res)
	if err != nil {
		return err
	}

	if res.DryRun {
		for i := range scoped {
			scoped[i].Outcome = lsrWouldRepair
			res.Outcomes[lsrWouldRepair]++
		}
		res.Changes = append(res.Changes, scoped...)
		return p.finishLibraryStateRepair(reporter, res)
	}

	applied := make([]libraryStateRepairChange, len(scoped))
	copy(applied, scoped)
	var mu sync.Mutex
	runErr := registry.RunItems(ctx, reporter, applied, func(_ context.Context, ch libraryStateRepairChange) error {
		out := applyLibraryStateRepair(store, ch)
		mu.Lock()
		res.Outcomes[out.Outcome]++
		res.Changes = append(res.Changes, out)
		mu.Unlock()
		return nil
	}, registry.RunItemsOptions{
		// Explicit: RunItemsOptions.Concurrency defaults to zero and run_items.go
		// clamps anything below 1 up to 1, so an omitted field is a sequential
		// loop wearing a worker-pool API.
		Concurrency: runtime.NumCPU(),
		// Label runs INSIDE each worker goroutine (run_items.go), so anything it
		// reads needs the same lock as the callback body. It reads only its two
		// arguments here, which is why there is no shared counter to guard.
		Label: func(i, total int) string {
			return fmt.Sprintf("repairing library_state %d/%d", i, total)
		},
	})
	if runErr != nil {
		return runErr
	}
	return p.finishLibraryStateRepair(reporter, res)
}

// libraryStateRepairScope pages every live book and classifies it. Rows that
// qualify come back for the apply; every other row is counted into an outcome
// bucket here, so "not repaired" always carries a reason.
//
// GetAllBooksCoreComplete, not the memdb-served twin, because this op writes.
func libraryStateRepairScope(
	ctx context.Context,
	store libraryStateRepairStore,
	params libraryStateRepairParams,
	rootDir string,
	itunesRoots []string,
	res *libraryStateRepairResult,
) ([]libraryStateRepairChange, error) {
	wanted := map[string]bool{}
	for _, id := range params.BookIDs {
		if s := strings.TrimSpace(id); s != "" {
			wanted[s] = true
		}
	}

	var scoped []libraryStateRepairChange
	for offset := 0; ; offset += libraryStateRepairPageSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := store.GetAllBooksCoreComplete(libraryStateRepairPageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("repair-library-state: list books at offset %d: %w", offset, err)
		}
		if len(page) == 0 {
			break
		}
		for i := range page {
			b := page[i]
			res.BooksScanned++
			if b.IsSoftDeleted() {
				res.SoftDeleted++
				continue
			}
			if len(wanted) > 0 && !wanted[b.ID] {
				continue
			}
			ch, keep := classifyLibraryStateRepair(b, params, rootDir, itunesRoots)
			if keep {
				scoped = append(scoped, ch)
				continue
			}
			res.Outcomes[ch.Outcome]++
		}
		if len(page) < libraryStateRepairPageSize {
			break
		}
	}
	return scoped, nil
}

// classifyLibraryStateRepair decides one book. keep=true means "repair this";
// otherwise the returned change carries the bucket that explains why not.
func classifyLibraryStateRepair(
	b database.BookCore,
	params libraryStateRepairParams,
	rootDir string,
	itunesRoots []string,
) (libraryStateRepairChange, bool) {
	state := ""
	if b.LibraryState != nil {
		state = *b.LibraryState
	}
	ch := libraryStateRepairChange{BookID: b.ID, Path: b.FilePath, OldState: state}

	switch {
	case strings.TrimSpace(b.FilePath) == "":
		ch.Outcome = lsrEmptyPath
	case state == libStateOrganized:
		ch.Outcome = lsrAlreadyOrganized
	case state == libStateOrganizedSource:
		// The demoted source of an organized copy. Its state is CORRECT; the
		// organized twin is the copy that should be visible.
		ch.Outcome = lsrOrganizedSource
	case underITunes(b.FilePath, itunesRoots):
		ch.Outcome = lsrITunes
	case !pathutil.IsWithin(b.FilePath, rootDir):
		ch.Outcome = lsrNotUnderLibroot
	case b.OrganizedFileHash == nil || strings.TrimSpace(*b.OrganizedFileHash) == "":
		// No witness that this book was ever organized. Left alone rather than
		// guessed at.
		ch.Outcome = lsrNoOrganizedHash
	case state == libStateSuspicious && !params.IncludeSuspicious:
		ch.Outcome = lsrSuspiciousExcluded
	case state == libStateImported, state == libStateSuspicious:
		return ch, true
	default:
		// Allowlist, not blocklist: an unclassified state is never rewritten.
		ch.Outcome = lsrStateNotAllowed
		ch.Detail = fmt.Sprintf("state %q is not repairable", state)
	}
	return ch, false
}

// applyLibraryStateRepair writes one row.
//
// The state is re-asserted INSIDE the ModifyBook closure, on the row re-read
// under the write lock, because the scope snapshot is minutes old by the time
// the last row is written and this op declares no ConcurrencyKey against the
// scan or organize paths that also write this column. A row whose state moved
// since the scan is reported as changed_since_scan and NOT clobbered.
//
// OrganizedFileHash is re-checked too: it is the evidence the whole repair
// rests on, and a row that lost it between scan and write no longer qualifies.
func applyLibraryStateRepair(store libraryStateRepairStore, ch libraryStateRepairChange) libraryStateRepairChange {
	wrote := false
	row, err := store.ModifyBook(ch.BookID, func(cur *database.Book) error {
		curState := ""
		if cur.LibraryState != nil {
			curState = *cur.LibraryState
		}
		if curState != ch.OldState {
			return database.ErrSkipBookWrite
		}
		if cur.OrganizedFileHash == nil || strings.TrimSpace(*cur.OrganizedFileHash) == "" {
			return database.ErrSkipBookWrite
		}
		organized := libStateOrganized
		cur.LibraryState = &organized
		wrote = true
		return nil
	})
	switch {
	case err != nil:
		ch.Outcome = lsrWriteFailed
		ch.Detail = err.Error()
	case row == nil:
		// ModifyBook returns (nil, nil) for a missing row.
		ch.Outcome = lsrRowGone
	case !wrote:
		// ErrSkipBookWrite returns (row, nil), which is indistinguishable from a
		// successful write by the return value alone — hence the explicit flag.
		ch.Outcome = lsrChangedSinceScan
	default:
		ch.Outcome = lsrRepaired
	}
	return ch
}

func (p *Plugin) finishLibraryStateRepair(reporter sdk.Reporter, res *libraryStateRepairResult) error {
	verb := "would repair"
	if !res.DryRun {
		verb = "repaired"
	}
	n := res.Outcomes[lsrRepaired] + res.Outcomes[lsrWouldRepair]
	msg := fmt.Sprintf("repair-library-state (dry_run=%t, include_suspicious=%t): scanned=%d %s=%d outcomes=%v",
		res.DryRun, res.IncludeSuspicious, res.BooksScanned, verb, n, res.Outcomes)
	if err := registry.ReporterSetResult(reporter, res); err != nil {
		reporter.Logger().Debug("repair-library-state: result not persisted", "err", err)
	}
	return reporter.UpdateProgress(res.BooksScanned, max(res.BooksScanned, 1), msg)
}
