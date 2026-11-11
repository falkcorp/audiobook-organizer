// file: core/kernel/restore_reapply.go
// version: 1.0.0
// guid: 2f6d8a14-c3e7-4b95-a018-9d4e7b1c5f36
// last-edited: 2026-11-11

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// This file is 06 P4d step 2: the journal move, the install check, the
// classifier, the tail re-apply and the bookkeeping lines (05-backup §5B.3.6
// row "Boot re-apply" (0)-(5)). It is dark, like the other F4-11 kernel files:
// nothing in boot.go, kernel.go or main.go calls it, so a boot is exactly what
// it was. F4-08 wires it after secrets.Open (the keyring is open) and the DB
// overrides (P4d step 1), before auth.BootWrites and any listener; the function
// refuses to run without both, which is what TestPostDAGReconcilerOrder reads.
// Its inputs are explicit, so the tests drive it over a store and a state
// directory built in the test, with no booted Kernel.
//
// The tail re-apply is ordered and sequential on purpose: the journal's order
// is the order the revocations were made in, a later line may depend on an
// earlier one (a delete after a disable), and the batches also advance the
// store's head, which must not pass a line not yet applied. It is not a
// full-library loop: the tail is the lines since the store's last committed
// head, normally none.

// errReapplyNotReady is a re-apply called before its prerequisites exist.
var errReapplyNotReady = errors.New("kernel: the journal re-apply needs the DB overrides attached and the keyring open")

// storeClass is what the classifier decided about the store this boot serves.
type storeClass string

const (
	// classSame is the store the journal and the location file describe (or one
	// no file describes yet: the first journaling boot, or another store's
	// file): a lagging head re-applies the tail and runs no hook.
	classSame storeClass = "same"
	// classRelocated is the current store at a new generation or directory
	// identity (a cross-device move, an rsync to a new disk) with no journal
	// evidence it was replaced: the new identity is recorded, no hook runs.
	classRelocated storeClass = "relocated"
	// classReplaced is a store replaced outside the staged and offline restore
	// (a file copy of an old store, a hand-copied checkpoint): the tail is
	// re-applied and the hooks run with KindExternal and no coverage.
	classReplaced storeClass = "replaced"
	// classRestoreBoot is a P0a restore boot, or a store a HooksOwed restore
	// journal names: none of the replacement evidence applies (R6-4).
	classRestoreBoot storeClass = "restore_boot"
)

// restoreJournal is the restore journal's edges a restore boot walks
// (restoreswap.Swapper's RestoreLine, HooksDone, MarkerWritten and
// HooksApplied).
type restoreJournal interface {
	RestoreLine(ctx context.Context, id string, seq uint64) error
	HooksDone(ctx context.Context, id string) error
	MarkerWritten(ctx context.Context, id string) error
	HooksApplied(ctx context.Context, id string) error
}

// restoreBoot is what the kernel knows of the restore this boot applies, from
// the P0a outcome (restoreswap.Outcome) and the intent. Nil on a plain boot.
type restoreBoot struct {
	ID        string
	Kind      journal.RestoreKind // KindArchive, KindRevert or KindCheckpoint
	ArchiveID string              // "" for revert and checkpoint
	// Foreign is an archive of another install or a host-loss restore: no
	// coverage (§5B.3.6 "Coverage at restore").
	Foreign bool
	// SourceSeq is the journal seq the restore's source had seen: the
	// manifest's for an archive, the checkpoint record's for a checkpoint, the
	// `staged` record's Seq for a revert (DATA2-25). Boundary = min(SourceSeq,
	// the swapped-in store's journal_head).
	SourceSeq      uint64
	AuditHighWater uint64
	// PreSwapDir is the pre-swap store's directory (restore/<id>/previous/...),
	// "" when there was no live store (host loss).
	PreSwapDir string
	// Owed is a restore a build without P4d steps 2-3 applied with HooksOwed:
	// its journal already reads applied, so no edge of it is walked, but the
	// hooks run now with the source's boundary.
	Owed    bool
	Records []restorejournal.Record
	Steps   restoreJournal // nil when Owed
}

