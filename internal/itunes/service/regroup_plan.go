// file: internal/itunes/service/regroup_plan.go
// version: 1.4.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-10-01

package itunesservice

import (
	"fmt"
	"sort"
)

// PIDLoc is the current DB location of one iTunes track PID.
type PIDLoc struct {
	FileID string
	BookID string
}

// BookMeta is the immutable per-book metadata the planner needs to choose a
// target book for a group. Built once from a DB read-snapshot; never mutated.
type BookMeta struct {
	ID    string
	Title string
	// IsPrimary is the EFFECTIVE primary flag. For a book in a version group it
	// is true only for the group's incumbent primary (the explicitly-true
	// member, else the one unset member); for an ungrouped book it is
	// database.EffectiveIsPrimaryVersion (unset reads as primary).
	IsPrimary      bool
	FileCount      int   // total BookFiles currently on this book
	DurationSec    int   // book aggregate duration (seconds)
	EnrichScore    int   // richer = preferred survivor (ISBN, description, …)
	CreatedAtUnix  int64 // older = preferred survivor (tiebreak)
	VersionGroupID string
	// GroupHasNoIncumbent: this book is in a version group in which no member
	// can be told apart as the primary (no explicit true and not exactly one
	// unset member), so the planner cannot know which member is the library
	// copy.
	GroupHasNoIncumbent bool
	// LegacyEntangled is the input of the rule this planner used before
	// 2026-10-01 (book is in a version group that has any member whose flag is
	// not explicitly true). It drives ONLY the dry-run delta report
	// (RegroupPlan.Unblocked / NewlyBlocked), never a decision.
	LegacyEntangled bool
}

// Entanglement skip reasons (GroupAction.EntangleReason). See entanglement.
const (
	EntangleGroupedSource = "grouped-source"  // files would leave a version-group member
	EntangleWouldEmpty    = "would-empty"     // a version-group member would be emptied (and deleted)
	EntanglePrimaryTarget = "primary-target"  // files would be added to a group's primary (library) copy
	EntangleAmbiguous     = "ambiguous-group" // target's group has no incumbent primary to tell copies apart
	EntangleMixedTarget   = "mixed-target"    // grouped target already holds another heal group's tracks
)

// Snapshot is an immutable read of the DB state the planner reasons over. It is
// built ONCE before planning and never changes during planning — so the plan is
// a pure function of (groups, snapshot) and dry-run == apply by construction.
type Snapshot struct {
	// PIDLoc maps every group PID that resolves in the DB to its file/book.
	// PIDs absent here are "unresolved" (in the XML but never imported).
	PIDLoc map[string]PIDLoc
	// Books holds metadata for every book referenced by PIDLoc.
	Books map[string]BookMeta
}

// FileMove relocates one PID's BookFile from its current book to the group target.
type FileMove struct {
	PID    string
	FileID string
	From   string
}

// GroupAction is the frozen resolution for one HealGroup.
type GroupAction struct {
	Title     string
	Target    string     // existing book ID claimed; "" iff FreshBook
	FreshBook bool       // a new book must be created to hold this group
	Moves     []FileMove // PIDs whose file must move onto Target
	Entangled bool       // skipped: version-group entanglement (no mutation)
	// EntangleReason is why Entangled is set (one of the Entangle* constants).
	EntangleReason string
	Unresolved     []string // group PIDs not present in the DB
}

