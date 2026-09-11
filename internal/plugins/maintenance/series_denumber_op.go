// file: internal/plugins/maintenance/series_denumber_op.go
// version: 2.5.0
// guid: 3f0b6c84-52d1-4a97-9e35-c8b71d0af426
// last-edited: 2026-09-11

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// SeriesDenumberParams are the JSON parameters for the series de-numbering.
type SeriesDenumberParams struct {
	// Apply, when true, performs the merges. Default false — dry run.
	Apply bool `json:"apply"`
	// Limit caps how many SERIES are merged (0 = all). Useful for a canary.
	// It caps the APPLY set, never the report: a dry run always reports every
	// candidate so the operator sees the whole picture before scoping the apply.
	Limit int `json:"limit,omitempty"`
	// ApplyMedium extends the apply set to medium-confidence plans — a single
	// bracketed number, "Dragon Born [04]". Default false, so the first
	// production apply is scoped to names a keyword vouches for, and the dry run
	// reports what medium WOULD have done before anyone risks it.
	//
	// There is deliberately no equivalent for low. See ApplyEligible.
	ApplyMedium bool `json:"applyMedium,omitempty"`
	// ReportPath, when set, writes every candidate (all tiers, eligible or not)
	// as TSV before a single row is touched.
	//
	// 🔑 This is the rollback artefact. A merge creates and deletes series rows,
	// so "undo" means replaying this file — there is no transaction to abort.
	// Write it during the dry run and keep it.
	ReportPath string `json:"reportPath,omitempty"`
}

// writeSeriesDenumberReport dumps every candidate as TSV, eligible or not.
//
// TSV rather than JSON because the point of this file is to be read by a person
// deciding whether to proceed, and sorted/grepped by shape or tier while they do
// it. The `eligible` column records the decision made at THIS invocation's
// settings, so the file explains itself later without the params alongside.
func writeSeriesDenumberReport(path string, plans []SeriesMergePlan, allowMedium bool) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	var b strings.Builder
	b.WriteString("from_series_id\tfrom_name\tinto_name\tposition\tbooks\tshape\tconfidence\teligible\treason\n")
	for _, pl := range plans {
		// Tabs and newlines inside a name would break the column alignment and
		// silently shift every field after it.
		clean := func(s string) string {
			return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
		}
		fmt.Fprintf(&b, "%d\t%s\t%s\t%d\t%d\t%s\t%s\t%t\t%s\n",
			pl.FromID, clean(pl.FromName), clean(pl.IntoName), pl.Position, pl.Books,
			pl.Shape, pl.Confidence, ApplyEligible(pl, allowMedium), clean(pl.Reason))
	}
	return os.WriteFile(path, []byte(b.String()), 0o664)
}

func (p *Plugin) seriesDenumberDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.series-denumber",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Merge series that carry a book number in their name",
		Description: "A series name should name the series, but many carry the book's position instead " +
			"(\"Discworld 05\", \"Safehold 01\", \"Schooled in Magic: Book 11\"), which splits one series into as " +
			"many one-book series as it has volumes. This merges them onto the base name and moves the number " +
			"into the book's series position. A bare trailing number is only trusted when the library " +
			"corroborates it — zero-padding, an explicit keyword, or another series sharing the base — so a real " +
			"name like \"Fahrenheit 451\" is left alone. Also reads positions embedded in the middle or at the " +
			"front of a name (\"Evil Genius: Book 4: ...\", \"Dragon Born [04]\", \"08. Battle for the Abyss\"), " +
			"each scored by confidence — a keyword-vouched position applies, a bracketed one needs " +
			"{\"applyMedium\": true}, and a bare number is only ever reported, because \"86—EIGHTY-SIX\" is a real " +
			"series name with the same shape. Dry-run by default; pass {\"apply\": true} to merge and " +
			"{\"reportPath\": \"...\"} to write the rollback report.",
		// RESUME AUDIT 2026-09-11 (b): ResumeDrop, was ResumeRestart with no
		// checkpoint. A from-zero restart recomputes the plan from the current
		// series table, and the already-merged series are gone from it, so the
		// merges themselves would not repeat — but two things the operator
		// relies on would silently break: Limit caps the APPLY set per run, so a
		// canary of 10 would apply the NEXT 10 on resume; and the rollback report
		// at ReportPath would be overwritten with a plan that no longer lists
		// the series the first attempt merged, destroying the only way back for
		// them. Apply is a reviewed, deliberate step; an interrupted one is
		// surfaced as interrupted_dropped for the operator to re-run.
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.series-denumber",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ProgressTimeout: 30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runSeriesDenumber,
	}
}

