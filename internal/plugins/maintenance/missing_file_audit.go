// file: internal/plugins/maintenance/missing_file_audit.go
// version: 1.1.0
// guid: 4e1c7a92-3b58-4d06-9f21-8c5a0e7b3d64
// last-edited: 2026-08-17

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- missing-file-audit ---
//
// 🔴 WHY THIS EXISTS. Downloads fail with "file not found" because a large share
// of book_file rows point at paths that hold no bytes. Measured on the live
// library 2026-08-17 over a 120-book sample: 552 of 1,322 rows (41.8%) were
// missing, and 49 of the 120 books (41%) had at least one dead file. Five books
// had NO surviving file at all.
//
// The shape is specific and it is what makes this worth its own op: EVERY missing
// path was under the organizer's own destination tree
// (/mnt/bigdata/books/audiobook-organizer/...), while nothing under the iTunes
// tree was missing. The typical broken book carries two rows — a phantom at the
// organized path and the real file still in the iTunes tree:
//
//	MISSING .../audiobook-organizer/Morgan Rice/.../A Vow of Glory - ... .m4b
//	OK      .../itunes/iTunes Media/Audiobooks/Morgan Rice/01 The Sorcerer's ... .m4b
//
// NO EXISTING OP FINDS THESE. orphan-book-files-cleanup matches rows whose
// book_id dangles; these rows have a valid book. dedupe-book-file-rows matches
// rows sharing an IDENTICAL file_path; these rows have different paths. Both walk
// straight past the entire population.
//
// 🔴 REPORT-ONLY, DELIBERATELY. No repair is offered because the right repair is
// not yet decided and the two candidates differ in kind: deleting the phantom row,
// or re-pointing it at the surviving file. Deleting is also not safe uniformly —
// for the books where every row is missing, deleting them all leaves a book with
// zero files. Measure first, then choose; see the todo fragment filed with this op.

// missingFileSampleLimit bounds how many example paths are surfaced, so the report
// stays readable when the finding is tens of thousands of rows.
const missingFileSampleLimit = 40

// missingFileStatConcurrency is the worker-pool size for the stat sweep.
//
// Sized for LATENCY, not for CPU: every item is a single os.Stat against the NAS
// mount, so the pool is bounded by round-trip time rather than by cores, and
// runtime.NumCPU() would leave the link idle. Kept to a fixed, modest number
// because the target is a network filesystem shared with playback and with any
// scan that happens to be running — this op is diagnostic and must not out-compete
// the things people are actually using.
const missingFileStatConcurrency = 24

// missingFileAuditParams are the JSON parameters accepted by the op.
type missingFileAuditParams struct {
	// PathPrefix, when set, restricts the audit to rows whose FilePath begins with
	// it — e.g. only the organized tree. Empty audits every row.
	PathPrefix string `json:"path_prefix"`

	// SampleLimit overrides how many example missing paths are reported (0 = the
	// default).
	SampleLimit int `json:"sample_limit"`
}

// missingFileReport is the outcome of one sweep.
type missingFileReport struct {
	TotalRows int
	Missing   int
	Present   int

	// Unreadable counts rows whose existence could NOT be determined — a
	// permission error, an I/O error, a dead mount.
	//
	// 🔴 COUNTED SEPARATELY FROM MISSING, NEVER FOLDED INTO IT. "I could not tell"
	// and "it is not there" are different findings, and merging them would let a
	// single unmounted share report the entire library as lost — which, for an op
	// whose number will be used to decide a bulk repair, is the most damaging
	// possible way to be wrong.
	Unreadable int

	BooksTotal    int
	BooksAllGone  int
	BooksPartial  int
	BooksIntact   int
	MissingByRoot map[string]int
	Sample        []string
}

func (r missingFileReport) summary() string {
	return fmt.Sprintf(
		"rows=%d missing=%d present=%d unreadable=%d | books=%d fully-broken=%d partially-broken=%d intact=%d",
		r.TotalRows, r.Missing, r.Present, r.Unreadable,
		r.BooksTotal, r.BooksAllGone, r.BooksPartial, r.BooksIntact)
}

func (p *Plugin) missingFileAuditDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.missing-file-audit",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Missing file audit",
		Description: "Stats every book_file row's path and reports which point at bytes that are " +
			"no longer on disk — the cause of 'file not found' on download. Reports totals, a " +
			"per-book breakdown (fully broken / partially broken / intact) and a breakdown of " +
			"missing paths by tree. REPORT-ONLY: takes no action and modifies nothing. Pass " +
			"path_prefix to restrict the sweep to one tree.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.missing-file-audit",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		// 🔴 READ ONLY. The op cannot write even if a future edit tried to: it never
		// requests CapLibraryWrite. That is the guarantee that makes it safe to run
		// against production at any time, including mid-scan.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runMissingFileAudit,
	}
}

// fileExistence is the per-row outcome of the stat sweep.
type fileExistence uint8

const (
	fileUnknown fileExistence = iota
	filePresent
	fileMissing
	fileUnreadable
)

// missingFileItem pairs a row with its index so a worker can record its result by
// position. Writing results[i] from the worker that owns i needs no lock, whereas
// a shared map would need one on every single row.
type missingFileItem struct {
	idx  int
	file database.BookFileCore
}