// RegroupPlan is the complete, deterministic, frozen plan. The executor applies
// it blindly; it performs NO reads that influence decisions.
type RegroupPlan struct {
	Groups      []GroupAction
	DeleteBooks []string // books projected to hold zero files after all moves

	// Metrics (for the dry-run model-validation report).
	TotalGroups      int
	AlreadyCorrect   int // group's resolved PIDs already all on one book (may be PARTIAL)
	Consolidated     int // groups requiring ≥1 move
	EntangledSkipped int
	// EntangledByReason breaks EntangledSkipped down by EntangleReason.
	EntangledByReason map[string]int
	FreshBooks        int
	PIDsResolved      int
	PIDsUnresolved    int

	// Rule-change delta (dry-run report). For every group that needs moves, the
	// pre-2026-10-01 rule (skip when any holder's version group has a member
	// whose flag is not explicitly true) is evaluated on the SAME holder set as
	// the current rule. Unblocked: legacy skipped it, the current rule plans it.
	// NewlyBlocked: legacy planned it, the current rule skips it. This is a
	// per-group comparison inside ONE plan, not a diff of two full plans: once a
	// formerly-skipped group claims a target, later groups' claims can differ
	// from what the legacy plan would have chosen.
	LegacyEntangledSkipped int
	Unblocked              int
	NewlyBlocked           int
	UnblockedExamples      []string

	// Completeness metrics — distinguish "correctly grouped" from "only partially
	// present". A group is PARTIAL when some of its XML PIDs resolve in the DB and
	// some do not: the book(s) holding it are missing tracks. CompleteGroups have
	// every XML PID present. SingleFileChapterBooks is the count of distinct books
	// that (a) hold ≥1 PID of a multi-track group and (b) have exactly one file —
	// i.e. lone single-file "books" that are really one chapter of a larger book.
	CompleteGroups         int
	PartialGroups          int
	SingleFileChapterBooks int

	// Single-file-chapter books bucketed by duration, to separate TRUE lone
	// chapters (short) from COMPLETE single-file books that merely share an album
	// tag with un-imported siblings (long). SFCExamples holds a few short-bucket
	// "title (dur)" samples for eyeballing.
	SFCShort    int // < 15min — likely a true chapter or intro/credit clip
	SFCMid      int // 15-90min — novella / long chapter / short book (ambiguous)
	SFCLong     int // >= 90min — a COMPLETE book (false alarm: series entry)
	SFCExamples []string
}

// PlanRegroup computes the frozen heal plan: assign each group exactly one target
// book under a one-book-at-most-one-group constraint, gather the group's resolved
// PIDs' files onto that target, and project which books end up empty.
//
// Pure and deterministic: groups MUST already be in deterministic order (as
// GroupLibraryForHeal returns them); claims are resolved in that order so a book
// contested by two groups always goes to the same one, and the loser allocates a
// fresh book — which is what actually SPLITS an over-merged book rather than
// silently retitling it.
func PlanRegroup(groups []HealGroup, snap Snapshot) RegroupPlan {
	plan := RegroupPlan{TotalGroups: len(groups), EntangledByReason: make(map[string]int)}
	claimed := make(map[string]bool, len(groups)) // existing books already taken as a target
	own := newOwnership(groups, snap)
	singleFileChapters := make(map[string]struct{}) // distinct single-file books in multi-track groups

	for gi, g := range groups {
		act := GroupAction{Title: g.Title}

		// Resolve this group's PIDs against the snapshot.
		resolved := make([]PIDLoc, 0, len(g.PIDs))
		resolvedPIDs := make([]string, 0, len(g.PIDs))
		for _, pid := range g.PIDs {
			if loc, ok := snap.PIDLoc[pid]; ok {
				resolved = append(resolved, loc)
				resolvedPIDs = append(resolvedPIDs, pid)
			} else {
				act.Unresolved = append(act.Unresolved, pid)
			}
		}
		plan.PIDsResolved += len(resolved)
		plan.PIDsUnresolved += len(act.Unresolved)

		// Completeness: did every XML track for this group make it into the DB?
		if len(resolved) > 0 {
			if len(resolved) == len(g.PIDs) {
				plan.CompleteGroups++
			} else {
				plan.PartialGroups++
			}
		}
		// Lone single-file books that are really one chapter of a multi-track book,
		// bucketed by duration so true chapters (short) separate from complete
		// single-file books sharing an album tag (long).
		if len(g.PIDs) > 1 {
			for _, loc := range resolved {
				b, ok := snap.Books[loc.BookID]
				if !ok || b.FileCount != 1 {
					continue
				}
				if _, seen := singleFileChapters[loc.BookID]; seen {
					continue
				}
				singleFileChapters[loc.BookID] = struct{}{}
				switch {
				case b.DurationSec < 900:
					plan.SFCShort++
					if len(plan.SFCExamples) < 12 {
						plan.SFCExamples = append(plan.SFCExamples, fmt.Sprintf("%q (%ds)", b.Title, b.DurationSec))
					}
				case b.DurationSec < 5400:
					plan.SFCMid++
				default:
					plan.SFCLong++
				}
			}
		}

		if len(resolved) == 0 {
			// Nothing in the DB to heal for this group (book never imported).
			plan.Groups = append(plan.Groups, act)
			continue
		}

		// Pick the target: the best UNCLAIMED book among the holders, then compute
		// the moves needed to gather this group's files onto it.
		target, fresh := pickTarget(resolved, snap, claimed)
		var moves []FileMove
		for i, loc := range resolved {
			if loc.BookID != target {
				moves = append(moves, FileMove{PID: resolvedPIDs[i], FileID: loc.FileID, From: loc.BookID})
			}
		}

		// Already correct: all of this group's files are on one existing book.
		// Nothing moves, so version entanglement is IRRELEVANT (nothing to orphan
		// or mis-merge) — claim the book and count it correct, never skip it.
		// (Checking entanglement BEFORE this point is the bug that made ~95% of
		// groups falsely skip merely for touching a version-grouped book.)
		if len(moves) == 0 && !fresh {
			act.Target = target
			claimed[target] = true
			plan.AlreadyCorrect++
			plan.Groups = append(plan.Groups, act)
			continue
		}

		// Moves ARE needed. Only now does entanglement matter (see entanglement
		// for the rule and why). The legacy rule is evaluated alongside it on the
		// same holders, for the dry-run delta report only.
		reason := entanglement(gi, moves, target, fresh, snap, own)
		legacy := legacyEntangledAmong(resolved, snap)
		if legacy {
			plan.LegacyEntangledSkipped++
		}
		switch {
		case legacy && reason == "":
			plan.Unblocked++
			if len(plan.UnblockedExamples) < 12 {
				plan.UnblockedExamples = append(plan.UnblockedExamples, fmt.Sprintf("%q (%d moves)", g.Title, len(moves)))
			}
		case !legacy && reason != "":
			plan.NewlyBlocked++
		}
		if reason != "" {
			act.Entangled = true
			act.EntangleReason = reason
			plan.EntangledSkipped++
			plan.EntangledByReason[reason]++
			plan.Groups = append(plan.Groups, act)
			continue
		}

		act.Target = target
		act.FreshBook = fresh
		act.Moves = moves
		if fresh {
			plan.FreshBooks++
		} else {
			claimed[target] = true
		}
		plan.Consolidated++
		plan.Groups = append(plan.Groups, act)
	}

	plan.SingleFileChapterBooks = len(singleFileChapters)
	plan.DeleteBooks = projectEmptyBooks(plan.Groups, snap)
	return plan
}

