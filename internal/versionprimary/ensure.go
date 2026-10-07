// file: internal/versionprimary/ensure.go
// version: 1.12.0
// guid: 0b7e4c52-9a1d-4f38-8c6e-2d51f0a7b9e3
// last-edited: 2026-10-06

package versionprimary

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Hand-off: the write half of the rule, for the paths that change a group's
// membership or a member's flag in passing (a soft-delete, a merge that moves
// a book to another group, a scan that links a copy, an undo). Before
// 2026-09-24 each of those either removed a group's primary without handing
// the flag on, or set primary=true without demoting the rest; see
// docs/audits and the PR that added this file.
//
// Two entry points:
//
//   - EnsureSinglePrimary repairs the INVARIANT (exactly one live explicit
//     primary, and it is eligible). A healthy group keeps its incumbent even
//     when Elect would rank another member higher: these callers run in hot
//     paths without ffprobe (Env.Probe is normally nil, so chapter counts come
//     from the chapter table and an unknown count ranks as an m4b without
//     chapters), while maintenance.version-group-primary-repair ranks with
//     ffprobe. If both re-ranked healthy groups they would flip a group back
//     and forth. Re-ranking a healthy group is that op's job alone.
//   - Crown writes an explicit choice (an undo, a user edit, a merge's
//     survivor) with no election, and demotes every other live member.
//
// Neither touches files: the only disk access is Loader's stat (and a probe
// when Env.Probe is set). Neither records history; each returns the writes it
// made so the caller can record them after the fact, in its own ledger.

var ensureLog = logger.New("versionprimary")

