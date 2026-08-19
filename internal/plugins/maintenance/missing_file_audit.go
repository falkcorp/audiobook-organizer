// file: internal/plugins/maintenance/missing_file_audit.go
// version: 1.4.0
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
// of book_file rows point at paths that hold no bytes.
//
// ⚠️ THE NUMBERS BELOW CAME FROM A 120-BOOK SAMPLE AND THE SAMPLE WAS NOT
// REPRESENTATIVE. Superseded 2026-08-17 by a full-library run of this op (all
// 532,296 rows across 61,528 books) — see
// docs/audits/2026-08-17-missing-file-audit-full-population.md:
//
//	                                  120-book sample     full population
//	rows missing                      41.8% (552/1,322)   13.52% (71,954/532,296)
//	books with NO surviving file      5     (4.2%)        16,265 (26.4%)
//	missing rows under the iTunes tree  0   ("nothing")   1,006
//
// The books-with-nothing-left gap is not sampling noise, and the arithmetic says
// so rather than an adjective: at the population rate p = 0.2644, a random n = 120
// draw expects 31.7 such books (sd 4.83). Observing 5 is z = −5.53, exact
// P(X ≤ 5) = 1.3e−10. The sample was drawn from a non-representative slice.
//
// ⚠️ COMPARE RATES, NOT COUNTS. It is tempting to say the sample "understated by
// 3,253×" (5 → 16,265). That is a ratio of raw counts across populations of 120
// and 61,528 and it means nothing. The comparable figure is the RATE ratio,
// 4.2% → 26.4% = 6.3×. For the same reason, do not claim the sample "overstated
// the row rate by 3×": the sample averaged 11.0 rows/book against the
// population's 8.6, so the two percentages have different denominators and
// neither corrects the other.
//
// `unreadable` is 0 across all 532,296 rows, so none of this is a flapping mount.
//
// 🔴 The "EVERY missing path was under the organizer's own tree, nothing under
// iTunes" claim below is FALSE as written — 1,006 missing rows are under the
// iTunes tree, and 61 carry a mangled "/X:/books/itunes/Audiobooks" path. The
// claim was true of the sample and was never true of the library. It is kept
// here rather than deleted because the two-row shape it describes is real and
// still the common case; only the "EVERY"/"nothing" quantifiers were wrong.
//
// The typical broken book carries two rows — a phantom at the organized path and
// the real file still in the iTunes tree:
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

// missingFileSignals counts how many rows carry each stored identity signal.
//
// 🔴 WHY THIS IS PART OF THE AUDIT. Repairing a missing row means pointing it at
// some candidate file, and the only honest way to do that is to VERIFY the
// candidate is the same file — not to infer it from a filename. Which
// verifications are available is a property of the data, not of the repair, and
// this sweep already visits every row, so it is the cheapest possible place to
// find out.
//
// Tallied SEPARATELY for missing and present rows. The present-row tally is the
// control: a signal that is rare on missing rows but common on present ones means
// the generator never reached these files, whereas one that is rare on both is
// simply not populated anywhere. Those imply very different repairs, and a single
// combined number cannot distinguish them.
//
// ⚠️ Fingerprint presence is measured via AcoustIDFingerprintDurationSec, not via
// the fingerprint itself: stripBookFileForMemdb nils the ~230 KB
// AcoustIDFingerprint blob in the Core projection, so counting the blob here would
// report 0% for a library that is fully fingerprinted. The duration is RETAINED
// and is written by the same fpcalc pass, so it is the honest proxy. An earlier
// hand probe of this coverage read acoustid_seg0 over the HTTP API and reported
// 0%; that field is genuinely serialized but is not the preferred whole-file
// fingerprint, and the API never exposes the one that is.
type missingFileSignals struct {
	Rows int

	// Decisive when present — these identify a file by its content.
	FileHash         int
	OriginalFileHash int
	PostMetadataHash int
	Fingerprint      int

	// Corroborating — cheap, and available on nearly every row.
	Duration int
	FileSize int

	// Provenance / secondary.
	DownloadHash int
	ITunesPID    int

	// Transcription, per file.
	TranscribedTitle int
	IntroTranscribed int

	// AnyDecisive counts rows carrying at least one content-identifying signal —
	// the share of a repair that could be PROVEN rather than inferred.
	AnyDecisive int
}

