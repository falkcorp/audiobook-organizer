// file: core/backup/journal/file.go
// version: 2.3.0
// guid: b701597d-6d06-4dd4-af78-0e0d6a9983ca
// last-edited: 2026-11-12

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// Owner is the owner of the c/backup/ core keyspace (06 §6.4.10 F4 row: the
// kernel registers StorageSpec under owner backup; backup adopts it in the
// backup flip).
const Owner storage.Owner = "core/backup"

// The c/backup/ keys of this package (05-backup §5B.3.6), relative to the
// keyspace and written as the spec names them. restore_hooked/<seq> (a
// hook run's completion record), journal_unapplied/<seq> (the queue of lines this
// build could not apply) and restore_run are the kernel's writers; they are
// declared here because StorageSpec carries them.
const (
	headKey       = "journal_head"    // Serialized: Head {seq, mac}, staged by Append through GetForUpdate
	servedBootKey = "served_boot"     // Serialized: ServedBoot {n, nonce}, every write a GetForUpdate (COR6-6)
	gapPrefix     = "journal_gap/"    // journal_gap/<since>: an interval whose revocation the journal may lack
	hookedPrefix  = "restore_hooked/" // restore_hooked/<seq>: the hooks of the restore line <seq> ran to completion
	// unappliedPrefix: journal_unapplied/<seq> is a revocation line of a kind this build cannot
	// apply, queued so the head can pass it (unapplied.go): the first build that knows the kind
	// applies it. restore_run (hookrun.go) is the ID of a restore hook run that has no restore line.
	unappliedPrefix = "journal_unapplied/"
	brokenLatchKey  = "journal_broken_closed"
	gapReasonAppend = "append_failed"
)

// StorageSpec returns the c/backup/ storage.Spec the kernel registers from the
// identity flip (COR5-6; 06 §6.4.10 F4 row): a keyspace whose journal_head and
// served_boot are Serialized, so every write of either carries an Expect, and
// whose other families, journal_gap/<since>, restore_hooked/<seq>,
// journal_unapplied/<seq> and restore_run, are plain writes. No migration: the
// keyspace is new in R2.
func StorageSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Serialized: []string{headKey, servedBootKey}}
}

// ErrCorrupt reports a journal file whose chain does not verify (a line that
// does not decode, a seq out of order, a mac that does not match, a derive
// key the keyring no longer holds), or a file that ends below the store's
// committed c/backup/journal_head or does not hold the line the store
// committed there (deleted, emptied, truncated or replaced). §5B.5's failure
// table treats these as one condition, "chain broken or truncated below the
// live store's head": the app stays open, coverage is closed, and the boot
// reports revocation_journal_broken. The journal is never renumbered from the
// head: every line's seq is the previous line's + 1 (§5B.3.6 row "Format and
// integrity"), so numbering on would forge a chain. The boot's reaction
// (fail closed, report) is the P4d step 2 of core/kernel/restore_reapply.go.
var ErrCorrupt = errors.New("journal: the revocation journal is corrupt")

// ErrOtherInstall reports a journal whose header names another install than
// the store's (§5B.3.6 "Boot re-apply" (0)): its lines are never used and
// never extended. The boot sets such a file aside before it opens a journal.
var ErrOtherInstall = errors.New("journal: the revocation journal belongs to another install")

// ErrLocked reports a journal another open File holds (flock(2) LOCK_EX on
// the file, in this process or another): two writers would reuse seqs.
var ErrLocked = errors.New("journal: another writer holds the revocation journal")

// ErrClosed is returned by Append after Close.
var ErrClosed = errors.New("journal: the revocation journal is closed")

// ErrNotRegular reports a journal path that is not a regular file: a symbolic
// link, a directory, a device or a pipe. The journal usually sits on a share
// other hosts can write to, so a name planted there must never make the boot
// open, cut or extend whatever it points at (05-backup §5B.3.6 row "Format and
// integrity"): the journal is read and written only as a regular file reached
// without following a link at its last component.
var ErrNotRegular = errors.New("journal: the journal path is not a regular file")

