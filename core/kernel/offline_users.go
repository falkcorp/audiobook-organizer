// file: core/kernel/offline_users.go
// version: 0.1.0
// guid: 8e2f6a14-b3d9-4c70-a5e1-6d0c9f47b283
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/journalfs"
	"github.com/falkcorp/mediacore/storage"
)

// This file is the kernel half of the offline identity commands (F4-08 B9b):
// `users reset-password` (05-authn §4.14 "The offline command") and
// `storage journal-queue list|clear` (F4-08 Q6a; 05-storage §5S.4 "Offline
// open" rule 4). The command layer in core/app is a thin parser over these.
//
// They open the store through OpenOffline, so they inherit its refusals (a
// running service, a staged or in-flight swap, an empty or migrating store, a
// non-owner of the state directory), and they then run the serve boot's own
// steps over that open, not a copy of them: P0's config resolution, P3's
// sections, P4b's secret store, P4d's attach of the DB layer, and P4d's
// revocation journal component. That is what makes the effective
// core.backup.dir (a DB override included), core.auth.* settings and keyring
// the ones a serve boot would use, so the journal line an offline reset
// appends lands in the journal the service reads, not in a second one beside
// it.
//
// What the open does NOT do, on purpose: it never re-applies or sets aside a
// journal, runs no restore hook and writes no served_boot (an offline open is
// not a serve boot; TestOfflineRevocationJournaled in the journal package pins
// that for the component), and it reads no boot record, so it never sets one
// aside. The writes it makes are the ones every serve boot's P4b makes first
// (the secret store's c/secrets/meta when absent, stale keyring temporaries
// removed) and the backup directory when it does not exist (reset-password
// only).

// ErrIdentityNotDeclared: the app did not declare the identity boot
// (Config.IdentityBoot), so its store holds no c/auth/, c/rbac/ or c/backup/
// keyspace and these commands have nothing to open. ABO is one until its flip.
var ErrIdentityNotDeclared = errors.New("kernel: this app has not declared the identity boot, so it has no users or revocation journal in the store for this command to open")

// offlineIdentity is the store opened offline with the serve boot's config,
// secret store and audit store over it.
type offlineIdentity struct {
	o         *Offline
	k         *Kernel
	db        *storage.DB
	jfs       journal.FS
	install   string
	prefix    string
	backupDir string
}

// journalFileName is the revocation journal's file name under the effective
// backup directory, <prefix>revocations.journal (runIdentityP4d binds the same
// path; TestOfflineIdentity_JournalPathIsTheServeBoots pins them equal).
func journalFileName(prefix string) string { return prefix + "revocations.journal" }

// openOfflineIdentity opens the store read-write offline and assembles what
// the identity commands read through. what names the command for the refusal
// of an app with no identity boot. The caller closes it.
func openOfflineIdentity(ctx context.Context, c Config, what string) (_ *offlineIdentity, err error) {
	if !c.IdentityBoot {
		return nil, fmt.Errorf("%w (%s)", ErrIdentityNotDeclared, what)
	}
	o, err := OpenOffline(ctx, c, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, o.Close())
		}
	}()
	k, db := o.k, o.db
	in := k.configInput(ctx)
	in.SkipBootRecord = true
	if err := k.bootCoreConfigOver(ctx, in); err != nil {
		return nil, err
	}
	if err := k.loadCoreConfig(ctx); err != nil {
		return nil, err
	}
	if err := k.bindCoreStores(ctx); err != nil {
		return nil, err
	}
	if k.audit, err = buildAudit(ctx, db, k.coreLog("audit"), bootClock(ctx)); err != nil {
		return nil, err
	}
	if err := k.attachCoreConfig(ctx); err != nil {
		return nil, err
	}
	jfs, err := journalfs.From(ctx)
	if err != nil {
		return nil, fmt.Errorf("kernel: identity: the journal filesystem: %w", err)
	}
	bh, ok := handleOf[backupSettings](k.cfgBoot.handles)
	if !ok {
		return nil, errors.New("kernel: identity: the core.backup section is not registered")
	}
	return &offlineIdentity{
		o: o, k: k, db: db, jfs: jfs, install: db.Admin().InstallID(), prefix: k.archivePrefix(),
		backupDir: resolveBackupDir(bh.Get().Dir, k.cfg.StoragePath),
	}, nil
}