// entanglement decides whether a group's planned moves may run, returning ""
// (allowed) or the Entangle* reason it is skipped. Its job is to keep every
// curated version link meaning what it meant: a version group links EDITIONS
// of one work (by the owner's rule, the library-folder copy is the primary and
// every iTunes copy linked to it is a non-primary member), so the regroup may
// complete an iTunes edition but must never change which files make up a
// different edition, nor remove a member. The rule, checked in order:
//
//  1. No file may LEAVE a version-group member (EntangleWouldEmpty when the
//     member would be left with no files and so deleted, else
//     EntangleGroupedSource). This covers a fresh-book split out of a grouped
//     book, a move between two members of the same group, and a move from a
//     member of another group into the target.
//  2. After (1) every source is an ungrouped iTunes record. A target that is
//     NOT in a version group may receive anything (ungrouped<->ungrouped).
//  3. A grouped target may receive files from those ungrouped fragments only
//     when it is a NON-primary member of a group that HAS an incumbent
//     primary, AND every iTunes PID it already holds belongs to THIS heal
//     group (PIDs no heal group owns -- in the DB, gone from the XML -- are
//     ignored): it is then an iTunes edition of this one album linked to its
//     library copy, and gathering the rest of the album's tracks onto it
//     completes the edition the link already points at. The library copy is
//     untouched.
//     - target is the incumbent primary -> EntanglePrimaryTarget: that is the
//     library copy (or, before the owner's rule is fully applied, the copy
//     users see), and pouring iTunes-folder rows into it would mix
//     locations and change what is shown and played.
//     - target's group has no incumbent ({unset,unset,...}, all false) ->
//     EntangleAmbiguous: the target could be the library copy itself, and
//     the incumbent rule refuses to guess, so the planner does too.
//
// A group whose files are already on one book (no moves) never reaches here.
func entanglement(gi int, moves []FileMove, target string, fresh bool, snap Snapshot, own ownership) string {
	out := make(map[string]int)
	for _, m := range moves {
		out[m.From]++
	}
	sources := make([]string, 0, len(out))
	for id := range out {
		sources = append(sources, id)
	}
	sort.Strings(sources)
	reason := ""
	for _, id := range sources {
		b, ok := snap.Books[id]
		if !ok || b.VersionGroupID == "" {
			continue
		}
		if b.FileCount-out[id] <= 0 {
			return EntangleWouldEmpty
		}
		reason = EntangleGroupedSource
	}
	if reason != "" {
		return reason
	}
	if fresh {
		return ""
	}
	t, ok := snap.Books[target]
	if !ok || t.VersionGroupID == "" {
		return ""
	}
	if t.GroupHasNoIncumbent {
		return EntangleAmbiguous
	}
	if t.IsPrimary {
		return EntanglePrimaryTarget
	}
	for _, pid := range own.bookPIDs[target] {
		if owner, ok := own.pidGroup[pid]; ok && owner != gi {
			return EntangleMixedTarget
		}
	}
	return ""
}