// reapplyApply is how the re-apply reaches auth's and rbac's stores: closures
// the kernel adapter binds to their P4c handles.
type reapplyApply struct {
	// NewBatch is an indexed batch over auth's store (authstore.Store.NewBatch).
	NewBatch func() *storage.Batch
	// Auth stages the revocations into b (auth.StageReapply).
	Auth func(ctx context.Context, b *storage.Batch, lines []journal.Revocation) error
	// RBAC stages the role revocations into b and returns the users a line
	// would have left without an active core:admin holder (rbac.StageReapply:
	// that line is skipped, KeptAdmins).
	RBAC func(ctx context.Context, b *storage.Batch, lines []journal.Revocation) (kept []string, err error)
	// RestrictLastAdmin restricts a kept admin after the batch committed
	// (auth.RestrictLastAdmin).
	RestrictLastAdmin func(ctx context.Context, userID string) error
	// FailClosed is auth's restore fallback with no coverage and no pre-swap
	// store (auth.PostRestore): every key needs_confirmation, every credential
	// epoch bumped, every session deleted.
	FailClosed func(ctx context.Context) (map[string]int, error)
}

// reapplyInput is everything reapplyRevocations reads.
type reapplyInput struct {
	Journal *journal.Component
	KS      storage.Keyspace // c/backup/
	Keys    journal.Keys     // the keyring opened at 05-storage §5S.4 step 8; required
	// Overrides is true once P4d step 1 attached the DB overrides, so
	// BackupDir and JournalPath are the effective ones; required.
	Overrides bool
	Install   string // c/storage/install
	App       string // the app's archive prefix, for the header of a journal the boot starts
	Identity  storage.StoreIdentity
	DBPath    string
	StateDir  string
	BackupDir string // the effective core.backup.dir
	// JournalPath is <effective backup dir>/<prefix>revocations.journal.
	JournalPath string
	Restore     *restoreBoot
	// SwappedBack reports whether the restore journal id reads swapped_back
	// (restoreswap.Swapper.Journal): such a restore's line is neither resumed
	// nor evidence.
	SwappedBack func(restoreID string) bool
	Apply       reapplyApply
	Clock       func() time.Time
	Log         *slog.Logger
}

func (in reapplyInput) now() time.Time {
	if in.Clock != nil {
		return in.Clock().UTC()
	}
	return time.Now().UTC()
}

func (in reapplyInput) log() *slog.Logger {
	if in.Log != nil {
		return in.Log
	}
	return slog.Default()
}

func (in reapplyInput) ready() error {
	switch {
	case in.Keys == nil, !in.Overrides:
		return errReapplyNotReady
	case in.Journal == nil, in.Install == "", in.DBPath == "", in.StateDir == "", in.JournalPath == "":
		return errors.New("kernel: the journal re-apply is missing its inputs")
	case in.Apply.NewBatch == nil || in.Apply.Auth == nil || in.Apply.RBAC == nil || in.Apply.RestrictLastAdmin == nil || in.Apply.FailClosed == nil:
		return errors.New("kernel: the journal re-apply is missing its appliers")
	}
	return nil
}

// reapplyResult is what step 2 did and step 3 needs.
type reapplyResult struct {
	Class      storeClass
	Why        string // the classifier's reason, for the log and the tests
	Path       string // the journal file in use
	BackupDir  string // the directory the journal sits in: what backup.location.json records
	Moved      string // the old journal file after a move, removed once the record points at the new one
	HeadBefore journal.Head
	// Chain is the journal as it was read before this boot appended anything
	// (empty after a reset or when there was no file).
	Chain     *journal.Chain
	Reapplied int            // revocation lines re-applied
	Counts    map[string]int // auth's counts of a fail-closed fallback
	// RestoreLine is this boot's restore line (a restore boot's, or a replaced
	// store's external line); RestoreBoundary the boundary it records.
	RestoreLine     uint64
	RestoreBoundary uint64
	// Owed are the external restore lines whose hooks have not completed: the
	// one this boot appended for a replaced store, or one an earlier boot
	// appended and crashed before its hooks finished (never duplicated).
	Owed         []journal.Line
	FailedClosed bool
	Broken       error // why the journal is unusable, nil when it is usable
	// Reset is true when another install's journal was set aside and a new one
	// started: nothing was re-applied from it.
	Reset bool
	// Deferred is true when this boot wrote no served_boot and no location file
	// at the end of step 2: a restore boot's are step 3's, after its hooks, and
	// so are those of a replaced store whose restore line could not be recorded
	// (a crash before the hooks finish would otherwise read as the same store
	// next boot, with nothing left to resume the hooks from).
	Deferred bool
}