// FileOptions is what the kernel opens the file journal with (coined: the
// spec names no constructor).
type FileOptions struct {
	// Path is the journal file, <backup dir>/<prefix>revocations.journal
	// (§5B.3.6 row "Where"); the kernel resolves it and records it in
	// backup.location.json.
	Path string
	// Keyspace is the c/backup/ handle (StorageSpec registered under Owner).
	Keyspace storage.Keyspace
	// Source is the writing binary's ID, written into a line whose Source is
	// empty (app.BinaryID; R5-7).
	Source string
	// Keys derives the line MAC's key; required. There is no unauthenticated
	// mode: a journal whose lines carry no proof would defeat §5B.3.6's
	// integrity rule.
	Keys Keys
	// InstallID is the store's c/storage/install, required: the header names
	// it, and a file whose header names another install is refused
	// (ErrOtherInstall).
	InstallID string
	// App is the app's archive prefix, written into a new header.
	App string
	// Clock stamps a new header; nil is time.Now.
	Clock func() time.Time

	// wrap, when set, wraps the opened file (the failure tests' seam; unset
	// outside this package's tests, and not settable from outside it).
	wrap func(lineFile) lineFile
}

// File is the file-backed RevocationJournal: one authenticated JSON line per
// revocation, fsynced before Append returns, with the store's head staged
// into the caller's batch. Safe for concurrent use; appends are serialised
// by one mutex (§5B.6), and one File per journal file at a time across
// processes (flock(2), ErrLocked), so no two writers number the same file.
//
// The line encoding (sealLine, sealHeader) and the file open (openJournal)
// each live in one place, and every write goes through writeLine, so there
// is no second writer.
type File struct {
	mu      sync.Mutex
	path    string
	f       lineFile
	size    int64  // bytes of complete, committed-to-disk lines
	last    uint64 // seq of the last line
	lastMAC string // the mac the next line chains from
	ks      storage.Keyspace
	keys    Keys
	source  string
	install string
	app     string
	now     func() time.Time
	header  Header
	broken  error // a failed append that could not be rolled back, or an open that failed: every later Append fails
	closed  bool

	// gaps are the starts of the intervals Gap recorded that no line has
	// closed yet; closing are those a line closed whose journal_gap/ record
	// is deleted again in the next Append's batch (the batch that staged the
	// first delete may have failed to commit).
	gaps    []time.Time
	closing []time.Time
}

var _ RevocationJournal = (*File)(nil)

// lineFile is what File needs of *os.File (a seam for the failure tests).
type lineFile interface {
	Write(p []byte) (int, error)
	Sync() error
	Truncate(size int64) error
	Close() error
}