// ownership indexes which heal group owns each PID and which PIDs each book
// currently holds, for the mixed-target check. Built once per plan.
type ownership struct {
	pidGroup map[string]int      // PID -> index of the heal group listing it
	bookPIDs map[string][]string // book ID -> PIDs whose file is on it (snapshot)
}

func newOwnership(groups []HealGroup, snap Snapshot) ownership {
	o := ownership{pidGroup: make(map[string]int), bookPIDs: make(map[string][]string)}
	for gi, g := range groups {
		for _, pid := range g.PIDs {
			if _, dup := o.pidGroup[pid]; !dup {
				o.pidGroup[pid] = gi
			}
		}
	}
	for pid, loc := range snap.PIDLoc {
		o.bookPIDs[loc.BookID] = append(o.bookPIDs[loc.BookID], pid)
	}
	return o
}

// legacyEntangledAmong is the pre-2026-10-01 rule: skip when any holder's
// version group has a member whose flag is not explicitly true. Kept ONLY to
// report the rule-change delta; it decides nothing.
func legacyEntangledAmong(resolved []PIDLoc, snap Snapshot) bool {
	for _, loc := range resolved {
		if b, ok := snap.Books[loc.BookID]; ok && b.VersionGroupID != "" && b.LegacyEntangled {
			return true
		}
	}
	return false
}

// pickTarget chooses the survivor book for a group from its holders, preferring
// richer/older books, skipping any already claimed by another group. Returns
// (bookID, false) for an existing target or ("", true) to create a fresh book.
func pickTarget(resolved []PIDLoc, snap Snapshot, claimed map[string]bool) (string, bool) {
	// Distinct candidate book IDs, deterministically ordered.
	seen := make(map[string]bool)
	var cands []string
	for _, loc := range resolved {
		if !seen[loc.BookID] {
			seen[loc.BookID] = true
			cands = append(cands, loc.BookID)
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		return betterSurvivor(snap.Books[cands[i]], snap.Books[cands[j]])
	})
	for _, id := range cands {
		if !claimed[id] {
			return id, false
		}
	}
	return "", true
}

// betterSurvivor orders books best-first: a version-grouped book, then primary,
// then richer enrichment, then more files, then older, then by ID for
// stability.
//
// A grouped holder ranks first because the entanglement rule never lets files
// leave a version-group member: with the grouped book as the target it only
// RECEIVES, which the rule can allow, whereas any other target would take its
// files and be skipped. It also keeps the book carrying the curated link as
// the survivor.
func betterSurvivor(a, b BookMeta) bool {
	if ag, bg := a.VersionGroupID != "", b.VersionGroupID != ""; ag != bg {
		return ag
	}
	if a.IsPrimary != b.IsPrimary {
		return a.IsPrimary
	}
	if a.EnrichScore != b.EnrichScore {
		return a.EnrichScore > b.EnrichScore
	}
	if a.FileCount != b.FileCount {
		return a.FileCount > b.FileCount
	}
	if a.CreatedAtUnix != b.CreatedAtUnix {
		return a.CreatedAtUnix < b.CreatedAtUnix
	}
	return a.ID < b.ID
}

// projectEmptyBooks returns, deterministically, the books that hold zero files
// after every planned move is applied: initial FileCount, minus files moved out,
// plus files moved in. Books with residual (non-group) files keep a positive
// count and are NOT listed — the executor additionally re-asserts no files AND no
// ext-id mappings before actually deleting.
func projectEmptyBooks(actions []GroupAction, snap Snapshot) []string {
	delta := make(map[string]int) // bookID -> net file change
	for _, a := range actions {
		for _, m := range a.Moves {
			delta[m.From]--
			if !a.FreshBook {
				delta[a.Target]++
			}
		}
	}
	var empty []string
	for id, d := range delta {
		meta, ok := snap.Books[id]
		if !ok {
			continue
		}
		if meta.FileCount+d <= 0 {
			empty = append(empty, id)
		}
	}
	sort.Strings(empty)
	return empty
}