// reapplyRevocations is P4d step 2.
func reapplyRevocations(ctx context.Context, in reapplyInput) (res reapplyResult, err error) {
	if err := in.ready(); err != nil {
		return res, err
	}
	log := in.log()
	restoring := in.Restore != nil
	// A restore boot, and a HooksOwed run, hold the anchor until their hooks
	// commit (§5B.3.6 row "Head in the store").
	in.Journal.SetFrozen(restoring)

	loc, haveLoc, locErr := journal.ReadLocation(in.StateDir)
	if locErr != nil {
		// A missing file is "no marker" (row "Where"); one that exists and
		// cannot be read or decoded is not: it may have held the evidence that
		// this store was replaced, so the classifier reads it as that.
		log.Error("kernel: backup.location.json is unreadable", "err", locErr)
		haveLoc = false
	}
	describes := haveLoc && loc.Describes(in.DBPath, in.Install)

	// (1) head before: read before anything is appended this boot.
	headBefore, err := journal.ReadHead(ctx, in.KS)
	if err != nil {
		return res, fmt.Errorf("kernel: read journal_head: %w", err)
	}
	pair, err := journal.ReadServedBoot(ctx, in.KS)
	if err != nil {
		return res, fmt.Errorf("kernel: read served_boot: %w", err)
	}
	res.HeadBefore, res.Chain = headBefore, &journal.Chain{}

	res.Path = in.settleLocation(loc, describes, &res)

	// (0) install check, before any line is used.
	chain := in.readChain(res.Path, headBefore)
	if chain.HasHeader && chain.Header.InstallID != in.Install {
		if res.Reset, err = in.setAside(ctx, res.Path, headBefore); err != nil {
			log.Error("kernel: another install's revocation journal could not be set aside", "path", res.Path, "err", err)
		} else {
			chain = &journal.Chain{}
		}
	}
	res.Chain = chain

	// Bind opens the journal for appends. Its failure says the boot cannot
	// write lines; it says nothing about the lines already verified, which the
	// re-apply (it writes to the stores, never to the journal) still uses.
	bindErr := in.Journal.Bind(ctx, res.Path)
	if bindErr != nil {
		log.Warn("kernel: the revocation journal is not usable for appends", "path", res.Path, "err", bindErr)
	}

	// (2) the classifier: its evidence is the store's identity and content and
	// the journal's verified lines, never head lag alone.
	aborted := chain.AbortedRestores(in.SwappedBack)
	res.Class, res.Why = classifyStore(classifyInput{RestoreBoot: restoring, Loc: loc, Describes: describes, LocUnreadable: locErr != nil,
		StorePair: pair, Head: headBefore, Identity: in.Identity, Chain: chain, Aborted: aborted})
	log.Info("kernel: classified the store", "class", string(res.Class), "why", res.Why)

	untrusted := in.untrusted(chain, headBefore, &res)
	switch {
	case res.Reset && bindErr == nil:
		// A new journal starts with a gap line, and nothing is re-applied from
		// the other install's (DATA4-15); the restore hooks, if any, get no
		// coverage because the journal has no header of this install's.
		if _, err := in.Journal.AppendLine(ctx, journal.GapLine(in.now(), journal.GapOtherInstall)); err != nil {
			return res, fmt.Errorf("kernel: start the new journal with a gap line: %w", err)
		}
		in.Journal.NoteReset()
	case untrusted:
		// Nothing is re-applied from a chain that does not verify, but a store
		// that may lack what it held fails closed.
		if err := in.failClosedIfBehind(ctx, chain, headBefore, restoring, &res); err != nil {
			return res, err
		}
	default:
		// (3) re-apply the verified tail after head_before, narrowing and
		// idempotent. A bind failure does not stop it.
		if res.Reapplied, err = in.reapplyTail(ctx, chain, headBefore, aborted); err != nil {
			return res, err
		}
	}
	if res.Broken == nil {
		res.Broken = bindErr
	}
	if res.Broken != nil {
		in.Journal.NoteBroken(res.Broken)
	}

	// (4) and (5): the restore line (a restore boot's, a replaced store's) and
	// the unfinished hooks of an earlier boot's.
	if err := in.bookkeeping(ctx, chain, aborted, &res); err != nil {
		return res, err
	}

	res.Deferred = restoring || (res.Class == classReplaced && res.RestoreLine == 0)
	if res.Deferred {
		return res, nil // the hooks, then served_boot and the location file, are step 3's
	}
	return res, in.writeMarkers(ctx, &res)
}