// Close closes the store and everything opened over it.
func (oi *offlineIdentity) Close() error { return oi.o.Close() }

// users is the auth store over the open's c/auth/ keyspace.
func (oi *offlineIdentity) users(ctx context.Context) (*authstore.Store, error) {
	aks, ok := oi.db.Keyspace(authstore.Owner)
	if !ok {
		return nil, fmt.Errorf("kernel: identity: no %s keyspace in the store", authstore.Owner)
	}
	users, err := authstore.New(aks, authstore.Options{Now: bootClock(ctx)})
	if err != nil {
		return nil, fmt.Errorf("kernel: identity: auth store: %w", err)
	}
	return users, nil
}

// keyspace is the c/backup/ keyspace.
func (oi *offlineIdentity) keyspace() (storage.Keyspace, error) {
	ks, ok := oi.db.Keyspace(journal.Owner)
	if !ok {
		return storage.Keyspace{}, fmt.Errorf("kernel: identity: no %s keyspace in the store", journal.Owner)
	}
	return ks, nil
}

// journalPath is the journal file the commands read and append to: where
// backup.location.json says the last serve boot left it when that file
// describes this store, else <effective backup directory>/<journal name>. The
// recorded path wins because a changed core.backup.dir is only acted on (the
// journal moved) by a serve boot, and appending to a new empty file beside the
// real one would split the history.
func (oi *offlineIdentity) journalPath() string {
	path := filepath.Join(oi.backupDir, journalFileName(oi.prefix))
	loc, have, err := journal.ReadLocation(oi.k.cfg.StateDir, journal.WithFS(oi.jfs))
	if err == nil && have && loc.Describes(oi.k.cfg.StoragePath, oi.install) && loc.Journal != "" {
		return loc.Journal
	}
	return path
}

// reapply is the part of the re-apply's input the journal reads need.
func (oi *offlineIdentity) reapply() reapplyInput {
	return reapplyInput{Keys: oi.k.sec.store, Install: oi.install, Log: oi.k.coreLog("journal"), FS: oi.jfs, Clock: bootClock(context.Background())}
}

// openJournalComponent creates the backup directory when absent, builds the
// journal component over the effective path and binds it, as the serve boot's
// P4d does, and hands it to the kernel so every return path closes it (it holds
// the journal file's lock and writes the last anchor) before the store closes.
// A journal that cannot be bound is not an error: the component then holds the
// broken file and every append records a gap, as a serving write does, so a
// journal that cannot be appended to never blocks a security action. Only the
// commands that write a revocation call this; a read, or the removal of an
// orphan, creates nothing.
func (oi *offlineIdentity) openJournalComponent(ctx context.Context, users *authstore.Store) (*journal.Component, error) {
	k := oi.k
	if err := ensureBackupDir(oi.jfs, oi.backupDir); err != nil {
		return nil, err
	}
	bks, err := oi.keyspace()
	if err != nil {
		return nil, err
	}
	binary, err := k.binaryID(ctx)
	if err != nil {
		return nil, err
	}
	log := k.coreLog("journal")
	comp, err := journal.NewComponent(journal.ComponentOptions{
		StateDir: k.cfg.StateDir, DBPath: k.cfg.StoragePath, Keyspace: bks, Keys: k.sec.store,
		InstallID: oi.install, App: oi.prefix, Source: binary, Clock: bootClock(ctx), Log: log, FS: oi.jfs,
	})
	if err != nil {
		return nil, fmt.Errorf("kernel: identity: the journal component: %w", err)
	}
	k.ident = &identityBoot{users: users, journal: comp, jfs: oi.jfs}
	if err := comp.Bind(ctx, oi.journalPath()); err != nil {
		log.Warn("kernel: the revocation journal is not usable for appends; writes will record a gap", "path", comp.Path(), "err", err)
	}
	return comp, nil
}

