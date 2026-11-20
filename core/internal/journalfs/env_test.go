// file: core/internal/journalfs/env_test.go
// version: 1.0.0
// guid: e34bd4bd-6585-4b25-aec6-e61610d311cb
// last-edited: 2026-11-19

package journalfs_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	"github.com/falkcorp/mediacore/internal/journalfs"
	"github.com/falkcorp/mediacore/storage"
)

// The in-memory filesystem's layout: a state directory and a backup directory
// beside it, both on the one filesystem the recorder wraps.
const (
	stateDir  = "/gotham/state"
	backupDir = "/gotham/backups"
	journalAt = backupDir + "/gotham_revocations.journal"
	movedTo   = "/gotham/arkham/gotham_revocations.journal"
	installID = "wayne-manor-install"
	authOwner = storage.Owner("core/auth")
)

// keys is a keyring with one derive key, the secrets store's DeriveKeyFor
// without importing secrets.
type keys struct{}

func (keys) ActiveDeriveID() string { return "d1" }
func (keys) DeriveKeyFor(kid, info string) ([]byte, error) {
	if kid != "d1" {
		return nil, fmt.Errorf("derive key %q: key unavailable", kid)
	}
	h := hmac.New(sha256.New, []byte("alfred-pennyworth-d1"))
	h.Write([]byte(info))
	return h.Sum(nil), nil
}

// store is a real store with c/backup/ and c/auth/: the journal's keyspace
// and the batch an append stages its head into. The journal files live on the
// in-memory filesystem; the store is where the committed head is read.
type store struct {
	ks, auth storage.Keyspace
}

func newStore(t *testing.T) store {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"), Build: "batcomputer-1", FreeSpaceMargin: 1,
		Owners: []storage.OwnerSpec{
			{Owner: journal.Owner, Spec: journal.StorageSpec(), Enabled: true},
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
	ks, _ := db.Keyspace(journal.Owner)
	auth, _ := db.Keyspace(authOwner)
	return store{ks: ks, auth: auth}
}

// env is one boot over the recording in-memory filesystem.
type env struct {
	t    *testing.T
	st   store
	rec  *bootseamtest.Recorder
	fsys journal.FS
	held *journal.File // holdOpen's journal
}

// newEnv is a boot whose recorder takes the options given (a crashable
// filesystem when it takes clones). The directories exist and are durable.
func newEnv(t *testing.T, st store, opts bootseamtest.Options) *env {
	t.Helper()
	mem := vfs.NewMem()
	if opts.Clones || opts.CloneIf != nil {
		mem = vfs.NewCrashableMem()
	}
	for _, dir := range []string{stateDir, backupDir, filepath.Dir(movedTo)} {
		if err := mem.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	syncTree(t, mem, "/gotham/state", "/gotham/backups", "/gotham/arkham", "/gotham", "/")
	rec := bootseamtest.New(mem, opts)
	fsys, err := journalfs.From(bootseam.With(context.Background(), rec.Seams()))
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, rec: rec, fsys: fsys}
}

// syncTree makes the directories' entries durable in a crashable filesystem.
func syncTree(t *testing.T, mem *vfs.MemFS, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		f, err := mem.OpenDir(d)
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			t.Fatal(err)
		}
	}
}

func fixedClock() time.Time { return time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC) }

// open is OpenFile over e's filesystem.
func (e *env) open(path string) (*journal.File, error) {
	return journal.OpenFile(context.Background(), journal.FileOptions{
		Path: path, Keyspace: e.st.ks, Keys: keys{}, InstallID: installID, App: "gotham", Source: "batcomputer-1",
		Clock: fixedClock, FS: e.fsys,
	})
}

// mustOpen is open that must succeed; the file is closed when the test ends.
func (e *env) mustOpen(path string) *journal.File {
	e.t.Helper()
	j, err := e.open(path)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = j.Close() })
	return j
}

