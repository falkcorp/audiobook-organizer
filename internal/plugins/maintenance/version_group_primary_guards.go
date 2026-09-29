// file: internal/plugins/maintenance/version_group_primary_guards.go
// version: 1.2.0
// guid: 5b0f3e7a-9c41-4d2e-8f6a-2d7c1e4b9a63
// last-edited: 2026-09-29

package maintenance

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// Group-level refusals of version-group-primary-repair. A group carrying one
// of these decisions is reported and never written, whatever the params say:
// dry_run:false and group_ids narrow a run, they do not unlock a refusal.
const (
	// vgDecisionSkipITunes: some member has an active file under
	// books/itunes/** (standing owner rule: the live iTunes library is
	// hands-off). The whole group is skipped, not just that member, because
	// demoting or crowning a sibling changes what ABS shows for the iTunes
	// copy too.
	vgDecisionSkipITunes = repairs.SkipITunes
	// vgDecisionSkipOwnerManual: some member's path or series is Doctor Who /
	// Big Finish / Torchwood, which the owner applies by hand
	// (applygate.IsOwnerManualOnly).
	vgDecisionSkipOwnerManual = repairs.SkipOwnerManual
	// vgDecisionRefuseNotOrganized: a writable decision whose winner is not
	// an organized book. Elect and the revive path never produce one; this is
	// the belt-and-braces check behind "never demote an organized member in
	// favour of a non-organized one".
	vgDecisionRefuseNotOrganized = "refused_winner_not_organized"
)

// vgGroupGuard returns a skip decision and its reason for a group that must
// not be touched, or "" when the group may be planned. Every member is
// checked, live or not: a merge loser's files are still on disk and still
// part of what the group shows. Paths come from every book_file row, missing
// ones included; Book.FilePath is checked as well because it is what older
// rows carry, and a false positive here only skips a group. The per-book
// check is repairs.GuardBookPaths, the same one the Repairs framework runs on
// every row, so the two can never disagree about a book. res memoizes symlink
// resolution per folder across the run's groups (nil: fresh per book).
func vgGroupGuard(store OpsStore, seriesNames map[int]string, res *repairs.PathResolver, members []database.Book) (kind, reason string, err error) {
	for i := range members {
		b := &members[i]
		if b.IsSoftDeleted() {
			continue
		}
		files, ferr := store.GetBookFiles(b.ID)
		if ferr != nil {
			return "", "", fmt.Errorf("read files of %s: %w", b.ID, ferr)
		}
		paths := []string{b.FilePath}
		// Missing rows count too: the hands-off rules are about where the
		// book lives, not whether its file is on disk right now.
		for _, f := range files {
			paths = append(paths, f.FilePath)
		}
		series := ""
		if b.SeriesID != nil {
			series = seriesNames[*b.SeriesID]
		}
		if kind, why := repairs.GuardBookPathsWith(res, b.ID, paths, series); kind != "" {
			return kind, why, nil
		}
	}
	return "", "", nil
}

// vgSkipKey is the skipped_by_reason key of a group apply will not write, or
// "" for a writable one.
func vgSkipKey(g *vgRepairGroupReport) string {
	switch {
	case g.Error != "" && g.Kind == "":
		return "error"
	case vgWritable(g.Kind):
		return ""
	case g.HoldReason != "":
		return g.HoldReason
	default:
		return g.Kind
	}
}