// ResetPasswordOffline is `<app> users reset-password <username>` (05-authn
// §4.14): inside a read-write OpenOffline and only as the state directory's
// owner, it generates a new password for the account, stores its hash,
// clears the account's sign-in restriction and reset-required flag, bumps its
// credential epoch (every session and one-time token of it dies), journals
// password.changed and credential_epoch.bumped in the same batch and audits
// it, Actor.Method "offline" and Actor.Username by (the OS user running the
// command). The password is returned once, to be printed to
// the operator's standard output; it is never stored or logged. It writes no
// external-identity link, API key, role or status. See auth.ResetPasswordOffline
// for the rules of who may be reset.
func ResetPasswordOffline(ctx context.Context, c Config, username, by string) (res auth.OfflineResetResult, err error) {
	oi, err := openOfflineIdentity(ctx, c, "users reset-password")
	if err != nil {
		return res, err
	}
	defer func() { err = errors.Join(err, oi.Close()) }()
	k := oi.k
	users, err := oi.users(ctx)
	if err != nil {
		return res, err
	}
	settings, err := k.authSettingsOver()
	if err != nil {
		return res, err
	}
	// Refuse a missing or ineligible account before anything is created: a
	// refused reset leaves no new backup directory and no new journal file.
	if _, err := auth.LookupResetTarget(ctx, users, username); err != nil {
		return res, err
	}
	comp, err := oi.openJournalComponent(ctx, users)
	if err != nil {
		return res, err
	}
	log := k.coreLog("journal")
	return auth.ResetPasswordOffline(ctx, auth.OfflineResetDeps{
		Store: users, NewBatch: users.NewBatch, Audit: k.audit, By: by, Revocations: comp,
		Settings: settings, Clock: bootClock(ctx), Log: log,
	}, username)
}

// JournalQueueState says what the queue entry is, which decides whether
// `clear` may remove it.
type JournalQueueState string

const (
	// QueuePending: the verified journal holds the line this entry names. A
	// build that knows the line's kind applies it at its next boot; removing the
	// entry would drop a revocation the store is owed. Never clearable.
	QueuePending JournalQueueState = "pending"
	// QueueOrphan: the journal verified clean, installed for this store, and
	// holds no line at this seq with this mac. The entry can never be applied
	// (the line is gone: retention dropped it, or the journal is another
	// history). Clearable.
	QueueOrphan JournalQueueState = "orphan"
	// QueuePoison: the row does not decode. Every boot that reads the queue
	// fails on it, and its Closed flag and mac are unknown, so it cannot just be
	// deleted: the revocation it pointed at may not have been answered by the
	// fail-closed pass. Clearable, by repair or by clearing and failing closed
	// (JournalQueueEntry.Clear says which).
	QueuePoison JournalQueueState = "poison"
	// QueueUnverified: the entry decodes, but the journal cannot say whether it
	// is pending or orphaned (missing, unreadable, broken, another install's,
	// or contradicted by the store's head or the location file). Not clearable:
	// clearing on a guess could drop a revocation.
	QueueUnverified JournalQueueState = "unverified"
	// QueueMalformedKey: the key is not journal_unapplied/<20-digit seq>, so
	// the row cannot be named by a seq. Listed, never cleared by this tool.
	QueueMalformedKey JournalQueueState = "malformed_key"
)

// JournalQueueAction is what `clear` does with a clearable entry.
type JournalQueueAction string

const (
	// ClearRemove deletes an orphan: the verified journal holds no line it
	// could ever apply, and the boot already treats it as inert.
	ClearRemove JournalQueueAction = "remove"
	// ClearRepair replaces a poison row with the entry for the line the
	// verified journal holds at the row's seq, Closed false, so the next plain
	// boot fails closed once for it and a build that knows the kind applies it.
	// Nothing is lost.
	ClearRepair JournalQueueAction = "repair"
	// ClearFailClosed deletes a poison row the journal cannot speak for, after
	// running auth's restore fallback with no coverage: every API key
	// needs_confirmation, every credential epoch bumped, every session deleted.
	// The revocation the row pointed at is unknowable, so everything it could
	// have revoked is revoked.
	ClearFailClosed JournalQueueAction = "fail_closed"
)

