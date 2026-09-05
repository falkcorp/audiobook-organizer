// file: internal/plugins/maintenance/merge_same_path_dupes.go
// version: 1.1.0
// guid: 31a21313-3b7f-41b3-919c-9fd48feebd6e
// last-edited: 2026-09-05

// Package maintenance — MERGE repair for duplicate book records that point at the
// exact same audio file.
//
// When an apply renamed a single-file book but (before the 2026-09-05 organizer
// fix) left its book_file row pointing at the old path, the next scan could not
// find the book at the new path and minted a SECOND record there. The library
// then held two book records for one physical file: the one the user applied
// metadata to, and a bare rescanned shell. Census 2026-09-05: 197 exact-file
// paths were shared by 396 book records; 44 were precisely "metadata book + bare
// shell".
//
// This op collapses those duplicates into one record via merge.Service (the same
// path the UI merge and dedup use: losers soft-deleted, external IDs reassigned,
// iTunes ITL entries cleaned, one version group). It is deliberately narrow:
//
//   - SAME EXACT FILE PATH only — never same-directory. Two records on one file
//     are unambiguously the same book. (A directory book's FilePath is a
//     directory, not an audio file, so the audio-extension filter excludes them.)
//   - HASH-CONFIRMED — the stored file hash on the shared-path row must be
//     present AND identical across every record in the group. Same path already
//     means same bytes, so this is a consistency gate: a record whose recorded
//     hash disagrees is NOT silently merged, it is flagged for review. This is
//     the check the owner asked for ("since we already calculate the sha, that
//     should be on both copies and see if that's identical as well").
//
// Report-only by default (a full per-group TSV is written on EVERY run); pass
// {"apply": true} to merge. A merge is destructive-ish (soft-delete), so the
// default must never write.
package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// mergeSamePathDefaultMax bounds how many GROUPS one run will merge, so a first
// production run is a sample rather than a mass collapse. 0 in params means this.
const mergeSamePathDefaultMax = 200

// mergeSamePathPageSize bounds each GetAllBooksCore read while grouping.
const mergeSamePathPageSize = 1000

// audioExtsForMerge are the extensions whose FilePath is a single audio file
// (and therefore a safe same-file merge key). A directory book's FilePath ends
// in none of these, so it is never grouped.
var audioExtsForMerge = map[string]bool{
	".m4b": true, ".m4a": true, ".mp3": true, ".ogg": true, ".opus": true,
	".flac": true, ".aac": true, ".wma": true, ".wav": true, ".mp4": true,
}

type mergeSamePathParams struct {
	// Apply must be explicitly true to merge. Default false = report only.
	Apply bool `json:"apply"`
	// PathPrefix scopes the sweep to one tree (e.g. only the organizer's tree).
	PathPrefix string `json:"pathPrefix"`
	// Max bounds merged groups per run. <=0 uses mergeSamePathDefaultMax.
	Max int `json:"max"`
	// ReportPath overrides where the full per-group TSV lands.
	ReportPath string `json:"reportPath,omitempty"`
}

// mergeGroupDecision is one same-path group's outcome. Every group with 2+ records
// lands in exactly one bucket and is reported, so a group that is NOT merged is
// visible rather than silently dropped.
type mergeGroupDecision struct {
	Path string `json:"path"`
	// Bucket is one of: "mergeable" | "hash-mismatch" | "unverified-hash" |
	// "read-error" | "capped" | "merged" | "merge-failed".
	Bucket    string   `json:"bucket"`
	PrimaryID string   `json:"primary_id,omitempty"`
	LoserIDs  []string `json:"loser_ids,omitempty"`
	Reason    string   `json:"reason"`
}

