// file: core/backup/journal/file_test.go
// version: 2.0.0
// guid: b7a073e7-211a-4d06-8bf4-6befeb60e0d3
// last-edited: 2026-11-11

package journal

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

// TestStorageSpec_SerializedKeysAndPrefixes: c/backup/ is a keyspace whose
// journal_head and served_boot are Serialized (exact keys) and whose gap
// family is not.
func TestStorageSpec_SerializedKeysAndPrefixes(t *testing.T) {
	s := StorageSpec()
	if !s.Keyspace || !slices.Equal(s.Serialized, []string{"journal_head", "served_boot"}) || len(s.Migrations) != 0 {
		t.Fatalf("StorageSpec() = %+v", s)
	}
	fx := newFixture(t)
	b := fx.ks.NewBatch()
	for _, k := range []string{headKey, servedBootKey} {
		if err := b.Expect(fx.ks, []byte(k), nil); err != nil {
			t.Errorf("%s is not Serialized: %v", k, err)
		}
	}
	b2 := fx.ks.NewBatch()
	if err := b2.Expect(fx.ks, gapKey(time.Now()), nil); !errors.Is(err, storage.ErrNotSerialized) {
		t.Errorf("journal_gap/ Expect: err = %v, want ErrNotSerialized", err)
	}
	if got := string(gapKey(time.Date(2026, 11, 10, 3, 4, 5, 6, time.FixedZone("MT", -7*3600)))); got != "journal_gap/2026-11-10T10:04:05.000000006Z" {
		t.Errorf("gapKey = %q", got)
	}
}

// TestJournal_AppendBeforeCommitHeadMonotone: each line is on disk before its
// revocation commits; two revocations committing out of append order never
// move the head backwards (the later commit conflicts and its retry appends
// again: a duplicate line is harmless, DATA4-7).
func TestJournal_AppendBeforeCommitHeadMonotone(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	ctx := context.Background()

	joker := fx.revoke(t, j, "joker")
	if got := fx.lines(t); len(got) != 2 || !strings.HasPrefix(got[0], `{"seq":0,"install_id":"`+installID+`"`) || !strings.Contains(got[1], `"seq":1`) {
		t.Fatalf("before commit the file holds %q, want the header and line 1", got)
	}
	penguin := fx.revoke(t, j, "penguin")
	must(t, penguin.Commit(ctx, storage.Sync))
	if h := fx.head(t); h.Seq != 2 || h.MAC != fx.chain(t).Lines[1].MAC {
		t.Fatalf("head = %+v after seq 2 committed, want seq 2 and its mac", h)
	}
	if err := joker.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("the out-of-order commit: err = %v, want ErrConflict (the head moved since it was read)", err)
	}
	if h := fx.head(t); h.Seq != 2 {
		t.Fatalf("head = %+v after the stale commit, want still seq 2", h)
	}
	// The caller retries the revoking batch, appending again.
	must(t, storage.RetryOnConflict(ctx, 3, func(ctx context.Context) error {
		return fx.revoke(t, j, "joker").Commit(ctx, storage.Sync)
	}))
	if h := fx.head(t); h.Seq != 3 {
		t.Fatalf("head = %+v after the retry, want seq 3", h)
	}
	if v, err := fx.auth.Get(ctx, []byte("user/joker")); err != nil || string(v) != "disabled" {
		t.Errorf("the revocation itself: %q, %v", v, err)
	}
	c := fx.chain(t)
	if c.Break != nil || len(c.Lines) != 3 {
		t.Fatalf("the chain holds %d lines, break %v; want the header and 3 verified lines", len(c.Lines), c.Break)
	}
	if r := c.Lines[2]; r.Seq != 3 || r.Kind != UserDisabled || r.Subject != (entity.Ref{Module: "core", Kind: "user", ID: "joker"}) || r.Source != "batcomputer-1" || r.KID != "d1" {
		t.Errorf("line 3 = %+v", r)
	}

	// Reopening continues the numbering. (A head past the file's end is
	// refused, never kept and appended under: TestAppend_NeverAtOrBelowHead.)
	must(t, j.Close())
	j2 := fx.open(t)
	must(t, fx.revoke(t, j2, "bane").Commit(ctx, storage.Sync))
	if got := fx.lines(t); !strings.Contains(got[len(got)-1], `"seq":4`) {
		t.Errorf("after reopen the last line is %s, want seq 4", got[len(got)-1])
	}
	if err := j.Append(ctx, fx.auth.NewBatch(), Revocation{Kind: UserDeleted}); !errors.Is(err, ErrClosed) {
		t.Errorf("Append after Close: err = %v, want ErrClosed", err)
	}
}

