// file: internal/plugins/dedup/quarantine_chapter_artifacts.go
// version: 1.8.0
// guid: 1d7a4f92-3c60-4e85-9b21-6a5e8c0d3f47
// last-edited: 2026-11-18

// Package dedup — op dedup.quarantine-chapter-artifacts.
//
// Drains the candidate explosion at its source: chapter/segment files that the
// scanner imported as STANDALONE books (idents, "Opening Credits", intro tracks)
// with generic titles that collide across hundreds of audiobooks. Those generic
// titles get cross-paired by the exact-title emitter into hundreds of thousands of
// bogus candidates (DEDUP-CANDIDATE-EXPLOSION-2026-06-18).
//
// A book is treated as a chapter artifact when ALL hold:
//   - its normalized title is shared by >= MinTitleCollisions OTHER books
//     (a real book title is not held by 5+ books; "Opening Credits" is held by many);
//   - it is a SINGLE-file book (not a multi-file audiobook);
//   - and either its single file is short (0 < duration < MaxDurationSec), OR it is
//     UNSCANNED (duration unknown — most idents/credits are) AND the title collides
//     with >= MinTitleCollisionsUnscanned books (a higher bar, since duration can't
//     confirm it's a segment). Long single files are never touched.
//
// Never quarantined, in either mode (so dry-run counts match apply):
//   - a book counting as its version group's primary (nil or true) while the
//     group keeps members that are not artifacts and none of those is an
//     explicit primary -- decided per group over the whole artifact set, so
//     two artifact primaries of one group cannot both go;
//   - a book whose own path or file is under the active iTunes library
//     (merge.GuardITunesProtectedLoaded; books/itunes/** is never mutated).
//
// Action is SOFT-delete (MarkedForDeletion) under the per-book lock
// (ModifyBook) — recoverable, not a hard delete. A failed write or an
// unreadable file list is counted and fails the op; it is not only logged.
// Dry-run by default: reports what it WOULD quarantine and writes nothing.
package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type quarantineChapterParams struct {
	Apply              bool `json:"apply"`
	MinTitleCollisions int  `json:"min_title_collisions,omitempty"` // default 5 (scanned short books)
	MaxDurationSec     int  `json:"max_duration_sec,omitempty"`     // default 1200 (20 min)
	// MinTitleCollisionsUnscanned is the (higher) collision bar for UNSCANNED
	// single-file books (duration unknown). Most chapter idents/credits are
	// unscanned mp3 segments, so they MUST be reachable — but a higher bar avoids
	// quarantining a few unscanned copies of a genuine book. Default 10.
	MinTitleCollisionsUnscanned int `json:"min_title_collisions_unscanned,omitempty"`
}

func (p *Plugin) quarantineChapterArtifactsDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.quarantine-chapter-artifacts",
		Preview:     sdk.PreviewSpec{Params: quarantineChapterParams{}},
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "dedup",
		DisplayName: "Quarantine chapter-file artifacts",
		Description: "Soft-deletes single short books whose generic title collides with many others " +
			"(idents/credits/intros the scanner imported as standalone books). Dry-run by default.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityNormal,
		ConcurrencyKey:  "dedup.quarantine-chapter-artifacts",
		Cancellable:     true,
		Timeout:         60 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runQuarantineChapterArtifacts,
	}
}

// quarantineChapterArtifactsKind is rule R3's native kind for
// dedup.quarantine-chapter-artifacts.
const quarantineChapterArtifactsKind = tasks.Kind("books.dedup.quarantine-chapter-artifacts")

// quarantineScanPar and quarantineApplyPar are the parallelism of the two item
// pools (rule R4): both carry HEAD's runtime.NumCPU() workers. The scan's
// per-book GetBookFiles read is the CPU/IO mix HEAD sized at NumCPU; the apply
// pass writes disjoint rows (one per artifact book ID).
func quarantineScanPar() tasks.Parallelism  { return tasks.CPU() }
func quarantineApplyPar() tasks.Parallelism { return tasks.CPU() }

// quarantineChapterNativeParams are the native params: HEAD's Apply is the
// run's mode.
type quarantineChapterNativeParams struct {
	MinTitleCollisions          int `json:"min_title_collisions,omitempty"`
	MaxDurationSec              int `json:"max_duration_sec,omitempty"`
	MinTitleCollisionsUnscanned int `json:"min_title_collisions_unscanned,omitempty"`
}

// quarantineSettings is the normalized threshold set the core runs with.
type quarantineSettings struct {
	MinTitleCollisions          int
	MaxDurationSec              int
	MinTitleCollisionsUnscanned int
}

