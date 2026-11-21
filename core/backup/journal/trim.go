// file: core/backup/journal/trim.go
// version: 0.1.0
// guid: 4f0b9e27-6a13-4c58-b3d2-81e7c05a9d64
// last-edited: 2026-11-20

package journal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// This file is the journal's retention: File.Trim drops the lines no retained
// archive can need and keeps the chain verifiable (05-backup §5B.3.6 row
// "Retention"). backup's retention calls it through Options.PruneJournal with
// the smallest boundary any retained archive records.
//
// What survives a trim to seq is exactly the lines after seq, as the bytes they
// were written with. The line at seq itself is dropped too, and stays on the
// chain as the header's base: base_seq = seq and base_mac = that line's mac, so
// a restore whose boundary is seq (it needs the lines after it) is still
// covered, and one whose boundary is below seq is not (Chain.Cover).
//
// Kept lines are never re-signed. The store's journal_head, an unapplied-queue
// entry and the location file's anchor each name a line by its mac, so a line
// that changed mac would read as a replaced file at the next open. The new
// header says so (Header.ChainBase) and the first kept line chains from the
// dropped line's mac, as it was signed.
//
// A trim holds back from the seq it is asked for, never past it, when going on
// would drop a line something still points at (TrimLimit). It keeps more than
// asked and says why in TrimResult.Limit; it never keeps less.

// TrimLimit names what held a trim back from the seq it was asked for.
type TrimLimit string

const (
	// TrimLimitNone: the trim went as far as it was asked.
	TrimLimitNone TrimLimit = ""
	// TrimLimitTail: the seq is past the journal's last line.
	TrimLimitTail TrimLimit = "tail"
	// TrimLimitHead: the seq is past the store's committed journal_head. The
	// lines above the head are written and not yet applied to the store, so a
	// boot may still have to re-apply them.
	TrimLimitHead TrimLimit = "head"
	// TrimLimitUnapplied: a line a newer build wrote and this one queued
	// (c/backup/journal_unapplied/<seq>) is still waiting for the build that
	// knows its kind; its entry is a pointer into the journal, not a copy.
	TrimLimitUnapplied TrimLimit = "unapplied"
	// TrimLimitGap: a gap line closed an interval whose c/backup/journal_gap/
	// record the store still holds (the delete of it has not committed). A boot
	// that finds the record and not the line would write the closing line again.
	TrimLimitGap TrimLimit = "gap"
	// TrimLimitRestore: a restore line whose hooks have not run to completion
	// (no c/backup/restore_hooked/<seq>) and that no restore_aborted line closes.
	// The next boot resumes it, and finds its own line by it.
	TrimLimitRestore TrimLimit = "restore"
)

// TrimResult is what one Trim did.
type TrimResult struct {
	// Requested is the seq Trim was asked to drop up to.
	Requested uint64
	// Base is the journal's base_seq afterwards: the seq of the last line dropped
	// so far (0 for a journal that never dropped one). It is Requested when
	// nothing held the trim back and nothing was dropped before.
	Base uint64
	// Dropped is the number of lines this call removed; Kept the number the
	// journal holds after its header when the call returns (all of them, for a
	// call that dropped none).
	Dropped, Kept int
	// Limit is what held the trim back below Requested; TrimLimitNone when
	// nothing did.
	Limit TrimLimit
}

// trimSuffix is the temporary file a trim writes beside the journal.
const trimSuffix = ".trimming"