// JournalQueueEntry is one row of the unapplied-revocation queue as the
// operator sees it. It carries no MAC and no value: the key, the kind and the
// times are what an operator needs.
type JournalQueueEntry struct {
	// Key is the stored key.
	Key string
	// Seq is the seq the key names (0 for QueueMalformedKey).
	Seq uint64
	// Kind and At are the entry's, empty for a poison row.
	Kind journal.Kind
	At   time.Time
	// Closed is the entry's: the boot that queued it already failed closed.
	Closed bool
	State  JournalQueueState
	// Reason says why the state is what it is, and for a clearable entry what
	// clear would do, in words with no secret.
	Reason string
	// Clear is what `clear` does with the entry; empty when it is not clearable.
	Clear JournalQueueAction
	// FailClosedCounts is set only on the entry a ClearFailClosed returns: the
	// rows the fallback changed (auth.RestoreCounts families).
	FailClosedCounts map[string]int

	row    journal.UnappliedRow
	repair journal.Line // the line a ClearRepair puts back
}

// Clearable reports whether `clear` may remove the entry.
func (e JournalQueueEntry) Clearable() bool { return e.Clear != "" }

// ErrJournalQueueNotClearable refuses to clear an entry that is not poison or
// provably orphaned; the wrapped text says which and why.
var ErrJournalQueueNotClearable = errors.New("kernel: the journal queue entry is not one this command may clear")

// ErrJournalQueueNoEntry: no row at the seq.
var ErrJournalQueueNoEntry = errors.New("kernel: no such entry in the unapplied-revocation queue")

// JournalQueueList is `storage journal-queue list`: every row of the queue,
// decodable or not, with the state `clear` would act on.
func JournalQueueList(ctx context.Context, c Config) (out []JournalQueueEntry, err error) {
	oi, err := openOfflineIdentity(ctx, c, "storage journal-queue")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, oi.Close()) }()
	return oi.classifyQueue(ctx)
}

// JournalQueueClear is `storage journal-queue clear <seq>`: it acts on the one
// entry at seq when it is clearable, in one batch with its audit record
// (core.storage.journal_queue_clear, Actor.Method "offline"), and refuses any
// other (ErrJournalQueueNotClearable) or an absent one (ErrJournalQueueNoEntry),
// changing nothing. The state is re-judged here, over the open the write runs
// under, not taken from an earlier list. What it does is the entry's Clear:
//
//   - ClearRemove (an orphan): delete the row. The boot already treats an entry
//     the verified chain does not hold as inert, so no access changes.
//   - ClearRepair (poison, the verified journal holds the line at its seq):
//     replace the row with that line's entry, Closed false.
//   - ClearFailClosed (poison, the journal cannot name the line): run auth's
//     restore fallback with no coverage FIRST (every API key
//     needs_confirmation, every credential epoch bumped, every session
//     deleted, journaled as any offline revoking write is), and only then delete
//     the row and audit it with the fallback's counts. The order matters: a
//     crash between the two leaves the row to be cleared again (the fallback
//     only restricts further, so a rerun is safe), never a deleted pointer with
//     no fallback behind it. A fallback that cannot run (the journal component
//     cannot be built, the store refuses) fails the command with the row kept;
//     there is no path that deletes a poison row without it.
func JournalQueueClear(ctx context.Context, c Config, seq uint64, by string) (cleared JournalQueueEntry, err error) {
	oi, err := openOfflineIdentity(ctx, c, "storage journal-queue")
	if err != nil {
		return cleared, err
	}
	defer func() { err = errors.Join(err, oi.Close()) }()
	entries, err := oi.classifyQueue(ctx)
	if err != nil {
		return cleared, err
	}
	var target *JournalQueueEntry
	for i := range entries {
		if entries[i].State != QueueMalformedKey && entries[i].Seq == seq {
			target = &entries[i]
			break
		}
	}
	if target == nil {
		return cleared, fmt.Errorf("%w: seq %d", ErrJournalQueueNoEntry, seq)
	}
	if !target.Clearable() {
		return cleared, fmt.Errorf("%w: seq %d is %s: %s", ErrJournalQueueNotClearable, seq, target.State, target.Reason)
	}
	details := map[string]any{"seq": target.Seq, "state": string(target.State), "action": string(target.Clear)}
	if target.Kind != "" {
		details["kind"] = string(target.Kind)
	}
	if target.Clear == ClearFailClosed {
		users, err := oi.users(ctx)
		if err != nil {
			return cleared, err
		}
		comp, err := oi.openJournalComponent(ctx, users)
		if err != nil {
			return cleared, err
		}
		authDeps := auth.RestoreDeps{Store: users, NewBatch: users.NewBatch, Revocations: comp, Clock: bootClock(ctx), Log: oi.k.coreLog("journal")}
		counts, err := auth.PostRestore(ctx, authDeps, journal.RestoreContext{Kind: journal.KindExternal})
		if err != nil {
			return cleared, fmt.Errorf("kernel: fail closed before clearing journal queue entry %d (the entry is kept): %w", seq, err)
		}
		details["fallback"] = "fail_closed"
		details["fallback_counts"] = map[string]int(counts)
		target.FailClosedCounts = map[string]int(counts)
	}
	bks, err := oi.keyspace()
	if err != nil {
		return cleared, err
	}
	b := bks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	switch target.Clear {
	case ClearRepair:
		err = journal.StageUnappliedRowRepair(b, bks, target.row, target.repair)
	default:
		err = journal.StageUnappliedRowDelete(b, bks, target.row)
	}
	if err != nil {
		return cleared, err
	}
	if err := oi.k.audit.RecordIn(ctx, b, audit.Event{
		Action:  "core.storage.journal_queue_clear",
		Outcome: audit.Success,
		Actor:   audit.Actor{Username: by, Method: "offline"},
		Target:  entity.Ref{Module: "core", Kind: "journal_queue_entry", ID: fmt.Sprintf("%d", target.Seq)},
		Reason:  "operator_clear",
		Details: details,
	}); err != nil {
		return cleared, err
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return cleared, fmt.Errorf("kernel: clear the journal queue entry %d: %w", seq, err)
	}
	return *target, nil
}