func (p *Plugin) runSeriesDenumber(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params SeriesDenumberParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	log := reporter.Logger()
	log.Info("series-denumber: starting", "apply", params.Apply, "limit", params.Limit)

	allSeries, err := store.GetAllSeries()
	if err != nil {
		return fmt.Errorf("GetAllSeries: %w", err)
	}
	counts, cerr := store.GetAllSeriesBookCounts()
	if cerr != nil {
		counts = map[int]int{}
	}

	in := make([]SeriesInput, 0, len(allSeries))
	for i := range allSeries {
		s := allSeries[i]
		aid := 0
		if s.AuthorID != nil {
			aid = *s.AuthorID
		}
		in = append(in, SeriesInput{ID: s.ID, Name: s.Name, AuthorID: aid, Books: counts[s.ID]})
	}

	candidates := SeriesDenumber(in)
	// Deterministic order so a dry run and the apply that follows it agree, and
	// so the FIRST plan for a base is the one that creates the base series.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].IntoName != candidates[j].IntoName {
			return candidates[i].IntoName < candidates[j].IntoName
		}
		return candidates[i].Position < candidates[j].Position
	})

	log.Info("series-denumber: plan complete", "series", len(in), "candidates", len(candidates))
	if len(candidates) == 0 {
		summary := fmt.Sprintf("series-denumber: %d series scanned, 0 carry a book number — nothing to do", len(in))
		_ = reporter.Log(slog.LevelInfo, summary)
		_ = reporter.UpdateProgress(1, 1, summary)
		return nil
	}

	// 🔑 The report covers EVERY candidate, including the tiers that will never
	// be applied. Written before anything is touched, because a merge creates and
	// deletes series rows and replaying this file is the only way back.
	if params.ReportPath != "" {
		if err := writeSeriesDenumberReport(params.ReportPath, candidates, params.ApplyMedium); err != nil {
			// Fail closed: an apply with no rollback artefact is exactly the
			// situation the report exists to prevent.
			return fmt.Errorf("write report %s: %w", params.ReportPath, err)
		}
		log.Info("series-denumber: report written", "path", params.ReportPath, "rows", len(candidates))
	} else if params.Apply {
		log.Warn("series-denumber: applying with no reportPath — there will be no rollback artefact")
	}

	// Partition by eligibility. Low never appears in plans, at any setting.
	plans := make([]SeriesMergePlan, 0, len(candidates))
	byTier := map[SeriesConfidence]int{}
	booksByTier := map[SeriesConfidence]int{}
	heldExamples := make([]string, 0, 6)
	for _, pl := range candidates {
		byTier[pl.Confidence]++
		booksByTier[pl.Confidence] += pl.Books
		if ApplyEligible(pl, params.ApplyMedium) {
			plans = append(plans, pl)
		} else if len(heldExamples) < 6 {
			heldExamples = append(heldExamples, fmt.Sprintf("%q → %q #%d [%s/%s]",
				pl.FromName, pl.IntoName, pl.Position, pl.Shape, pl.Confidence))
		}
	}
	if params.Limit > 0 && len(plans) > params.Limit {
		plans = plans[:params.Limit]
	}

	tiers := fmt.Sprintf("high=%d(%d books) medium=%d(%d books) low=%d(%d books)",
		byTier[ConfidenceHigh], booksByTier[ConfidenceHigh],
		byTier[ConfidenceMedium], booksByTier[ConfidenceMedium],
		byTier[ConfidenceLow], booksByTier[ConfidenceLow])

	byReason := map[string]int{}
	bases := map[string]struct{}{}
	booksAffected := 0
	var examples []string
	for _, pl := range plans {
		byReason[pl.Reason]++
		bases[strings.ToLower(pl.IntoName)] = struct{}{}
		booksAffected += pl.Books
		if len(examples) < 12 {
			examples = append(examples, fmt.Sprintf("%q → %q #%d [%s]",
				pl.FromName, pl.IntoName, pl.Position, pl.Reason))
		}
	}

	// The unfiltered reference guard, fetched ONCE for the whole run — BEFORE
	// the dry-run branch, not only on the apply path. It answers "how many
	// book rows reference this series in ANY state" — live, trashed, and
	// non-primary — which the per-book merge loop below cannot answer on its
	// own: GetBooksBySeriesIDAllVersions (like every series membership
	// getter) excludes soft-deleted rows by design, so a series whose only
	// members are trashed enumerates zero books, the loop that would flip
	// movedAll never runs, and movedAll stays vacuously true. Comparing this
	// unfiltered count against what each plan actually enumerates is what
	// closes that gap — see safeToDelete below. Same shared guard, same
	// fail-closed contract as the other three series-delete paths: compare
	// internal/maintenance/jobs/cleanup_series.go and
	// internal/plugins/maintenance/author_purge_empty.go — both of which also
	// fetch their ref-count guard unconditionally before their own dry-run
	// branch, for the same reason: a guard applied only on the apply path
	// would make the dry run disagree with what apply actually does, on the
	// one op where an operator reviews the dry run BEFORE approving apply.
	//
	// Fails CLOSED: a run (dry or apply) that cannot answer the unfiltered
	// question aborts entirely rather than falling back to the filtered book
	// counts — that fallback is precisely the bug, because it deletes rows
	// while reporting success.
	refCounts, rcErr := database.SeriesRefCounts(store)
	if rcErr != nil {
		return fmt.Errorf("series-denumber: refusing to run without unfiltered series reference counts: %w", rcErr)
	}

	if !params.Apply {
		reasons := make([]string, 0, len(byReason))
		for r, n := range byReason {
			reasons = append(reasons, fmt.Sprintf("%s=%d", r, n))
		}
		sort.Strings(reasons)

		// Preview which of the eligible plans the apply loop below would
		// merge but refuse to DELETE — read-only (GetBooksBySeriesIDAllVersions
		// never writes), so computing it here does not turn the dry run into
		// one that touches anything. Scoped to `plans` (the eligible set),
		// not every candidate, to match "Would merge %d" below.
		var heldBackByGuard int
		var heldBackExamples []string
		for _, pl := range plans {
			books, berr := store.GetBooksBySeriesIDAllVersions(pl.FromID)
			if berr != nil {
				// The apply loop treats this identically (counts it under
				// failed); avoid double-reporting the same row here.
				continue
			}
			if refCounts[pl.FromID] > len(books) {
				heldBackByGuard++
				if len(heldBackExamples) < 6 {
					heldBackExamples = append(heldBackExamples, fmt.Sprintf("%q (series %d)", pl.FromName, pl.FromID))
				}
			}
		}

		summary := fmt.Sprintf(
			"series-denumber: %d series scanned, %d carry a position (%s). "+
				"Would merge %d into %d base series (%d books would move), %d would be merged but "+
				"NOT deleted (still referenced by rows this run cannot move: %s) — by evidence: %s | "+
				"applying: %s | held: %s",
			len(in), len(candidates), tiers,
			len(plans), len(bases), booksAffected, heldBackByGuard, strings.Join(heldBackExamples, "; "),
			strings.Join(reasons, " "),
			strings.Join(examples, "; "), strings.Join(heldExamples, "; "))
		_ = reporter.Log(slog.LevelInfo, summary)
		_ = reporter.UpdateProgress(len(candidates), len(candidates), summary)
		return nil
	}

	if len(plans) == 0 {
		summary := fmt.Sprintf(
			"series-denumber: %d candidates found (%s) but none are eligible to apply — "+
				"pass applyMedium to include bracketed positions; low never applies",
			len(candidates), tiers)
		_ = reporter.Log(slog.LevelInfo, summary)
		_ = reporter.UpdateProgress(1, 1, summary)
		return nil
	}

	// 🔒 SEQUENTIAL ON PURPOSE. Two numbered series folding onto the same base
	// must not both decide the base is missing and create it — that would replace
	// one split series with two. Series creation is the shared mutable state here,
	// so this loop is deliberately not parallelised (contrast dedupe-book-file-rows,
	// where every book is disjoint).
	targets := map[string]int{} // lowercased base+author → series ID
	var merged, movedBooks, created, deleted, failed, refusedByGuard int

	for idx, pl := range plans {
		if ctx.Err() != nil {
			log.Warn("series-denumber: cancelled", "merged", merged, "remaining", len(plans)-idx)
			return ctx.Err()
		}

		var authorID *int
		for i := range allSeries {
			if allSeries[i].ID == pl.FromID {
				authorID = allSeries[i].AuthorID
				break
			}
		}
		aid := 0
		if authorID != nil {
			aid = *authorID
		}
		key := fmt.Sprintf("%s\x00%d", strings.ToLower(pl.IntoName), aid)

		targetID, ok := targets[key]
		if !ok {
			targetID = pl.IntoID
		}
		if targetID == 0 {
			// Re-check the store before creating: another plan in this same run may
			// have created it, and GetSeriesByName is the authority.
			if existing, gerr := store.GetSeriesByName(pl.IntoName, authorID); gerr == nil && existing != nil {
				targetID = existing.ID
			} else {
				ns, cerr := store.CreateSeries(pl.IntoName, authorID)
				if cerr != nil || ns == nil {
					failed++
					log.Warn("series-denumber: CreateSeries failed", "name", pl.IntoName, "err", cerr)
					continue
				}
				targetID = ns.ID
				created++
			}
		}
		targets[key] = targetID

		if targetID == pl.FromID {
			continue // already the canonical row
		}

		// AllVersions, not the Core listing getter. The movedAll guard below
		// CANNOT cover for the filtered getter here, which is why this line is
		// the fix rather than the guard: movedAll starts true and is only ever
		// set false inside the loop over these rows, so a non-primary version
		// the Core getter excluded is never iterated, never flips the flag, and
		// the delete proceeds anyway. The guard's sample space was the filtered
		// set the bug lives outside of.
		books, berr := store.GetBooksBySeriesIDAllVersions(pl.FromID)
		if berr != nil {
			failed++
			log.Warn("series-denumber: GetBooksBySeriesIDAllVersions failed", "series", pl.FromID, "err", berr)
			continue
		}

		// safeToDelete is the fix: whether DeleteSeries below is safe does NOT
		// depend on movedAll alone. movedAll only tracks the rows THIS loop
		// saw (the AllVersions enumeration above); when that enumeration is
		// empty the loop body never runs and movedAll is vacuously true, which
		// is exactly the trashed-row gap #2908 closed for the sibling paths.
		// refCounts[pl.FromID] is the unfiltered count fetched once above; when
		// it exceeds len(books), rows this run cannot see (trashed, or an
		// unhydratable non-primary version) still hold the series, and
		// deleting it would strand them. The books this run CAN see still get
		// moved below — only the delete of a still-referenced source series is
		// refused.
		safeToDelete := refCounts[pl.FromID] <= len(books)
		if !safeToDelete {
			refusedByGuard++
			log.Warn("series-denumber: series still referenced by rows this run cannot move — merging books but not deleting",
				"series", pl.FromID, "name", pl.FromName, "unfiltered_refs", refCounts[pl.FromID], "movable_books", len(books))
		}

		movedAll := true
		for i := range books {
			full, gerr := store.GetBookByID(books[i].ID)
			if gerr != nil || full == nil {
				failed++
				movedAll = false
				continue
			}
			sid := targetID
			pos := pl.Position
			full.SeriesID = &sid
			// Only fill the position when the book has none — an existing sequence
			// was set deliberately and outranks one parsed from a name.
			if full.SeriesSequence == nil {
				full.SeriesSequence = &pos
			}
			if _, uerr := store.UpdateBook(full.ID, full); uerr != nil {
				failed++
				movedAll = false
				log.Warn("series-denumber: UpdateBook failed", "book", full.ID, "err", uerr)
				continue
			}
			movedBooks++
		}

		// Only drop the emptied series when every book actually moved AND the
		// unfiltered guard above confirms nothing outside this run's view
		// still references it; a partial move, or an unseen (trashed)
		// reference, plus a delete would orphan the stragglers.
		if movedAll && safeToDelete {
			if derr := store.DeleteSeries(pl.FromID); derr != nil {
				log.Warn("series-denumber: DeleteSeries failed", "series", pl.FromID, "err", derr)
			} else {
				deleted++
			}
		}
		merged++
		if (idx+1)%25 == 0 || idx+1 == len(plans) {
			_ = reporter.UpdateProgress(idx+1, len(plans),
				fmt.Sprintf("merged %d/%d series (%d books moved)", merged, len(plans), movedBooks))
		}
	}

	// Creating base series, moving books between them and deleting the emptied
	// ones all change the cached series list (24-hour TTL, warmed at startup).
	// Without this the denumber result is invisible on /api/v1/series until the
	// next restart, which reads as an op that did nothing.
	if created > 0 || deleted > 0 || movedBooks > 0 {
		p.deps.InvalidateSeriesCache()
	}

	summary := fmt.Sprintf(
		"series-denumber: %d series scanned, merged %d into %d base series "+
			"(%d books moved, %d base series created, %d emptied series deleted, "+
			"%d held back by the reference guard), failed %d | e.g. %s",
		len(in), merged, len(bases), movedBooks, created, deleted, refusedByGuard, failed, strings.Join(examples, "; "))
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(plans), len(plans), summary)
	return nil
}