// OpenFile opens the journal at o.Path, creating it (mode 0600, header line)
// when absent or empty, verifying its whole chain when not, and holds it
// exclusively (ErrLocked while another File holds it) until Close. An
// incomplete last line, left by a crash inside a write that never returned
// and so never let its revocation commit, is cut off.
//
// One rule for every failure: with a path, the c/backup/ keyspace, Keys and
// an install ID, OpenFile always returns a File, and a non-nil error means
// that File is broken. Its Append fails with the error and stages nothing,
// its Gap works (Gap needs only the keyspace, never the file), and Close
// releases what it holds. A disk problem never blocks a security action and
// never stops a gap being recorded (§5B.3.6 rows "Append failure" and
// "Writers"; §5B.5 keeps the app open on a broken journal), so the revoking
// callers always have a Gap to call. The failures:
//
//   - the store's journal_head or journal_gap/ records cannot be read;
//   - the file cannot be opened, locked (ErrLocked; errors.ErrUnsupported
//     where flock(2) is absent) or read;
//   - ErrCorrupt: the chain does not verify (see ErrCorrupt), the file ends
//     below the store's committed journal_head, or the line at that head is
//     not the one the store committed; nothing is written to such a file;
//   - ErrOtherInstall: the header names another install; nothing is written;
//   - the incomplete last line cannot be cut, or the header line cannot be
//     written or made durable (a full disk).
//
// When the file was opened, the broken File keeps it locked until Close, so
// no second writer numbers it meanwhile; the kernel closes it before it sets
// the file aside or reopens the path. Only a caller's mistake (an empty path,
// another owner's keyspace, no Keys, no install ID) returns a nil File.
func OpenFile(ctx context.Context, o FileOptions) (*File, error) {
	switch {
	case o.Path == "":
		return nil, errors.New("journal: OpenFile without a path")
	case o.Keyspace.Owner() != Owner:
		return nil, fmt.Errorf("journal: OpenFile needs the %s keyspace, got %q", Owner, o.Keyspace.Owner())
	case o.Keys == nil:
		return nil, errors.New("journal: OpenFile without Keys (there is no unauthenticated journal)")
	case o.InstallID == "":
		return nil, errors.New("journal: OpenFile without the store's install ID")
	}
	j := &File{path: o.Path, ks: o.Keyspace, keys: o.Keys, source: o.Source, install: o.InstallID, app: o.App, now: o.Clock}
	if j.now == nil {
		j.now = time.Now
	}
	head, err := ReadHead(ctx, o.Keyspace)
	if err != nil {
		return j.fail(fmt.Errorf("journal: read %s: %w", headKey, err))
	}
	open, err := readGaps(ctx, o.Keyspace)
	if err != nil {
		return j.fail(fmt.Errorf("journal: read %s records: %w", gapPrefix, err))
	}
	f, data, err := openJournal(o.Path)
	if err != nil {
		return j.fail(err)
	}
	j.f = f
	if o.wrap != nil {
		j.f = o.wrap(f)
	}
	chain := ReadChain(data, o.Keys)
	switch {
	case chain.Break != nil:
		return j.fail(fmt.Errorf("%s: %w: %w", o.Path, ErrCorrupt, chain.Break))
	case chain.HasHeader && chain.Header.InstallID != o.InstallID:
		return j.fail(fmt.Errorf("%s: %w: it names install %q, the store's is %q", o.Path, ErrOtherInstall, chain.Header.InstallID, o.InstallID))
	}
	if end := chain.IntactEnd(); head.Seq > end {
		return j.fail(fmt.Errorf("%w: %s ends at seq %d, below the store's %s %d (the file was deleted, truncated or replaced)",
			ErrCorrupt, o.Path, end, headKey, head.Seq))
	}
	if head.MAC != "" {
		if got, ok := chain.MACAt(head.Seq); ok && !macEqual(got, head.MAC) {
			return j.fail(fmt.Errorf("%w: %s holds another line at seq %d than the store's %s committed (the file was replaced)",
				ErrCorrupt, o.Path, head.Seq, headKey))
		}
	}
	if chain.Torn > 0 {
		if err := errors.Join(j.f.Truncate(chain.ValidLen), j.f.Sync()); err != nil {
			return j.fail(fmt.Errorf("journal: cut the incomplete last line of %s: %w", o.Path, err))
		}
	}
	j.size = chain.ValidLen
	if chain.HasHeader {
		j.header, j.last, j.lastMAC = chain.Header, chain.IntactEnd(), chain.PrevMAC()
		j.adoptGaps(chain, open)
		return j, nil
	}
	j.adoptGaps(chain, open)
	hdr := Header{InstallID: o.InstallID, App: o.App, StartedAt: j.now().UTC(), KID: o.Keys.ActiveDeriveID()}
	line, hdr, err := sealHeader(o.Keys, hdr)
	if err != nil {
		return j.fail(err)
	}
	if err := j.writeLine(line); err != nil {
		return j.fail(err)
	}
	if err := syncDir(filepath.Dir(o.Path)); err != nil {
		return j.fail(err)
	}
	j.header, j.last, j.lastMAC = hdr, 0, hdr.MAC
	return j, nil
}

