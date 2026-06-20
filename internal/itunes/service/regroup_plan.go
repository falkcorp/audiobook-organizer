// file: internal/itunes/service/regroup_plan.go
// version: 1.0.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-06-20

package itunesservice

import "sort"

// PIDLoc is the current DB location of one iTunes track PID.
type PIDLoc struct {
	FileID string
	BookID string
}

// BookMeta is the immutable per-book metadata the planner needs to choose a
// target book for a group. Built once from a DB read-snapshot; never mutated.
type BookMeta struct {
	ID                   string
	IsPrimary            bool
	FileCount            int   // total BookFiles currently on this book
	EnrichScore          int   // richer = preferred survivor (ISBN, description, …)
	CreatedAtUnix        int64 // older = preferred survivor (tiebreak)
	VersionGroupID       string
	HasNonPrimaryMembers bool // this book's version group has ≥1 non-primary member
}

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
	Title      string
	Target     string     // existing book ID claimed; "" iff FreshBook
	FreshBook  bool       // a new book must be created to hold this group
	Moves      []FileMove // PIDs whose file must move onto Target
	Entangled  bool       // skipped: version-group entanglement (no mutation)
	Unresolved []string   // group PIDs not present in the DB
}

// RegroupPlan is the complete, deterministic, frozen plan. The executor applies
// it blindly; it performs NO reads that influence decisions.
type RegroupPlan struct {
	Groups      []GroupAction
	DeleteBooks []string // books projected to hold zero files after all moves

	// Metrics (for the dry-run model-validation report).
	TotalGroups      int
	AlreadyCorrect   int // target already holds exactly the group's resolved PIDs
	Consolidated     int // groups requiring ≥1 move
	EntangledSkipped int
	FreshBooks       int
	PIDsResolved     int
	PIDsUnresolved   int
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
	plan := RegroupPlan{TotalGroups: len(groups)}
	claimed := make(map[string]bool, len(groups)) // existing books already taken as a target

	for _, g := range groups {
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

		if len(resolved) == 0 {
			// Nothing in the DB to heal for this group (book never imported).
			plan.Groups = append(plan.Groups, act)
			continue
		}

		// Version-entanglement guard: if any book holding this group's files is
		// part of a version group with non-primary members, skip (conservative
		// v1 — auto-merging could orphan or mis-merge curated versions).
		if entangledAmong(resolved, snap) {
			act.Entangled = true
			plan.EntangledSkipped++
			plan.Groups = append(plan.Groups, act)
			continue
		}

		// Pick the target: the best UNCLAIMED book among the holders. If every
		// holder is already claimed by an earlier group, allocate a fresh book.
		target, fresh := pickTarget(resolved, snap, claimed)
		act.Target = target
		act.FreshBook = fresh
		if fresh {
			plan.FreshBooks++
		} else {
			claimed[target] = true
		}

		// Emit a move for every resolved PID whose file is not already on target.
		for i, loc := range resolved {
			if loc.BookID != target {
				act.Moves = append(act.Moves, FileMove{PID: resolvedPIDs[i], FileID: loc.FileID, From: loc.BookID})
			}
		}

		if len(act.Moves) == 0 && !fresh {
			plan.AlreadyCorrect++
		} else {
			plan.Consolidated++
		}
		plan.Groups = append(plan.Groups, act)
	}

	plan.DeleteBooks = projectEmptyBooks(plan.Groups, snap)
	return plan
}

// entangledAmong reports whether any distinct book holding the group's resolved
// files is in a version group with non-primary members.
func entangledAmong(resolved []PIDLoc, snap Snapshot) bool {
	for _, loc := range resolved {
		if b, ok := snap.Books[loc.BookID]; ok && b.VersionGroupID != "" && b.HasNonPrimaryMembers {
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

// betterSurvivor orders books best-first: primary, then richer enrichment, then
// more files, then older, then by ID for stability.
func betterSurvivor(a, b BookMeta) bool {
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
