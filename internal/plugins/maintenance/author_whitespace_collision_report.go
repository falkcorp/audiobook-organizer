// file: internal/plugins/maintenance/author_whitespace_collision_report.go
// version: 1.0.0
// guid: 6a5d0c32-4fbc-47b6-ab12-2cfbbff82249
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- author-whitespace-collision-report ---
//
// WHY THIS EXISTS. Since 2026-09-12 util.NormalizeAuthor collapses internal
// whitespace (TASK-086), so "Raymond  L.  Weil" and "Raymond L. Weil" now
// normalize to one key. Rows created before that are still separate authors.
// Merging them, and re-keying the legacy author:name: index entries (see
// internal/database/pebble_store_name_index.go), both have to pick a surviving
// row per group; the owner chose to see the groups first. This op lists them.
//
// Two kinds of group are counted separately:
//   - whitespace: members differ under the legacy key (trim + lowercase) and
//     collide only under the new one. This is what the normalization change
//     newly identifies.
//   - legacy: every member already shared one legacy key (pure case duplicates,
//     or rows minted by the pre-#2920 CreateAuthor race). Known before the
//     change; listed so the picture is complete.
//
// REPORT ONLY. No merges, renames or deletes; CapLibraryRead only, and no
// schedule (manual trigger only).

// whitespaceCollisionSampleLimit caps the groups logged PER KIND.
const whitespaceCollisionSampleLimit = 200

const (
	collisionKindWhitespace = "whitespace"
	collisionKindLegacy     = "legacy"
)

type whitespaceCollisionMember struct {
	ID   int
	Name string
	// Books is the DISPLAY count (GetAllAuthorBookCounts); Refs is the
	// unfiltered reference count (database.AuthorRefCounts) that merge/delete
	// guards use. Both are shown because a "0 books" row may still hold refs.
	Books int
	Refs  int
}

type whitespaceCollisionGroup struct {
	// Key is the new normalized name every member shares.
	Key     string
	Kind    string
	Members []whitespaceCollisionMember // ordered by ID
	// ResolvesTo is the author ID a name lookup returns for this key today,
	// i.e. the row the next import of any member spelling attaches to. 0 when
	// no index entry resolves. Filled in by the run, not by the pure scan.
	ResolvesTo int
	TotalRefs  int
}

type whitespaceCollisionReport struct {
	TotalAuthors int
	// Blank counts authors whose name normalizes to "". Not grouped: a blank
	// name is a different defect. Counted so they are not silently dropped.
	Blank            int
	Groups           int
	WhitespaceGroups int
	LegacyGroups     int
	AuthorsInGroups  int
	WhitespaceSample []whitespaceCollisionGroup
	LegacySample     []whitespaceCollisionGroup
}

func (r whitespaceCollisionReport) summary() string {
	return fmt.Sprintf(
		"REPORT ONLY (nothing changed) — authors=%d collision_groups=%d whitespace_groups=%d legacy_groups=%d authors_in_groups=%d blank(skipped)=%d",
		r.TotalAuthors, r.Groups, r.WhitespaceGroups, r.LegacyGroups, r.AuthorsInGroups, r.Blank)
}

// findWhitespaceCollisions groups authors by util.NormalizeAuthor and keeps the
// groups with more than one row.
//
// A plain loop, not a worker pool: the per-author work is one in-memory string
// normalization and a map insert over an already-loaded slice, with no DB,
// network or subprocess call per item.
func findWhitespaceCollisions(authors []database.Author, bookCounts, refCounts map[int]int, sampleLimit int) whitespaceCollisionReport {
	report := whitespaceCollisionReport{TotalAuthors: len(authors)}
	byKey := map[string][]database.Author{}
	for _, a := range authors {
		key := util.NormalizeAuthor(a.Name)
		if key == "" {
			report.Blank++
			continue
		}
		byKey[key] = append(byKey[key], a)
	}

	var ws, legacy []whitespaceCollisionGroup
	for key, members := range byKey {
		if len(members) < 2 {
			continue
		}
		g := whitespaceCollisionGroup{Key: key, Kind: collisionKindLegacy}
		legacyKeys := map[string]struct{}{}
		for _, a := range members {
			legacyKeys[util.NormalizeAuthorLegacy(a.Name)] = struct{}{}
			m := whitespaceCollisionMember{ID: a.ID, Name: a.Name, Books: bookCounts[a.ID], Refs: refCounts[a.ID]}
			g.TotalRefs += m.Refs
			g.Members = append(g.Members, m)
		}
		sort.Slice(g.Members, func(i, j int) bool { return g.Members[i].ID < g.Members[j].ID })
		report.Groups++
		report.AuthorsInGroups += len(members)
		if len(legacyKeys) > 1 {
			g.Kind = collisionKindWhitespace
			report.WhitespaceGroups++
			ws = append(ws, g)
		} else {
			report.LegacyGroups++
			legacy = append(legacy, g)
		}
	}
	report.WhitespaceSample = topCollisionGroups(ws, sampleLimit)
	report.LegacySample = topCollisionGroups(legacy, sampleLimit)
	return report
}