func (p *Plugin) runMissingFileAudit(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params missingFileAuditParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	report, err := auditMissingFiles(ctx, store, params, reporter)
	if err != nil {
		return err
	}

	log := reporter.Logger()
	roots := make([]string, 0, len(report.MissingByRoot))
	for r := range report.MissingByRoot {
		roots = append(roots, r)
	}
	sort.Slice(roots, func(a, b int) bool { return report.MissingByRoot[roots[a]] > report.MissingByRoot[roots[b]] })
	for _, r := range roots {
		log.Info("missing-file-audit: missing by tree", "tree", r, "count", report.MissingByRoot[r])
	}
	log.Info("missing-file-audit complete",
		"rows", report.TotalRows, "missing", report.Missing, "unreadable", report.Unreadable,
		"books_fully_broken", report.BooksAllGone, "books_partially_broken", report.BooksPartial,
		"sample", report.Sample)
	return nil
}

// auditMissingFiles performs the sweep and RETURNS the report.
//
// Split out from the op body so the numbers can be asserted as values rather than
// scraped from a progress string — the counts are the entire product of this op and
// a destructive repair will be sized from them, so they deserve to be tested
// directly.
func auditMissingFiles(ctx context.Context, store bookFileCoreScanner, params missingFileAuditParams, reporter sdk.Reporter) (missingFileReport, error) {
	sampleLimit := params.SampleLimit
	if sampleLimit <= 0 {
		sampleLimit = missingFileSampleLimit
	}
	log := reporter.Logger()
	log.Info("missing-file-audit start", "path_prefix", params.PathPrefix, "sample_limit", sampleLimit)

	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return missingFileReport{}, fmt.Errorf("load book files: %w", err)
	}

	items := make([]missingFileItem, 0, len(files))
	for i := range files {
		path := strings.TrimSpace(files[i].FilePath)
		// A row with no path at all is a different defect and is not what this op
		// measures; counting it as "missing bytes" would inflate the number that a
		// repair decision gets made from.
		if path == "" {
			continue
		}
		if params.PathPrefix != "" && !strings.HasPrefix(path, params.PathPrefix) {
			continue
		}
		items = append(items, missingFileItem{idx: len(items), file: files[i]})
	}

	results := make([]fileExistence, len(items))
	var missing, present, unreadable atomic.Int64

	prog := sdk.NewProgress(reporter, len(items))
	prog.Start(fmt.Sprintf("Checking %d book_file path(s)…", len(items)))

	err = registry.RunItems(ctx, reporter, items, func(_ context.Context, it missingFileItem) error {
		switch _, serr := os.Stat(it.file.FilePath); {
		case serr == nil:
			results[it.idx] = filePresent
			present.Add(1)
		case os.IsNotExist(serr):
			results[it.idx] = fileMissing
			missing.Add(1)
		default:
			// Could not determine. Recorded as its own outcome rather than as
			// missing — see missingFileReport.Unreadable.
			results[it.idx] = fileUnreadable
			unreadable.Add(1)
			log.Warn("missing-file-audit: could not stat", "path", it.file.FilePath, "err", serr)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: missingFileStatConcurrency,
		// One unreadable path must not abandon the sweep: the whole point is the
		// total, and a partial total is the one number that cannot be acted on.
		ErrMode: registry.ErrModeCollect,
		Label: func(i, t int) string {
			return fmt.Sprintf("Checked %d/%d paths (missing=%d)", i+1, t, missing.Load())
		},
	})
	if err != nil {
		return missingFileReport{}, fmt.Errorf("stat sweep: %w", err)
	}

	report := missingFileReport{
		TotalRows:     len(items),
		Missing:       int(missing.Load()),
		Present:       int(present.Load()),
		Unreadable:    int(unreadable.Load()),
		MissingByRoot: map[string]int{},
	}

	// Per-book roll-up. Done after the sweep from the results slice rather than in
	// the workers, so no shared map is written concurrently.
	type bookTally struct{ total, gone int }
	byBook := make(map[string]*bookTally, len(items))
	for i := range items {
		id := items[i].file.BookID
		t := byBook[id]
		if t == nil {
			t = &bookTally{}
			byBook[id] = t
		}
		t.total++
		if results[i] == fileMissing {
			t.gone++
			if len(report.Sample) < sampleLimit {
				report.Sample = append(report.Sample, items[i].file.FilePath)
			}
			report.MissingByRoot[missingPathRoot(items[i].file.FilePath)]++
		}
	}
	report.BooksTotal = len(byBook)
	for _, t := range byBook {
		switch {
		case t.gone == 0:
			report.BooksIntact++
		case t.gone == t.total:
			report.BooksAllGone++
		default:
			report.BooksPartial++
		}
	}

	prog.Done("REPORT ONLY (nothing modified) — " + report.summary())
	return report, nil
}

// missingPathRoot reduces a path to the tree it lives under, so the report can say
// WHERE the missing rows are concentrated rather than only how many there are.
// That grouping is what turned the live finding from "41% of files are gone" into
// "every missing file is in the organizer's own destination tree and none are in
// the iTunes tree" — the same number, but the second one names a cause.
func missingPathRoot(path string) string {
	cleaned := filepath.Clean(path)
	parts := strings.Split(strings.TrimPrefix(cleaned, string(filepath.Separator)), string(filepath.Separator))
	// Three segments is deep enough to separate the library trees that matter
	// (mnt/bigdata/books/audiobook-organizer vs mnt/bigdata/books/itunes) without
	// fragmenting the report into one row per author directory.
	const rootDepth = 4
	if len(parts) > rootDepth {
		parts = parts[:rootDepth]
	}
	return string(filepath.Separator) + filepath.Join(parts...)
}