// normalizeQuarantineSettings applies HEAD's defaults and floors to the three
// thresholds (zero or too-low values fall back).
func normalizeQuarantineSettings(minColl, maxDur, minUnscanned int) quarantineSettings {
	set := quarantineSettings{MinTitleCollisions: minColl, MaxDurationSec: maxDur, MinTitleCollisionsUnscanned: minUnscanned}
	if set.MinTitleCollisions < 2 {
		set.MinTitleCollisions = 5
	}
	if set.MaxDurationSec <= 0 {
		set.MaxDurationSec = 1200
	}
	if set.MinTitleCollisionsUnscanned < set.MinTitleCollisions {
		set.MinTitleCollisionsUnscanned = max(set.MinTitleCollisions, 10)
	}
	return set
}

// quarantineChapterArtifactsStore is the reads the core takes.
type quarantineChapterArtifactsStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// quarantineChapterArtifactsWriter is the writes the core makes: the soft-delete
// under the per-book lock and the version-group primary hand-off after it.
type quarantineChapterArtifactsWriter interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	// EnsureSinglePrimary is versionprimary.EnsureSinglePrimary for the group,
	// with the configured library root.
	EnsureSinglePrimary(ctx context.Context, groupID string) error
}

// quarantineChapterArtifactsEnv is what the core takes from its shell. Live is
// HEAD's apply; a dry run never calls Writer, which may then be nil.
type quarantineChapterArtifactsEnv struct {
	kindEnv
	Store    quarantineChapterArtifactsStore
	Writer   quarantineChapterArtifactsWriter
	Settings quarantineSettings
}

// quarantineChapterArtifactsResult is the native run's result.
type quarantineChapterArtifactsResult struct {
	Examined       int `json:"examined"`
	Artifacts      int `json:"artifacts"`
	Quarantined    int `json:"quarantined"`
	Failed         int `json:"failed"`
	Gone           int `json:"gone"`
	ReadErrors     int `json:"read_errors"`
	SkippedPrimary int `json:"skipped_primary"`
	SkippedITunes  int `json:"skipped_itunes"`
}