// settleLocation picks the journal file and moves it when the effective
// backup directory changed (§5B.3.6 row "Where"). A restore boot never moves
// it: while a restore journal is between swapped and applied the journal stays
// at the recorded location, so a live directory change can never split it, and
// the first boot after applied moves it. A move that fails verification keeps
// the old file and the boot reports revocation_journal_broken.
func (in reapplyInput) settleLocation(loc journal.Location, describes bool, res *reapplyResult) string {
	recorded := ""
	if describes {
		recorded = loc.Journal // another store's record says where its journal is, not ours
	}
	switch {
	case recorded == "" || recorded == in.JournalPath:
		return in.JournalPath
	case in.Restore != nil && !in.Restore.Owed:
		return recorded
	}
	if err := journal.MoveFile(recorded, in.JournalPath, in.Keys); err != nil {
		in.log().Error("kernel: the revocation journal was not moved to the new backup directory", "from", recorded, "to", in.JournalPath, "err", err)
		in.Journal.NoteBroken(err)
		return recorded
	}
	res.Moved = recorded
	return in.JournalPath
}

// readChain reads and verifies the journal at path. A missing file is an empty
// chain (a store with no journal yet). A file that exists and cannot be read is
// not: it is a broken journal of unknown length, so the caller fails closed on
// it the way it does on a chain that does not verify, and never continues as
// if the journal were empty.
func (in reapplyInput) readChain(path string, head journal.Head) *journal.Chain {
	c, err := journal.ReadFile(path, in.Keys)
	switch {
	case err == nil:
		return c
	case errors.Is(err, os.ErrNotExist):
		return &journal.Chain{}
	}
	in.log().Error("kernel: the revocation journal could not be read", "path", path, "err", err)
	return &journal.Chain{
		Break:      &journal.Break{Seq: head.Seq + 1, Reason: breakUnreadable, Err: err},
		ClaimedEnd: math.MaxUint64, // its length is unknown, so a store at any head may be behind it
	}
}

// breakUnreadable is the Break reason of a journal file that exists and cannot
// be read.
const breakUnreadable journal.BreakReason = "unreadable"

// setAside renames another install's journal aside and starts a new one that
// continues after the store's head (journal.CreateBased), so the store's head
// still lies on its chain. It returns true when the new journal exists.
func (in reapplyInput) setAside(ctx context.Context, path string, head journal.Head) (bool, error) {
	aside := path + ".other-install-" + in.now().Format("20060102T150405Z")
	for n := 2; ; n++ {
		if _, err := os.Lstat(aside); errors.Is(err, os.ErrNotExist) {
			break
		}
		aside = fmt.Sprintf("%s.other-install-%s-%d", path, in.now().Format("20060102T150405Z"), n)
	}
	if err := os.Rename(path, aside); err != nil {
		return false, fmt.Errorf("kernel: set the other install's journal aside: %w", err)
	}
	if err := syncDirOf(path); err != nil {
		return false, err
	}
	hdr := journal.Header{InstallID: in.Install, App: in.App, StartedAt: in.now()}
	if err := journal.CreateBased(path, in.Keys, hdr, head); err != nil {
		return false, fmt.Errorf("kernel: start the new journal: %w", err)
	}
	return true, nil
}

