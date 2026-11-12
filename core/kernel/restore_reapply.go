// file: core/kernel/restore_reapply.go
// version: 1.5.2
// guid: 2f6d8a14-c3e7-4b95-a018-9d4e7b1c5f36
// last-edited: 2026-11-12

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
//
// The store's head moves last, and only once something durable says why. The
// head below the location file's anchor, and the journal's lines after the
// head, are the evidence a store was replaced; moving the head to the journal's
// end erases both. So a replaced store's verdict (the external restore line,
// which the next boot resumes) is written before the first batch and its head is
// what the last batch commits; when the line cannot be written the head is held
// until the hooks have run (reapplyResult.Held).
//
// A revocation line of a kind this build does not know (a newer build wrote it)
// is not a reason to stop. It is recorded in c/backup/journal_unapplied/<seq>
// before anything can move the head past it, the head then passes it like any
// other line, and the first boot of a build that knows the kind applies it and
// deletes the entry in the same batch (drainUnapplied). Holding the head below
// the line instead made every later boot re-apply the lines after it, undoing an
// admin's re-enable or a new password, and refuse the journal for good.
//
// A kept last admin's restriction is a journal append, and an append stages its
// own seq as the head: seq above every line of the journal. So nothing that can
// append runs before every line after the store's head, and every queued line this
// build can now apply, has been applied, queued or stepped over (a restriction
// first let the head pass lines that were not, and a crash right after it lost
// them for good), and the line that kept the admin does not complete (its head, its
// queue entry's removal) before the restriction ran (the restriction was a separate
// commit after it, so a crash between the two left an admin the journal proved was
// demoted with the role, unrestricted, and nothing to replay it). keptAdmins is
// what waits.

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
	// RestrictLastAdmins restricts every kept admin in one batch
	// (auth.RestrictLastAdmins), once the whole tail is applied: its journal lines
	// stage the head at the top of the journal, which no line may be left below,
	// and one commit for all of them leaves no point at which the head is up and
	// an admin the journal proved demoted is not yet restricted. A user a later
	// line deleted is skipped.
	RestrictLastAdmins func(ctx context.Context, userIDs []string) error
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
	// TailBatch is how many journal lines one re-apply batch carries: zero is
	// tailBatch. The crash tests set it to 1 so every line is its own batch.
	TailBatch int
	// Known tells whether this build can apply a revocation line of that kind;
	// nil is journal.KnownKind. The tests set it to play an older build against a
	// journal a newer one wrote, and then the same journal under this one.
	Known func(journal.Kind) bool
	// QueueBatch opens the batches of the unapplied queue's own writes, the
	// entries queueUnapplied stages and the removal of the entries of drained lines
	// that kept an admin (restrictKept): nil is KS.NewBatch. The crash tests wrap
	// it, as they wrap Apply.NewBatch, to end a boot between two of them.
	QueueBatch func() *storage.Batch
	Clock      func() time.Time
	Log        *slog.Logger
}

func (in reapplyInput) now() time.Time {
	if in.Clock != nil {
		return in.Clock().UTC()
	}
	return time.Now().UTC()
}