// classifyQueue reads the queue and the journal and states what each entry is.
func (oi *offlineIdentity) classifyQueue(ctx context.Context) ([]JournalQueueEntry, error) {
	bks, err := oi.keyspace()
	if err != nil {
		return nil, err
	}
	rows, err := journal.ScanUnapplied(ctx, bks)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	proof := oi.readProof(ctx, bks)
	out := make([]JournalQueueEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, classifyRow(r, proof))
	}
	return out, nil
}

// journalProof is what the journal can say about a queue entry: its verified
// chain, and why that chain cannot prove an absence (empty when it can).
type journalProof struct {
	chain *journal.Chain
	// whyNot is why a missing line is not proof of an orphan; "" when the
	// journal verified clean, is this store's, and agrees with the head and
	// the location file.
	whyNot string
}

// readProof reads and judges the journal the way the serve boot's re-apply
// does (readChain, checkAnchor, untrusted), without acting on any verdict, and
// then asks for more: an absence is only proof when the journal is positively
// this store's whole history. Every one of these leaves whyNot set, so no entry
// can be called an orphan: the file is missing, is not a regular file, is empty
// or has no verified header, is another install's, breaks anywhere, does not
// reach (or disagrees with) the store's committed head, or the location file
// that anchors it is unreadable or does not describe this store. It only reads:
// nothing here renames, creates or truncates a file (the boot's set-aside is
// not borrowed).
func (oi *offlineIdentity) readProof(ctx context.Context, bks storage.Keyspace) journalProof {
	in := oi.reapply()
	path := oi.journalPath()
	no := func(chain *journal.Chain, why string) journalProof {
		if chain == nil {
			chain = &journal.Chain{}
		}
		return journalProof{chain: chain, whyNot: why}
	}
	head, err := journal.ReadHead(ctx, bks)
	if err != nil {
		return no(nil, "the store's journal head cannot be read")
	}
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return no(nil, "the journal file does not exist")
	case err != nil:
		return no(nil, "the journal file cannot be examined")
	case !fi.Mode().IsRegular():
		return no(nil, "the journal path is not a regular file")
	}
	chain := in.readChain(path, head)
	switch {
	case chain.Break != nil:
		return no(chain, "the journal does not verify past a point ("+string(chain.Break.Reason)+")")
	case !chain.HasHeader:
		return no(chain, "the journal file is empty or has no verified header")
	case otherInstall(chain, oi.install):
		return no(chain, "the journal file is another install's")
	}
	loc, have, lerr := journal.ReadLocation(oi.k.cfg.StateDir, journal.WithFS(oi.jfs))
	switch {
	case lerr != nil:
		return no(chain, "backup.location.json cannot be read")
	case !have || !loc.Describes(oi.k.cfg.StoragePath, oi.install):
		return no(chain, "backup.location.json does not describe this store")
	}
	chain = in.checkAnchor(chain, loc, true)
	var res reapplyResult
	if in.untrusted(chain, head, &res) || res.Broken != nil || chain.Break != nil {
		return no(chain, "the journal does not agree with the store's committed head")
	}
	if head.Seq > 0 {
		if mac, ok := chain.MACAt(head.Seq); !ok || mac != head.MAC {
			return no(chain, "the journal does not hold the line the store committed at its head")
		}
	}
	return journalProof{chain: chain}
}

