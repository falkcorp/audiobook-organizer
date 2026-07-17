// file: internal/plugins/maintenance/dedup_triage.go
// version: 1.3.0
// guid: 3a4b5c6d-7e8f-9012-abcd-ef1234567890
// last-edited: 2026-07-17

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// TriageClass labels the inferred population of a dedup candidate.
type TriageClass string

const (
	// TriageClassGenuine — file-hash, ISBN/ASIN, or metadata-source-hash signal
	// present. These candidates represent real duplicates and must NOT be purged.
	TriageClassGenuine TriageClass = "genuine"

	// TriageClassStub — one or both books have no real audio (< 256 KiB file size
	// with sub-5-second duration). The match was noise from an empty placeholder.
	TriageClassStub TriageClass = "stub"

	// TriageClassFragment — duration ratio between the two books is < 5%, suggesting
	// one is a single iTunes chapter (fragment) matched against the full book.
	// These are CONS-FRAG artifacts that survived PurgeStaleCandidates because they
	// live in different directories. Only classified as fragment when neither book
	// shows a CONS-17 suspect duration (> maxPlausibleAudioSeconds with no
	// DurationVerifiedAt stamp) — otherwise falls through to unknown.
	TriageClassFragment TriageClass = "fragment"

	// TriageClassTitleLeak — an exact-layer candidate with no hard signal
	// (file-hash / ISBN / meta-hash) in the ScoreBreakdown, where EITHER
	//   (a) both books are iTunes imports (the original CONS-17 signature), OR
	//   (b) the two books carry an identical normalized title (the leak's own
	//       evidence, provenance-independent).
	// These are CONS-17 artifacts: the iTunes importer set every track's title
	// to the first track's title, causing spurious title-identity matches
	// across unrelated chapters. Branch (b) exists because proven title-leak
	// books live under the organized library path after being moved out of the
	// iTunes tree — the both-iTunes precondition alone classified 0 of 6,921
	// proven title-leak pairs. Books under ANY library path qualify when the
	// title-identity evidence fires.
	TriageClassTitleLeak TriageClass = "title_leak"

	// TriageClassUnknown — pre-T015 candidate with no ScoreBreakdown, or a
	// combination of signals that doesn't fit the above buckets cleanly.
	// These must be reviewed manually; they are never auto-purged.
	TriageClassUnknown TriageClass = "unknown"
)

// TriageExample records minimal context on a sampled candidate for the report.
type TriageExample struct {
	CandidateID int64       `json:"candidate_id"`
	BookAID     string      `json:"book_a_id"`
	BookBID     string      `json:"book_b_id"`
	BookATitle  string      `json:"book_a_title"`
	BookBTitle  string      `json:"book_b_title"`
	Layer       string      `json:"layer"`
	Reason      string      `json:"reason"`
}

// TriagePopulation holds the count and up to 5 examples for one class.
type TriagePopulation struct {
	Class    TriageClass    `json:"class"`
	Count    int            `json:"count"`
	Examples []TriageExample `json:"examples,omitempty"`
}

// TriageReport is the output of the dry-run triage op.
// It is logged as JSON at the end of the run and returned as op result data.
type TriageReport struct {
	ScannedAt      time.Time                    `json:"scanned_at"`
	TotalScanned   int                          `json:"total_scanned"`
	Populations    map[TriageClass]TriagePopulation `json:"populations"`
	PurgeableCount int                          `json:"purgeable_count"`
	KeepCount      int                          `json:"keep_count"`
	ReviewCount    int                          `json:"review_count"`
}

// purgeableClasses are the populations safe to delete without human review.
// TriageClassFragment is intentionally excluded: duration-data quality issues
// (CONS-17 books with unverified ms-stored durations) mean fragment candidates
// require manual review before any purge is authorised.
var purgeableClasses = map[TriageClass]bool{
	TriageClassStub:      true,
	TriageClassTitleLeak: true,
}

// IsPurgeable reports whether cls is safe to delete without human review.
func IsPurgeable(cls TriageClass) bool { return purgeableClasses[cls] }

// minPlausibleAudioBytes is copied from internal/dedup/engine.go to avoid an
// import cycle. Files smaller than this with sub-5-second duration are stubs.
const minPlausibleAudioBytes = 256 * 1024