// topCollisionGroups orders groups by total references descending (most
// consequential first), then by lowest member ID so two runs can be diffed,
// and returns at most limit of them.
func topCollisionGroups(groups []whitespaceCollisionGroup, limit int) []whitespaceCollisionGroup {
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].TotalRefs != groups[j].TotalRefs {
			return groups[i].TotalRefs > groups[j].TotalRefs
		}
		return groups[i].Members[0].ID < groups[j].Members[0].ID
	})
	if limit >= 0 && len(groups) > limit {
		groups = groups[:limit]
	}
	return groups
}

// formatCollisionGroup renders one group for the activity log. Names use %q:
// with %s a whitespace group would print the same-looking name twice.
func formatCollisionGroup(g whitespaceCollisionGroup) string {
	parts := make([]string, 0, len(g.Members))
	for _, m := range g.Members {
		parts = append(parts, fmt.Sprintf("id=%d name=%q books=%d refs=%d", m.ID, m.Name, m.Books, m.Refs))
	}
	return fmt.Sprintf("collision group kind=%s key=%q resolves_to=%d members=[%s]",
		g.Kind, g.Key, g.ResolvesTo, strings.Join(parts, "; "))
}

func (p *Plugin) authorWhitespaceCollisionReportDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.author-whitespace-collision-report",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Author whitespace-collision report",
		Description: "REPORT ONLY — changes nothing. Groups author rows whose names are equal once " +
			"internal whitespace is collapsed and case is ignored (e.g. 'Raymond  L.  Weil' and " +
			"'Raymond L. Weil'). 'whitespace' groups collide only under the new normalization; " +
			"'legacy' groups already shared a key. Logs counts plus up to 200 groups per kind with " +
			"each member's author ID, quoted name, book count and reference count, and the author ID " +
			"a name lookup resolves to. Books is the DISPLAY counter (omits trashed books, " +
			"non-primary versions and junction-only co-author credits); refs is the unfiltered count.",
		// ResumeDrop: a read-only report is cheap to re-trigger.
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.author-whitespace-collision-report",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         10 * time.Minute,
		// No Schedule: manual trigger only.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runAuthorWhitespaceCollisionReport,
	}
}

func (p *Plugin) runAuthorWhitespaceCollisionReport(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, "Starting author whitespace-collision report (report only)")
	_ = reporter.UpdateProgress(0, 4, "Listing authors…")

	authors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("list authors: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	_ = reporter.UpdateProgress(1, 4, "Counting books and references per author…")
	// Both counts FAIL the op on error rather than rendering zeros: a report of
	// zeros is exactly the artifact someone would later cite to justify a merge.
	bookCounts, err := store.GetAllAuthorBookCounts()
	if err != nil {
		return fmt.Errorf("author book counts: %w", err)
	}
	refCounts, err := database.AuthorRefCounts(store)
	if err != nil {
		return fmt.Errorf("author reference counts: %w", err)
	}

	_ = reporter.UpdateProgress(2, 4, fmt.Sprintf("Grouping %d author names…", len(authors)))
	report := findWhitespaceCollisions(authors, bookCounts, refCounts, whitespaceCollisionSampleLimit)

	_ = reporter.UpdateProgress(3, 4, "Resolving the index owner of each logged group…")
	// One read per LOGGED group (bounded by 2 × the sample limit), not per author.
	for _, sample := range [][]whitespaceCollisionGroup{report.WhitespaceSample, report.LegacySample} {
		for i := range sample {
			if err := ctx.Err(); err != nil {
				return err
			}
			owner, err := store.GetAuthorByName(sample[i].Members[0].Name)
			if err != nil {
				return fmt.Errorf("resolve %q: %w", sample[i].Key, err)
			}
			if owner != nil {
				sample[i].ResolvesTo = owner.ID
			}
		}
	}

	summary := report.summary()
	_ = reporter.Log(slog.LevelInfo, summary)
	for _, sample := range [][]whitespaceCollisionGroup{report.WhitespaceSample, report.LegacySample} {
		for _, g := range sample {
			ids := make([]string, 0, len(g.Members))
			for _, m := range g.Members {
				ids = append(ids, fmt.Sprint(m.ID))
			}
			_ = reporter.Log(slog.LevelInfo, formatCollisionGroup(g),
				slog.String("kind", g.Kind), slog.String("key", g.Key),
				slog.Int("resolves_to", g.ResolvesTo), slog.Int("total_refs", g.TotalRefs),
				slog.String("author_ids", strings.Join(ids, ",")))
		}
	}
	_ = reporter.UpdateProgress(4, 4, summary)
	return nil
}