// classifyPoison decides what clearing an undecodable row does. A poison row
// is a pointer whose Closed flag and mac are lost, and the revocation it
// pointed at may never have been answered by the boot's fail-closed pass, so it
// is never simply deleted: when the verified journal holds a line at the seq
// the key names the row is rebuilt from that line (Closed false); when it does
// not, or the journal cannot be trusted, clearing fails closed first.
func classifyPoison(e JournalQueueEntry, r journal.UnappliedRow, proof journalProof) JournalQueueEntry {
	e.State = QueuePoison
	if proof.whyNot == "" {
		if l, ok := proof.chain.LineAt(r.Seq); ok {
			e.Clear, e.repair = ClearRepair, l
			e.Kind, e.At = l.Kind, l.At
			e.Reason = fmt.Sprintf("the row does not decode; the verified journal holds a line at seq %d (kind %s), so clear replaces the row with an entry for it (not yet failed closed for) and the next plain boot answers it; nothing is lost", r.Seq, l.Kind)
			return e
		}
		e.Clear = ClearFailClosed
		e.Reason = fmt.Sprintf("the row does not decode and the verified journal holds no line at seq %d, so the revocation it pointed at cannot be named; clear removes it after failing closed (every API key needs_confirmation, every credential epoch bumped, every session deleted)", r.Seq)
		return e
	}
	e.Clear = ClearFailClosed
	e.Reason = fmt.Sprintf("the row does not decode and the journal cannot name the line it pointed at (%s); clear removes it after failing closed (every API key needs_confirmation, every credential epoch bumped, every session deleted)", proof.whyNot)
	return e
}

// classifyRow is the state of one row under proof.
func classifyRow(r journal.UnappliedRow, proof journalProof) JournalQueueEntry {
	e := JournalQueueEntry{Key: r.Key, Seq: r.Seq, row: r}
	switch {
	case r.Malformed:
		e.State, e.Reason = QueueMalformedKey, "the key is not journal_unapplied/<seq>, so no seq names it"
		return e
	case r.Err != nil:
		return classifyPoison(e, r, proof)
	}
	e.Kind, e.At, e.Closed = r.Entry.Kind, r.Entry.At, r.Entry.Closed
	// The boot reads an entry by its own seq and mac, never its key's, so that
	// is what is judged here; a key that names another seq is only noted.
	note := ""
	if r.Entry.Seq != r.Seq {
		note = fmt.Sprintf(" (the key names seq %d, the entry seq %d; the boot reads the entry's)", r.Seq, r.Entry.Seq)
	}
	if l, ok := proof.chain.LineAt(r.Entry.Seq); ok && l.MAC == r.Entry.MAC {
		e.State, e.Reason = QueuePending, "the journal holds this line; a build that knows kind "+string(r.Entry.Kind)+" applies it at its next boot"+note
		return e
	}
	if proof.whyNot != "" {
		e.State, e.Reason = QueueUnverified, proof.whyNot+", so the entry cannot be shown to be unreachable"+note
		return e
	}
	e.Clear = ClearRemove
	e.State, e.Reason = QueueOrphan, "the verified journal holds no line at seq "+fmt.Sprint(r.Entry.Seq)+" with this mac (dropped by retention, or another history); nothing can ever apply it"+note
	return e
}