type mergeSamePathPlan struct {
	Apply           bool `json:"apply"`
	BooksScanned    int  `json:"books_scanned"`
	SharedPaths     int  `json:"shared_paths"`      // paths held by 2+ records
	RecordsInShared int  `json:"records_in_shared"` // total records across those paths
	Mergeable       int  `json:"mergeable"`         // groups that pass the hash gate
	HashMismatch    int  `json:"hash_mismatch"`
	UnverifiedHash  int  `json:"unverified_hash"` // a record had no stored hash for the shared file
	ReadError       int  `json:"read_error"`      // GetBookFiles failed — a store-health signal, NOT "no hash"
	Merged          int  `json:"merged"`          // groups actually merged (apply)
	RecordsMerged   int  `json:"records_merged"`  // loser records soft-deleted (apply)
	MergeFailed     int  `json:"merge_failed"`
	CappedAt        int  `json:"capped_at,omitempty"` // the cap value, when it bit
	Capped          int  `json:"capped,omitempty"`    // mergeable groups deferred by the cap

	ReportPath string `json:"report_path,omitempty"`

	// Samples is a small per-bucket sample for the JSON log line.
	Samples []mergeGroupDecision `json:"samples,omitempty"`

	all           []mergeGroupDecision
	bucketSampled map[string]int
}

const mergeSamplesPerBucket = 10

func (p *mergeSamePathPlan) record(d mergeGroupDecision) {
	p.all = append(p.all, d)
	if p.bucketSampled == nil {
		p.bucketSampled = map[string]int{}
	}
	if p.bucketSampled[d.Bucket] < mergeSamplesPerBucket {
		p.bucketSampled[d.Bucket]++
		p.Samples = append(p.Samples, d)
	}
}

func (p mergeSamePathPlan) summary() string {
	mode := "DRY RUN"
	if p.Apply {
		mode = "APPLIED"
	}
	return fmt.Sprintf(
		"%s books=%d shared-paths=%d records-in-shared=%d mergeable=%d capped=%d merged=%d records-merged=%d | refused: hash-mismatch=%d unverified-hash=%d read-error=%d merge-failed=%d",
		mode, p.BooksScanned, p.SharedPaths, p.RecordsInShared, p.Mergeable, p.Capped,
		p.Merged, p.RecordsMerged, p.HashMismatch, p.UnverifiedHash, p.ReadError, p.MergeFailed)
}

// mergeSamePathStore is the narrow read surface this op needs.
type mergeSamePathStore interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// bookMergeFunc merges bookIDs into primaryID and returns the loser count.
type bookMergeFunc func(bookIDs []string, primaryID string) (int, error)

func (p *Plugin) mergeSamePathDupesDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.merge-same-path-dupes",
		DisplayName: "Merge duplicate books that share one audio file",
		Description: "Merges book records that point at the EXACT same audio file, when the stored " +
			"file hash matches across them, keeping the metadata-bearing record as primary. Uses the " +
			"safe merge path (soft-delete losers, reassign external IDs). Same-file only, never " +
			"same-directory. Default dry-run; pass {\"apply\": true} to merge.",
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.merge-same-path-dupes",
		// ResumeDrop: this op WRITES (soft-deletes), and an apply interrupted
		// midway must not silently resume. Re-running is safe — a collapsed group
		// no longer has two live records, so it is simply not selected again.
		ResumePolicy: sdk.ResumeDrop,
		Liveness:     sdk.LivenessRunItems,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run: func(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
			return p.runMergeSamePathDupes(ctx, raw, reporter)
		},
	}
}

