// file: internal/itunes/service/regroup_plan.go
// version: 1.7.1
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-10-05

package itunesservice

import (
	"fmt"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
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

	// HasLibraryFile: at least one of the book's book_file rows (missing or
	// not) has a path under the library root and outside the frozen iTunes
	// tree. That is a library-folder copy, whatever its primary flag says: a
	// library copy can hold iTunes PIDs (itunes.clone-into-library moves the
	// PID onto the library rows) and need not be its group's primary.
	HasLibraryFile bool
	// HasNonITunesFile: at least one book_file row is outside the frozen
	// iTunes tree. With Organized it marks a library copy even when the root
	// comparison missed it (a symlinked or oddly spelled root).
	HasNonITunesFile bool
	// Organized: library_state is "organized". ABS shows a primary organized
	// book, so files leaving one can take visible content away.
	Organized bool
	// FilesWithoutPID counts the book's book_file rows that carry no iTunes
	// PID. Such a row cannot be shown to belong to any heal group.
	FilesWithoutPID int
	// ManualOnly: the book is Doctor Who / Big Finish / Torchwood by the
	// whole-book check (applygate.BookManualOnly: its row, series, credits,
	// tags, every file path and the files' transcribed fields). The regroup
	// never touches it.
	ManualOnly bool
}

// Entanglement skip reasons (GroupAction.EntangleReason). See entanglement.
const (
	EntangleGroupedSource = "grouped-source"  // files would leave a version-group member
	EntangleWouldEmpty    = "would-empty"     // a version-group member would be emptied (and deleted)
	EntanglePrimaryTarget = "primary-target"  // files would be added to a group's primary (library) copy
	EntangleAmbiguous     = "ambiguous-group" // target's group has no incumbent primary to tell copies apart
	EntangleMixedTarget   = "mixed-target"    // grouped target already holds another heal group's tracks, or a row with no PID
	EntangleLibraryTarget = "library-target"  // target holds a file under the library root (a library copy)
	EntangleLibrarySource = "library-source"  // a source is organized or holds a file under the library root
	EntangleUnknownBook   = "unknown-book"    // a holder is not a live book (trashed, deleted, merged away)
	EntangleChanged       = "changed"         // apply-time recheck: a planned file is no longer where the plan saw it
)

// ReasonOwnerManualOnly is the GroupAction.SkipReason of a group left alone
// because its title or a holder names an owner-manual-only library
// (applygate.IsOwnerManualOnly on the title, BookMeta.ManualOnly on holders).
const ReasonOwnerManualOnly = applygate.ReasonOwnerManualOnly