// failingFile wraps the real file: failWrite writes half of a line and then
// fails (a full disk); failSync refuses fsync.
type failingFile struct {
	lineFile
	failWrite, failSync bool
}

func (f *failingFile) Write(p []byte) (int, error) {
	if f.failWrite {
		n, _ := f.lineFile.Write(p[:len(p)/2])
		return n, errors.New("the Batcave's disk is full")
	}
	return f.lineFile.Write(p)
}

func (f *failingFile) Sync() error {
	if f.failSync {
		return errors.New("the Batcave's disk is flooded")
	}
	return f.lineFile.Sync()
}

// TestJournal_AppendFailureStagesNothing: a failed append records no line
// (the partial write is cut back) and stages nothing into the revoking batch;
// the revocation still commits with Gap's journal_gap/<since> record, and the
// next append numbers on without a gap.
func TestJournal_AppendFailureStagesNothing(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	ctx := context.Background()
	before, err := os.ReadFile(fx.path)
	must(t, err)

	ff := &failingFile{lineFile: j.f, failWrite: true}
	j.f = ff
	b := fx.auth.NewBatch()
	must(t, b.Set([]byte("user/joker"), []byte("disabled")))
	at := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	if err := j.Append(ctx, b, Revocation{At: at, Kind: UserDisabled}); err == nil {
		t.Fatal("Append succeeded on a failing disk")
	}
	after, err := os.ReadFile(fx.path)
	must(t, err)
	if !bytes.Equal(before, after) {
		t.Errorf("the failed append left %q in the file, want it unchanged", after[len(before):])
	}
	// The head read bound c/backup/ to b before the write; nothing is staged
	// there (the head stays absent after the commit, below).
	if b.Count() != 1 || !slices.Equal(b.Participants(), []storage.Owner{authOwner, Owner}) {
		t.Errorf("the failed append staged into the batch: count %d, participants %v", b.Count(), b.Participants())
	}

	must(t, j.Gap(b, at))
	must(t, b.Commit(ctx, storage.Sync))
	if v, err := fx.auth.Get(ctx, []byte("user/joker")); err != nil || string(v) != "disabled" {
		t.Errorf("the revocation did not commit: %q, %v", v, err)
	}
	if v, err := fx.ks.Get(ctx, gapKey(at)); err != nil || !strings.Contains(string(v), `"reason":"append_failed"`) {
		t.Errorf("journal_gap record: %q, %v", v, err)
	}
	if h := fx.head(t); h.Seq != 0 {
		t.Errorf("head = %+v, want none", h)
	}

	// The next successful Append closes the gap in the same fsynced write: a
	// gap line, then the revocation, numbered on without a hole, and the
	// journal_gap/ record is deleted in the revoking batch.
	ff.failWrite = false
	must(t, fx.revoke(t, j, "penguin").Commit(ctx, storage.Sync))
	c := fx.chain(t)
	if c.Break != nil || len(c.Lines) != 2 || c.Lines[0].Kind != Gap || c.Lines[1].Kind != UserDisabled || c.Lines[1].Seq != 2 {
		t.Fatalf("after the failure the chain is %+v (break %v), want a gap line then seq 2", c.Lines, c.Break)
	}
	if d := c.Lines[0].Detail; len(d) != 2 || d[0] != at.Format(time.RFC3339Nano) || d[1] != "append_failed" {
		t.Errorf("the gap line's detail = %q, want [since, append_failed]", d)
	}
	if _, err := fx.ks.Get(ctx, gapKey(at)); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("the closed interval's journal_gap/ record: err = %v, want deleted", err)
	}

	// A failure that cannot be cut back durably (the cut fails, or the
	// fsync after it, the disk's state then unknown) breaks the journal:
	// every later Append fails (the callers record gaps) rather than risk
	// reusing a seq; reopening the file recovers.
	for name, broken := range map[string]lineFile{
		"truncate fails": &truncFails{lineFile: ff.lineFile, failSync: true},
		"fsync fails":    &failingFile{lineFile: ff.lineFile, failSync: true},
	} {
		j.broken = nil
		j.f = broken
		if err := j.Append(ctx, fx.auth.NewBatch(), Revocation{Kind: UserDeleted}); err == nil {
			t.Fatalf("%s: Append succeeded", name)
		}
		if j.broken == nil {
			t.Errorf("%s: the journal is not marked broken", name)
		}
	}
	j.f = ff
	if err := j.Append(ctx, fx.auth.NewBatch(), Revocation{Kind: UserDeleted}); err == nil || !strings.Contains(err.Error(), "could not be cut back") {
		t.Errorf("Append on a broken journal: err = %v", err)
	}
}

