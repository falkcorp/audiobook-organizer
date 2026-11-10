// file: core/backup/journal/file_test.go
// version: 1.0.0
// guid: b7a073e7-211a-4d06-8bf4-6befeb60e0d3
// last-edited: 2026-11-10

package journal

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/archtest"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

// authOwner stands in for core/auth, the revoking subsystem whose batch the
// journal joins.
const authOwner storage.Owner = "core/auth"

// fixture is a store with c/backup/ and c/auth/, booted the way the kernel's
// P2, P4a and P4b do (as coretest.Store does; coretest is not imported
// because it imports kernel, which will import this package).
type fixture struct {
	db   *storage.DB
	ks   storage.Keyspace // c/backup/
	auth storage.Keyspace // c/auth/
	path string           // the journal file
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(dir, "storage", "main.pebble"), Build: "batcomputer-1", FreeSpaceMargin: 1,
		Owners: []storage.OwnerSpec{
			{Owner: Owner, Spec: StorageSpec(), Enabled: true},
			{Owner: authOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatal(errors.Join(err, o.Close()))
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, _ := db.Keyspace(Owner)
	auth, _ := db.Keyspace(authOwner)
	return &fixture{db: db, ks: ks, auth: auth, path: filepath.Join(dir, "backups", "gotham_revocations.journal")}
}

func (fx *fixture) open(t *testing.T) *File {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(fx.path), 0o700); err != nil {
		t.Fatal(err)
	}
	j, err := OpenFile(context.Background(), FileOptions{Path: fx.path, Keyspace: fx.ks, Source: "batcomputer-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

// head reads the committed c/backup/journal_head seq (0 when absent).
func (fx *fixture) head(t *testing.T) uint64 {
	t.Helper()
	raw, err := fx.ks.Get(context.Background(), []byte(headKey))
	if errors.Is(err, storage.ErrNotFound) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	seq, err := decodeSeq(raw)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// lines reads the journal file's lines.
func (fx *fixture) lines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(fx.path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// openBroken opens fx.path expecting ErrCorrupt and checks the broken File
// that comes with it (§5B.5: the app stays open on a broken journal): it
// holds the lock, its Append fails with ErrCorrupt and stages nothing, and
// its Gap records journal_gap/<since> so the revocation still commits.
func (fx *fixture) openBroken(t *testing.T, what string) {
	t.Helper()
	ctx := context.Background()
	j, err := OpenFile(ctx, FileOptions{Path: fx.path, Keyspace: fx.ks})
	if !errors.Is(err, ErrCorrupt) || j == nil {
		if j != nil {
			_ = j.Close()
		}
		t.Fatalf("%s: OpenFile = %v, %v; want a broken File and ErrCorrupt", what, j, err)
	}
	defer func() { must(t, j.Close()) }()
	if j2, err := OpenFile(ctx, FileOptions{Path: fx.path, Keyspace: fx.ks}); !errors.Is(err, ErrLocked) {
		if j2 != nil {
			_ = j2.Close()
		}
		t.Errorf("%s: a second opener of the broken journal: err = %v, want ErrLocked", what, err)
	}
	b := fx.auth.NewBatch()
	must(t, b.Set([]byte("user/bane"), []byte("disabled")))
	if err := j.Append(ctx, b, Revocation{Kind: UserDisabled}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("%s: Append on the broken journal: err = %v, want ErrCorrupt", what, err)
	}
	if b.Count() != 1 {
		t.Errorf("%s: the refused Append staged into the batch: count %d", what, b.Count())
	}
	at := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	must(t, j.Gap(b, at))
	must(t, b.Commit(ctx, storage.Sync))
	if v, err := fx.ks.Get(ctx, gapKey(at)); err != nil || !strings.Contains(string(v), `"reason":"append_failed"`) {
		t.Errorf("%s: the gap record: %q, %v", what, v, err)
	}
}

// revoke is a revoking batch of core/auth: the user row and the journal line.
func (fx *fixture) revoke(t *testing.T, j RevocationJournal, user string) *storage.Batch {
	t.Helper()
	b := fx.auth.NewBatch()
	if err := b.Set([]byte("user/"+user), []byte("disabled")); err != nil {
		t.Fatal(err)
	}
	r := Revocation{At: time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC), Kind: UserDisabled,
		Subject: entity.Ref{Module: "core", Kind: "user", ID: user}}
	if err := j.Append(context.Background(), b, r); err != nil {
		t.Fatal(err)
	}
	return b
}

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
	if got := fx.lines(t); len(got) != 2 || got[0] != `{"seq":0}` || !strings.Contains(got[1], `"seq":1`) {
		t.Fatalf("before commit the file holds %q, want the header and line 1", got)
	}
	penguin := fx.revoke(t, j, "penguin")
	must(t, penguin.Commit(ctx, storage.Sync))
	if h := fx.head(t); h != 2 {
		t.Fatalf("head = %d after seq 2 committed, want 2", h)
	}
	if err := joker.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("the out-of-order commit: err = %v, want ErrConflict (the head moved since it was read)", err)
	}
	if h := fx.head(t); h != 2 {
		t.Fatalf("head = %d after the stale commit, want still 2", h)
	}
	// The caller retries the revoking batch, appending again.
	must(t, storage.RetryOnConflict(ctx, 3, func(ctx context.Context) error {
		return fx.revoke(t, j, "joker").Commit(ctx, storage.Sync)
	}))
	if h := fx.head(t); h != 3 {
		t.Fatalf("head = %d after the retry, want 3", h)
	}
	if v, err := fx.auth.Get(ctx, []byte("user/joker")); err != nil || string(v) != "disabled" {
		t.Errorf("the revocation itself: %q, %v", v, err)
	}
	got := fx.lines(t)
	if len(got) != 4 {
		t.Fatalf("file holds %d lines, want the header and 3", len(got))
	}
	r, err := decodeLine([]byte(got[3]))
	must(t, err)
	if r.Seq != 3 || r.Kind != UserDisabled || r.Subject != (entity.Ref{Module: "core", Kind: "user", ID: "joker"}) || r.Source != "batcomputer-1" {
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
	if h := fx.head(t); h != 0 {
		t.Errorf("head = %d, want none", h)
	}

	ff.failWrite = false
	must(t, fx.revoke(t, j, "penguin").Commit(ctx, storage.Sync))
	if got := fx.lines(t); len(got) != 2 || !strings.Contains(got[1], `"seq":1`) {
		t.Errorf("after the failure the file holds %q, want line 1 next", got)
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

	must(t, os.WriteFile(fx.path, []byte("{\"seq\":0}\n{\"seq\":2}\n"), 0o600))
	fx.openBroken(t, "seq gap")
	must(t, os.WriteFile(fx.path, []byte("{\"seq\":0}\nnot json\n"), 0o600))
	fx.openBroken(t, "garbage line")
	if _, err := OpenFile(context.Background(), FileOptions{Path: fx.path, Keyspace: fx.auth}); err == nil {
		t.Error("OpenFile accepted another owner's keyspace")
	}
	if _, err := OpenFile(context.Background(), FileOptions{Keyspace: fx.ks}); err == nil {
		t.Error("OpenFile accepted an empty path")
	}
	// An empty file for a store with no head gets the header (fx's store
	// has head 2 by now, so a fresh store).
	empty := filepath.Join(t.TempDir(), "empty.journal")
	must(t, os.WriteFile(empty, nil, 0o600))
	j3, err := OpenFile(context.Background(), FileOptions{Path: empty, Keyspace: newFixture(t).ks})
	must(t, err)
	must(t, j3.Close())
	if b, _ := os.ReadFile(empty); string(b) != "{\"seq\":0}\n" {
		t.Errorf("an empty file got %q, want the header", b)
	}
}

// TestJournalImportsOnlyAllowed: backup/journal imports only entity, storage
// and storage/restorejournal of core (and the standard library), so auth ->
// journal and rbac -> journal close no cycle; its transitive closure adds
// nothing beyond what storage itself imports (05-backup §5B.3.2; the brief's
// go list -deps allow-list).
func TestJournalImportsOnlyAllowed(t *testing.T) {
	wd, err := os.Getwd()
	must(t, err)
	root, err := archtest.ModuleRoot(wd)
	must(t, err)
	pkgs, err := archtest.Load(archtest.LoadOptions{Dir: root, Patterns: []string{"./backup/journal"}, TagSets: archtest.TagSets()})
	must(t, err)
	const mod = "github.com/falkcorp/mediacore/"
	allowed := map[string]bool{mod + "entity": true, mod + "storage": true, mod + "storage/restorejournal": true}
	p := pkgs[mod+"backup/journal"]
	if p == nil {
		t.Fatal("the loader did not return backup/journal")
	}
	for _, imp := range p.Imports {
		if strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") && !allowed[imp] {
			t.Errorf("backup/journal imports %s; only entity, storage, storage/restorejournal and the standard library are allowed", imp)
		}
	}

	deps := func(pkg string) map[string]bool {
		cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", pkg)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", pkg, err)
		}
		set := map[string]bool{}
		for l := range strings.FieldsSeq(string(out)) {
			set[l] = true
		}
		return set
	}
	storageDeps := deps("./storage")
	for d := range deps("./backup/journal") {
		if !storageDeps[d] && !allowed[d] && d != mod+"backup/journal" {
			t.Errorf("backup/journal depends on %s, which storage does not import", d)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
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
	if h := fx.head(t); h != 3 {
		t.Fatalf("head = %d, want 3", h)
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
			fx.openBroken(t, name)
			after, err := os.ReadFile(fx.path)
			must(t, err)
			if !bytes.Equal(after, content) {
				t.Errorf("the refused open wrote %q, want the file unchanged (%q)", after, content)
			}
		})
	}
	if h := fx.head(t); h != 3 {
		t.Errorf("head = %d after the refusals, want 3", h)
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
	if h := fx.head(t); h != 5 {
		t.Errorf("head = %d, want 5 (never lowered)", h)
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
	if j2, err := OpenFile(ctx, FileOptions{Path: fx.path, Keyspace: fx.ks}); !errors.Is(err, ErrLocked) {
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
	j2, err := OpenFile(ctx, FileOptions{Path: fx.path, Keyspace: fx.ks})
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
	j, err := OpenFile(context.Background(), FileOptions{Path: fx.path, Keyspace: fx.ks,
		wrap: func(f lineFile) lineFile { return &failingFile{lineFile: f, failWrite: true} }})
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
	j, err := OpenFile(ctx, FileOptions{Path: fx.path, Keyspace: fx.ks})
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
	j, err := OpenFile(context.Background(), FileOptions{Path: filepath.Join(t.TempDir(), "gone", "x.journal"), Keyspace: fx.ks})
	if err == nil {
		t.Fatal("OpenFile succeeded in a missing directory")
	}
	fx.gapStillWorks(t, j, err, "scarecrow")
}
