// file: internal/plugins/maintenance/version_group_primary_revive.go
// version: 1.0.0
// guid: 6e9fda51-d134-40e9-a8f9-cf9377034a24
// last-edited: 2026-09-24

package maintenance

import (
	"fmt"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// Decisions this op adds on top of versionprimary's elect / already_ok / held.
//
// WHY. Before 2026-09-24 MATCH-4 (metafetch checkMetadataSourceHashDuplicates)
// kept "most files, then earliest created" as the survivor of a
// metadata-hash cluster. For an organize pair that is the unorganized source,
// so the organized library copy was merged INTO its own source: the only copy
// ABS can list became a non-live merge loser and the group was held. Prod had
// 146 such groups; 128 more had their organized copy merged into a book of a
// different group; 227 groups still carried a non-live member flagged primary.
const (
	// vgDecisionRevive: a held group whose organized copy was merged into a
	// member of the SAME group. The merge is cleared and that copy crowned.
	vgDecisionRevive = "revive_merged_copy"
	// vgDecisionLeftover: a held group whose organized copy was merged into
	// a live organized book of ANOTHER group. Reported only, never written.
	vgDecisionLeftover = "leftover_merged_elsewhere"
	// vgDecisionDemoteNonLive: the live members are already right; only
	// non-live members still flagged primary are demoted.
	vgDecisionDemoteNonLive = "demote_nonlive"
)

// vgWritable reports whether apply writes a group with this decision.
func vgWritable(kind string) bool {
	switch kind {
	case versionprimary.DecisionElect, vgDecisionRevive, vgDecisionDemoteNonLive:
		return true
	}
	return false
}

// vgReviveCandidates are the non-live members a held group could revive: not
// soft-deleted, organized, merged into a live member of the same group, and
// with no active file in the iTunes library (hands-off, owner rule). The
// eligibility rest (files present under the root) is left to Elect.
func vgReviveCandidates(ms []versionprimary.Member, files map[string][]database.BookFile, alive func(string) bool) map[string]bool {
	inGroup := make(map[string]bool, len(ms))
	for _, m := range ms {
		inGroup[m.Book.ID] = true
	}
	out := map[string]bool{}
	for _, m := range ms {
		b := m.Book
		if m.Signals.Live || b.IsSoftDeleted() || b.MergedIntoBookID == nil {
			continue
		}
		target := *b.MergedIntoBookID
		if !inGroup[target] || !alive(target) {
			continue
		}
		if b.LibraryState == nil || *b.LibraryState != "organized" {
			continue
		}
		itunes := false
		for _, f := range files[b.ID] {
			if !f.Missing && authorPathLinkIsITunes(f.FilePath) {
				itunes = true
			}
		}
		if !itunes {
			out[b.ID] = true
		}
	}
	return out
}

// vgSimulateRevive re-runs Elect as if every candidate's merge were cleared.
// It returns the decision and true only when that election crowns one of the
// candidates; otherwise the group stays as planned (for example the source
// has a better tier and the group is rightly held). Only the winner is
// revived: the returned members keep every other candidate non-live.
func vgSimulateRevive(ms []versionprimary.Member, cands map[string]bool) (versionprimary.Decision, bool) {
	if len(cands) == 0 {
		return versionprimary.Decision{}, false
	}
	sim := reviveMembers(ms, cands)
	d := versionprimary.Elect(sim)
	if d.WinnerID == "" || !cands[d.WinnerID] {
		return versionprimary.Decision{}, false
	}
	// Re-elect with only the winner revived so the other candidates are
	// reported, and demoted, as the non-live rows they stay.
	d = versionprimary.Elect(reviveMembers(ms, map[string]bool{d.WinnerID: true}))
	d.Kind = vgDecisionRevive
	return d, true
}

func reviveMembers(ms []versionprimary.Member, ids map[string]bool) []versionprimary.Member {
	out := make([]versionprimary.Member, len(ms))
	for i, m := range ms {
		out[i] = m
		if ids[m.Book.ID] {
			b := *m.Book
			b.MergedIntoBookID = nil
			out[i].Book = &b
			out[i].Signals.Live = true
		}
	}
	return out
}

// vgNonLivePrimaries lists members that are merge losers (not live, not
// soft-deleted) yet still count as primary (explicit true or nil). ABS lists
// them: it does not filter merge losers.
func vgNonLivePrimaries(d *versionprimary.Decision, byID map[string]*database.Book) []string {
	var out []string
	for _, m := range d.Members {
		if b := byID[m.BookID]; b == nil || b.IsSoftDeleted() {
			continue
		}
		if !m.Live && m.StoredPrimary != "false" && m.BookID != d.WinnerID {
			out = append(out, m.BookID)
		}
	}
	return out
}

// classifyMerged refines Elect's decision for the merge-loser shapes:
//   - held + a same-group loser Elect would crown once un-merged → revive;
//   - still held + an organized loser merged into a live organized book of
//     another group → leftover (report only);
//   - non-live members still flagged primary → demoted when the group ends
//     with a live primary, else kept and reported.
func (a *vgApplier) classifyMerged(ms []versionprimary.Member, alive func(string) bool,
	byID map[string]*database.Book, g *vgRepairGroupReport) error {
	if g.Kind == versionprimary.DecisionHeld {
		files := map[string][]database.BookFile{}
		for _, m := range ms {
			if m.Signals.Live || m.Book.MergedIntoBookID == nil {
				continue
			}
			fs, err := a.store.GetBookFiles(m.Book.ID)
			if err != nil {
				return fmt.Errorf("read files of %s: %w", m.Book.ID, err)
			}
			files[m.Book.ID] = fs
		}
		if d, ok := vgSimulateRevive(ms, vgReviveCandidates(ms, files, alive)); ok {
			g.Decision = d
			g.RevivedID = d.WinnerID
			g.RevivedFrom = *byID[d.WinnerID].MergedIntoBookID
		} else if err := a.labelLeftover(ms, g); err != nil {
			return err
		}
	}
	nl := vgNonLivePrimaries(&g.Decision, byID)
	switch g.Kind {
	case versionprimary.DecisionElect, vgDecisionRevive:
		g.DemoteNonLive = nl
	case versionprimary.DecisionAlreadyOK:
		if len(nl) > 0 {
			g.Kind, g.DemoteNonLive = vgDecisionDemoteNonLive, nl
		}
	default:
		g.NonLiveKept = nl
	}
	return nil
}

// labelLeftover marks a held group whose organized copy was merged into a
// live organized book of another group. The first such member by id wins so
// the report is deterministic.
func (a *vgApplier) labelLeftover(ms []versionprimary.Member, g *vgRepairGroupReport) error {
	ids := make([]string, 0, len(ms))
	byID := map[string]*database.Book{}
	for _, m := range ms {
		ids = append(ids, m.Book.ID)
		byID[m.Book.ID] = m.Book
	}
	sort.Strings(ids)
	for _, id := range ids {
		b := byID[id]
		if b.IsSoftDeleted() || b.MergedIntoBookID == nil || b.LibraryState == nil || *b.LibraryState != "organized" {
			continue
		}
		target := *b.MergedIntoBookID
		if _, same := byID[target]; same {
			continue
		}
		t, err := a.store.GetBookByID(target)
		if err != nil {
			return fmt.Errorf("read merge target %s of %s: %w", target, id, err)
		}
		if t == nil || t.IsSoftDeleted() || (t.MergedIntoBookID != nil && *t.MergedIntoBookID != "") || t.LibraryState == nil || *t.LibraryState != "organized" {
			continue
		}
		g.Kind = vgDecisionLeftover
		g.LeftoverBookID, g.LeftoverTargetBookID = id, target
		if t.VersionGroupID != nil {
			g.LeftoverTargetGroupID = *t.VersionGroupID
		}
		return nil
	}
	return nil
}