func syncDirOf(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// untrusted decides whether the chain cannot be re-applied from, and records in
// res.Broken why: it does not verify (Break), it is another install's that
// could not be set aside, it verifies but is not the one the store committed
// (forked), or it starts above the store's head (pruned: the lines between are
// gone). It also records, without refusing the chain, a journal that ends below
// the store's head (deleted, emptied or truncated): its lines are good and there
// is nothing after the head to apply, but it is reported broken. A bind failure
// is not a reason here (the caller reports it); the verified lines do not
// depend on it.
func (in reapplyInput) untrusted(chain *journal.Chain, head journal.Head, res *reapplyResult) bool {
	switch {
	case chain.Break != nil:
		res.Broken = chain.Break
	case otherInstall(chain, in.Install):
		res.Broken = fmt.Errorf("the journal names install %q, the store's is %q, and it could not be set aside", chain.Header.InstallID, in.Install)
	case forkedAt(chain, head):
		res.Broken = fmt.Errorf("the journal does not hold the line the store committed at seq %d (the file was replaced)", head.Seq)
	case chain.HasHeader && head.Seq < chain.Header.BaseSeq:
		res.Broken = fmt.Errorf("the journal starts after seq %d, above the store's head %d: the lines between are gone", chain.Header.BaseSeq, head.Seq)
	default:
		if !res.Reset && head.Seq > chain.IntactEnd() {
			res.Broken = fmt.Errorf("the journal ends at seq %d, below the store's head %d (the file was deleted, emptied or truncated)", chain.IntactEnd(), head.Seq)
		}
		return false
	}
	return true
}

// otherInstall reports a verified header that names another install.
func otherInstall(chain *journal.Chain, install string) bool {
	return chain.HasHeader && chain.Header.InstallID != install
}

// forkedAt reports a journal that verifies but is not the one the store
// committed: the line at the store's head differs.
func forkedAt(chain *journal.Chain, head journal.Head) bool {
	if head.MAC == "" || chain.Break != nil {
		return false
	}
	mac, ok := chain.MACAt(head.Seq)
	return ok && mac != head.MAC
}

// failClosedIfBehind reacts to a chain that cannot be re-applied from. When the
// store may lack a revocation the journal holds or held, the boot cannot know
// what it skipped, so it fails closed through auth's restore fallback (every
// API key needs_confirmation, every credential epoch bumped) and reports
// revocation_journal_broken. "May lack" is every way the store can be behind or
// unrelated to the journal: the chain's end (for a chain that does not verify,
// everything it claims) is above the store's head, the file is not the one the
// store committed or starts above its head, or it is another install's while the
// store has a head (its own journal is gone). Another install's journal never
// answers "is the store behind it": its seqs are not this store's, so a store
// that has never journaled (head 0) has lost nothing to it, however many lines
// it holds. It happens once per broken state (journal.Chain.Fingerprint), so a
// restart does not bump every epoch again. On a restore boot the hooks do their
// own fallback with no coverage.
func (in reapplyInput) failClosedIfBehind(ctx context.Context, chain *journal.Chain, head journal.Head, restoring bool, res *reapplyResult) error {
	foreign := otherInstall(chain, in.Install)
	forked := forkedAt(chain, head) || (foreign && head.Seq > 0)
	pruned := chain.Break == nil && chain.HasHeader && head.Seq < chain.Header.BaseSeq
	behind := !foreign && chain.Behind(head.Seq)
	if restoring || !(forked || pruned || behind) {
		return nil
	}
	fp := chain.Fingerprint(head, forked)
	latched, err := journal.ReadBrokenLatch(ctx, in.KS)
	if err != nil {
		return fmt.Errorf("kernel: read the broken-journal latch: %w", err)
	}
	if latched == fp {
		in.log().Warn("kernel: the revocation journal is broken and this state already failed closed", "err", res.Broken)
		return nil
	}
	in.log().Error("kernel: the revocation journal is broken below the store's head: failing closed", "err", res.Broken)
	counts, err := in.Apply.FailClosed(ctx)
	if err != nil {
		return fmt.Errorf("kernel: fail closed on a broken revocation journal: %w", err)
	}
	res.Counts, res.FailedClosed = counts, true
	return journal.WriteBrokenLatch(ctx, in.KS, fp, in.now())
}

// classifyInput is what the classifier reads.
type classifyInput struct {
	RestoreBoot bool
	Loc         journal.Location
	Describes   bool // Loc describes this store (db_path and install_id)
	// LocUnreadable: backup.location.json exists and could not be read or
	// decoded, so the evidence it held is unknown.
	LocUnreadable bool
	StorePair     journal.ServedBoot
	Head          journal.Head // head_before
	Identity      storage.StoreIdentity
	Chain         *journal.Chain
	Aborted       map[uint64]bool // restore lines closed by restore_aborted or a swapped-back journal
}

// classifyStore is the one classifier (§5B.3.6 "Boot re-apply" (2)); no other
// file adds a signal (R7-4, DATA7-10). It reads store identity and content,
// never head lag alone.
func classifyStore(c classifyInput) (storeClass, string) {
	switch {
	case c.RestoreBoot:
		return classRestoreBoot, "a restore boot: none of the replacement evidence applies"
	case c.LocUnreadable && (c.Head.Seq > 0 || c.StorePair.N > 0):
		// The store has journaled before, so the file it was compared with
		// existed; one that cannot be read may have said it was replaced.
		return classReplaced, "backup.location.json is unreadable and the store has journaled before"
	case !c.Describes:
		return classSame, "no location record describes this store"
	case c.Loc.HasServedBoot() && servedOlder(c.StorePair, c.Loc.ServedBoot):
		return classReplaced, "the store's served_boot pair is older than the file's"
	case c.Head.Seq < c.Loc.Head.Seq:
		return classReplaced, "the store's head is below the committed-head anchor"
	case c.Identity == c.Loc.Store:
		return classSame, "the generation and directory identity match"
	case journalShowsReplaced(c.Chain, c.Head, c.Aborted):
		return classReplaced, "another generation or directory identity, and the journal shows the store is not the current one"
	}
	return classRelocated, "another generation or directory identity with no journal evidence of a replacement"
}

// servedOlder reports a store pair older than the file's: a lower n, or the
// same n with another nonce. A store with no pair has n 0.
func servedOlder(store, file journal.ServedBoot) bool {
	return store.N < file.N || (store.N == file.N && store.Nonce != file.Nonce)
}

// journalShowsReplaced is evidence (c)'s second half: the store has not seen
// every revocation line of the journal (head_before is below the last one, or
// below the lines retention dropped, or the store has no head while the journal
// has revocation lines), or the journal holds a gap or restore line after
// head_before. A restore line a restore_aborted line closes, or whose restore
// journal reads swapped_back, does not count (DATA6-3), and neither do the hook
// lines that restore wrote: they narrowed a store that was swapped away
// (DATA8-7), so they say nothing about whether this one is current. A chain
// that does not verify speaks through what it claims (Chain.Behind).
func journalShowsReplaced(chain *journal.Chain, head journal.Head, aborted map[uint64]bool) bool {
	if chain.Break != nil {
		if chain.Behind(head.Seq) {
			return true
		}
	} else if chain.HasHeader && head.Seq < chain.Header.BaseSeq {
		return true
	}
	return chain.Has(head.Seq, func(l journal.Line) bool {
		switch {
		case l.Kind == journal.Gap:
			return true
		case l.Kind == journal.Restore:
			return !aborted[l.Seq]
		case journal.IsRevocation(l.Kind):
			return !(l.RestoreSeq != 0 && aborted[l.RestoreSeq])
		}
		return false
	})
}

// tailBatch is how many lines one re-apply batch carries.
const tailBatch = 128

// reapplyTail re-applies every verified line after head, except a hook line of
// an aborted restore (verified, part of the chain, never applied: the store it
// narrowed was swapped away, DATA8-7 (a)), in batches that also write the new
// head. It returns the number of revocation lines it applied.
func (in reapplyInput) reapplyTail(ctx context.Context, chain *journal.Chain, head journal.Head, aborted map[uint64]bool) (int, error) {
	var tail []journal.Line
	for l := range chain.After(head.Seq) {
		tail = append(tail, l)
	}
	applied := 0
	for part := range slices.Chunk(tail, tailBatch) {
		var revs []journal.Revocation
		for _, l := range part {
			if journal.IsRevocation(l.Kind) && !(l.RestoreSeq != 0 && aborted[l.RestoreSeq]) {
				revs = append(revs, l.Revocation)
			}
		}
		last := part[len(part)-1]
		kept, err := in.commitTail(ctx, revs, journal.Head{Seq: last.Seq, MAC: last.MAC})
		if err != nil {
			return applied, fmt.Errorf("kernel: re-apply the revocation journal up to seq %d: %w", last.Seq, err)
		}
		applied += len(revs)
		for _, u := range kept {
			if err := in.Apply.RestrictLastAdmin(ctx, u); err != nil {
				return applied, fmt.Errorf("kernel: restrict the last admin %s: %w", u, err)
			}
		}
	}
	return applied, nil
}

// commitTail stages revs into one batch with the head and commits it, again
// with a fresh batch on a conflict (the stagers re-read what they decide on).
func (in reapplyInput) commitTail(ctx context.Context, revs []journal.Revocation, head journal.Head) (kept []string, err error) {
	err = storage.RetryOnConflict(ctx, 5, func(ctx context.Context) (err error) {
		b := in.Apply.NewBatch()
		defer func() { err = errors.Join(err, b.Close()) }()
		kept = nil
		if len(revs) > 0 {
			if err := in.Apply.Auth(ctx, b, revs); err != nil {
				return err
			}
			if kept, err = in.Apply.RBAC(ctx, b, revs); err != nil {
				return err
			}
		}
		if err := journal.StageHead(ctx, b, in.KS, head); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
	return kept, err
}

// bookkeeping is steps (4) and (5)'s lines: the restore line of a restore boot
// (found, never appended twice) or of a replaced store (an external line,
// unless one an earlier boot appended still owes its hooks), each with its head
// update in its own batch (File.AppendLine).
func (in reapplyInput) bookkeeping(ctx context.Context, chain *journal.Chain, aborted map[uint64]bool, res *reapplyResult) error {
	if in.Restore != nil {
		return in.restoreLine(ctx, chain, aborted, res)
	}
	owed, err := in.unhookedExternal(ctx, chain, aborted)
	if err != nil {
		return err
	}
	res.Owed = owed
	switch {
	case res.Class == classReplaced && len(owed) == 0:
		res.RestoreBoundary = res.HeadBefore.Seq
		line := journal.RestoreLineOf(journal.RestoreInfo{Kind: journal.RestoreExternal, Boundary: res.RestoreBoundary}, in.now())
		h, err := in.Journal.AppendLine(ctx, line)
		if err != nil {
			// The hooks still run (they fail closed); only the record that
			// resumes them after a crash is missing.
			in.log().Error("kernel: the restore line of a replaced store could not be appended", "err", err)
			return nil
		}
		line.Seq = h.Seq
		res.RestoreLine = h.Seq
		res.Owed = []journal.Line{{Revocation: line, MAC: h.MAC}}
	case len(owed) > 0:
		res.RestoreLine = owed[0].Seq
		if info, ok := owed[0].RestoreInfo(); ok {
			res.RestoreBoundary = info.Boundary
		}
	}
	return nil
}

// unhookedExternal lists the external restore lines whose hooks have not
// completed and that were not aborted: a crash after the line and before the
// hooks leaves one, and the next boot resumes it (§5B.3.6 (5)).
func (in reapplyInput) unhookedExternal(ctx context.Context, chain *journal.Chain, aborted map[uint64]bool) ([]journal.Line, error) {
	var out []journal.Line
	for _, l := range chain.Lines {
		info, ok := l.RestoreInfo()
		if !ok || info.Kind != journal.RestoreExternal || aborted[l.Seq] {
			continue
		}
		done, err := journal.Hooked(ctx, in.KS, l.Seq)
		if err != nil {
			return nil, fmt.Errorf("kernel: read restore_hooked: %w", err)
		}
		if !done {
			out = append(out, l)
		}
	}
	return out, nil
}

// restoreLine finds this restore's line in the journal (by the restore
// journal's ID, so a crash between the append and the `restore_line` journal
// record never appends a second) or appends it, then journals restore_line.
func (in reapplyInput) restoreLine(ctx context.Context, chain *journal.Chain, aborted map[uint64]bool, res *reapplyResult) error {
	rb := in.Restore
	res.RestoreBoundary = min(rb.SourceSeq, res.HeadBefore.Seq)
	for _, l := range chain.Lines {
		if info, ok := l.RestoreInfo(); ok && info.RestoreID == rb.ID && !aborted[l.Seq] {
			res.RestoreLine, res.RestoreBoundary = l.Seq, info.Boundary
			break
		}
	}
	if res.RestoreLine == 0 {
		line := journal.RestoreLineOf(journal.RestoreInfo{Kind: restoreKindLabel(rb.Kind), Source: rb.ArchiveID,
			Boundary: res.RestoreBoundary, RestoreID: rb.ID}, in.now())
		if h, err := in.Journal.AppendLine(ctx, line); err != nil {
			// A journal that cannot take a line (broken, locked, unwritable)
			// does not stop the restore: the hooks run, with no coverage when
			// the journal is broken, and only the record that ties their lines
			// to this restore is missing. restore_line is journaled with seq 0.
			in.log().Error("kernel: the restore line could not be appended", "restore", rb.ID, "err", err)
		} else {
			res.RestoreLine = h.Seq
		}
	}
	if rb.Steps != nil {
		if err := rb.Steps.RestoreLine(ctx, rb.ID, res.RestoreLine); err != nil {
			return fmt.Errorf("kernel: journal restore_line: %w", err)
		}
	}
	return nil
}

func restoreKindLabel(k journal.RestoreKind) string {
	switch k {
	case journal.KindArchive:
		return journal.RestoreArchive
	case journal.KindRevert:
		return journal.RestoreRevert
	case journal.KindCheckpoint:
		return journal.RestoreCheckpoint
	}
	return journal.RestoreExternal
}

// writeMarkers is the end of step 2 on a plain boot.
func (in reapplyInput) writeMarkers(ctx context.Context, res *reapplyResult) error {
	return writeStoreMarkers(ctx, in.Journal, res.Path, in.Identity, res.Moved, in.log())
}

// writeStoreMarkers writes c/backup/served_boot and then backup.location.json
// with this store's identity, that pair and the committed head (the end of
// step 2 on a plain boot, step h on a restore boot, after the hooks); then it
// removes the old journal file of a move, now that the record points at the new
// one.
func writeStoreMarkers(ctx context.Context, comp *journal.Component, path string, id storage.StoreIdentity, moved string, log *slog.Logger) error {
	pair, err := comp.AdvanceServedBoot(ctx)
	if err != nil {
		return fmt.Errorf("kernel: write served_boot: %w", err)
	}
	desc := journal.LocationDesc{BackupDir: filepath.Dir(path), Journal: path, Store: id}
	if err := comp.WriteLocation(ctx, desc, pair); err != nil {
		return fmt.Errorf("kernel: write %s: %w", journal.LocationFile, err)
	}
	if moved != "" {
		if err := os.Remove(moved); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn("kernel: the old revocation journal could not be removed after the move", "path", moved, "err", err)
		}
	}
	return nil
}