// Snapshot is an immutable read of the DB state the planner reasons over. It is
// built ONCE before planning and never changes during planning — so the plan is
// a pure function of (groups, snapshot) and dry-run == apply by construction.
type Snapshot struct {
	// PIDLoc maps every group PID that resolves in the DB to its file/book.
	// PIDs absent here are "unresolved" (in the XML but never imported).
	PIDLoc map[string]PIDLoc
	// Books holds metadata for every LIVE book: not soft-deleted, not merged
	// away. PIDLoc must only point at books listed here; a location whose book
	// is missing is refused (EntangleUnknownBook), never planned.
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
	// ManualOnly: skipped because the group's title or one of its holders is
	// owner-manual-only (Doctor Who / Big Finish / Torchwood). No mutation,
	// not even a retitle.
	ManualOnly bool
	// KeepTitle: the existing target is a library-folder copy (LibraryCopy).
	// Its metadata comes from the metadata pipeline, never from iTunes album
	// tags, so the apply must not retitle it (or write any other field).
	KeepTitle  bool
	Unresolved []string // group PIDs not present in the DB
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
	// ManualOnlySkipped counts groups skipped as owner-manual-only.
	ManualOnlySkipped int
	// LibraryTitleKept counts planned groups whose existing target is a
	// library-folder copy, so its title is left as it is (KeepTitle).
	LibraryTitleKept int
	PIDsResolved     int
	PIDsUnresolved   int

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

	// pidGroup maps each PID to the index of the heal group listing it. Kept
	// so the apply-time Recheck can rebuild the mixed-target check on fresh
	// rows.
	pidGroup map[string]int
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
	plan.pidGroup = own.pidGroup
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

		// Holder-level refusals come BEFORE the already-correct shortcut: the
		// apply retitles every target, moves or not, so a trashed or
		// owner-manual-only book must not even be claimed. Each branch appends
		// exactly one action so plan.Groups[gi] stays aligned with groups[gi]
		// (Recheck depends on it).
		if applygate.IsOwnerManualOnly(g.Title, "") || anyHolder(resolved, snap, func(b BookMeta) bool { return b.ManualOnly }) {
			act.ManualOnly = true
			plan.ManualOnlySkipped++
			plan.Groups = append(plan.Groups, act)
			continue
		}
		if unknownHolder(resolved, snap) {
			act.Entangled = true
			act.EntangleReason = EntangleUnknownBook
			plan.EntangledSkipped++
			plan.EntangledByReason[EntangleUnknownBook]++
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
			act.KeepTitle = LibraryCopy(snap.Books[target])
			if act.KeepTitle {
				plan.LibraryTitleKept++
			}
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
			// entanglement already refuses a target with a library file; an
			// organized target without one still keeps its title.
			act.KeepTitle = LibraryCopy(snap.Books[target])
			if act.KeepTitle {
				plan.LibraryTitleKept++
			}
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
// curated version link meaning what it meant and every library-folder copy
// exactly as it is. By the owner's rule every book gets a copy in the library
// folder and that copy is primary; iTunes copies linked to it are non-primary
// members. The primary FLAG is not a reliable stand-in for "is the library
// copy" (a library copy can sit unflagged while an iTunes copy holds the
// flag), so the rule reads the book's LOCATION too (BookMeta.HasLibraryFile,
// BookMeta.Organized). The regroup may complete an iTunes edition but must
// never change which files make up a different edition or a library copy,
// nor remove a member. The rule, checked in order:
//
//  0. Every source and the target must be a live book in the snapshot
//     (EntangleUnknownBook otherwise: trashed, deleted or merged away).
//  1. No file may LEAVE a version-group member (EntangleWouldEmpty when the
//     member would be left with no files and so deleted, else
//     EntangleGroupedSource). This covers a fresh-book split out of a grouped
//     book, a move between two members of the same group, and a move from a
//     member of another group into the target.
//  2. No file may leave a book that is organized or holds a file under the
//     library root (EntangleLibrarySource): moving rows off it could take
//     ABS-visible content onto a book ABS does not show. Checked before the
//     fresh-book case, since a split also takes files off its sources.
//  3. A fresh-book target is allowed from here (its sources passed 1 and 2).
//  4. No existing target may hold a file under the library root, nor be
//     organized with any file outside the frozen iTunes tree
//     (EntangleLibraryTarget): pouring iTunes-folder rows into a library copy
//     scatters its files across two locations, whatever its flag says.
//  5. A target NOT in a version group may now receive (ungrouped<->ungrouped).
//  6. A grouped target may receive files from those ungrouped fragments only
//     when it is a NON-primary member of a group that HAS an incumbent
//     primary, AND every one of its book_file rows carries an iTunes PID, and
//     every such PID that some heal group owns belongs to THIS heal group
//     (PIDs no heal group owns -- in the DB, gone from the XML -- are
//     ignored): it is then an iTunes edition of this one album linked to its
//     library copy, and gathering the rest of the album's tracks onto it
//     completes the edition the link already points at.
//     - target is the incumbent primary -> EntanglePrimaryTarget: it is the
//     copy users see, and pouring iTunes-folder rows into it would change
//     what is shown and played.
//     - target's group has no incumbent ({unset,unset,...}, all false) ->
//     EntangleAmbiguous: the target could be the copy users see, and the
//     incumbent rule refuses to guess, so the planner does too.
//     - target holds a row with no PID, or another heal group's PID ->
//     EntangleMixedTarget.
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
	for _, id := range sources {
		if _, ok := snap.Books[id]; !ok {
			return EntangleUnknownBook
		}
	}
	var t BookMeta
	if !fresh {
		var ok bool
		if t, ok = snap.Books[target]; !ok {
			return EntangleUnknownBook
		}
	}
	reason := ""
	for _, id := range sources {
		b := snap.Books[id]
		if b.VersionGroupID == "" {
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
	for _, id := range sources {
		if b := snap.Books[id]; b.Organized || b.HasLibraryFile {
			return EntangleLibrarySource
		}
	}
	if fresh {
		return ""
	}
	// An organized target with any row outside the iTunes tree is a library
	// copy even if HasLibraryFile missed it (root spelling, symlink). An
	// organized book whose rows are ALL in the iTunes tree is an in-place
	// iTunes edition and may still receive its own album's fragments.
	if t.HasLibraryFile || (t.Organized && t.HasNonITunesFile) {
		return EntangleLibraryTarget
	}
	if t.VersionGroupID == "" {
		return ""
	}
	if t.GroupHasNoIncumbent {
		return EntangleAmbiguous
	}
	if t.IsPrimary {
		return EntanglePrimaryTarget
	}
	if t.FilesWithoutPID > 0 {
		return EntangleMixedTarget
	}
	for _, pid := range own.bookPIDs[target] {
		if owner, ok := own.pidGroup[pid]; ok && owner != gi {
			return EntangleMixedTarget
		}
	}
	return ""
}

// LibraryCopy reports whether b is a library-folder copy: it has a file
// under the library root or is organized. Owner rule: that copy is the
// canonical book and its metadata comes from the metadata pipeline, so the
// regroup never retitles it from an iTunes album tag.
func LibraryCopy(b BookMeta) bool {
	return b.HasLibraryFile || b.Organized
}

// anyHolder reports whether pred holds for any known book among the holders.
func anyHolder(resolved []PIDLoc, snap Snapshot, pred func(BookMeta) bool) bool {
	for _, loc := range resolved {
		if b, ok := snap.Books[loc.BookID]; ok && pred(b) {
			return true
		}
	}
	return false
}

// unknownHolder reports whether any holder is missing from snap.Books (not a
// live book). The snapshot builder drops such locations, so this is the
// planner's own refusal for a snapshot that did not.
func unknownHolder(resolved []PIDLoc, snap Snapshot) bool {
	for _, loc := range resolved {
		if _, ok := snap.Books[loc.BookID]; !ok {
			return true
		}
	}
	return false
}

// Recheck re-runs the plan's refusal rules for plan.Groups[gi] on FRESH rows,
// read immediately before the apply writes that group, and returns "" when
// the group may still be applied or the reason it must now be skipped
// (ReasonOwnerManualOnly or an Entangle* constant). fresh must hold the
// group's target (unless FreshBook) and every move source, each with every
// PID on its current book_file rows in fresh.PIDLoc; a book that is no longer
// live is left out of fresh.Books and refused as EntangleUnknownBook.
//
// It also refuses (EntangleChanged) when a planned file is no longer on the
// book the plan moves it from, so a move is never applied from a stale read.
func (p RegroupPlan) Recheck(gi int, fresh Snapshot) string {
	if gi < 0 || gi >= len(p.Groups) {
		return EntangleUnknownBook
	}
	a := p.Groups[gi]
	if a.Entangled || a.ManualOnly || (a.Target == "" && !a.FreshBook) {
		return ""
	}
	ids := make([]string, 0, len(a.Moves)+1)
	if !a.FreshBook {
		ids = append(ids, a.Target)
	}
	for _, m := range a.Moves {
		ids = append(ids, m.From)
	}
	for _, id := range ids {
		b, ok := fresh.Books[id]
		if !ok {
			return EntangleUnknownBook
		}
		if b.ManualOnly {
			return ReasonOwnerManualOnly
		}
	}
	if len(a.Moves) == 0 {
		return ""
	}
	for _, m := range a.Moves {
		if loc, ok := fresh.PIDLoc[m.PID]; !ok || loc.FileID != m.FileID || loc.BookID != m.From {
			return EntangleChanged
		}
	}
	own := ownership{pidGroup: p.pidGroup, bookPIDs: make(map[string][]string)}
	for pid, loc := range fresh.PIDLoc {
		own.bookPIDs[loc.BookID] = append(own.bookPIDs[loc.BookID], pid)
	}
	return entanglement(gi, a.Moves, a.Target, a.FreshBook, fresh, own)
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
// stability. It only ranks; entanglement still refuses a target that is a
// library copy (HasLibraryFile) or a group's primary.
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