// known is Known, defaulting to journal.KnownKind.
func (in reapplyInput) known(k journal.Kind) bool {
	if in.Known != nil {
		return in.Known(k)
	}
	return journal.KnownKind(k)
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
	case in.Apply.NewBatch == nil || in.Apply.Auth == nil || in.Apply.RBAC == nil || in.Apply.RestrictLastAdmins == nil || in.Apply.FailClosed == nil:
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
	// Queued is the number of revocation lines after the store's head that this
	// build cannot apply (a kind only a newer build knows) and recorded in the
	// unapplied queue this boot; the head passed them. Drained is the number of
	// queued lines of earlier boots that this build applied and removed from it;
	// Unapplied the entries still in the queue when the boot ended (a kind this
	// build still does not know, or a line the verified journal no longer holds).
	// A queued line is not a broken journal: Broken stays nil for it.
	Queued, Drained, Unapplied int
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
	// Held is the head a replaced store's boot did not commit because nothing
	// durable yet says the store was replaced; step 3 commits it once the hooks
	// have completed. Zero when none is held.
	Held journal.Head
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
	if otherInstall(chain, in.Install) {
		if res.Reset, err = in.setAside(ctx, res.Path, headBefore); err != nil {
			log.Error("kernel: another install's revocation journal could not be set aside", "path", res.Path, "err", err)
		} else {
			chain = &journal.Chain{}
		}
	} else {
		chain = in.checkAnchor(chain, loc, describes)
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

	// The lines this boot owes the journal come before any head moves: what
	// the verdict that the store was replaced is made durable by.
	pend, err := in.writeBookkeeping(ctx, chain, aborted, bindErr == nil, &res)
	if err != nil {
		return res, err
	}
	// A verdict nothing durable records holds the head back until the hooks ran.
	hold := !restoring && res.Class == classReplaced && res.RestoreLine == 0
	if hold {
		// The hooks' own journal lines would stage their seqs as the head, which
		// is what the hold keeps back; runRestoreHooks lifts it (Resume) once they
		// have completed and the held head is committed.
		in.Journal.Hold(errors.New("the journal could not take the restore line of a replaced store, so it takes no line until the hooks have run"))
	}

	// (3) re-apply what the store has not seen: the verified tail after
	// head_before, narrowing and idempotent. A bind failure does not stop it.
	// A chain that does not verify still yields the lines that did (the store
	// lacks nothing of what they hold, whatever the rest of the file is).
	//
	// First the queue: a line an earlier boot could not apply and this build can
	// is applied now and its entry deleted with it. Then the lines after the head
	// that this build cannot apply are queued, all of them, before the first batch
	// that can move the head. No restriction of a kept last admin runs until the
	// whole tail is done (an append stages its own seq as the head, which would
	// pass every line not yet handled): the drain and the tail record the admins
	// they kept in owe, and restrictKept answers them last.
	var owe keptAdmins
	if res.Drained, err = in.drainUnapplied(ctx, chain, aborted, !untrusted, &owe); err != nil {
		return res, err
	}
	lines := in.tailLines(chain, headBefore, untrusted)
	if res.Queued, err = in.queueUnapplied(ctx, lines, aborted); err != nil {
		return res, err
	}
	plan := tailPlan{Lines: lines, Aborted: aborted, Pending: pend, Hold: hold}
	if res.Reapplied, err = in.applyTail(ctx, plan, &owe); err != nil {
		return res, err
	}
	after := headBefore
	if n := len(lines); n > 0 {
		after = journal.Head{Seq: lines[n-1].Seq, MAC: lines[n-1].MAC}
	}
	if plan.Pending != nil && plan.Pending.Head().Seq > after.Seq {
		after = plan.Pending.Head()
	}
	if err := in.restrictKept(ctx, &owe, plan, after); err != nil {
		return res, err
	}
	if hold && after.Seq > headBefore.Seq {
		res.Held = after
	}

	if untrusted {
		// A store that may lack what the file holds or held fails closed.
		if err := in.failClosedIfBehind(ctx, chain, headBefore, after, restoring, &res); err != nil {
			return res, err
		}
	}
	// A line this build could not apply may have been a revocation the store
	// lacks, and fail-closed is what answers that until a build that knows the
	// kind applies it; once per entry, never again at every boot.
	if err := in.settleUnapplied(ctx, restoring, &res); err != nil {
		return res, err
	}
	if res.Broken == nil {
		res.Broken = bindErr
	}
	if res.Broken != nil {
		in.Journal.NoteBroken(res.Broken)
	}

	// (4) a restore boot's restore line, after the tail: nothing reads the head
	// of a restore boot as evidence (R6-4), and its restore journal is the
	// durable verdict.
	if restoring {
		if err := in.restoreLine(ctx, chain, aborted, &res); err != nil {
			return res, err
		}
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
	// Only a regular file is renamed: a link planted at the journal's name is not
	// the journal, and moving it would take whatever it points at out of the
	// share's reach.
	if fi, err := os.Lstat(path); err != nil {
		return false, fmt.Errorf("kernel: set the other install's journal aside: %w", err)
	} else if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("kernel: set the other install's journal aside: %w: %s", journal.ErrNotRegular, path)
	}
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

// The Break reasons of a chain the location file's anchor contradicts.
const (
	// breakBelowAnchor: the journal ends below the committed head backup.location.json
	// recorded: lines were cut off the end, or the file was replaced by a shorter one.
	breakBelowAnchor journal.BreakReason = "below_anchor"
	// breakForkedAnchor: the line the file recorded at its committed head is not the
	// one the journal holds at that seq: the file is another history.
	breakForkedAnchor journal.BreakReason = "forked_anchor"
)

// checkAnchor reads the journal against backup.location.json's committed head
// (05-backup §5B.3.6 row "Head in the store"), the one record that says how far
// the journal had got that does not live in the store a copy can replace. When
// the file describes this store, a journal that ends below that head has lost
// lines (a cut at a line boundary verifies as a shorter chain, so nothing in the
// chain says so), and one that holds another line at that seq is not the journal
// the file recorded. Either is a break at the first line the journal should
// have: the verified lines before it are still used, and the store, if it is
// behind the anchor, fails closed for what it may lack (ClaimedEnd is raised to
// the anchor so Chain.Behind says so). It returns chain itself when the anchor
// is on it, the file does not describe this store, or has no committed head. A
// forked_anchor chain still applies its verified lines after the store's head
// (tailLines; forkedAt, which applies nothing, is about the store's own head).
func (in reapplyInput) checkAnchor(chain *journal.Chain, loc journal.Location, describes bool) *journal.Chain {
	if !describes || loc.Head.Seq == 0 {
		return chain
	}
	c := *chain
	switch {
	case chain.IntactEnd() < loc.Head.Seq:
		c.ClaimedEnd = max(c.ClaimedEnd, loc.Head.Seq)
		if c.Break == nil {
			c.Break = &journal.Break{Seq: chain.IntactEnd() + 1, Reason: breakBelowAnchor,
				Err: fmt.Errorf("the journal ends at seq %d, below the committed head %d backup.location.json recorded (lines were cut off, or the file was replaced)", chain.IntactEnd(), loc.Head.Seq)}
		}
	case loc.Head.MAC != "" && chain.Break == nil:
		mac, ok := chain.MACAt(loc.Head.Seq)
		if !ok || mac == loc.Head.MAC {
			return chain
		}
		c.Break = &journal.Break{Seq: loc.Head.Seq, Reason: breakForkedAnchor,
			Err: fmt.Errorf("the journal holds another line at seq %d than backup.location.json recorded (the file was replaced)", loc.Head.Seq)}
	default:
		return chain
	}
	return &c
}

// otherInstall reports a verified header that names another install.
func otherInstall(chain *journal.Chain, install string) bool {
	return chain.HasHeader && chain.Header.InstallID != install
}

// forkedAt reports a journal that is not the one the store committed: its
// verified line at the store's head differs. A chain that breaks later still
// answers (MACAt speaks only for the lines that verified); one that breaks
// before the head cannot, and is reported by its break. It is the verdict under
// which nothing is applied: the lines are another history. Not to be confused
// with checkAnchor's forked_anchor, where the journal does hold the store's head
// but not the line the location file recorded further on: those verified lines
// after the store's head are applied.
func forkedAt(chain *journal.Chain, head journal.Head) bool {
	if head.MAC == "" {
		return false
	}
	mac, ok := chain.MACAt(head.Seq)
	return ok && mac != head.MAC
}

// failClosedIfBehind reacts to a chain that cannot be trusted whole. The lines
// of it that verify were re-applied first (tailLines); what the store may still
// lack is what the rest of the file held, which the boot cannot know, so when
// the store may lack a revocation the journal holds or held it fails closed
// through auth's restore fallback (every API key needs_confirmation, every
// credential epoch bumped, every session deleted) and reports
// revocation_journal_broken. "May lack" is every way the store can be behind or
// unrelated to the journal: the chain's end (for a chain that does not verify,
// everything it claims, or the anchor the location file recorded) is above the
// store's head, the file is not the one the store committed or starts above its
// head, or it is another install's while the store has a head (its own journal
// is gone). Another install's journal never answers "is the store behind it":
// its seqs are not this store's, so a store that has never journaled (head 0) has
// lost nothing to it, however many lines it holds. It happens once per broken
// state (journal.Chain.Fingerprint, keyed on the head after the re-apply, so the
// re-apply's own head move does not make the next boot's state look new), so a
// restart does not bump every epoch again. On a restore boot the hooks do their
// own fallback with no coverage.
func (in reapplyInput) failClosedIfBehind(ctx context.Context, chain *journal.Chain, before, after journal.Head, restoring bool, res *reapplyResult) error {
	foreign := otherInstall(chain, in.Install)
	forked := forkedAt(chain, before) || (foreign && before.Seq > 0)
	pruned := chain.Break == nil && chain.HasHeader && before.Seq < chain.Header.BaseSeq
	behind := !foreign && chain.Behind(before.Seq)
	if restoring || !(forked || pruned || behind) {
		return nil
	}
	state := ""
	if forked {
		state = "forked"
	}
	return in.failClosedOnce(ctx, chain.Fingerprint(after, state), res)
}

// failClosedOnce runs auth's fail-closed fallback unless the broken state fp
// was already failed closed on, and latches fp.
func (in reapplyInput) failClosedOnce(ctx context.Context, fp string, res *reapplyResult) error {
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

// tailLines are the lines the re-apply goes through: every verified line after
// head, in order. That includes a revocation of a kind this build cannot apply
// (queueUnapplied records it before anything moves the head, and applyTail steps
// over it) and the hook line of an aborted restore (verified, part of the chain,
// never applied: the store it narrowed was swapped away, DATA8-7 (a)).
//
// On a chain that does not verify the lines are its verified prefix up to the
// break, which Chain.Lines already is: they are authentic revocations after the
// store's head, narrowing-only, and the store lacks nothing less for the rest of
// the file being doubtful. They are not applied when they are not this store's
// history: a journal of another install, or one that verifies but does not hold
// the line the store committed at its head (forkedAt, whatever Break says). That
// is the one place an untrusted chain applies nothing; a journal the location
// file's anchor contradicts (forked_anchor, below_anchor) and a pruned or
// truncated one do apply their verified lines after the store's head, because the
// store's own head is on them. A journal that starts above the store's head
// (pruned) has only the lines it holds, applied; the interval between is what the
// fail-closed fallback is for.
func (in reapplyInput) tailLines(chain *journal.Chain, head journal.Head, untrusted bool) []journal.Line {
	if untrusted && (otherInstall(chain, in.Install) || forkedAt(chain, head)) {
		return nil
	}
	return slices.Collect(chain.After(head.Seq))
}

// tailSize is how many lines one re-apply batch carries.
func (in reapplyInput) tailSize() int {
	if in.TailBatch < 1 {
		return tailBatch
	}
	return in.TailBatch
}

// hookOfAborted reports whether l is a line a restore's hooks wrote and that
// restore was aborted.
func hookOfAborted(l journal.Line, aborted map[uint64]bool) bool {
	return l.RestoreSeq != 0 && aborted[l.RestoreSeq]
}

// applicable reports whether the re-apply applies l: a revocation of a kind this
// build knows that is not the hook line of an aborted restore.
func (in reapplyInput) applicable(l journal.Line, aborted map[uint64]bool) bool {
	return journal.IsRevocation(l.Kind) && in.known(l.Kind) && !hookOfAborted(l, aborted)
}

// owed reports whether l is a revocation this build cannot apply and that the
// store is owed anyway: a kind only a newer build knows. IsRevocation counts an
// unknown kind as one so a newer build's line is never skipped.
func (in reapplyInput) owed(l journal.Line, aborted map[uint64]bool) bool {
	return journal.IsRevocation(l.Kind) && !in.known(l.Kind) && !hookOfAborted(l, aborted)
}

// queueBatch is QueueBatch, defaulting to a batch of the c/backup/ keyspace.
func (in reapplyInput) queueBatch() *storage.Batch {
	if in.QueueBatch != nil {
		return in.QueueBatch()
	}
	return in.KS.NewBatch()
}

// queueUnapplied records every line of lines this build cannot apply in
// c/backup/journal_unapplied/<seq>, durably and in batches of in.TailBatch,
// before applyTail's first batch: the head may then pass them, and the first
// build that knows their kind applies them (drainUnapplied). It returns how many
// there were. Staging a line already queued (an earlier boot crashed after this
// step) changes nothing.
func (in reapplyInput) queueUnapplied(ctx context.Context, lines []journal.Line, aborted map[uint64]bool) (int, error) {
	var owed []journal.Line
	for _, l := range lines {
		if in.owed(l, aborted) {
			owed = append(owed, l)
		}
	}
	if len(owed) == 0 {
		return 0, nil
	}
	for part := range slices.Chunk(owed, in.tailSize()) {
		err := func() (err error) {
			b := in.queueBatch()
			defer func() { err = errors.Join(err, b.Close()) }()
			for _, l := range part {
				if err := journal.StageUnapplied(ctx, b, in.KS, journal.UnappliedOf(l, false)); err != nil {
					return err
				}
			}
			if b.Count() == 0 {
				return nil // every one is queued already
			}
			return b.Commit(ctx, storage.Sync)
		}()
		if err != nil {
			return 0, fmt.Errorf("kernel: queue the revocation lines this build cannot apply, up to seq %d: %w", part[len(part)-1].Seq, err)
		}
	}
	in.log().Warn("kernel: the journal holds revocation lines of a kind only a newer build knows: queued for it, the head passes them",
		"lines", len(owed), "first_seq", owed[0].Seq, "first_kind", owed[0].Kind)
	return len(owed), nil
}

// drainUnapplied applies the queued lines this build can now apply, and deletes
// each one's entry in the batch that applies it, so a line is applied and
// dequeued together or neither (a crash between the two batches of one chunk
// re-applies it, which narrows and is idempotent). A chunk that kept a last admin
// does not delete its entries yet: the restriction is still owed, and the entries
// are all there is to replay it from, so they wait in owe until restrictKept ran
// it. The journal says what to apply: an entry names a line, and only the verified
// chain's line at that seq with that mac is ever applied. An entry whose kind this
// build still does not know stays; so does one the verified chain does not hold
// (retention dropped it, or the journal is another history), which is reported,
// not failed on again; and nothing is applied from a chain that is not trusted
// (the entries were answered when they were queued, and the boot's other rules
// already report and fail closed for the chain). A line that is now a known
// bookkeeping kind, or the hook line of an aborted restore, is owed nothing and
// its entry goes. It returns the number of lines applied.
func (in reapplyInput) drainUnapplied(ctx context.Context, chain *journal.Chain, aborted map[uint64]bool, trusted bool, owe *keptAdmins) (int, error) {
	entries, err := journal.ReadUnapplied(ctx, in.KS)
	if err != nil {
		return 0, fmt.Errorf("kernel: read the unapplied-revocation queue: %w", err)
	}
	if len(entries) == 0 || !trusted {
		return 0, nil
	}
	var ready []journal.Line
	for _, e := range entries {
		if l, ok := chain.LineAt(e.Seq); ok && l.MAC == e.MAC && in.known(l.Kind) {
			ready = append(ready, l)
		}
	}
	drained := 0
	for part := range slices.Chunk(ready, in.tailSize()) {
		var revs []journal.Revocation
		for _, l := range part {
			if in.applicable(l, aborted) {
				revs = append(revs, l.Revocation)
			}
		}
		dequeue := func(_ context.Context, b *storage.Batch) error {
			for _, l := range part {
				if err := journal.StageUnappliedDelete(b, in.KS, l.Seq); err != nil {
					return err
				}
			}
			return nil
		}
		kept, completed, err := in.commitTail(ctx, revs, dequeue, false)
		if err != nil {
			return drained, fmt.Errorf("kernel: apply the queued revocation lines up to seq %d: %w", part[len(part)-1].Seq, err)
		}
		drained += len(part)
		owe.keep(kept)
		if !completed {
			for _, l := range part {
				owe.Dequeue = append(owe.Dequeue, l.Seq)
			}
		}
	}
	if drained > 0 {
		in.log().Info("kernel: applied revocation lines a newer build wrote, queued by an earlier boot", "lines", drained)
	}
	return drained, nil
}

// keptAdmins is what the re-apply owes the last admins it kept: rbac skips a line
// that would leave no active core:admin holder and reports the holder, and the
// holders are then restricted (auth.RestrictLastAdmins, journaled, so an append that
// stages the head at the top of the journal). That is why they wait for the end of
// the tail, and why what the kept line would have completed waits with them. One
// line can keep several: when it would leave no active admin, rbac keeps the role
// and reports every active holder, so the restriction is one call for all of them
// and one batch, never a loop of commits.
type keptAdmins struct {
	// Users are the admins kept, in the order they were, each once.
	Users []string
	// Dequeue are the seqs of drained lines whose entry is not deleted yet.
	Dequeue []uint64
	// Head is true once a tail chunk's head was withheld: that chunk and every one
	// after it stage none, so the head cannot pass the chunk that kept an admin
	// before the restriction ran, and restrictKept commits it.
	Head bool
}

// keep adds the admins a batch kept.
func (k *keptAdmins) keep(users []string) {
	for _, u := range users {
		if !slices.Contains(k.Users, u) {
			k.Users = append(k.Users, u)
		}
	}
}

// restrictKept restricts the admins the drain and the tail kept, once the whole
// tail has been applied, and then completes what waited for them: the queue
// entries of the drained lines that kept one and the head a chunk withheld. A
// crash anywhere before this re-applies the chunks that kept an admin at the next
// boot (their head and their entries are still below), which keeps the admin
// again; a crash after the restriction has the head at the top of the journal
// when the journal took its line, and every line below it handled by then.
// The head is committed here whether or not the restriction's own append moved it:
// a journal that cannot take the line records a gap and leaves the head where it
// is, and the commit never moves it back.
func (in reapplyInput) restrictKept(ctx context.Context, owe *keptAdmins, p tailPlan, after journal.Head) error {
	if len(owe.Users) > 0 {
		if err := in.Apply.RestrictLastAdmins(ctx, owe.Users); err != nil {
			return fmt.Errorf("kernel: restrict the last admins %v: %w", owe.Users, err)
		}
	}
	if len(owe.Dequeue) > 0 {
		err := func() (err error) {
			b := in.queueBatch()
			defer func() { err = errors.Join(err, b.Close()) }()
			for _, seq := range owe.Dequeue {
				if err := journal.StageUnappliedDelete(b, in.KS, seq); err != nil {
					return err
				}
			}
			return b.Commit(ctx, storage.Sync)
		}()
		if err != nil {
			return fmt.Errorf("kernel: remove the applied lines from the unapplied-revocation queue: %w", err)
		}
	}
	if !owe.Head || p.Hold {
		return nil
	}
	if p.Pending != nil {
		if err := p.Pending.Commit(ctx); err != nil {
			return fmt.Errorf("kernel: commit the journal head: %w", err)
		}
		return nil
	}
	if err := journal.CommitHead(ctx, in.KS, after); err != nil {
		return fmt.Errorf("kernel: commit the journal head: %w", err)
	}
	return nil
}

// settleUnapplied reports what the queue holds once the tail is done and answers
// the lines this build queued: a revocation it could not read may be one the
// store lacks, so a plain boot fails closed (auth's fallback), once for each
// entry (Closed, set after the pass: a crash between the two repeats the pass at
// the next boot), never again at every boot. A restore boot does not: its hooks
// do their own fallback, and the entries stay open for the next plain boot, as
// the stopped re-apply it replaces did. Nothing here is a break: the journal
// takes lines and the head has moved.
func (in reapplyInput) settleUnapplied(ctx context.Context, restoring bool, res *reapplyResult) error {
	entries, err := journal.ReadUnapplied(ctx, in.KS)
	if err != nil {
		return fmt.Errorf("kernel: read the unapplied-revocation queue: %w", err)
	}
	res.Unapplied = len(entries)
	if len(entries) == 0 {
		return nil
	}
	in.log().Warn("kernel: revocation lines of a newer build are still waiting for a build that can apply them",
		"waiting", len(entries), "first_seq", entries[0].Seq, "first_kind", entries[0].Kind, "queued_now", res.Queued, "applied_now", res.Drained)
	if restoring {
		return nil
	}
	var open []journal.Unapplied
	for _, e := range entries {
		if !e.Closed {
			open = append(open, e)
		}
	}
	if len(open) == 0 {
		return nil
	}
	if !res.FailedClosed {
		in.log().Error("kernel: the store may lack revocations this build cannot read: failing closed", "entries", len(open), "first_seq", open[0].Seq, "first_kind", open[0].Kind)
		counts, err := in.Apply.FailClosed(ctx)
		if err != nil {
			return fmt.Errorf("kernel: fail closed for revocation lines this build cannot apply: %w", err)
		}
		res.Counts, res.FailedClosed = counts, true
	}
	if err := journal.MarkUnappliedClosed(ctx, in.KS, open); err != nil {
		return fmt.Errorf("kernel: record the fail-closed pass for the unapplied revocation lines: %w", err)
	}
	return nil
}

// tailPlan is what applyTail applies and the head it moves.
type tailPlan struct {
	Lines   []journal.Line
	Aborted map[uint64]bool
	// Pending is the line this boot wrote before the re-apply (a restore line,
	// or the gap line of a new journal): the last batch commits its head, which
	// is above the last line's.
	Pending *journal.Pending
	// Hold stages no head at all: nothing durable yet says why the store's head
	// may move, so the caller commits it after the hooks (reapplyResult.Held).
	Hold bool
}

// applyTail applies p.Lines in order, in batches of in.TailBatch, each batch
// moving the head with its last write; it returns the number of revocations it
// applied. A batch that kept a last admin moves no head, and neither does any
// batch after it (owe.Head): the restriction is owed and restrictKept commits the
// head once it ran. A line this build cannot apply (queued by queueUnapplied
// already) and the hook line of an aborted restore are stepped over, not applied.
// The head never passes a line that is neither applied nor queued. A run of lines
// that is empty still commits the pending line's head.
func (in reapplyInput) applyTail(ctx context.Context, p tailPlan, owe *keptAdmins) (int, error) {
	parts := slices.Collect(slices.Chunk(p.Lines, in.tailSize()))
	applied := 0
	for i, part := range parts {
		var revs []journal.Revocation
		for _, l := range part {
			if in.applicable(l, p.Aborted) {
				revs = append(revs, l.Revocation)
			}
		}
		last := part[len(part)-1]
		var head func(context.Context, *storage.Batch) error
		switch {
		case p.Hold:
		case i == len(parts)-1 && p.Pending != nil:
			head = p.Pending.Stage
		default:
			h := journal.Head{Seq: last.Seq, MAC: last.MAC}
			head = func(ctx context.Context, b *storage.Batch) error { return journal.StageHead(ctx, b, in.KS, h) }
		}
		kept, completed, err := in.commitTail(ctx, revs, head, owe.Head)
		if err != nil {
			return applied, fmt.Errorf("kernel: re-apply the revocation journal up to seq %d: %w", last.Seq, err)
		}
		applied += len(revs)
		owe.keep(kept)
		if head != nil && !completed {
			owe.Head = true
		}
	}
	if len(parts) == 0 && p.Pending != nil && !p.Hold {
		if err := p.Pending.Commit(ctx); err != nil {
			return applied, fmt.Errorf("kernel: commit the journal head: %w", err)
		}
	}
	return applied, nil
}

// commitTail applies revs in two batches and stages complete into the second
// (the head of a tail chunk, the dequeue of a drained one): auth's revocations
// commit first, alone, and then rbac's, which read the users auth's changed, with
// complete. Staged into one batch, rbac's write of a user row (built from a
// snapshot read before auth's was committed) replaced auth's, so the head moved
// past a disable, a delete or a password blank that never took effect, and the
// last-admin guard could not see a disable staged beside it. A crash between the
// two re-applies the chunk, which is idempotent: complete goes with the second.
// Each batch is retried with a fresh one on a conflict (the stagers re-read what
// they decide on).
//
// complete is left out when withhold is set and when rbac kept an admin (the
// restriction is owed, and completing first would let a crash between the two
// leave it with nothing to replay it): completed reports whether it was staged.
func (in reapplyInput) commitTail(ctx context.Context, revs []journal.Revocation, complete func(context.Context, *storage.Batch) error, withhold bool) (kept []string, completed bool, err error) {
	if len(revs) > 0 {
		err := storage.RetryOnConflict(ctx, 5, func(ctx context.Context) (err error) {
			b := in.Apply.NewBatch()
			defer func() { err = errors.Join(err, b.Close()) }()
			if err := in.Apply.Auth(ctx, b, revs); err != nil {
				return err
			}
			if b.Count() == 0 {
				return nil
			}
			return b.Commit(ctx, storage.Sync)
		})
		if err != nil {
			return nil, false, err
		}
	}
	err = storage.RetryOnConflict(ctx, 5, func(ctx context.Context) (err error) {
		b := in.Apply.NewBatch()
		defer func() { err = errors.Join(err, b.Close()) }()
		kept, completed = nil, false
		if len(revs) > 0 {
			if kept, err = in.Apply.RBAC(ctx, b, revs); err != nil {
				return err
			}
		}
		if complete != nil && !withhold && len(kept) == 0 {
			if err := complete(ctx, b); err != nil {
				return err
			}
			completed = true
		}
		if b.Count() == 0 {
			return nil
		}
		return b.Commit(ctx, storage.Sync)
	})
	return kept, completed, err
}

// writeBookkeeping writes the lines this boot owes the journal before it moves
// the store's head, and returns the Pending whose head the re-apply commits
// last: the gap line a new journal starts with after another install's was set
// aside, and, on a plain boot of a replaced store, the external restore line
// (unless one an earlier boot appended still owes its hooks: never duplicated).
// The restore line is the durable verdict: a crash anywhere after it leaves a
// journal the next boot reads as a replaced store with unfinished hooks, and
// resumes. When it cannot be written (a journal that cannot take a line) the
// hooks still run, and res.RestoreLine stays 0, which is what holds the head
// (reapplyResult.Held). A restore boot's line is restoreLine's, after the tail.
func (in reapplyInput) writeBookkeeping(ctx context.Context, chain *journal.Chain, aborted map[uint64]bool, bound bool, res *reapplyResult) (*journal.Pending, error) {
	var pend *journal.Pending
	if res.Reset && bound {
		// A new journal starts with a gap line, and nothing is re-applied from the
		// other install's (DATA4-15); the restore hooks, if any, get no coverage
		// because the journal has no header of this install's.
		p, err := in.Journal.WriteLine(ctx, journal.GapLine(in.now(), journal.GapOtherInstall))
		if err != nil {
			return nil, fmt.Errorf("kernel: start the new journal with a gap line: %w", err)
		}
		pend = &p
		in.Journal.NoteReset()
	}
	if in.Restore != nil {
		return pend, nil
	}
	owed, err := in.unhookedExternal(ctx, chain, aborted)
	if err != nil {
		return nil, err
	}
	res.Owed = owed
	switch {
	case res.Class == classReplaced && len(owed) == 0:
		res.RestoreBoundary = res.HeadBefore.Seq
		line := journal.RestoreLineOf(journal.RestoreInfo{Kind: journal.RestoreExternal, Boundary: res.RestoreBoundary}, in.now())
		p, err := in.Journal.WriteLine(ctx, line)
		if err != nil {
			in.log().Error("kernel: the restore line of a replaced store could not be appended", "err", err)
			return pend, nil
		}
		line.Seq = p.Head().Seq
		res.RestoreLine = line.Seq
		res.Owed = []journal.Line{{Revocation: line, MAC: p.Head().MAC}}
		pend = &p
	case len(owed) > 0:
		res.RestoreLine = owed[0].Seq
		if info, ok := owed[0].RestoreInfo(); ok {
			res.RestoreBoundary = info.Boundary
		}
	}
	return pend, nil
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