// appendUncommitted appends a line whose batch is never committed, the state a
// crash after the line's fsync and before the store's commit leaves.
func (e *env) appendUncommitted(j *journal.File) error {
	b := e.st.auth.NewBatch()
	defer func() { _ = b.Close() }()
	return j.Append(context.Background(), b, journal.Revocation{Kind: journal.UserDeleted, At: fixedClock()})
}

// location is the file backup.location.json holds, for a Location that
// round-trips.
func location(id string) journal.Location {
	return journal.Location{BackupDir: backupDir, Journal: journalAt, DBPath: "/gotham/main.pebble", InstallID: installID,
		ServedBoot: journal.ServedBoot{N: 7, Nonce: id}, Head: journal.Head{Seq: 3, MAC: "oracle"}}
}

// summarize is the recorded durable operations, each as "label:kind".
func summarize(evs []bootseamtest.Event) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, ev.Label+":"+kindName(ev.Kind))
	}
	return out
}

func kindName(k errorfs.OpKind) string {
	switch k {
	case errorfs.OpCreate:
		return "create"
	case errorfs.OpLink:
		return "link"
	case errorfs.OpRemove:
		return "remove"
	case errorfs.OpRemoveAll:
		return "removeall"
	case errorfs.OpRename:
		return "rename"
	case errorfs.OpReuseForWrite:
		return "reuse"
	case errorfs.OpMkdirAll:
		return "mkdir"
	case errorfs.OpFileWrite:
		return "write"
	case errorfs.OpFileWriteAt:
		return "writeat"
	case errorfs.OpFileSync, errorfs.OpFileSyncData, errorfs.OpFileSyncTo:
		return "sync"
	}
	return fmt.Sprintf("op%d", k)
}

// since is the events recorded after the first n.
func (e *env) since(n int) []bootseamtest.Event { return e.rec.Events()[n:] }

// unlabelled fails the test for every event without a writer-table label.
func unlabelled(t *testing.T, evs []bootseamtest.Event) {
	t.Helper()
	for _, ev := range evs {
		if ev.Label == bootseamtest.LabelNone {
			t.Errorf("event %d (%s of %s) carries no label: a durable operation the matrix cannot attribute to a writer", ev.Seq, kindName(ev.Kind), ev.Path)
		}
	}
}

func hasLabelPrefix(evs []bootseamtest.Event, prefix string) bool {
	return slices.ContainsFunc(evs, func(ev bootseamtest.Event) bool { return strings.HasPrefix(ev.Label, prefix) })
}

// noOpts is a recorder that records and takes no clone and injects nothing.
func noOpts() bootseamtest.Options { return bootseamtest.Options{} }

// holdOpen opens the journal at path and keeps it in e.held until the test ends.
func (e *env) holdOpen(path string) { e.held = e.mustOpen(path) }

// tearTail leaves an incomplete last line at the end of the journal at path,
// the way a crash inside a write does.
func (e *env) tearTail(path string) {
	e.t.Helper()
	mem := e.rec.Mem()
	f, err := mem.OpenReadWrite(path, vfs.WriteCategoryUnspecified)
	if err != nil {
		e.t.Fatal(err)
	}
	fi, err := f.Stat()
	if err == nil {
		_, err = f.WriteAt([]byte(`{"seq":3,"kind":"user_de`), fi.Size())
	}
	if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
		e.t.Fatal(err)
	}
}

func errorsJoin(errs ...error) error { return errors.Join(errs...) }

// plant writes a durable file at path, as an earlier attempt that died left it.
func (e *env) plant(path, content string) {
	e.t.Helper()
	mem := e.rec.Mem()
	f, err := mem.Create(path, vfs.WriteCategoryUnspecified)
	if err == nil {
		_, err = f.Write([]byte(content))
	}
	if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
		e.t.Fatal(err)
	}
	syncTree(e.t, mem, filepath.Dir(path))
}