// adoptGaps sets the intervals the journal still owes a closing line: every
// journal_gap/ record in the store with no gap line for its start in the
// journal (the records a line already closed are only left to delete again).
func (j *File) adoptGaps(chain *Chain, records []time.Time) {
	closed := map[int64]bool{}
	for _, l := range chain.Lines {
		if t, ok := gapSince(l.Revocation); ok {
			closed[t.UnixNano()] = true
		}
	}
	for _, t := range records {
		if closed[t.UnixNano()] {
			j.closing = append(j.closing, t)
		} else {
			j.gaps = append(j.gaps, t)
		}
	}
}

// fail marks j broken with err and returns both: OpenFile's one failure
// shape. (A header write whose rollback failed already set j.broken; the
// error writeLine returned joins that rollback error, so err replaces it.)
func (j *File) fail(err error) (*File, error) {
	j.broken = err
	return j, err
}

// openJournal is the one place the journal file is opened: read-write,
// append-only writes, created 0600, never through a symbolic link, required to
// be a regular file (ErrNotRegular), locked exclusively (lockJournal) before
// anything is read, and its current content.
func openJournal(path string) (*os.File, []byte, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND|openNoFollow, 0o600)
	if err != nil {
		return nil, nil, openError("open", path, err)
	}
	if err := regularFile(f); err != nil {
		return nil, nil, errors.Join(err, f.Close())
	}
	if err := lockJournal(f); err != nil {
		return nil, nil, errors.Join(err, f.Close())
	}
	data, err := readCapped(f, path)
	if err != nil {
		return nil, nil, errors.Join(err, f.Close())
	}
	return f, data, nil
}

// maxJournalBytes is the largest journal file the boot reads. A line is a few
// hundred bytes, so this is hundreds of thousands of revocations; a file past it
// is not a journal this build wrote, and reading it whole (it is read into
// memory, in a boot that has not yet bound the journal) would take the process
// down with it. It is an ErrCorrupt, which the boot treats like any chain it
// cannot read: it fails closed once and reports the journal broken.
const maxJournalBytes = 256 << 20

// readCapped reads f whole, refusing more than maxJournalBytes.
func readCapped(f *os.File, path string) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("journal: stat %s: %w", path, err)
	}
	return readLimited(f, path, fi.Size(), maxJournalBytes)
}

// readLimited reads r whole, which is size bytes by its own account, and
// refuses it past limit: by the size alone (nothing is read), and again while
// reading, for a file that grew since it was measured.
func readLimited(r io.Reader, path string, size, limit int64) ([]byte, error) {
	if size > limit {
		return nil, fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit", ErrCorrupt, path, size, limit)
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("journal: read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s grew past the %d-byte limit while it was read", ErrCorrupt, path, limit)
	}
	return data, nil
}

// openError is the error of failing to open path for op: a symbolic link at the
// path (the open refuses to follow it) is ErrNotRegular, anything else is the
// open's own error.
func openError(op, path string, err error) error {
	if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symbolic link", ErrNotRegular, path)
	}
	return fmt.Errorf("journal: %s %s: %w", op, path, err)
}

// regularFile reports ErrNotRegular for an open file that is not a regular
// file. It runs on the descriptor, after the open, so the check and the use are
// of the same object.
func regularFile(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("journal: stat %s: %w", f.Name(), err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is a %s", ErrNotRegular, f.Name(), fileKind(fi.Mode()))
	}
	return nil
}

// fileKind names a non-regular mode for an error.
func fileKind(m os.FileMode) string {
	switch {
	case m.IsDir():
		return "directory"
	case m&os.ModeSymlink != 0:
		return "symbolic link"
	case m&os.ModeNamedPipe != 0:
		return "named pipe"
	case m&os.ModeSocket != 0:
		return "socket"
	case m&os.ModeDevice != 0:
		return "device"
	}
	return "special file"
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("journal: open %s: %w", dir, err)
	}
	return errors.Join(d.Sync(), d.Close())
}