type truncFails struct{ failingFile }

func (f *truncFails) Truncate(int64) error { return errors.New("read-only filesystem") }

// TestOpenFile_HeaderAndRecovery: a new journal is created 0600 with the
// header line; an incomplete last line is cut off; a seq out of order is
// ErrCorrupt; a keyspace other than c/backup/ is refused.
func TestOpenFile_HeaderAndRecovery(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	st, err := os.Stat(fx.path)
	must(t, err)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	must(t, fx.revoke(t, j, "joker").Commit(context.Background(), storage.Sync))
	must(t, j.Close())

	f, err := os.OpenFile(fx.path, os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	_, err = f.WriteString(`{"seq":2,"kind":"user.dis`)
	must(t, errors.Join(err, f.Close()))
	j2 := fx.open(t)
	if got := fx.lines(t); len(got) != 2 {
		t.Errorf("the incomplete line was not cut: %q", got)
	}
	must(t, fx.revoke(t, j2, "penguin").Commit(context.Background(), storage.Sync))
	must(t, j2.Close())

	good := fx.lines(t)
	fx.write(t, good[0], "not json")
	fx.openBroken(t, "garbage line", ErrCorrupt)
	fx.write(t, "{\"seq\":0}")
	fx.openBroken(t, "a first line without a mac", ErrCorrupt)

	o := fx.opts()
	for name, mut := range map[string]func(*FileOptions){
		"another owner's keyspace": func(o *FileOptions) { o.Keyspace = fx.auth },
		"an empty path":            func(o *FileOptions) { o.Path = "" },
		"no keys":                  func(o *FileOptions) { o.Keys = nil },
		"no install ID":            func(o *FileOptions) { o.InstallID = "" },
	} {
		oo := o
		mut(&oo)
		if j, err := OpenFile(context.Background(), oo); err == nil || j != nil {
			t.Errorf("OpenFile accepted %s: %v, %v", name, j, err)
		}
	}

	// An empty file for a store with no head gets the header.
	fresh := newFixture(t)
	must(t, os.MkdirAll(filepath.Dir(fresh.path), 0o700))
	must(t, os.WriteFile(fresh.path, nil, 0o600))
	j3 := fresh.open(t)
	must(t, j3.Close())
	hdr := fresh.chain(t)
	if !hdr.HasHeader || hdr.Header.InstallID != installID || hdr.Header.App != "gotham" || hdr.Header.KID != "d1" ||
		hdr.Header.BaseSeq != 0 || hdr.Header.BaseMAC != "" || !hdr.Header.StartedAt.Equal(time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC)) || len(hdr.Lines) != 0 {
		t.Errorf("an empty file got the header %+v", hdr.Header)
	}
}

