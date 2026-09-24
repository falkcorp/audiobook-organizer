// file: internal/versionprimary/ensure.go
// version: 1.0.0
// guid: 0b7e4c52-9a1d-4f38-8c6e-2d51f0a7b9e3
// last-edited: 2026-09-24

package versionprimary

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
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
}

// Hand-off outcomes.
const (
	OutcomeEmpty          = "empty"            // no rows in the group
	OutcomeHealthy        = "healthy"          // one live eligible explicit primary; only siblings demoted, if any
	OutcomeElected        = "elected"          // Elect ran and its winner was written
	OutcomeHeld           = "held"             // Elect held the group; nothing written
	OutcomeCrowned        = "crowned"          // Crown wrote its explicit choice
	OutcomeWinnerChanged  = "winner_changed"   // the winner changed under us; nothing written
	OutcomeCrownNotMember = "crown_not_member" // Crown's book is not a live member of the group
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

// errHandoffAbort aborts a winner write whose row changed since the read.
var errHandoffAbort = errors.New("versionprimary: member changed since the group read")

// groupLocks serialises hand-offs per group in this process, so two workers
// retiring members of one group cannot interleave read-decide-write. It is
// the innermost lock: nothing else is taken while it is held.
var groupLocks [64]sync.Mutex

func lockGroup(gid string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(gid))
	mu := &groupLocks[h.Sum32()%uint32(len(groupLocks))]
	mu.Lock()
	return mu.Unlock
}

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
		return res, nil
	}
	defer lockGroup(gid)()

	members, err := store.GetBooksByVersionGroup(gid)
	if err != nil {
		return res, fmt.Errorf("read version group %s: %w", gid, err)
	}
	if len(members) == 0 {
		return res, nil
	}
	alive := storeAlive(store)
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
		s, lerr := loader.Load(ctx, inc, true)
		if lerr != nil {
			return res, lerr
		}
		if IneligibleReason(inc, s) == "" {
			res.Outcome, res.PrimaryID = OutcomeHealthy, inc.ID
			res.Writes, err = demoteOthers(store, gid, members, inc.ID, alive)
			return res, err
		}
	}

	ms, err := loader.LoadMembers(ctx, members, alive)
	if err != nil {
		return res, err
	}
	d := Elect(ms)
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
	return writeWinner(store, gid, members, d.WinnerID, alive, OutcomeElected, res)
}

// Crown makes keepID the explicit primary of group gid and every other live
// member explicit false, with no election: for a caller whose choice is
// deliberate (an undo, a user edit). keepID must be a live member.
func Crown(store EnsureStore, gid, keepID string) (HandoffResult, error) {
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
	return writeWinner(store, gid, members, keepID, alive, OutcomeCrowned, res)
}

// writeWinner writes explicit true on winnerID, then explicit false on every
// other live member. When the winner write aborts, no demotion is written.
func writeWinner(store EnsureStore, gid string, members []database.Book, winnerID string,
	alive func(string) bool, outcome string, res HandoffResult) (HandoffResult, error) {
	var observedMerge *string
	for i := range members {
		if members[i].ID == winnerID {
			observedMerge = members[i].MergedIntoBookID
		}
	}
	prev, wrote := "", false
	written, err := store.ModifyBook(winnerID, func(b *database.Book) error {
		wrote = false
		if b.IsSoftDeleted() || b.VersionGroupID == nil || *b.VersionGroupID != gid ||
			derefStr(b.MergedIntoBookID) != derefStr(observedMerge) {
			return errHandoffAbort
		}
		if explicitTrue(b) {
			return database.ErrSkipBookWrite
		}
		prev = storedFlag(b.IsPrimaryVersion)
		t := true
		b.IsPrimaryVersion = &t
		wrote = true
		return nil
	})
	switch {
	case errors.Is(err, errHandoffAbort) || (err == nil && written == nil):
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
	demoted, err := demoteOthers(store, gid, members, winnerID, alive)
	res.Writes = append(res.Writes, demoted...)
	return res, err
}

// demoteOthers writes explicit false on every live member other than keepID
// that is not already explicit false. A member that left the group since the
// read is skipped.
func demoteOthers(store EnsureStore, gid string, members []database.Book, keepID string,
	alive func(string) bool) ([]FlagWrite, error) {
	var writes []FlagWrite
	for i := range members {
		m := &members[i]
		if m.ID == keepID || !Electable(m, alive) {
			continue
		}
		if m.IsPrimaryVersion != nil && !*m.IsPrimaryVersion {
			continue
		}
		prev, wrote := "", false
		if _, err := store.ModifyBook(m.ID, func(b *database.Book) error {
			wrote = false
			if b.VersionGroupID == nil || *b.VersionGroupID != gid ||
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