// writeLine is the one writer: it appends data (one line or several) and
// fsyncs, or rolls the file back to its previous length and reports the
// error.
func (j *File) writeLine(data []byte) error {
	prev := j.size
	n, err := j.f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = j.f.Sync()
	}
	if err != nil {
		return errors.Join(fmt.Errorf("journal: append to %s: %w", j.path, err), j.rollback(prev))
	}
	j.size = prev + int64(len(data))
	return nil
}

// rollback cuts the file back to size bytes, so a line whose revocation will
// not be recorded leaves nothing behind. When the cut fails the journal is
// broken: a later Append would reuse a seq, so every one fails instead and
// the callers record gaps.
func (j *File) rollback(size int64) error {
	if err := errors.Join(j.f.Truncate(size), j.f.Sync()); err != nil {
		j.broken = fmt.Errorf("journal: %s may hold a line no store committed and could not be cut back: %w", j.path, err)
		return j.broken
	}
	j.size = size
	return nil
}

// headRecord is c/backup/journal_head's value.
type headRecord struct {
	Seq uint64 `json:"seq"`
	MAC string `json:"mac,omitempty"`
}

// ReadHead reads the committed c/backup/journal_head through ks: the zero
// Head when the store has none (it predates the journal, or is an older
// archive).
func ReadHead(ctx context.Context, ks storage.Keyspace) (Head, error) {
	h, err := decodeHead(ks.Get(ctx, []byte(headKey)))
	return Head(h), err
}

// decodeHead decodes a journal_head read; absent is seq 0.
func decodeHead(raw []byte, err error) (headRecord, error) {
	var h headRecord
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return h, nil
	case err != nil:
		return h, err
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return h, fmt.Errorf("decode %s: %w", headKey, err)
	}
	return h, nil
}

// StageHead stages c/backup/journal_head = max(head, h) into b, reading the
// head with GetForUpdate (journal_head is Serialized), so a head never moves
// backwards however batches commit. It is how a re-apply batch writes the new
// head beside the rows it changed (§5B.3.6 "Boot re-apply" (3)): a conflict
// retries the batch.
func StageHead(ctx context.Context, b *storage.Batch, ks storage.Keyspace, h Head) error {
	if err := bind(b, ks); err != nil {
		return err
	}
	cur, err := decodeHead(b.GetForUpdate(ctx, ks, []byte(headKey)))
	if err != nil {
		return fmt.Errorf("journal: read %s: %w", headKey, err)
	}
	if cur.Seq >= h.Seq {
		return nil
	}
	return setHead(b, ks, h)
}

// setHead stages journal_head = h into b.
func setHead(b *storage.Batch, ks storage.Keyspace, h Head) error {
	v, err := json.Marshal(headRecord(h))
	if err != nil {
		return err
	}
	return b.In(ks).Set([]byte(headKey), v)
}

// bind joins the c/backup/ handle to b. Bind poisons b instead of binding a
// handle of another store, and b.In would then panic, so it is checked.
func bind(b *storage.Batch, ks storage.Keyspace) error {
	if !slices.Contains(b.Bind(ks).Participants(), Owner) {
		return fmt.Errorf("journal: the batch refused the %s handle (a batch of another store?): %w", Owner, storage.ErrOutsideKeyspace)
	}
	return nil
}