func (p *Plugin) runMergeSamePathDupes(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	var params mergeSamePathParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("merge-same-path-dupes: decode params: %w", err)
		}
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	log := reporter.Logger()

	reportPath := params.ReportPath
	if reportPath == "" {
		name := registry.ReporterOpID(reporter)
		if name == "" {
			name = "unknown-op"
		}
		reportPath = filepath.Join("reports", "merge-same-path-dupes-"+name+".tsv")
	}

	// The report is the ONLY complete record of which losers were soft-deleted
	// (the JSON log line samples only mergeSamplesPerBucket per bucket). On an
	// apply run, prove it is writable BEFORE any merge happens: a destructive op
	// must not soft-delete records it then cannot account for. A dry run does not
	// destroy anything, so a report failure there is logged, not fatal.
	if params.Apply {
		if err := preflightReportPath(reportPath); err != nil {
			return fmt.Errorf("merge-same-path-dupes: refusing to apply — the audit report path %q is not writable: %w", reportPath, err)
		}
	}

	plan, err := planMergeSamePathDupes(ctx, store, p.deps.MergeBooks, params, reporter)
	if err != nil {
		return err
	}

	if wErr := writeMergeReport(reportPath, plan.all); wErr != nil {
		log.Error("merge-same-path-dupes: FAILED to write the per-group report",
			"path", reportPath, "err", wErr, "groups", len(plan.all))
	} else {
		plan.ReportPath = reportPath
		log.Info("merge-same-path-dupes: per-group report written",
			"path", reportPath, "groups", len(plan.all))
	}

	if b, mErr := json.Marshal(plan); mErr == nil {
		log.Info("merge-same-path-dupes report (JSON)", "report", string(b))
	} else {
		log.Error("merge-same-path-dupes: could not marshal the JSON report line", "err", mErr)
	}
	if plan.ReadError > 0 {
		log.Error("merge-same-path-dupes: groups skipped because book_files could not be READ — the store may be unhealthy; "+
			"these are NOT 'no hash', they are I/O failures. Re-run once the store is healthy.",
			"groups", plan.ReadError, "report", reportPath)
	}
	if plan.HashMismatch > 0 {
		log.Warn("merge-same-path-dupes: groups REFUSED because the stored hash disagrees across records "+
			"sharing one path — these are NOT auto-merged; review the report's hash-mismatch rows.",
			"groups", plan.HashMismatch, "report", reportPath)
	}
	if plan.CappedAt > 0 {
		log.Warn("merge-same-path-dupes: more mergeable groups than the cap — run again to continue",
			"cap", plan.CappedAt, "mergeable", plan.Mergeable)
	}
	log.Info("merge-same-path-dupes complete", "summary", plan.summary())
	return nil
}

// hashForPath returns the stored hash of the row that names sharedPath within one
// book, preferring the current-content FileHash and falling back to
// OriginalFileHash. Empty means the book has no hash recorded for that file.
func hashForPath(files []database.BookFile, sharedPath string) string {
	for i := range files {
		if files[i].FilePath == sharedPath {
			if h := strings.TrimSpace(files[i].FileHash); h != "" {
				return h
			}
			return strings.TrimSpace(files[i].OriginalFileHash)
		}
	}
	return ""
}

// electPrimary picks the survivor of a same-path group: the record the user has
// invested in. Order: an applied metadata match; then organized state; then a
// real (non-nil) author; then an existing primary; then the oldest record; then
// the smallest ID for a fully deterministic tie-break.
func electPrimary(group []database.BookCore) database.BookCore {
	best := group[0]
	for _, b := range group[1:] {
		if primaryBetter(b, best) {
			best = b
		}
	}
	return best
}

func primaryBetter(a, b database.BookCore) bool {
	if am, bm := isMatched(a), isMatched(b); am != bm {
		return am
	}
	if ao, bo := isOrganized(a), isOrganized(b); ao != bo {
		return ao
	}
	if aa, ba := a.AuthorID != nil, b.AuthorID != nil; aa != ba {
		return aa
	}
	if ap, bp := isPrimary(a), isPrimary(b); ap != bp {
		return ap
	}
	if a.CreatedAt != nil && b.CreatedAt != nil && !a.CreatedAt.Equal(*b.CreatedAt) {
		return a.CreatedAt.Before(*b.CreatedAt)
	}
	return a.ID < b.ID
}

func isMatched(b database.BookCore) bool {
	return b.MetadataReviewStatus != nil && *b.MetadataReviewStatus == "matched"
}
func isOrganized(b database.BookCore) bool {
	return b.LibraryState != nil && *b.LibraryState == "organized"
}
func isPrimary(b database.BookCore) bool {
	return b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
}