// Trim drops the lines at or below seq: it rewrites the journal (a temporary
// file beside it, renamed over it) with a header whose base_seq and base_mac are
// the last dropped line's, and the lines after it unchanged. Nothing is dropped
// when seq is not above the header's base_seq, which makes a repeated call a
// no-op that writes nothing.
//
// The seq to drop to is lowered, never raised, to the lowest of: the journal's
// last line, the store's committed journal_head, the line before the oldest
// unapplied-queue entry, the line before a gap line whose journal_gap/ record is
// still in the store, and the line before a restore line that is neither hooked
// nor aborted (TrimLimit; TrimResult.Limit names the one that bound). The caller
// owns the rest of the retention rule: it must not ask while a restore is
// pending or undo-able, and seq must be the smallest boundary any retained
// archive records.
//
// The file is replaced atomically, and the replacement is locked before the
// rename, so the journal is never open to a second writer and a crash leaves
// the old journal or the new one, both whole; a temporary file the crash left
// is removed by the next trim. A failure before the rename leaves the journal
// untouched and usable. When the directory fsync after the rename fails, the
// new file is what the File appends to from here on, but its name is not known
// to be durable, so the File is broken (every Append fails, the callers record
// gaps) until the boot reopens it, as for a rollback that cannot be cut back.
//
// A journal that is closed or broken, or whose file does not match what this File
// holds, is refused (the last also breaks the File: ErrCorrupt). Appends wait
// while a trim runs: the file is read, verified, rewritten and verified again
// under the journal's mutex, because a line appended in between would be lost.
// The chain is a sequential MAC chain, so none of it can be spread across cores.
func (j *File) Trim(ctx context.Context, seq uint64) (TrimResult, error) {
	if err := ctx.Err(); err != nil {
		return TrimResult{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	res := TrimResult{Requested: seq, Base: j.header.BaseSeq}
	switch {
	case j.closed:
		return res, ErrClosed
	case j.broken != nil:
		return res, j.broken
	}
	data, err := j.fsys.ReadFile(j.path)
	if err != nil {
		return res, fmt.Errorf("journal: read %s to trim it: %w", j.path, err)
	}
	if int64(len(data)) > maxJournalBytes {
		return res, fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit", ErrCorrupt, j.path, len(data), maxJournalBytes)
	}
	c := ReadChain(data, j.keys)
	if err := j.holdsOurChain(c); err != nil {
		j.broken = err
		return res, err
	}
	cut, limit, err := j.trimTarget(ctx, c, seq)
	if err != nil {
		return res, err
	}
	res.Limit, res.Kept = limit, len(c.Lines)
	if cut <= c.Header.BaseSeq {
		return res, nil
	}
	baseMAC, ok := c.MACAt(cut)
	if !ok {
		return res, fmt.Errorf("%w: %s has no line at seq %d to base the trim on", ErrCorrupt, j.path, cut)
	}
	hdr := Header{InstallID: c.Header.InstallID, App: c.Header.App, StartedAt: c.Header.StartedAt,
		KID: j.keys.ActiveDeriveID(), BaseSeq: cut, BaseMAC: baseMAC, ChainBase: true}
	hline, hdr, err := sealHeader(j.keys, hdr)
	if err != nil {
		return res, err
	}
	lines, _ := completeLines(data)
	kept := lines[1+int(cut-c.Header.BaseSeq):]
	out := make([]byte, 0, len(hline)+len(data))
	out = append(out, hline...)
	for _, l := range kept {
		out = append(append(out, l...), '\n')
	}
	renamed, err := j.replaceWith(out, hdr, cut, baseMAC, len(kept))
	if renamed {
		res.Base, res.Dropped, res.Kept = cut, int(cut-c.Header.BaseSeq), len(kept)
	}
	return res, err
}

// holdsOurChain checks that the file read is the chain this File has been
// appending to: verified, whole, ending where the File's memory ends, with the
// mac the next line chains from and the header it opened with. Anything else
// means a writer that ignored the lock or a file replaced under it, and a trim
// must not rewrite it.
func (j *File) holdsOurChain(c *Chain) error {
	switch {
	case c.Break != nil:
		return fmt.Errorf("%s: %w: %w", j.path, ErrCorrupt, c.Break)
	case !c.HasHeader:
		return fmt.Errorf("%w: %s holds no header although this File opened one", ErrCorrupt, j.path)
	case c.Torn != 0 || c.ValidLen != j.size:
		return fmt.Errorf("%w: %s is %d bytes (%d incomplete), this File wrote %d", ErrCorrupt, j.path, c.ValidLen+c.Torn, c.Torn, j.size)
	case c.IntactEnd() != j.last || !macEqual(c.PrevMAC(), j.lastMAC) || !macEqual(c.Header.MAC, j.header.MAC):
		return fmt.Errorf("%w: %s is not the chain this File holds (it ends at seq %d, this File's last line is %d)", ErrCorrupt, j.path, c.IntactEnd(), j.last)
	}
	return nil
}

// trimTarget is the seq a trim to want may go to, and what lowered it.
func (j *File) trimTarget(ctx context.Context, c *Chain, want uint64) (uint64, TrimLimit, error) {
	cut, limit := want, TrimLimitNone
	lower := func(to uint64, why TrimLimit) {
		if to < cut {
			cut, limit = to, why
		}
	}
	lower(j.last, TrimLimitTail)
	head, err := ReadHead(ctx, j.ks)
	if err != nil {
		return 0, "", fmt.Errorf("journal: read %s: %w", headKey, err)
	}
	lower(head.Seq, TrimLimitHead)
	queued, err := ReadUnapplied(ctx, j.ks)
	if err != nil {
		return 0, "", err
	}
	for _, u := range queued {
		if u.Seq > 0 {
			lower(u.Seq-1, TrimLimitUnapplied)
		}
	}
	records, err := readGaps(ctx, j.ks)
	if err != nil {
		return 0, "", fmt.Errorf("journal: read %s records: %w", gapPrefix, err)
	}
	open := make(map[int64]bool, len(records))
	for _, t := range records {
		open[t.UnixNano()] = true
	}
	aborted := c.AbortedRestores(nil)
	for _, l := range c.Lines {
		if l.Seq > cut {
			break
		}
		switch l.Kind {
		case Gap:
			if t, ok := gapSince(l.Revocation); ok && open[t.UnixNano()] {
				lower(l.Seq-1, TrimLimitGap)
			}
		case Restore:
			if aborted[l.Seq] {
				continue
			}
			hooked, err := Hooked(ctx, j.ks, l.Seq)
			if err != nil {
				return 0, "", fmt.Errorf("journal: read %s%d: %w", hookedPrefix, l.Seq, err)
			}
			if !hooked {
				lower(l.Seq-1, TrimLimitRestore)
			}
		}
	}
	return cut, limit, nil
}

// replaceWith writes out beside the journal, verifies it, and renames it over the
// journal, then moves j onto it. out is the whole new file: its header (hdr, with
// its mac) and the keep lines after base (cut) with baseMAC. renamed is whether
// the journal is the new file, which an error after the rename (the directory
// fsync) does not undo. j.mu is held.
func (j *File) replaceWith(out []byte, hdr Header, cut uint64, baseMAC string, keep int) (renamed bool, err error) {
	tmp := j.path + trimSuffix
	if err := removeLabelled(j.fsys, LabelTrimClear, tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("journal: clear %s: %w", tmp, err)
	}
	nf, created, err := createLocked(j.fsys, tmp, out)
	if err != nil {
		return false, j.dropTemp(tmp, created, err)
	}
	if err := j.verifyCopy(tmp, out, cut, baseMAC, keep); err != nil {
		return false, errors.Join(j.dropTemp(tmp, true, err), nf.Close())
	}
	if err := renameLabelled(j.fsys, LabelTrimRename, tmp, j.path); err != nil {
		return false, errors.Join(j.dropTemp(tmp, true, fmt.Errorf("journal: rename %s over %s: %w", tmp, j.path, err)), nf.Close())
	}
	// The rename is done: the journal is the new file, and the descriptor that
	// holds its lock is the one to append to. The old descriptor is of a file
	// that is no longer anywhere, and a line written to it would be lost.
	old := j.f
	j.f, j.size, j.header = nf, int64(len(out)), hdr
	closeErr := old.Close()
	if err := syncDir(j.fsys, LabelTrimDirSync, filepath.Dir(j.path)); err != nil {
		j.broken = fmt.Errorf("journal: the trimmed %s was renamed into place but its directory could not be made durable, so a crash may bring the old file back under lines appended to this one: %w", j.path, err)
		return true, errors.Join(j.broken, closeErr)
	}
	return true, closeErr
}

// createLocked creates path exclusively (0600, never through a link), takes its
// lock before anything is written to it, writes data and fsyncs, in
// LabelTrimCopy's region. It returns the open file, and whether path was
// created (an error before the create leaves something that is not ours there).
func createLocked(fsys FS, path string, data []byte) (f FSFile, created bool, err error) {
	defer fsys.Region(LabelTrimCopy)()
	f, err = fsys.Open(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|os.O_APPEND|openNoFollow, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("journal: create %s: %w", path, err)
	}
	if err := f.Lock(); err != nil {
		return nil, true, errors.Join(fmt.Errorf("journal: lock %s: %w", path, err), f.Close())
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		return nil, true, errors.Join(fmt.Errorf("journal: write %s: %w", path, err), f.Close())
	}
	return f, true, nil
}

// verifyCopy reads the temporary file back and checks it is the bytes written, a
// chain that verifies from the new header, and the chain it replaces: the same
// last line with the same mac, and the kept lines after the dropped line the
// header names.
func (j *File) verifyCopy(tmp string, want []byte, cut uint64, baseMAC string, keep int) error {
	got, err := j.fsys.ReadFile(tmp)
	if err != nil {
		return fmt.Errorf("journal: read back %s: %w", tmp, err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("journal: the copy at %s does not match what was written", tmp)
	}
	c := ReadChain(got, j.keys)
	switch {
	case c.Break != nil || !c.HasHeader:
		return fmt.Errorf("journal: the copy at %s does not verify (header %t, break %v)", tmp, c.HasHeader, c.Break)
	case c.Header.BaseSeq != cut || !macEqual(c.Header.BaseMAC, baseMAC) || !c.Header.ChainBase:
		return fmt.Errorf("journal: the copy at %s does not start after seq %d", tmp, cut)
	case len(c.Lines) != keep || c.IntactEnd() != j.last || !macEqual(c.PrevMAC(), j.lastMAC):
		return fmt.Errorf("journal: the copy at %s does not end where the journal does (seq %d, %d lines)", tmp, c.IntactEnd(), len(c.Lines))
	}
	return nil
}

// dropTemp removes the temporary file in LabelTrimCleanup's region when the
// trim made it, and returns err joined with the removal's.
func (j *File) dropTemp(tmp string, created bool, err error) error {
	if !created {
		return err
	}
	return errors.Join(err, removeLabelled(j.fsys, LabelTrimCleanup, tmp))
}