// quarantineChapterArtifactsCore is the body of runQuarantineChapterArtifacts,
// shared by both shells.
func quarantineChapterArtifactsCore(ctx context.Context, env quarantineChapterArtifactsEnv) (quarantineChapterArtifactsResult, error) {
	params := env.Settings
	apply := env.Live
	env.Logger.Info("quarantine-chapter-artifacts start",
		"apply", apply, "min_collisions", params.MinTitleCollisions, "max_duration_sec", params.MaxDurationSec)

	env.Progress(0, 3, "Loading library…")
	books, err := env.Store.GetAllBooksCore(0, 0)
	if err != nil {
		return quarantineChapterArtifactsResult{}, fmt.Errorf("get all books: %w", err)
	}

	// Pass 1: count books per normalized title (skip empty titles / already soft-deleted).
	env.Progress(1, 3, fmt.Sprintf("Counting title collisions over %d books…", len(books)))
	titleCount := make(map[string]int, len(books))
	for i := range books {
		b := &books[i]
		if b.IsSoftDeleted() {
			continue
		}
		norm := util.NormalizeTitle(b.Title)
		if norm == "" {
			continue
		}
		titleCount[norm]++
	}

	// Pass 2: identify chapter artifacts among the colliding-title books.
	//
	// Cheap in-memory pre-filter stays sequential (no DB I/O); only books that
	// clear the title-collision bar go on to the per-book GetBookFiles read,
	// which IS a per-item DB call and therefore runs through registry.RunItems
	// with a bounded worker pool (mandatory concurrency rule — see CLAUDE.md).
	env.Progress(2, 3, "Identifying chapter artifacts…")
	type artifact struct {
		ID            string
		Title         string
		Group         string
		CountsPrimary bool
		Explicit      bool
	}
	type candidate struct {
		ID       string
		Title    string
		Norm     string
		FilePath string
		Group    string
		// CountsPrimary uses the destructive-guard reading: nil counts as
		// primary. Readers disagree about nil (see merge.MergeBooks), and the
		// reading that protects the group is the one a soft-delete must take.
		CountsPrimary bool
		Explicit      bool
	}
	// Live members and explicit primaries per version group, read-only in the
	// workers below. A primary is protected only while it is the group's sole
	// possible primary and the group has someone else to strand.
	groupLive := make(map[string]int)
	groupExplicit := make(map[string]int)
	for i := range books {
		b := &books[i]
		if b.IsSoftDeleted() || b.VersionGroupID == nil || *b.VersionGroupID == "" {
			continue
		}
		groupLive[*b.VersionGroupID]++
		if b.IsPrimaryVersion != nil && *b.IsPrimaryVersion {
			groupExplicit[*b.VersionGroupID]++
		}
	}
	var candidates []candidate
	for i := range books {
		b := &books[i]
		if b.IsSoftDeleted() {
			continue
		}
		norm := util.NormalizeTitle(b.Title)
		if norm == "" || titleCount[norm] < params.MinTitleCollisions {
			continue
		}
		c := candidate{ID: b.ID, Title: b.Title, Norm: norm, FilePath: b.FilePath}
		if b.VersionGroupID != nil {
			c.Group = *b.VersionGroupID
		}
		c.Explicit = b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
		c.CountsPrimary = b.IsPrimaryVersion == nil || c.Explicit
		candidates = append(candidates, c)
	}

	// Shared mutable state written from worker goroutines below — all guarded
	// by mu. titleCount is read-only in this phase (safe for concurrent reads,
	// no writers), so it needs no lock.
	var (
		mu             sync.Mutex
		artifacts      []artifact
		examined       int
		readErrors     int
		skippedPrimary int
		skippedITunes  int
		sampleByTitle  = make(map[string]int)
	)
	if err := env.Items(ctx, quarantineScanPar(), len(candidates), kindItemsOptions{
		PureLabel: func(i, total int) string {
			return fmt.Sprintf("Identifying chapter artifacts %d/%d…", i+1, total)
		},
	}, func(ctx context.Context, i int) error {
		c := candidates[i]
		mu.Lock()
		examined++
		mu.Unlock()

		files, ferr := env.Store.GetBookFiles(c.ID)
		if ferr != nil {
			// Unreadable: not examined, so not quarantined -- but counted and
			// reported, never silently treated as "not an artifact".
			mu.Lock()
			readErrors++
			mu.Unlock()
			env.Logger.Error("quarantine read files error", "book_id", c.ID, "error", ferr)
			return nil
		}
		if len(files) != 1 {
			return nil // multi-file audiobook — not a single-segment artifact
		}
		dur := files[0].AcoustIDFingerprintDurationSec
		if dur <= 0 {
			if sec, ok := files[0].DurationSec(); ok {
				dur = float64(sec)
			}
		}
		switch {
		case dur >= float64(params.MaxDurationSec):
			return nil // long — a real audiobook, never an artifact
		case dur > 0:
			// Scanned + short: the ≥ MinTitleCollisions gate above is enough.
		default:
			// Unscanned (duration unknown — most idents/credits are unscanned mp3
			// segments). Require the higher collision bar so we don't quarantine a
			// few unscanned copies of a genuine book.
			if titleCount[c.Norm] < params.MinTitleCollisionsUnscanned {
				return nil
			}
		}
		if gerr := merge.GuardITunesProtectedLoaded(
			[]*database.Book{{ID: c.ID, FilePath: c.FilePath}},
			map[string][]database.BookFile{c.ID: files},
		); gerr != nil {
			mu.Lock()
			skippedITunes++
			mu.Unlock()
			return nil
		}
		mu.Lock()
		artifacts = append(artifacts, artifact{ID: c.ID, Title: c.Title, Group: c.Group, CountsPrimary: c.CountsPrimary, Explicit: c.Explicit})
		mu.Unlock()
		return nil
	}); err != nil {
		return quarantineChapterArtifactsResult{}, err
	}

	// Primary protection is decided per version group over the WHOLE artifact
	// set, not per book against pre-run counts: two explicit primaries of one
	// group that are both artifacts each saw "another explicit primary exists"
	// and both went, leaving the group with none. A group whose members are
	// not all artifacts keeps every artifact that counts as primary unless a
	// surviving (non-artifact) member is already an explicit primary.
	artifactsInGroup := make(map[string]int)
	explicitArtifactsInGroup := make(map[string]int)
	for _, a := range artifacts {
		if a.Group == "" {
			continue
		}
		artifactsInGroup[a.Group]++
		if a.Explicit {
			explicitArtifactsInGroup[a.Group]++
		}
	}
	kept := artifacts[:0]
	for _, a := range artifacts {
		if a.Group != "" && a.CountsPrimary {
			survivors := groupLive[a.Group] - artifactsInGroup[a.Group]
			survivingExplicit := groupExplicit[a.Group] - explicitArtifactsInGroup[a.Group]
			if survivors > 0 && survivingExplicit == 0 {
				skippedPrimary++
				continue
			}
		}
		kept = append(kept, a)
		sampleByTitle[a.Title]++
	}
	artifacts = kept

	// Build a short, human-readable sample of the offending titles.
	type tc struct {
		Title string
		N     int
	}
	samples := make([]tc, 0, len(sampleByTitle))
	for t, n := range sampleByTitle {
		samples = append(samples, tc{t, n})
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].N > samples[j].N })
	var sampleStr strings.Builder
	for i := 0; i < len(samples) && i < 10; i++ {
		sampleStr.WriteString(fmt.Sprintf("%q×%d ", samples[i].Title, samples[i].N))
	}

	var quarantinedCount, failedCount, goneCount int64
	if apply {
		now := time.Now()
		// Each artifact is a distinct book ID (built once per book above), so
		// workers never touch the same row — safe to run concurrently without
		// any additional partitioning. The soft-delete is a ModifyBook: the
		// read and the write happen under the book's lock, so a concurrent
		// write to the row is not reverted. The callback only mutates.
		if err := env.Items(ctx, quarantineApplyPar(), len(artifacts), kindItemsOptions{
			PureLabel: func(i, total int) string {
				return fmt.Sprintf("Quarantining %d/%d chapter artifacts…", i+1, total)
			},
		}, func(ctx context.Context, i int) error {
			a := artifacts[i]
			wrote := false
			updated, uerr := env.Writer.ModifyBook(a.ID, func(b *database.Book) error {
				if b.IsSoftDeleted() {
					return database.ErrSkipBookWrite
				}
				marked := true
				b.MarkedForDeletion = &marked
				b.MarkedForDeletionAt = &now
				// A retired row is never left counting as its group's primary
				// (the same rule as dedup-books' soft-delete).
				if b.VersionGroupID != nil && *b.VersionGroupID != "" &&
					(b.IsPrimaryVersion == nil || *b.IsPrimaryVersion) {
					f := false
					b.IsPrimaryVersion = &f
				}
				wrote = true
				return nil
			})
			switch {
			case uerr != nil:
				// One book's write error doesn't abort the whole op, but it is
				// counted and fails the op at the end.
				atomic.AddInt64(&failedCount, 1)
				env.Logger.Error("quarantine soft-delete error", "book_id", a.ID, "error", uerr)
			case updated == nil:
				atomic.AddInt64(&goneCount, 1)
			case wrote:
				atomic.AddInt64(&quarantinedCount, 1)
				// The plan kept every artifact whose group had no surviving
				// explicit primary, but that was decided on the pre-run read:
				// a survivor retired since (another op, a user delete) would
				// leave the group with none. Re-check the invariant after the
				// write; a healthy group costs one stat of its primary.
				if a.Group != "" && a.CountsPrimary {
					if herr := env.Writer.EnsureSinglePrimary(ctx, a.Group); herr != nil {
						atomic.AddInt64(&failedCount, 1)
						env.Logger.Error("quarantine primary hand-off error", "group", a.Group, "error", herr)
					}
				}
			}
			return nil
		}); err != nil {
			return quarantineChapterArtifactsResult{}, err
		}
	}
	quarantined := int(quarantinedCount)
	failed := int(failedCount)

	summary := fmt.Sprintf("examined=%d artifacts=%d quarantined=%d failed=%d gone=%d read_errors=%d skipped_primary=%d skipped_itunes=%d (apply=%v) top: %s",
		examined, len(artifacts), quarantined, failed, goneCount, readErrors, skippedPrimary, skippedITunes, apply, sampleStr.String())
	env.Logger.Info("quarantine-chapter-artifacts complete", "summary", summary)
	if !apply {
		env.Progress(3, 3, fmt.Sprintf(
			"Dry-run — %d chapter-artifact book(s) would be soft-deleted. Pass apply=true to quarantine. %s",
			len(artifacts), summary))
	} else {
		env.Progress(3, 3, fmt.Sprintf("Soft-deleted %d chapter-artifact book(s). %s", quarantined, summary))
	}
	out := quarantineChapterArtifactsResult{Examined: examined, Artifacts: len(artifacts), Quarantined: quarantined,
		Failed: failed, Gone: int(goneCount), ReadErrors: readErrors, SkippedPrimary: skippedPrimary, SkippedITunes: skippedITunes}
	if failed > 0 || readErrors > 0 {
		return out, errors.Join(errQuarantineIncomplete,
			fmt.Errorf("%d soft-delete(s) failed and %d book(s) could not be read: %s", failed, readErrors, summary))
	}
	return out, nil
}

// runQuarantineChapterArtifacts is the v2 shell: HEAD's Run over
// quarantineChapterArtifactsCore.
func (p *Plugin) runQuarantineChapterArtifacts(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}
	params := quarantineChapterParams{MinTitleCollisions: 5, MaxDurationSec: 1200, MinTitleCollisionsUnscanned: 10}
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}
	_, err := quarantineChapterArtifactsCore(ctx, quarantineChapterArtifactsEnv{
		kindEnv:  headEnv(reporter, params.Apply),
		Store:    headScoringReads{store: p.store},
		Writer:   headScoringWriter{store: p.store},
		Settings: normalizeQuarantineSettings(params.MinTitleCollisions, params.MaxDurationSec, params.MinTitleCollisionsUnscanned),
	})
	return err
}

// errQuarantineIncomplete is returned when any book could not be read or
// written, so the op result reports failure instead of a clean finish.
var errQuarantineIncomplete = errors.New("quarantine-chapter-artifacts incomplete")