// Append reads c/backup/journal_head with GetForUpdate, writes r, numbered
// with the next seq and sealed with its mac, as one fsynced line before the
// caller commits, then stages c/backup/journal_head = {seq, mac} into b. When
// a Gap was recorded since the last successful Append, the gap's closing
// `gap` line is written first, in the same fsynced write, and the closed
// interval's journal_gap/ record is deleted in b (§5B.3.6 row "Append
// failure"). A head at or past the next seq means the file is behind the store
// (another writer, or a file replaced under this one): nothing is written, the
// journal is broken (ErrCorrupt) and every later Append fails, so no seq at or
// below a head consumers have applied is ever assigned. On any error nothing
// is staged and the file holds no new line (the Expect the head read recorded
// in b is the one trace: it only makes a concurrent head change retry the
// batch); the caller commits its revocation with Gap(b, at).
func (j *File) Append(ctx context.Context, b *storage.Batch, r Revocation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b == nil {
		return errors.New("journal: Append without a batch")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	switch {
	case j.closed:
		return ErrClosed
	case j.broken != nil:
		return j.broken
	}
	if err := bind(b, j.ks); err != nil {
		return err
	}
	cur, err := decodeHead(b.GetForUpdate(ctx, j.ks, []byte(headKey)))
	if err != nil {
		return fmt.Errorf("journal: read %s: %w", headKey, err)
	}
	if cur.Seq >= j.last+1 {
		j.broken = fmt.Errorf("%w: the store's %s is %d but %s ends at seq %d: another writer, or the file was replaced",
			ErrCorrupt, headKey, cur.Seq, j.path, j.last)
		return j.broken
	}
	data, closed, h, err := j.prepare(r)
	if err != nil {
		return err
	}
	stale, err := j.liveClosing(ctx)
	if err != nil {
		return err
	}
	prev := j.size
	if err := j.writeLine(data); err != nil {
		return err
	}
	if err := setHead(b, j.ks, h); err != nil {
		return errors.Join(fmt.Errorf("journal: stage the head for seq %d: %w", h.Seq, err), j.rollback(prev))
	}
	if err := stageGapDeletes(b, j.ks, append(slices.Clone(stale), closed...)); err != nil {
		return errors.Join(err, j.rollback(prev))
	}
	j.commitLines(h, stale, closed)
	return nil
}

// prepare seals the lines one append writes, without touching the file or
// the in-memory chain position: the closing gap lines of every open interval,
// then r. It returns the bytes, the intervals they close and the head r's
// line gives.
func (j *File) prepare(r Revocation) (data []byte, closed []time.Time, h Head, err error) {
	if r.Source == "" {
		r.Source = j.source
	}
	seq, prevMAC := j.last, j.lastMAC
	closed = slices.Clone(j.gaps)
	for _, since := range closed {
		seq++
		g := Revocation{Seq: seq, At: j.now().UTC(), Kind: Gap, Source: j.source,
			Detail: []string{since.UTC().Format(time.RFC3339Nano), gapReasonAppend}}
		line, mac, err := sealLine(j.keys, prevMAC, g)
		if err != nil {
			return nil, nil, Head{}, err
		}
		data, prevMAC = append(data, line...), mac
	}
	seq++
	r.Seq = seq
	line, mac, err := sealLine(j.keys, prevMAC, r)
	if err != nil {
		return nil, nil, Head{}, err
	}
	return append(data, line...), closed, Head{Seq: seq, MAC: mac}, nil
}

// liveClosing is the intervals an earlier line closed whose journal_gap/
// record the store still holds: the batch that staged the delete did not
// commit (or has not yet), so the next Append stages it again. A record the
// store no longer holds is dropped, so the set stays as small as the number of
// deletes that have not landed.
func (j *File) liveClosing(ctx context.Context) ([]time.Time, error) {
	var live []time.Time
	for _, since := range j.closing {
		_, err := j.ks.Get(ctx, gapKey(since))
		switch {
		case errors.Is(err, storage.ErrNotFound):
		case err != nil:
			return nil, fmt.Errorf("journal: read the gap record from %s: %w", since.UTC().Format(time.RFC3339Nano), err)
		default:
			live = append(live, since)
		}
	}
	return live, nil
}

// stageGapDeletes stages the deletion of the journal_gap/ records of the
// intervals a line closed.
func stageGapDeletes(b *storage.Batch, ks storage.Keyspace, closed []time.Time) error {
	for _, since := range closed {
		if err := b.In(ks).Delete(gapKey(since)); err != nil {
			return fmt.Errorf("journal: stage the close of the gap from %s: %w", since.UTC().Format(time.RFC3339Nano), err)
		}
	}
	return nil
}

// commitLines moves the in-memory chain position past the lines just written:
// stale are the earlier closes whose delete is still owed, closed the ones
// these lines made.
func (j *File) commitLines(h Head, stale, closed []time.Time) {
	j.last, j.lastMAC = h.Seq, h.MAC
	j.closing, j.gaps = append(slices.Clone(stale), closed...), nil
}

// AppendLine writes r, a bookkeeping line (a gap, restore or restore_aborted
// line, §5B.3.6 "Boot re-apply" (5)), as one fsynced line and commits
// c/backup/journal_head = {seq, mac} in a batch of its own, retried on a
// conflict, and returns the head. The line is durable before the head is
// staged, so a failed commit leaves the head lagging the file, the re-apply
// case that never reads as a replaced store.
func (j *File) AppendLine(ctx context.Context, r Revocation) (Head, error) {
	if err := ctx.Err(); err != nil {
		return Head{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	p, err := j.writeLineLocked(ctx, r)
	if err != nil {
		return Head{}, err
	}
	return p.head, p.Commit(ctx)
}

// Pending is a bookkeeping line WriteLine made durable whose head is not in the
// store yet: the head to commit, and the journal_gap/ records the line closed
// (their deletes ride with the head). Staging or committing it is the caller's
// to decide, which is the whole point: a boot that must not move the store's
// head before something durable says why can write the line first and move the
// head last (05-backup §5B.3.6 "Boot re-apply" (3)-(5): the head lags the
// file, never the other way round).
type Pending struct {
	ks      storage.Keyspace
	head    Head
	deletes []time.Time
}

// Head is the head the line gives: what Stage and Commit write.
func (p Pending) Head() Head { return p.head }

// Stage stages c/backup/journal_head = max(its head, the line's) and the deletes
// of the gap records the line closed into b. StageHead reads the head with
// GetForUpdate, so a head never moves backwards however batches commit, and a
// conflict retries the caller's batch.
func (p Pending) Stage(ctx context.Context, b *storage.Batch) error {
	if err := StageHead(ctx, b, p.ks, p.head); err != nil {
		return err
	}
	return stageGapDeletes(b, p.ks, p.deletes)
}

// Commit commits Stage in a batch of its own, retried on a conflict.
func (p Pending) Commit(ctx context.Context) error {
	err := storage.RetryOnConflict(ctx, 5, func(ctx context.Context) (err error) {
		b := p.ks.NewBatch()
		defer func() { err = errors.Join(err, b.Close()) }()
		if err := p.Stage(ctx, b); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
	if err != nil {
		return fmt.Errorf("journal: commit %s for seq %d: %w", headKey, p.head.Seq, err)
	}
	return nil
}

// CommitHead commits c/backup/journal_head = max(its value, h) in a batch of
// its own: the head a boot held back (Pending without the line).
func CommitHead(ctx context.Context, ks storage.Keyspace, h Head) error {
	return Pending{ks: ks, head: h}.Commit(ctx)
}

// WriteLine writes r like AppendLine, one fsynced line, and leaves the head to
// the caller: it returns the Pending that stages or commits it. Until then the
// store's head lags the file, which a boot reads as a store that has not seen
// the line, and nothing else changes. An open Gap's closing line is written
// first, as for any line.
func (j *File) WriteLine(ctx context.Context, r Revocation) (Pending, error) {
	if err := ctx.Err(); err != nil {
		return Pending{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.writeLineLocked(ctx, r)
}

// writeLineLocked is the line write both AppendLine and WriteLine make; j.mu is
// held.
func (j *File) writeLineLocked(ctx context.Context, r Revocation) (Pending, error) {
	switch {
	case j.closed:
		return Pending{}, ErrClosed
	case j.broken != nil:
		return Pending{}, j.broken
	}
	data, closed, h, err := j.prepare(r)
	if err != nil {
		return Pending{}, err
	}
	stale, err := j.liveClosing(ctx)
	if err != nil {
		return Pending{}, err
	}
	if err := j.writeLine(data); err != nil {
		return Pending{}, err
	}
	j.commitLines(h, stale, closed)
	return Pending{ks: j.ks, head: h, deletes: append(slices.Clone(stale), closed...)}, nil
}

// gapRecord is a journal_gap/<since> value.
type gapRecord struct {
	Since  time.Time `json:"since"`
	Reason string    `json:"reason"`
}

// gapKey is journal_gap/<since> with since in fixed-width UTC, so the keys
// sort by time.
func gapKey(since time.Time) []byte {
	return []byte(gapPrefix + since.UTC().Format("2006-01-02T15:04:05.000000000Z"))
}

// readGaps lists the committed journal_gap/ records' starts, in key order.
func readGaps(ctx context.Context, ks storage.Keyspace) ([]time.Time, error) {
	var out []time.Time
	for kv, err := range ks.ScanPrefix(ctx, []byte(gapPrefix)) {
		if err != nil {
			return nil, err
		}
		var g gapRecord
		if err := json.Unmarshal(kv.Value, &g); err != nil {
			return nil, fmt.Errorf("decode %s: %w", kv.Key, err)
		}
		out = append(out, g.Since.UTC())
	}
	return out, nil
}

// OpenGaps reports whether journal_gap/ records exist in ks that no journal
// line closes yet is not knowable from the store alone; this reports whether
// the store holds any journal_gap/ record, which is §5B.3.6 "Coverage at
// restore"'s "open c/backup/journal_gap/ record": the line that closes an
// interval deletes its record in the same batch.
func OpenGaps(ctx context.Context, ks storage.Reader) (bool, error) {
	for _, err := range ks.Scan(ctx, storage.ScanOptions{Start: []byte(gapPrefix), End: []byte(gapPrefix + "\xff"), KeysOnly: true, Limit: 1}) {
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// stageGapRecord stages c/backup/journal_gap/<since> into b: the interval from
// since may hold a revocation the journal lacks. It needs only the keyspace.
func stageGapRecord(b *storage.Batch, ks storage.Keyspace, since time.Time) error {
	if b == nil {
		return errors.New("journal: Gap without a batch")
	}
	v, err := json.Marshal(gapRecord{Since: since.UTC(), Reason: gapReasonAppend})
	if err != nil {
		return err
	}
	if err := bind(b, ks); err != nil {
		return err
	}
	return b.In(ks).Set(gapKey(since), v)
}

// Gap stages c/backup/journal_gap/<since> into b: the interval from since
// may hold a revocation the journal lacks (§5B.3.6 row "Append failure"). It
// touches no file, so it works when Append cannot, and it makes the next
// successful Append write the closing `gap` line.
func (j *File) Gap(b *storage.Batch, since time.Time) error {
	if err := stageGapRecord(b, j.ks, since); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if s := since.UTC(); !slices.ContainsFunc(j.gaps, s.Equal) {
		j.gaps = append(j.gaps, s)
		slices.SortFunc(j.gaps, time.Time.Compare)
	}
	return nil
}

// Status is the journal's health as the component reports it.
type Status struct {
	// Broken is the error that makes every Append fail: nil for a healthy
	// journal. errors.Is(Broken, ErrCorrupt) separates a broken chain
	// (revocation_journal_broken) from a write that failed
	// (revocation_journal_unwritable).
	Broken error
	// OpenGaps is the number of intervals with no closing gap line yet.
	OpenGaps int
}

// Status reports whether the journal can take a line and how many gaps it
// still owes a closing line.
func (j *File) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return Status{Broken: j.broken, OpenGaps: len(j.gaps)}
}

// Path is the journal file's path.
func (j *File) Path() string { return j.path }

// Header is the journal's header, as written or read at open (zero for a File
// whose open failed before it was read).
func (j *File) Header() Header {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.header
}

// Tail is the last line this File knows: the seq and mac the next line
// chains after.
func (j *File) Tail() Head {
	j.mu.Lock()
	defer j.mu.Unlock()
	return Head{Seq: j.last, MAC: j.lastMAC}
}

// Close closes the file; later appends fail with ErrClosed.
func (j *File) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	if j.f == nil { // a broken File whose file never opened
		return nil
	}
	return j.f.Close()
}