// EnsureStore is the store surface the hand-off needs.
type EnsureStore interface {
	FileReader
	database.ChapterReader
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
	GetBookByID(id string) (*database.Book, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// Env is what eligibility needs besides the store.
type Env struct {
	// RootDir is the library root (config.AppConfig.RootDir). Empty means
	// no member is eligible, so every group that needs an election is held.
	RootDir string
	// Probe may be nil (the hot-path default): the chapter table decides,
	// and a book with no chapter row ranks as TierM4BNoChapters.
	Probe ChapterProber
	// Stat defaults to os.Stat.
	Stat func(string) (os.FileInfo, error)
	// Expect, when set, is the member the caller predicted the hand-off
	// leaves primary (a Repairs fixer that checked it under its own lock,
	// before slower steps). EnsureSinglePrimary then writes nothing unless
	// its decision, taken under the group lock, is that member: it returns
	// ErrUnexpectedWinner instead of crowning anyone else. A group with no
	// rows (or no group at all) is refused the same way: nobody is left to
	// be the expected primary.
	Expect string
	// MayWrite, when set, is asked about every member whose
	// is_primary_version the hand-off is about to write, BEFORE any of
	// them is written, with the rows read under the group lock. The write
	// set is decided once from that read (writePlan) -- the winner when its
	// read flag is not explicit true, and every member it demotes, with
	// their electability as read -- and the hand-off then writes exactly
	// that set: never a member it did not ask about (a winner whose fresh
	// row lost its true aborts the hand-off; a member that turned
	// electable after the read is not written).
	//
	// A REFUSAL must wrap ErrWriteRefused: the whole hand-off is then
	// refused, nothing is written, and the error is returned (still
	// wrapping ErrWriteRefused, outcome OutcomeWriteRefused). Any other
	// error (a failed read) is a failure, not a refusal: nothing is
	// written and it is returned WITHOUT ErrWriteRefused, so a caller
	// retries it rather than recording a refusal. A Repairs fixer passes
	// its never-write-an-iTunes-book rule here (itunesguard.MayWrite).
	MayWrite func(m *database.Book) error
}

// ErrUnexpectedWinner: EnsureSinglePrimary's decision was not Env.Expect, so
// nothing was written.
var ErrUnexpectedWinner = errors.New("the hand-off's winner is not the expected member")

// ErrWriteRefused: Env.MayWrite refused a member the hand-off would write,
// so nothing was written. A MayWrite refusal wraps it (itunesguard's
// ErrITunesMember does); a hand-off returns it only for such a refusal.
var ErrWriteRefused = errors.New("the hand-off would write a member the caller refuses")

// Hand-off outcomes.
const (
	OutcomeEmpty          = "empty"            // no rows in the group
	OutcomeHealthy        = "healthy"          // one live eligible explicit primary; only siblings demoted, if any
	OutcomeElected        = "elected"          // Elect ran and its winner was written
	OutcomeHeld           = "held"             // Elect held the group; nothing written
	OutcomeCrowned        = "crowned"          // Crown wrote its explicit choice
	OutcomeWinnerChanged  = "winner_changed"   // the winner changed under us; nothing written
	OutcomeCrownNotMember = "crown_not_member" // Crown's book is not a live member of the group
	OutcomeWriteRefused   = "write_refused"    // Env.MayWrite refused a member; nothing written
)

// FlagWrite is one is_primary_version write the hand-off committed.
type FlagWrite struct {
	BookID   string `json:"book_id"`
	Previous string `json:"previous"` // "true", "false" or "nil"
	Primary  bool   `json:"primary"`
}

// HandoffResult reports one hand-off.
type HandoffResult struct {
	GroupID string `json:"group_id"`
	Outcome string `json:"outcome"`
	// PrimaryID is the member left as primary: the incumbent, the winner or
	// the crowned book. Empty when held or empty.
	PrimaryID string `json:"primary_id,omitempty"`
	// Decision is set when Elect ran.
	Decision *Decision   `json:"decision,omitempty"`
	Writes   []FlagWrite `json:"writes,omitempty"`
}

// WrotePrimary reports whether the hand-off itself wrote PrimaryID's
// explicit true. False when PrimaryID already carried it (a healthy
// incumbent kept, or a Crown of a member already explicit primary): the
// hand-off then made nobody primary, and a ledger note says so
// (undo.HandOffNoteValue) so an operation's revert never re-crowns over a
// primary the operation did not write.
func (r HandoffResult) WrotePrimary() bool {
	if r.PrimaryID == "" {
		return false
	}
	for _, w := range r.Writes {
		if w.BookID == r.PrimaryID && w.Primary {
			return true
		}
	}
	return false
}

// errHandoffAbort aborts a winner write whose row changed since the read.
var errHandoffAbort = errors.New("versionprimary: member changed since the group read")

// groupLocks serialises hand-offs and membership changes per group in this
// process, so two workers retiring members of one group cannot interleave
// read-decide-write. Group IDs hash onto the first groupStripes entries; the
// last one, noGroupStripe, is the "no group" sentinel: the lock every write
// that moves a book INTO a group from no group takes, and that a reader
// relying on a book staying ungrouped (itunes.regroup's apply on an ungrouped
// target) holds.
var groupLocks [groupStripes + 1]sync.Mutex

const (
	groupStripes  = 64
	noGroupStripe = groupStripes
)

// LockGroup takes group gid's hand-off lock and returns its release. A
// caller that reads the group to decide a member's flag and then writes it
// (a restore reading IncumbentExcept before YieldToIncumbent) holds it across
// both, so no hand-off can change the group in between.
//
// Lock order: the merge lock (merge.LockMergeRMW) before this one, and a
// book's write lock (ModifyBook) inside it, as EnsureSinglePrimary already
// does. It is not reentrant: release it before calling EnsureSinglePrimary
// or anything else that hands off a group's primary.
func LockGroup(gid string) (unlock func()) { return lockGroup(gid) }

func lockGroup(gid string) func() {
	mu := &groupLocks[groupStripe(gid)]
	mu.Lock()
	return mu.Unlock
}

// groupStripe is gid's index in groupLocks. gid is trimmed first, as
// groupOfBook trims, so " g" and "g" share a lock; "" (or all whitespace)
// means no group and maps to the sentinel stripe, noGroupStripe.
func groupStripe(gid string) int {
	gid = strings.TrimSpace(gid)
	if gid == "" {
		return noGroupStripe
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(gid))
	return int(h.Sum32() % groupStripes)
}

// LockGroups takes the hand-off locks of every gid -- each stripe once, in
// ascending stripe order -- and returns the release. "" is the no-group
// sentinel (always the last stripe), so a write that moves a book from no
// group into one passes both "" and the destination. Two groups can
// share a stripe, so a caller that needs more than one group (a write that
// moves a book from one version group to another) must take them through
// here, never by calling LockGroup twice: the second call could self-deadlock
// on the first's stripe, and two callers locking in different orders could
// deadlock each other.
//
// A write that changes a book's VersionGroupID holds the locks of the group
// it leaves and the group it joins across the write, so a reader that checks
// a group's membership and incumbent under LockGroup (itunes.regroup's
// apply-time recheck) sees no member leave or join until it releases. The
// same order rules as LockGroup apply: merge lock before, book write lock
// inside, and release before any hand-off (EnsureSinglePrimary, Crown).
func LockGroups(gids ...string) (unlock func()) {
	idx := make([]int, 0, len(gids))
	for _, g := range gids {
		idx = append(idx, groupStripe(g))
	}
	slices.Sort(idx)
	idx = slices.Compact(idx)
	for _, i := range idx {
		groupLocks[i].Lock()
	}
	return func() {
		for j := len(idx) - 1; j >= 0; j-- {
			groupLocks[idx[j]].Unlock()
		}
	}
}

// ErrMembershipChanged reports that a book's version group changed between
// the read a caller locked groups from and its write: the caller holds the
// wrong group locks and must not write.
var ErrMembershipChanged = errors.New("versionprimary: book changed version group since it was read")

// BookReader is the point read LockBookGroups uses.
type BookReader interface {
	GetBookByID(id string) (*database.Book, error)
}

// LockBookGroups takes, in ONE LockGroups acquisition, the locks of the
// current version group of every book in ids ("" = no group, the sentinel)
// plus every group in extra (the groups the caller will move books into).
// The books are read, their groups locked, and the books re-read under the
// locks; when one moved in between, the locks are dropped and the read
// repeated (at most five times, then ErrMembershipChanged). A missing book
// is skipped.
//
// It returns the release and each book's group as read under the locks: the
// raw stored value, untrimmed ("" = none), because callers write it back as a
// group id (SplitVersion creates the new member in it) and group ids are not
// normalised on write, so a trimmed copy would name a different group. The
// locks themselves are taken on the trimmed id (groupStripe), and
// CheckMembership trims both sides, so padding never changes which stripe is
// held or whether a membership check passes. A caller writing through
// ModifyBook checks the group there (CheckMembership); since every membership
// writer holds the group a book leaves, a book cannot move while its group is
// locked, so that check is a backstop.
//
// Order rules as LockGroups: merge lock before, book write locks inside,
// never another stripe or a hand-off while holding these.
func LockBookGroups(store BookReader, ids []string, extra ...string) (unlock func(), groups map[string]string, err error) {
	read := func() (map[string]string, error) {
		m := make(map[string]string, len(ids))
		for _, id := range ids {
			b, err := store.GetBookByID(id)
			if err != nil {
				return nil, fmt.Errorf("read book %s for its version group: %w", id, err)
			}
			if b == nil {
				continue
			}
			m[id] = rawGroupOfBook(b)
		}
		return m, nil
	}
	for attempt := 0; attempt < 5; attempt++ {
		before, err := read()
		if err != nil {
			return nil, nil, err
		}
		gids := append([]string(nil), extra...)
		for _, g := range before {
			gids = append(gids, g)
		}
		unlock := LockGroups(gids...)
		after, err := read()
		if err != nil {
			unlock()
			return nil, nil, err
		}
		if maps.Equal(before, after) {
			return unlock, after, nil
		}
		unlock()
	}
	return nil, nil, fmt.Errorf("lock version groups of %v: %w", ids, ErrMembershipChanged)
}

// rawGroupOfBook is b's version group exactly as stored; "" for none.
func rawGroupOfBook(b *database.Book) string {
	if b == nil || b.VersionGroupID == nil {
		return ""
	}
	return *b.VersionGroupID
}

// LockPlannedGroups is LockBookGroups for a caller that PLANNED its write
// from an earlier read of the books: expected maps each book whose plan
// depends on its group to the version group it read then ("" = none). It
// takes the locks of the current groups of every book in ids and in expected
// (a book in ids but not in expected is locked, not compared) plus extra,
// then compares each book's group as read
// under the locks with expected (trimmed). A book that moved between the
// planning read and the lock -- or no longer exists -- would have the caller
// write from a stale plan and leave a group it never locked or handed off,
// so on any mismatch the locks are released and ErrMembershipChanged is
// returned (wrapped with the book and both groups). The caller re-plans or
// fails; it never writes.
//
// It returns the release and the groups as LockBookGroups does. Order rules
// as LockGroups.
func LockPlannedGroups(store BookReader, ids []string, expected map[string]string, extra ...string) (unlock func(), groups map[string]string, err error) {
	planned := slices.Sorted(maps.Keys(expected))
	all := slices.Clone(ids)
	for _, id := range planned {
		if !slices.Contains(all, id) {
			all = append(all, id)
		}
	}
	unlock, groups, err = LockBookGroups(store, all, extra...)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range planned {
		got, ok := groups[id]
		want := strings.TrimSpace(expected[id])
		if !ok {
			// A planned book that is gone: the plan was made from a row
			// that no longer exists. (A book only in ids is skipped, as
			// LockBookGroups skips it.)
			unlock()
			return nil, nil, fmt.Errorf("book %s: planned in version group %q, now missing: %w", id, want, ErrMembershipChanged)
		}
		if strings.TrimSpace(got) != want {
			unlock()
			return nil, nil, fmt.Errorf("book %s: planned in version group %q, now in %q: %w", id, want, got, ErrMembershipChanged)
		}
	}
	return unlock, groups, nil
}

// groupOfBook is b's version group, trimmed; "" for none.
func groupOfBook(b *database.Book) string {
	if b == nil || b.VersionGroupID == nil {
		return ""
	}
	return strings.TrimSpace(*b.VersionGroupID)
}

// CheckMembership returns ErrMembershipChanged (wrapped with the book and
// both groups) when cur's version group is not lockedGID. A caller that took
// LockGroups from a pre-read calls it first inside its ModifyBook callback,
// under the book's write lock.
//
// Both sides are compared trimmed, as merge.RestoreFromTrash reads a group.
func CheckMembership(cur *database.Book, lockedGID string) error {
	g := groupOfBook(cur)
	if g != strings.TrimSpace(lockedGID) {
		return fmt.Errorf("book %s: read in version group %q, now in %q: %w", cur.ID, lockedGID, g, ErrMembershipChanged)
	}
	return nil
}

// StoreAlive is the alive answer Crown and EnsureSinglePrimary use for a
// merge survivor outside the group: live unless soft-deleted or gone, and
// live on a read error (so a loser is never made Electable by a failed read).
func StoreAlive(store EnsureStore) func(string) bool { return storeAlive(store) }

// storeAlive is the merge-target liveness check: a point read, with a read
// error counting as alive (the loser then stays ineligible).
func storeAlive(store EnsureStore) func(string) bool {
	return func(id string) bool {
		b, err := store.GetBookByID(id)
		if err != nil {
			return true
		}
		return b != nil && !b.IsSoftDeleted()
	}
}

// IneligibleReason returns "" when b, with signals s, may be crowned, and the
// ineligibility reason otherwise.
func IneligibleReason(b *database.Book, s Signals) string { return ineligibleReason(b, s) }

// EnsureSinglePrimary leaves group gid with exactly one live explicit
// primary when the rule allows one: see the file comment for why a healthy
// group keeps its incumbent. A held group is logged and left as it is. An
// empty gid is a no-op.
func EnsureSinglePrimary(ctx context.Context, store EnsureStore, gid string, env Env) (HandoffResult, error) {
	res := HandoffResult{GroupID: gid, Outcome: OutcomeEmpty}
	if strings.TrimSpace(gid) == "" {
		if env.Expect != "" {
			return res, fmt.Errorf("%w: no version group, not the expected %s", ErrUnexpectedWinner, env.Expect)
		}
		return res, nil
	}
	defer lockGroup(gid)()

	members, err := store.GetBooksByVersionGroup(gid)
	if err != nil {
		return res, fmt.Errorf("read version group %s: %w", gid, err)
	}
	if len(members) == 0 {
		if env.Expect != "" {
			// The caller predicted a primary; an empty group has none to
			// leave, so the prediction is wrong and the caller hears it.
			return res, fmt.Errorf("%w: group %s has no members, not the expected %s", ErrUnexpectedWinner, gid, env.Expect)
		}
		return res, nil
	}
	alive := storeAlive(store)
	inc, d, err := decideSingle(ctx, store, members, env, alive)
	if err != nil {
		return res, err
	}
	if env.Expect != "" {
		got := ""
		switch {
		case inc != nil:
			got = inc.ID
		case d.Kind != DecisionHeld:
			got = d.WinnerID
		}
		if got != env.Expect {
			res.Outcome, res.Decision = OutcomeWinnerChanged, &d
			if got == "" {
				return res, fmt.Errorf("%w: group %s would be held (%s), not the expected %s", ErrUnexpectedWinner, gid, d.HoldReason, env.Expect)
			}
			return res, fmt.Errorf("%w: group %s would make %s primary, not the expected %s", ErrUnexpectedWinner, gid, got, env.Expect)
		}
	}
	keep, crown := "", false
	switch {
	case inc != nil:
		keep = inc.ID
	case d.Kind != DecisionHeld:
		keep, crown = d.WinnerID, true
	}
	var plan writePlan
	if keep != "" {
		plan = planWrites(members, keep, crown, alive)
		if err := plan.guard(gid, env.MayWrite); err != nil {
			if errors.Is(err, ErrWriteRefused) {
				res.Outcome = OutcomeWriteRefused
			}
			if inc == nil {
				res.Decision = &d
			}
			return res, err
		}
	}
	if inc != nil {
		res.Outcome, res.PrimaryID = OutcomeHealthy, inc.ID
		res.Writes, err = plan.demote(store, gid)
		return res, err
	}
	res.Decision = &d
	if d.Kind == DecisionHeld {
		res.Outcome = OutcomeHeld
		if strings.TrimSpace(env.RootDir) == "" {
			ensureLog.Warn("version group %s held: no library root configured, so no member is eligible",
				logger.SanitizeLogValue(gid))
		} else {
			ensureLog.Info("version group %s held (%s): %s; left for version-group-primary-repair",
				logger.SanitizeLogValue(gid), d.HoldReason, d.Reason)
		}
		return res, nil
	}
	return plan.write(store, gid, OutcomeElected, res)
}

// GuardPlanned is EnsureSinglePrimary's decision and guard over a
// HYPOTHETICAL membership of group gid, with no lock and no writes: a caller
// about to link books into gid passes the members the group will have, and
// learns before writing anything whether the hand-off that follows would be
// refused (env.MayWrite about exactly the members it would write). held
// reports a group the rule would hold, which writes nothing. The real
// hand-off still guards under the group lock: the group can change between
// this check and that one.
func GuardPlanned(ctx context.Context, store EnsureStore, gid string, members []database.Book, env Env) (held bool, err error) {
	if len(members) == 0 {
		return false, nil
	}
	alive := storeAlive(store)
	inc, d, err := decideSingle(ctx, store, members, env, alive)
	if err != nil {
		return false, err
	}
	keep, crown := "", false
	switch {
	case inc != nil:
		keep = inc.ID
	case d.Kind != DecisionHeld:
		keep, crown = d.WinnerID, true
	default:
		return true, nil
	}
	return false, planWrites(members, keep, crown, alive).guard(gid, env.MayWrite)
}

// GuardPlannedCrown is GuardPlanned for CrownEnv(gid, keepID): env.MayWrite
// is asked about exactly the members that crown would write over the
// hypothetical membership members. No lock, no writes.
func GuardPlannedCrown(store EnsureStore, gid string, members []database.Book, keepID string, env Env) error {
	return planWrites(members, keepID, true, storeAlive(store)).guard(gid, env.MayWrite)
}

// decideSingle is EnsureSinglePrimary's decision, with no writes: the
// healthy incumbent it keeps (one live explicit primary that is eligible),
// else the election over every member.
func decideSingle(ctx context.Context, store EnsureStore, members []database.Book, env Env, alive func(string) bool) (*database.Book, Decision, error) {
	loader := Loader{Files: store, Chapters: store, RootDir: env.RootDir, Probe: env.Probe, Stat: env.Stat}
	var incumbents []*database.Book
	for i := range members {
		m := &members[i]
		if Electable(m, alive) && explicitTrue(m) {
			incumbents = append(incumbents, m)
		}
	}
	if len(incumbents) == 1 {
		inc := incumbents[0]
		s, err := loader.Load(ctx, inc, true)
		if err != nil {
			return nil, Decision{}, err
		}
		if IneligibleReason(inc, s) == "" {
			return inc, Decision{}, nil
		}
	}
	ms, err := loader.LoadMembers(ctx, members, alive)
	if err != nil {
		return nil, Decision{}, err
	}
	return nil, Elect(ms), nil
}

// ChooseSinglePrimary is the member EnsureSinglePrimary would leave as the
// primary of a group whose rows are members, or "" when it would hold the
// group. Read-only: the caller passes the rows as it wants them judged (a
// replay of a hand-off passes them as they stood before it), and nothing is
// written or locked. The disk access is Loader's stat, plus a probe when
// Env.Probe is set.
func ChooseSinglePrimary(ctx context.Context, store EnsureStore, members []database.Book, env Env) (string, error) {
	inc, d, err := decideSingle(ctx, store, members, env, storeAlive(store))
	if err != nil {
		return "", err
	}
	if inc != nil {
		return inc.ID, nil
	}
	if d.Kind == DecisionHeld {
		return "", nil
	}
	return d.WinnerID, nil
}

// Crown makes keepID the explicit primary of group gid and every other live
// member explicit false, with no election: for a caller whose choice is
// deliberate (an undo, a user edit). keepID must be a live member.
func Crown(store EnsureStore, gid, keepID string) (HandoffResult, error) {
	return CrownEnv(store, gid, keepID, Env{})
}

// CrownEnv is Crown honouring env.MayWrite (the other Env fields are not
// read): every member Crown would write -- keepID unless already explicit
// true, and every member it would demote -- is asked under the group lock
// before any write, and one refusal writes nothing and returns
// ErrWriteRefused (outcome OutcomeWriteRefused). The op revert passes its
// never-write-an-iTunes-book rule here.
func CrownEnv(store EnsureStore, gid, keepID string, env Env) (HandoffResult, error) {
	res := HandoffResult{GroupID: gid, Outcome: OutcomeEmpty}
	if strings.TrimSpace(gid) == "" {
		return res, nil
	}
	defer lockGroup(gid)()
	members, err := store.GetBooksByVersionGroup(gid)
	if err != nil {
		return res, fmt.Errorf("read version group %s: %w", gid, err)
	}
	alive := storeAlive(store)
	found := false
	for i := range members {
		if members[i].ID == keepID && Electable(&members[i], alive) {
			found = true
		}
	}
	if !found {
		res.Outcome = OutcomeCrownNotMember
		return res, nil
	}
	plan := planWrites(members, keepID, true, alive)
	if err := plan.guard(gid, env.MayWrite); err != nil {
		if errors.Is(err, ErrWriteRefused) {
			res.Outcome = OutcomeWriteRefused
		}
		return res, err
	}
	return plan.write(store, gid, OutcomeCrowned, res)
}

// writePlan is the exact set of is_primary_version writes one hand-off
// makes, decided ONCE from the members read under the group lock: guard asks
// Env.MayWrite about exactly this set, and write/demote write exactly this
// set, so no member is ever written that the guard was not asked about.
type writePlan struct {
	keep *database.Book
	// winner: keep's explicit true is written (crown, and keep's read flag
	// is not explicit true).
	winner bool
	// demotes: every member other than keep that was Electable at the read
	// and not explicit false, in members order.
	demotes []*database.Book
}

// planWrites decides the write set of a hand-off that keeps keepID: its
// winner write when crown and keepID's read flag is not explicit true, and
// every member demoted (Electable at the read, by alive evaluated here once,
// and not read explicit false).
func planWrites(members []database.Book, keepID string, crown bool, alive func(string) bool) writePlan {
	var p writePlan
	for i := range members {
		m := &members[i]
		if m.ID == keepID {
			p.keep = m
			p.winner = crown && !explicitTrue(m)
			continue
		}
		if !Electable(m, alive) || (m.IsPrimaryVersion != nil && !*m.IsPrimaryVersion) {
			continue
		}
		p.demotes = append(p.demotes, m)
	}
	return p
}

// guard asks mayWrite (nil allows all) about every member of the plan,
// before any write. A refusal (wrapping ErrWriteRefused) is returned
// wrapped with the group and member; any other error is returned as a
// failure, without ErrWriteRefused.
func (p writePlan) guard(gid string, mayWrite func(*database.Book) error) error {
	if mayWrite == nil {
		return nil
	}
	ask := func(m *database.Book) error {
		err := mayWrite(m)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, ErrWriteRefused):
			return fmt.Errorf("group %s: member %s: %w", gid, m.ID, err)
		default:
			return fmt.Errorf("group %s: check member %s before writing its primary flag: %w", gid, m.ID, err)
		}
	}
	if p.winner && p.keep != nil {
		if err := ask(p.keep); err != nil {
			return err
		}
	}
	for _, m := range p.demotes {
		if err := ask(m); err != nil {
			return err
		}
	}
	return nil
}