// maxPlausibleAudioSeconds is the upper bound for a sane audiobook duration.
// Any unverified book exceeding this almost certainly has its duration stored
// as milliseconds (CONS-17 iTunes-importer bug). 360 000 s = 100 hours.
const maxPlausibleAudioSeconds = 360_000

// ClassifyCandidate classifies a dedup candidate into one of the four triage
// populations. It returns the class and a short human-readable reason string.
// a and b may be nil if the book was deleted; in that case it falls to unknown.
func ClassifyCandidate(c database.DedupCandidate, a, b *database.Book) (TriageClass, string) {
	if a == nil || b == nil {
		return TriageClassUnknown, "one or both books missing"
	}

	// 1. Stub: either book has a tiny file + no real audio duration.
	if isTriageStub(a) {
		return TriageClassStub, fmt.Sprintf("book A (%s) is a byte-empty stub", a.ID)
	}
	if isTriageStub(b) {
		return TriageClassStub, fmt.Sprintf("book B (%s) is a byte-empty stub", b.ID)
	}

	// 2. Hard signal in ScoreBreakdown → genuine duplicate.
	if hasHardSignal(c) {
		return TriageClassGenuine, hardSignalName(c)
	}

	// 3. Fragment-vs-full: duration ratio < 5%.
	if cls, reason, ok := checkFragment(c, a, b); ok {
		return cls, reason
	}

	// 4. Title-leak: exact-layer candidate, no hard signal (step 2 already
	// returned genuine for any hard-signal breakdown), and EITHER both books
	// are iTunes imports (original CONS-17 signature) OR the two books share
	// an identical normalized title (the leak's own evidence). The identical-
	// title branch is deliberately stricter than the engine's Levenshtein<=2
	// exact-title matcher: title-leak classification feeds a purge decision,
	// so only verified title identity qualifies a non-iTunes pair. Note the
	// "exact" layer also covers ISBN / file-hash / duration emitters — for a
	// pre-T015 nil-breakdown row that provenance is unknowable, which is why
	// the title-identity evidence is required rather than layer alone.
	if c.Layer == "exact" {
		if a.ITunesPersistentID != nil && b.ITunesPersistentID != nil {
			return TriageClassTitleLeak, "both books are iTunes imports with exact-layer title match and no hard signal"
		}
		if t := normalizedLeakTitle(a.Title); t != "" && t == normalizedLeakTitle(b.Title) {
			return TriageClassTitleLeak, fmt.Sprintf(
				"identical normalized title %q with exact-layer match and no hard signal (CONS-17 title leak)", t)
		}
	}

	// 5. Pre-T015 (no ScoreBreakdown) or unclassifiable mixed signals.
	if c.ScoreBreakdown == nil {
		return TriageClassUnknown, "pre-T015 candidate, no ScoreBreakdown"
	}
	return TriageClassUnknown, fmt.Sprintf("layer=%s, no hard signal, signals=%s", c.Layer, signalList(c))
}

// normalizedLeakTitle canonicalizes a title for the title-leak identity check:
// lowercase with whitespace runs collapsed. Deliberately conservative — no
// punctuation stripping, no fuzzy distance — because a title_leak verdict is
// purgeable and must only fire on verified title identity.
func normalizedLeakTitle(t string) string {
	return strings.Join(strings.Fields(strings.ToLower(t)), " ")
}

func isTriageStub(b *database.Book) bool {
	if b.FileSize != nil && *b.FileSize < minPlausibleAudioBytes {
		dur := 0
		if b.Duration != nil {
			dur = *b.Duration
		}
		if dur < 5 {
			return true
		}
	}
	return false
}

func hasHardSignal(c database.DedupCandidate) bool {
	if c.ScoreBreakdown == nil {
		return false
	}
	for _, sig := range c.ScoreBreakdown.Signals {
		switch sig.Kind {
		case unified.SigExactFile, unified.SigISBNASIN, unified.SigMetaSrcHash, unified.SigExactAcoustID:
			return true
		}
	}
	return false
}

func hardSignalName(c database.DedupCandidate) string {
	if c.ScoreBreakdown == nil {
		return ""
	}
	for _, sig := range c.ScoreBreakdown.Signals {
		switch sig.Kind {
		case unified.SigExactFile:
			return "exact file-hash match"
		case unified.SigISBNASIN:
			return "ISBN/ASIN match"
		case unified.SigMetaSrcHash:
			return "same metadata source hash"
		case unified.SigExactAcoustID:
			return "exact AcoustID match"
		}
	}
	return ""
}

