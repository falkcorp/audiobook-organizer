// file: internal/plugins/maintenance/series_denumber_op.go
// version: 1.0.0
// guid: 3f0b6c84-52d1-4a97-9e35-c8b71d0af426
// last-edited: 2026-08-04

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// SeriesDenumberParams are the JSON parameters for the series de-numbering.
type SeriesDenumberParams struct {
	// Apply, when true, performs the merges. Default false — dry run.
	Apply bool `json:"apply"`
	// Limit caps how many SERIES are merged (0 = all). Useful for a canary.
	Limit int `json:"limit,omitempty"`
}

func (p *Plugin) seriesDenumberDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.series-denumber",
		Plugin:      "maintenance",
		DisplayName: "Merge series that carry a book number in their name",
		Description: "A series name should name the series, but many carry the book's position instead " +
			"(\"Discworld 05\", \"Safehold 01\", \"Schooled in Magic: Book 11\"), which splits one series into as " +
			"many one-book series as it has volumes. This merges them onto the base name and moves the number " +
			"into the book's series position. A bare trailing number is only trusted when the library " +
			"corroborates it — zero-padding, an explicit keyword, or another series sharing the base — so a real " +
			"name like \"Fahrenheit 451\" is left alone. Dry-run by default; pass {\"apply\": true} to merge.",
		ResumePolicy:    sdk.ResumeRestart,
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
	store := p.deps.Store()
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

	plans := SeriesDenumber(in)
	// Deterministic order so a dry run and the apply that follows it agree, and
	// so the FIRST plan for a base is the one that creates the base series.
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].IntoName != plans[j].IntoName {
			return plans[i].IntoName < plans[j].IntoName
		}
		return plans[i].Position < plans[j].Position
	})
	if params.Limit > 0 && len(plans) > params.Limit {
		plans = plans[:params.Limit]
	}

	log.Info("series-denumber: plan complete", "series", len(in), "merges", len(plans))
	if len(plans) == 0 {
		summary := fmt.Sprintf("series-denumber: %d series scanned, 0 carry a book number — nothing to do", len(in))
		_ = reporter.Log(slog.LevelInfo, summary)
		_ = reporter.UpdateProgress(1, 1, summary)
		return nil
	}

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

	if !params.Apply {
		reasons := make([]string, 0, len(byReason))
		for r, n := range byReason {
			reasons = append(reasons, fmt.Sprintf("%s=%d", r, n))
		}
		sort.Strings(reasons)
		summary := fmt.Sprintf(
			"series-denumber: %d series scanned, would merge %d into %d base series "+
				"(%d books would move) — by evidence: %s | e.g. %s",
			len(in), len(plans), len(bases), booksAffected, strings.Join(reasons, " "), strings.Join(examples, "; "))
		_ = reporter.Log(slog.LevelInfo, summary)
		_ = reporter.UpdateProgress(len(plans), len(plans), summary)
		return nil
	}

	// 🔒 SEQUENTIAL ON PURPOSE. Two numbered series folding onto the same base
	// must not both decide the base is missing and create it — that would replace
	// one split series with two. Series creation is the shared mutable state here,
	// so this loop is deliberately not parallelised (contrast dedupe-book-file-rows,
	// where every book is disjoint).
	targets := map[string]int{} // lowercased base+author → series ID
	var merged, movedBooks, created, deleted, failed int

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

		books, berr := store.GetBooksBySeriesIDCore(pl.FromID)
		if berr != nil {
			failed++
			log.Warn("series-denumber: GetBooksBySeriesIDCore failed", "series", pl.FromID, "err", berr)
			continue
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

		// Only drop the emptied series when every book actually moved; a partial
		// move plus a delete would orphan the stragglers.
		if movedAll {
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

	summary := fmt.Sprintf(
		"series-denumber: %d series scanned, merged %d into %d base series "+
			"(%d books moved, %d base series created, %d emptied series deleted), failed %d | e.g. %s",
		len(in), merged, len(bases), movedBooks, created, deleted, failed, strings.Join(examples, "; "))
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(plans), len(plans), summary)
	return nil
}