// TestOpenFile_RefusesFileBehindHead: a journal file that ends below the
// store's committed journal_head (emptied, truncated, deleted and recreated)
// is ErrCorrupt and is not renumbered from the head or extended: no seq at or
// below what consumers already applied is ever assigned again (§5B.3.6
// "Format and integrity"; §5B.5 "chain broken or truncated below the live
// store's head"). The broken File that comes back still records gaps, so
// revocations keep committing (openBroken).
func TestOpenFile_RefusesFileBehindHead(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	for _, u := range []string{"joker", "penguin", "riddler"} {
		must(t, fx.revoke(t, j, u).Commit(ctx, storage.Sync))
	}
	must(t, j.Close())
	if h := fx.head(t); h.Seq != 3 {
		t.Fatalf("head = %+v, want seq 3", h)
	}
	full, err := os.ReadFile(fx.path)
	must(t, err)
	header := full[:bytes.IndexByte(full, '\n')+1]
	upToTwo := full[:bytes.Index(full, []byte(`{"seq":3`))]

	for name, content := range map[string][]byte{
		"empty":            nil,
		"header only":      header,
		"truncated to two": upToTwo,
		"deleted":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			if name == "deleted" {
				must(t, os.Remove(fx.path))
			} else {
				must(t, os.WriteFile(fx.path, content, 0o600))
			}
			fx.openBroken(t, name, ErrCorrupt)
			after, err := os.ReadFile(fx.path)
			if name == "deleted" {
				// The refused open created the (empty) file it locked; nothing was written to it.
				if err != nil || len(after) != 0 {
					t.Errorf("the refused open of a deleted journal left %q, %v", after, err)
				}
				return
			}
			must(t, err)
			if !bytes.Equal(after, content) {
				t.Errorf("the refused open wrote %q, want the file unchanged (%q)", after, content)
			}
		})
	}
	if h := fx.head(t); h.Seq != 3 {
		t.Errorf("head = %+v after the refusals, want seq 3", h)
	}
}

// TestAppend_NeverAtOrBelowHead: when the store's head reaches the next seq
// under an open journal (another writer committed through another file, or
// the file was replaced), Append writes nothing, breaks the journal, and
// every later Append fails too, so the callers record gaps instead of
// reusing seqs.
func TestAppend_NeverAtOrBelowHead(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	must(t, fx.revoke(t, j, "joker").Commit(ctx, storage.Sync))

	b := fx.ks.NewBatch()
	_, err := b.GetForUpdate(ctx, fx.ks, []byte(headKey))
	must(t, err)
	must(t, b.Set([]byte(headKey), []byte(`{"seq":5}`)))
	must(t, b.Commit(ctx, storage.Sync))
	before, err := os.ReadFile(fx.path)
	must(t, err)

	for i, user := range []string{"penguin", "riddler"} {
		rb := fx.auth.NewBatch()
		must(t, rb.Set([]byte("user/"+user), []byte("disabled")))
		err := j.Append(ctx, rb, Revocation{Kind: UserDisabled, Subject: entity.Ref{Module: "core", Kind: "user", ID: user}})
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("append %d: err = %v, want ErrCorrupt", i, err)
		}
		if rb.Count() != 1 {
			t.Errorf("append %d staged into the batch: count %d", i, rb.Count())
		}
		at := time.Date(2026, 11, 10, 3, 0, i, 0, time.UTC)
		must(t, j.Gap(rb, at))
		must(t, rb.Commit(ctx, storage.Sync))
	}
	after, err := os.ReadFile(fx.path)
	must(t, err)
	if !bytes.Equal(before, after) {
		t.Errorf("a refused append wrote %q", after[len(before):])
	}
	if h := fx.head(t); h.Seq != 5 {
		t.Errorf("head = %+v, want seq 5 (never lowered)", h)
	}
}

// TestOpenFile_SecondOpenerRefused: a journal file has one writer. A second
// OpenFile of the same path, which would number the same seqs, is ErrLocked
// until the first closes. flock(2) locks belong to the open file, so an
// opener in this process is refused like one in another process.
func TestOpenFile_SecondOpenerRefused(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	must(t, fx.revoke(t, j, "joker").Commit(ctx, storage.Sync))
	before, err := os.ReadFile(fx.path)
	must(t, err)
	if j2, err := OpenFile(ctx, fx.opts()); !errors.Is(err, ErrLocked) {
		if j2 != nil {
			_ = j2.Close()
		}
		t.Fatalf("second opener: err = %v, want ErrLocked", err)
	}
	after, err := os.ReadFile(fx.path)
	must(t, err)
	if !bytes.Equal(before, after) {
		t.Errorf("the refused opener changed the file: %q", after)
	}
	must(t, fx.revoke(t, j, "penguin").Commit(ctx, storage.Sync)) // the holder keeps writing
	must(t, j.Close())
	j2, err := OpenFile(ctx, fx.opts())
	must(t, err)
	must(t, fx.revoke(t, j2, "riddler").Commit(ctx, storage.Sync))
	must(t, j2.Close())
	if got := fx.lines(t); len(got) != 4 || !strings.Contains(got[3], `"seq":3`) {
		t.Errorf("lines = %q, want header and seqs 1-3", got)
	}
}