func planMergeSamePathDupes(ctx context.Context, store mergeSamePathStore, mergeFn bookMergeFunc, params mergeSamePathParams, reporter sdk.Reporter) (mergeSamePathPlan, error) {
	log := reporter.Logger()
	maxMerges := params.Max
	if maxMerges <= 0 {
		maxMerges = mergeSamePathDefaultMax
	}
	log.Info("merge-same-path-dupes start", "apply", params.Apply, "path_prefix", params.PathPrefix, "max", maxMerges)

	// Group live, single-audio-file book records by their exact FilePath.
	byPath := map[string][]database.BookCore{}
	scanned := 0
	for offset := 0; ; offset += mergeSamePathPageSize {
		page, perr := store.GetAllBooksCore(mergeSamePathPageSize, offset)
		if perr != nil {
			return mergeSamePathPlan{}, fmt.Errorf("load books: %w", perr)
		}
		for i := range page {
			scanned++
			b := page[i]
			if b.IsSoftDeleted() {
				continue // a soft-deleted loser must not pull a live book into a merge
			}
			path := strings.TrimSpace(b.FilePath)
			if path == "" || !audioExtsForMerge[strings.ToLower(filepath.Ext(path))] {
				continue
			}
			if params.PathPrefix != "" && !strings.HasPrefix(path, params.PathPrefix) {
				continue
			}
			byPath[path] = append(byPath[path], b)
		}
		if len(page) < mergeSamePathPageSize {
			break
		}
	}
	plan := mergeSamePathPlan{Apply: params.Apply, BooksScanned: scanned}

	// Keep only the shared paths, in a deterministic order.
	type group struct {
		path  string
		books []database.BookCore
	}
	var groups []group
	for path, books := range byPath {
		if len(books) < 2 {
			continue
		}
		plan.SharedPaths++
		plan.RecordsInShared += len(books)
		groups = append(groups, group{path: path, books: books})
	}
	sort.Slice(groups, func(a, b int) bool { return groups[a].path < groups[b].path })

	// Phase 1 — classify each group (I/O: one GetBookFiles per record). Runs on
	// the bounded worker pool rather than a serial loop (CLAUDE.md concurrency
	// rule); groups are independent and only READ here.
	type classified struct {
		g         group
		mergeable bool
		primaryID string
		loserIDs  []string
		bucket    string
		reason    string
	}
	results := make([]classified, len(groups))

	prog := sdk.NewProgress(reporter, len(groups))
	prog.Start(fmt.Sprintf("Classifying %d shared-path group(s)…", len(groups)))

	err := registry.RunItems(ctx, reporter, groups, func(_ context.Context, g group) error {
		idx := -1
		for i := range groups {
			if groups[i].path == g.path {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil
		}
		// Confirm the stored hash is present and identical across every record.
		var refHash string
		verified := true
		for _, b := range g.books {
			files, ferr := store.GetBookFiles(b.ID)
			if ferr != nil {
				// A store read failure is NOT "no hash recorded" — conflating the
				// two would let an unhealthy store (every GetBookFiles failing)
				// report as a clean "nothing to merge" dry run. Its own bucket,
				// logged at Error by the caller.
				verified = false
				results[idx] = classified{g: g, bucket: "read-error",
					reason: "could not read book_files for " + b.ID + ": " + ferr.Error()}
				break
			}
			h := hashForPath(files, g.path)
			if h == "" {
				verified = false
				results[idx] = classified{g: g, bucket: "unverified-hash",
					reason: "record " + b.ID + " has no stored hash for the shared file"}
				break
			}
			if refHash == "" {
				refHash = h
			} else if h != refHash {
				verified = false
				results[idx] = classified{g: g, bucket: "hash-mismatch",
					reason: "stored hash disagrees across records on the same path"}
				break
			}
		}
		if !verified {
			return nil
		}
		primary := electPrimary(g.books)
		losers := make([]string, 0, len(g.books)-1)
		for _, b := range g.books {
			if b.ID != primary.ID {
				losers = append(losers, b.ID)
			}
		}
		results[idx] = classified{g: g, mergeable: true, primaryID: primary.ID,
			loserIDs: losers, bucket: "mergeable", reason: "hash-confirmed same file"}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: missingFileStatConcurrency,
		ErrMode:     registry.ErrModeCollect,
		Label:       func(i, t int) string { return fmt.Sprintf("Classified %d/%d groups", i+1, t) },
	})
	if err != nil {
		return mergeSamePathPlan{}, fmt.Errorf("classify: %w", err)
	}

	// Tally and collect the mergeable groups (deterministic order preserved).
	var mergeable []classified
	for _, r := range results {
		switch r.bucket {
		case "hash-mismatch":
			plan.HashMismatch++
			plan.record(mergeGroupDecision{Path: r.g.path, Bucket: r.bucket, Reason: r.reason})
		case "unverified-hash":
			plan.UnverifiedHash++
			plan.record(mergeGroupDecision{Path: r.g.path, Bucket: r.bucket, Reason: r.reason})
		case "read-error":
			plan.ReadError++
			plan.record(mergeGroupDecision{Path: r.g.path, Bucket: r.bucket, Reason: r.reason})
		case "mergeable":
			plan.Mergeable++
			mergeable = append(mergeable, r)
		default:
			// A group that fell through classify with no bucket set (the idx<0
			// guard, unreachable today) would otherwise vanish from every count
			// and the report. Make that loud rather than a silent drop.
			log.Error("merge-same-path-dupes: group classified into no bucket — REPORT THIS",
				"path", r.g.path)
			plan.record(mergeGroupDecision{Path: r.g.path, Bucket: "unclassified",
				Reason: "internal: group produced no classification"})
		}
	}

	// Every shared path must land in exactly one classification bucket. If this
	// invariant breaks, a group was silently dropped — fail loud.
	if sum := plan.HashMismatch + plan.UnverifiedHash + plan.ReadError + plan.Mergeable; sum != plan.SharedPaths {
		log.Error("merge-same-path-dupes: bucket counts do not sum to shared paths — a group was dropped",
			"sum", sum, "shared_paths", plan.SharedPaths,
			"hash_mismatch", plan.HashMismatch, "unverified_hash", plan.UnverifiedHash,
			"read_error", plan.ReadError, "mergeable", plan.Mergeable)
	}

	if len(mergeable) > maxMerges {
		plan.CappedAt = maxMerges
		plan.Capped = len(mergeable) - maxMerges
		// Record the deferred groups BEFORE truncating so the "full TSV on every
		// run" promise holds — a capped-but-mergeable group must be visible, not
		// silently absent from the report.
		for _, r := range mergeable[maxMerges:] {
			plan.record(mergeGroupDecision{Path: r.g.path, Bucket: "capped",
				PrimaryID: r.primaryID, LoserIDs: r.loserIDs,
				Reason: "mergeable but beyond this run's cap; re-run to continue"})
		}
		mergeable = mergeable[:maxMerges]
	}

	if !params.Apply {
		for _, r := range mergeable {
			plan.record(mergeGroupDecision{Path: r.g.path, Bucket: "mergeable",
				PrimaryID: r.primaryID, LoserIDs: r.loserIDs, Reason: r.reason})
		}
		log.Info("merge-same-path-dupes: DRY RUN — no records merged", "would_merge_groups", len(mergeable))
		return plan, nil
	}

	// Phase 2 — merge. SERIAL and deliberate: merge.Service soft-deletes rows and
	// rewrites version groups, so two merges must not run at once. Groups are
	// disjoint by book ID, but the merge service's own state is not proven
	// concurrency-safe, and 197 groups take milliseconds serially.
	for _, r := range mergeable {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		allIDs := append([]string{r.primaryID}, r.loserIDs...)
		merged, mErr := mergeFn(allIDs, r.primaryID)
		if mErr != nil {
			plan.MergeFailed++
			plan.record(mergeGroupDecision{Path: r.g.path, Bucket: "merge-failed",
				PrimaryID: r.primaryID, LoserIDs: r.loserIDs, Reason: mErr.Error()})
			log.Warn("merge-same-path-dupes: merge failed", "path", r.g.path, "primary", r.primaryID, "err", mErr)
			continue
		}
		plan.Merged++
		plan.RecordsMerged += merged
		plan.record(mergeGroupDecision{Path: r.g.path, Bucket: "merged",
			PrimaryID: r.primaryID, LoserIDs: r.loserIDs,
			Reason: fmt.Sprintf("merged %d loser(s) into primary", merged)})
	}
	return plan, nil
}

// preflightReportPath proves the report can be written before any destructive
// work: it creates the parent directory and touches the file. Any failure
// (relative dir under a read-only working directory, permission denied) surfaces
// here, where the caller can refuse to apply, rather than after the merges when
// the loser IDs would already be gone from the DB and unrecordable.
func preflightReportPath(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o664)
	if err != nil {
		return err
	}
	return f.Close()
}

func writeMergeReport(path string, decisions []mergeGroupDecision) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace
	var b strings.Builder
	b.WriteString("bucket\tpath\tprimary_id\tloser_ids\treason\n")
	for _, d := range decisions {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n",
			d.Bucket, clean(d.Path), d.PrimaryID, strings.Join(d.LoserIDs, ","), clean(d.Reason))
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}