// tally folds one row into the counters.
func (s *missingFileSignals) tally(f database.BookFileCore) {
	s.Rows++
	decisive := false
	if f.FileHash != "" {
		s.FileHash++
		decisive = true
	}
	if f.OriginalFileHash != "" {
		s.OriginalFileHash++
		decisive = true
	}
	if f.PostMetadataHash != "" {
		s.PostMetadataHash++
		decisive = true
	}
	if f.AcoustIDFingerprintDurationSec > 0 {
		s.Fingerprint++
		decisive = true
	}
	if decisive {
		s.AnyDecisive++
	}
	if f.Duration > 0 {
		s.Duration++
	}
	if f.FileSize > 0 {
		s.FileSize++
	}
	if f.DownloadHash != "" {
		s.DownloadHash++
	}
	if f.ITunesPersistentID != "" {
		s.ITunesPID++
	}
	if f.TranscribedTitle != nil && *f.TranscribedTitle != "" {
		s.TranscribedTitle++
	}
	if f.IntroTranscribedAt != nil {
		s.IntroTranscribed++
	}
}

// pct renders n as a percentage of the tallied rows, guarding the empty sweep.
func (s missingFileSignals) pct(n int) string {
	if s.Rows == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(s.Rows))
}

// missingFileReport is the outcome of one sweep.
type missingFileReport struct {
	TotalRows int
	Missing   int
	Present   int

	// SignalsMissing / SignalsPresent are the identity-signal census, split so the
	// present rows act as a control for the missing ones.
	SignalsMissing missingFileSignals
	SignalsPresent missingFileSignals

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
	store := p.deps.OpsStore()
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
	// The identity-signal census, missing rows beside their present-row control.
	// Logged as one record per arm rather than interleaved, so the two are directly
	// comparable in the log without arithmetic.
	for _, arm := range []struct {
		name string
		s    missingFileSignals
	}{
		{"missing", report.SignalsMissing},
		{"present (control)", report.SignalsPresent},
	} {
		s := arm.s
		log.Info("missing-file-audit: identity signals",
			"arm", arm.name, "rows", s.Rows,
			"any_decisive", fmt.Sprintf("%d (%s)", s.AnyDecisive, s.pct(s.AnyDecisive)),
			"file_hash", fmt.Sprintf("%d (%s)", s.FileHash, s.pct(s.FileHash)),
			"original_file_hash", fmt.Sprintf("%d (%s)", s.OriginalFileHash, s.pct(s.OriginalFileHash)),
			"post_metadata_hash", fmt.Sprintf("%d (%s)", s.PostMetadataHash, s.pct(s.PostMetadataHash)),
			"fingerprint", fmt.Sprintf("%d (%s)", s.Fingerprint, s.pct(s.Fingerprint)),
			"duration", fmt.Sprintf("%d (%s)", s.Duration, s.pct(s.Duration)),
			"file_size", fmt.Sprintf("%d (%s)", s.FileSize, s.pct(s.FileSize)),
			"download_hash", fmt.Sprintf("%d (%s)", s.DownloadHash, s.pct(s.DownloadHash)),
			"itunes_pid", fmt.Sprintf("%d (%s)", s.ITunesPID, s.pct(s.ITunesPID)),
			"transcribed_title", fmt.Sprintf("%d (%s)", s.TranscribedTitle, s.pct(s.TranscribedTitle)),
			"intro_transcribed", fmt.Sprintf("%d (%s)", s.IntroTranscribed, s.pct(s.IntroTranscribed)))
	}

	log.Info("missing-file-audit complete",
		"rows", report.TotalRows, "missing", report.Missing, "unreadable", report.Unreadable,
		"books_fully_broken", report.BooksAllGone, "books_partially_broken", report.BooksPartial,
		"missing_rows_with_a_decisive_signal", report.SignalsMissing.AnyDecisive,
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
		switch results[i] {
		case fileMissing:
			t.gone++
			report.SignalsMissing.tally(items[i].file)
			if len(report.Sample) < sampleLimit {
				report.Sample = append(report.Sample, items[i].file.FilePath)
			}
			report.MissingByRoot[missingPathRoot(items[i].file.FilePath)]++
		case filePresent:
			// The control arm. Only present rows, so an unreadable row does not
			// quietly land in the baseline it is supposed to be compared against.
			report.SignalsPresent.tally(items[i].file)
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