// gapStillWorks checks a broken File from a failed OpenFile: its Append fails
// with the open's error and stages nothing, its Gap records journal_gap/<since>
// so the revocation still commits, and Close succeeds (§5B.3.6: a disk problem
// never stops a gap being recorded).
func (fx *fixture) gapStillWorks(t *testing.T, j *File, openErr error, user string) {
	t.Helper()
	ctx := context.Background()
	if j == nil {
		t.Fatalf("OpenFile returned a nil File with %v: the revoking callers would have no Gap", openErr)
	}
	b := fx.auth.NewBatch()
	must(t, b.Set([]byte("user/"+user), []byte("disabled")))
	if err := j.Append(ctx, b, Revocation{Kind: UserDisabled}); err == nil || !errors.Is(err, openErr) {
		t.Errorf("Append on the broken File: err = %v, want the open's error %v", err, openErr)
	}
	if b.Count() != 1 {
		t.Errorf("the refused Append staged into the batch: count %d", b.Count())
	}
	at := time.Date(2026, 11, 10, 5, 0, len(user), 0, time.UTC)
	must(t, j.Gap(b, at))
	must(t, b.Commit(ctx, storage.Sync))
	if v, err := fx.auth.Get(ctx, []byte("user/"+user)); err != nil || string(v) != "disabled" {
		t.Errorf("the revocation did not commit: %q, %v", v, err)
	}
	if v, err := fx.ks.Get(ctx, gapKey(at)); err != nil || !strings.Contains(string(v), `"reason":"append_failed"`) {
		t.Errorf("the gap record: %q, %v", v, err)
	}
	must(t, j.Close())
}

// TestOpenFile_HeaderWriteFailureStillGaps: a header line that cannot be
// written (a full disk) leaves a broken File, never a nil one; the file is
// cut back to empty.
func TestOpenFile_HeaderWriteFailureStillGaps(t *testing.T) {
	fx := newFixture(t)
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	o := fx.opts()
	o.wrap = func(f lineFile) lineFile { return &failingFile{lineFile: f, failWrite: true} }
	j, err := OpenFile(context.Background(), o)
	if err == nil {
		t.Fatal("OpenFile succeeded although the header write failed")
	}
	fx.gapStillWorks(t, j, err, "joker")
	if b, _ := os.ReadFile(fx.path); len(b) != 0 {
		t.Errorf("the failed header left %q in the file", b)
	}
}

// TestOpenFile_LockedStillGaps: a second opener gets ErrLocked with a broken
// File whose Gap works; closing it releases nothing the holder needs.
func TestOpenFile_LockedStillGaps(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	holder := fx.open(t)
	j, err := OpenFile(ctx, fx.opts())
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second opener: err = %v, want ErrLocked", err)
	}
	fx.gapStillWorks(t, j, err, "penguin")
	must(t, fx.revoke(t, holder, "riddler").Commit(ctx, storage.Sync)) // the holder is unaffected
	if got := fx.lines(t); len(got) != 2 || !strings.Contains(got[1], `"seq":1`) {
		t.Errorf("lines = %q, want the header and the holder's seq 1", got)
	}
}

// TestOpenFile_UnopenableStillGaps: a journal that cannot be opened at all (its
// directory is missing) still yields a broken File whose Gap works.
func TestOpenFile_UnopenableStillGaps(t *testing.T) {
	fx := newFixture(t)
	o := fx.opts()
	o.Path = filepath.Join(t.TempDir(), "gone", "x.journal")
	j, err := OpenFile(context.Background(), o)
	if err == nil {
		t.Fatal("OpenFile succeeded in a missing directory")
	}
	fx.gapStillWorks(t, j, err, "scarecrow")
}