// write makes the plan's winner write, then its demotes. The winner's fresh
// row must still be what was planned from: live, in the group, with the
// merge pointer read, and -- since only a planned write was guarded -- its
// flag as read when the plan writes it, or still explicit true when the
// plan does not (keep was already primary). Anything else aborts with
// nothing written (OutcomeWinnerChanged).
func (p writePlan) write(store EnsureStore, gid, outcome string, res HandoffResult) (HandoffResult, error) {
	winnerID := p.keep.ID
	prev, wrote := "", false
	written, err := store.ModifyBook(winnerID, func(b *database.Book) error {
		wrote = false
		if b.IsSoftDeleted() || b.VersionGroupID == nil || *b.VersionGroupID != gid ||
			derefStr(b.MergedIntoBookID) != derefStr(p.keep.MergedIntoBookID) {
			return errHandoffAbort
		}
		if !p.winner {
			if explicitTrue(b) {
				return database.ErrSkipBookWrite
			}
			return errHandoffAbort // lost the true it was planned with; this write was never guarded
		}
		if storedFlag(b.IsPrimaryVersion) != storedFlag(p.keep.IsPrimaryVersion) {
			return errHandoffAbort
		}
		prev = storedFlag(b.IsPrimaryVersion)
		t := true
		b.IsPrimaryVersion = &t
		wrote = true
		return nil
	})
	switch {
	case errors.Is(err, errHandoffAbort) || (err == nil && written == nil): // changed, or gone
		res.Outcome = OutcomeWinnerChanged
		ensureLog.Info("version group %s: winner %s changed before the write; nothing written",
			logger.SanitizeLogValue(gid), logger.SanitizeLogValue(winnerID))
		return res, nil
	case err != nil:
		return res, fmt.Errorf("write primary %s: %w", winnerID, err)
	}
	res.Outcome, res.PrimaryID = outcome, winnerID
	if wrote {
		res.Writes = append(res.Writes, FlagWrite{BookID: winnerID, Previous: prev, Primary: true})
	}
	demoted, err := p.demote(store, gid)
	res.Writes = append(res.Writes, demoted...)
	return res, err
}

// demote writes explicit false on every planned demote, and on no other
// member. A planned member whose fresh row left the group, was soft-deleted,
// changed its merge pointer, or is explicit false by now is skipped.
func (p writePlan) demote(store EnsureStore, gid string) ([]FlagWrite, error) {
	var writes []FlagWrite
	for _, m := range p.demotes {
		prev, wrote := "", false
		if _, err := store.ModifyBook(m.ID, func(b *database.Book) error {
			wrote = false
			if b.IsSoftDeleted() || b.VersionGroupID == nil || *b.VersionGroupID != gid ||
				derefStr(b.MergedIntoBookID) != derefStr(m.MergedIntoBookID) ||
				(b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion) {
				return database.ErrSkipBookWrite
			}
			prev = storedFlag(b.IsPrimaryVersion)
			f := false
			b.IsPrimaryVersion = &f
			wrote = true
			return nil
		}); err != nil {
			return writes, fmt.Errorf("demote %s: %w", m.ID, err)
		}
		if wrote {
			writes = append(writes, FlagWrite{BookID: m.ID, Previous: prev, Primary: false})
		}
	}
	return writes, nil
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