// isCons17Suspect returns true when a book's stored duration looks like it was
// saved in milliseconds rather than seconds (CONS-17 iTunes-importer bug) and
// the duration-reextract op has not yet corrected it.
func isCons17Suspect(b *database.Book) bool {
	return b.Duration != nil &&
		*b.Duration > maxPlausibleAudioSeconds &&
		b.DurationVerifiedAt == nil
}

func checkFragment(_ database.DedupCandidate, a, b *database.Book) (TriageClass, string, bool) {
	// Guard: if either book looks like a CONS-17 ms-stored-as-seconds victim, the
	// raw duration numbers are unreliable — skip fragment classification entirely
	// and let the caller fall through to unknown.
	if isCons17Suspect(a) || isCons17Suspect(b) {
		return "", "", false
	}

	durA, durB := 0, 0
	if a.Duration != nil {
		durA = *a.Duration
	}
	if b.Duration != nil {
		durB = *b.Duration
	}
	if durA <= 0 || durB <= 0 {
		return "", "", false
	}
	lo, hi := durA, durB
	if lo > hi {
		lo, hi = hi, lo
	}
	ratio := float64(lo) / float64(hi)
	if ratio < 0.05 {
		shortID := a.ID
		if durB < durA {
			shortID = b.ID
		}
		return TriageClassFragment, fmt.Sprintf(
			"duration ratio %.1f%% (short book %s, %ds vs %ds) — CONS-FRAG artifact",
			math.Round(ratio*1000)/10, shortID, lo, hi,
		), true
	}
	return "", "", false
}

func signalList(c database.DedupCandidate) string {
	if c.ScoreBreakdown == nil {
		return "(none)"
	}
	var kinds []string
	for _, sig := range c.ScoreBreakdown.Signals {
		kinds = append(kinds, string(sig.Kind))
	}
	if len(kinds) == 0 {
		return "(none)"
	}
	return strings.Join(kinds, ",")
}

// --- op definition ---

func (p *Plugin) dedupExactTriageDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.dedup-exact-triage",
		Plugin:          "maintenance",
		DisplayName:     "Dedup exact-pending triage (dry-run)",
		Description:     "Classifies all pending exact-layer dedup candidates into 4 populations (genuine/stub/fragment/title_leak) and reports counts. Dry-run only — no candidates are deleted.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.dedup-exact-triage",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         30 * time.Minute,
		Schedule:        nil,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		Run:             p.runDedupExactTriage,
	}
}

func (p *Plugin) runDedupExactTriage(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	if !p.deps.HasDedupEngine() {
		_ = reporter.Log(slog.LevelInfo, "Dedup engine not initialized, skipping exact triage")
		return nil
	}

	_ = reporter.Log(slog.LevelInfo, "Starting exact-pending dedup triage (dry-run — no deletes)")
	_ = reporter.UpdateProgress(0, 0, "Scanning candidates…")

	report, err := p.deps.DedupTriageExactPending(ctx)
	if err != nil {
		return fmt.Errorf("triage scan: %w", err)
	}

	_ = reporter.UpdateProgress(report.TotalScanned, report.TotalScanned, "Classification complete")

	// Log per-population summaries.
	for _, cls := range []TriageClass{
		TriageClassGenuine, TriageClassStub, TriageClassFragment, TriageClassTitleLeak, TriageClassUnknown,
	} {
		pop := report.Populations[cls]
		action := "KEEP"
		if purgeableClasses[cls] {
			action = "purgeable"
		}
		_ = reporter.Log(slog.LevelInfo,
			fmt.Sprintf("%-12s  %5d candidates  [%s]", string(cls), pop.Count, action),
		)
	}

	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"Summary: total=%d  purgeable=%d  keep=%d  review=%d",
		report.TotalScanned, report.PurgeableCount, report.KeepCount, report.ReviewCount,
	))

	// Emit the full report as a structured log entry so the UI can surface it.
	reportJSON, _ := json.MarshalIndent(report, "", "  ")
	_ = reporter.Log(slog.LevelInfo, "Triage report (JSON)", slog.String("report", string(reportJSON)))

	return nil
}
