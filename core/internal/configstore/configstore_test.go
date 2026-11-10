// file: core/internal/configstore/configstore_test.go
// version: 1.1.4
// guid: cab01bd1-5d76-48c6-a4f9-4db76141c009
// last-edited: 2026-11-10

package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Owners of the test store besides c/config/: c/audit/ (the record shares the
// batch) and a stand-in for c/secrets/ (F3-03 owns the real one).
const (
	auditOwner   storage.Owner = "core/audit"
	secretsOwner storage.Owner = "core/secrets"
)

// env is a booted store with the config, audit and secrets keyspaces.
type env struct {
	db      *storage.DB
	ks      storage.Keyspace
	secKS   storage.Keyspace
	store   *Store
	audit   *audit.Store
	stager  *stager
	commit  *Committer
	lookups map[config.Path]config.Path
}

// boot opens a fresh in-memory store as coretest does (coretest reaches the
// kernel, which imports this package).
func boot(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"),
		Owners: []storage.OwnerSpec{
			OwnerSpec(),
			{Owner: auditOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true},
			{Owner: secretsOwner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true},
		},
		Build: "coretest+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("boot: %v", errors.Join(err, o.Close()))
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	e := &env{db: db, lookups: map[config.Path]config.Path{}}
	var ok bool
	if e.ks, ok = db.Keyspace(Owner); !ok {
		t.Fatal("no c/config/ keyspace")
	}
	if e.secKS, ok = db.Keyspace(secretsOwner); !ok {
		t.Fatal("no secrets keyspace")
	}
	aks, ok := db.Keyspace(auditOwner)
	if !ok {
		t.Fatal("no audit keyspace")
	}
	if e.audit, err = audit.New(ctx, audit.Options{}, audit.Deps{Keyspace: aks, Seq: aks.Sequence("seq")}); err != nil {
		t.Fatal(err)
	}
	e.store = New(e.ks)
	e.stager = &stager{ks: e.secKS}
	if e.commit, err = NewCommitter(CommitterOptions{Store: e.store, Audit: e.audit, Keys: e, Secrets: e.stager}); err != nil {
		t.Fatal(err)
	}
	return e
}

// Lookup is the committer's KeyLookup: the section of every fixture key.
func (e *env) Lookup(p config.Path) (config.KeyState, bool) {
	for _, sec := range []config.Path{configtest.BatcavePath, configtest.WatchPath} {
		if p == sec || len(p) > len(sec) && p[:len(sec)+1] == sec+"." {
			return config.KeyState{Path: p, Section: sec}, true
		}
	}
	return config.KeyState{}, false
}

// stager stands in for secrets.(*Store).Stage: it writes the secret's name
// into its own keyspace, in the caller's batch, or fails.
type stager struct {
	ks   storage.Keyspace
	fail error
}

func (s *stager) Stage(ctx context.Context, b *storage.Batch, w secrets.Write) error {
	if s.fail != nil {
		return s.fail
	}
	b.Bind(s.ks)
	k := keys.Join(keys.Str(string(w.Name)))
	if _, err := b.GetForUpdate(ctx, s.ks, k); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	if w.Delete {
		return b.In(s.ks).Delete(k)
	}
	return b.In(s.ks).Set(k, []byte("sealed"))
}

// harness is the suite's view of e.
func (e *env) harness() configtest.Harness {
	return configtest.Harness{
		Store:     e.store,
		Committer: e.commit,
		Commit: func(ctx context.Context, stage func(ctx context.Context, b *storage.Batch) error) error {
			return storage.RetryOnConflict(ctx, 5, func(ctx context.Context) (err error) {
				b := e.ks.NewBatch()
				defer func() { err = errors.Join(err, b.Close()) }()
				if err := stage(ctx, b); err != nil {
					return err
				}
				return b.Commit(ctx, storage.Sync)
			})
		},
		Rows: e.rows,
		Corrupt: func(ctx context.Context, p config.Path, raw []byte) (err error) {
			b := e.ks.NewBatch()
			defer func() { err = errors.Join(err, b.Close()) }()
			if _, err := b.GetForUpdate(ctx, e.ks, RowKey(p)); err != nil && !errors.Is(err, storage.ErrNotFound) {
				return err
			}
			if err := b.Set(RowKey(p), raw); err != nil {
				return err
			}
			return b.Commit(ctx, storage.Sync)
		},
	}
}

// rows is every raw row of c/config/, by path.
func (e *env) rows(ctx context.Context) (map[config.Path][]byte, error) {
	out := map[config.Path][]byte{}
	for kv, err := range e.ks.ScanPrefix(ctx, nil) {
		if err != nil {
			return nil, err
		}
		segs, err := keys.Split(kv.Key)
		if err != nil || len(segs) != 2 {
			return nil, errors.Join(errors.New("not a row key"), err)
		}
		out[config.Path(segs[1])] = append([]byte(nil), kv.Value...)
	}
	return out, nil
}

// TestNativeOverrideStore_RoundTrip (05-config §1.11): the native store over
// c/config/ passes configtest.OverrideStoreSuite (set, unset and reload of
// scalar, list, map-valued and restart keys; one row per set key).
func TestNativeOverrideStore_RoundTrip(t *testing.T) {
	configtest.OverrideStoreSuite(t, func(t *testing.T) configtest.Harness { return boot(t).harness() })
}

// TestNativeOverrideStore_RowFormat pins the row: key ("v", path), value the
// envelope {value, gen, at, by}.
func TestNativeOverrideStore_RowFormat(t *testing.T) {
	ctx := context.Background()
	e := boot(t)
	h := e.harness()
	at := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	ov := config.Override{Path: "core.batcave.level", Value: json.RawMessage(`"warn"`), Gen: 3, At: at, By: "alfred"}
	if err := h.Commit(ctx, func(ctx context.Context, b *storage.Batch) error {
		return e.store.Stage(ctx, b, []config.Override{ov}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := e.ks.Get(ctx, keys.Join(keys.Str("v"), keys.Str("core.batcave.level")))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"value":"warn","gen":3,"at":"2026-11-10T08:00:00Z","by":"alfred"}`; string(raw) != want {
		t.Errorf("row = %s, want %s", raw, want)
	}
}

// TestNativeOverrideStore_StageNeedsExpect: a blind write to c/config/ fails
// (the keyspace is Serialized), so Stage's GetForUpdate is what lets it
// commit.
func TestNativeOverrideStore_StageNeedsExpect(t *testing.T) {
	ctx := context.Background()
	e := boot(t)
	b := e.ks.NewBatch()
	if err := b.Set(RowKey("core.batcave.level"), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	err := b.Commit(ctx, storage.Sync)
	if !errors.Is(err, storage.ErrBlindWrite) {
		t.Fatalf("blind write committed (%v): c/config/ must be Serialized", err)
	}
}

// TestApply_SecretAndOverrideOneBatch (05-config §1.11, the committer half):
// Commit.Set, Commit.Secrets and the core.settings.update record land in one
// batch: all of them, or (when the secret stage fails) none of them.
func TestApply_SecretAndOverrideOneBatch(t *testing.T) {
	ctx := context.Background()
	e := boot(t)
	cm := config.Commit{
		Set:     []config.Override{{Path: "core.batcave.level", Value: json.RawMessage(`"warn"`), Gen: 1, By: "alfred"}},
		Unset:   []config.Path{"core.batcave.gadget.count"},
		Secrets: []secrets.Write{{Name: "core.batcave.api_key", Value: secrets.NewValue("wayne-tech"), By: "alfred"}},
		Gen:     1,
		Boot:    []config.Path{"core.batcave.mode"},
		By:      "alfred",
	}
	e.stager.fail = errors.New("the vault is sealed")
	if err := e.commit.Commit(ctx, cm); err == nil {
		t.Fatal("Commit succeeded with a failing secret stage")
	}
	if rows, err := e.rows(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("after a failed commit: rows %v (%v), want none", rows, err)
	}
	if recs := records(t, e); len(recs) != 0 {
		t.Fatalf("after a failed commit: %d audit records, want none", len(recs))
	}

	e.stager.fail = nil
	if err := e.commit.Commit(ctx, cm); err != nil {
		t.Fatal(err)
	}
	rows, err := e.rows(ctx)
	if err != nil || len(rows) != 1 || rows["core.batcave.level"] == nil {
		t.Fatalf("rows %v (%v), want the one set key", rows, err)
	}
	if v, err := e.secKS.Get(ctx, keys.Join(keys.Str("core.batcave.api_key"))); err != nil || string(v) != "sealed" {
		t.Errorf("secret row = %q, %v; want it staged in the same commit", v, err)
	}
	recs := records(t, e)
	if len(recs) != 1 {
		t.Fatalf("%d audit records, want 1", len(recs))
	}
	r := recs[0]
	if r.Action != "core.settings.update" || r.Outcome != audit.Success || r.Actor.UserID != "alfred" {
		t.Errorf("record = %s %s by %q", r.Action, r.Outcome, r.Actor.UserID)
	}
	b, err := json.Marshal(r.Details)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"changed":["core.batcave.api_key","core.batcave.gadget.count","core.batcave.level","core.batcave.mode"],"generation":1,"sections":["core.batcave"]}`
	if string(b) != want {
		t.Errorf("details = %s, want %s", b, want)
	}
	if strings.Contains(string(b), "wayne-tech") || strings.Contains(string(b), "warn") {
		t.Errorf("the audit record carries a value: %s", b)
	}
}

// TestCommitter_AuditActor (T4 review MAJOR-3, 05-config §1.3.3): the
// core.settings.update record's actor is the request's actor on ctx, whole,
// when it names Commit.By, and {UserID: Commit.By} without one, so a commit
// that only unsets keys or only changes boot keys still names its writer. A
// ctx actor naming someone else, a row written by someone else, or a secret
// written by someone else is refused and nothing is written.
func TestCommitter_AuditActor(t *testing.T) {
	e := boot(t)
	alfred := audit.Actor{UserID: "alfred", Method: "session", ClientAddr: "wayne-tower", UserAgent: "batcomputer"}
	withAlfred := audit.WithActor(context.Background(), alfred)
	level := config.Path("core.batcave.level")
	steps := []struct {
		name string
		ctx  context.Context
		cm   config.Commit
		want audit.Actor
	}{
		{"set, request actor", withAlfred, config.Commit{Set: []config.Override{{Path: level, Value: json.RawMessage(`"warn"`), Gen: 1, At: time.Now(), By: "alfred"}}, Gen: 1, By: "alfred"}, alfred},
		{"unset only, request actor", withAlfred, config.Commit{Unset: []config.Path{level}, Gen: 2, By: "alfred"}, alfred},
		{"boot only, no request actor", context.Background(), config.Commit{Boot: []config.Path{"core.batcave.mode"}, Gen: 3, By: "alfred"}, audit.Actor{UserID: "alfred"}},
	}
	for i, s := range steps {
		if err := e.commit.Commit(s.ctx, s.cm); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		recs := records(t, e)
		if len(recs) != i+1 {
			t.Fatalf("%s: %d records, want %d", s.name, len(recs), i+1)
		}
		var got *audit.Record
		for j := range recs {
			if recs[j].Details["generation"] == float64(s.cm.Gen) || recs[j].Details["generation"] == s.cm.Gen {
				got = &recs[j]
			}
		}
		if got == nil {
			t.Fatalf("%s: no record of generation %d in %+v", s.name, s.cm.Gen, recs)
		}
		if got.Actor != s.want {
			t.Errorf("%s: actor %+v, want %+v", s.name, got.Actor, s.want)
		}
	}
	refused := []struct {
		name string
		ctx  context.Context
		cm   config.Commit
	}{
		{"request actor is someone else", audit.WithActor(context.Background(), audit.Actor{UserID: "joker"}),
			config.Commit{Set: []config.Override{{Path: level, Value: json.RawMessage(`"debug"`), Gen: 4, At: time.Now(), By: "alfred"}}, Gen: 4, By: "alfred"}},
		{"row written by someone else", context.Background(),
			config.Commit{Set: []config.Override{{Path: level, Value: json.RawMessage(`"debug"`), Gen: 4, At: time.Now(), By: "joker"}}, Gen: 4, By: "alfred"}},
		{"secret written by someone else", context.Background(),
			config.Commit{
				Set:     []config.Override{{Path: level, Value: json.RawMessage(`"debug"`), Gen: 4, At: time.Now(), By: "alfred"}},
				Secrets: []secrets.Write{{Name: "core.batcave.api_key", Value: secrets.NewValue("wayne-tech"), By: "joker"}},
				Gen:     4, By: "alfred",
			}},
	}
	for _, r := range refused {
		if err := e.commit.Commit(r.ctx, r.cm); err == nil {
			t.Errorf("%s: Commit succeeded", r.name)
		}
	}
	if n := len(records(t, e)); n != len(steps) {
		t.Errorf("%d records after the refused commits, want %d", n, len(steps))
	}
	if rows, err := e.rows(context.Background()); err != nil || rows[level] != nil {
		t.Errorf("rows %v (%v): a refused commit wrote", rows, err)
	}
	if v, err := e.secKS.Get(context.Background(), keys.Join(keys.Str("core.batcave.api_key"))); err == nil {
		t.Errorf("secret row = %q: a refused commit staged it", v)
	}
}

// TestImporter_RefusesAfterAttach (T4 review MINOR-3, 05-config §1.3.3):
// once Attach has run, Importer.Stage is refused and writes nothing. It
// checks no protection and records no audit event, which is right only for a
// cutover step before P4d; an owner-protected key staged at run time would
// otherwise skip both.
func TestImporter_RefusesAfterAttach(t *testing.T) {
	ctx := context.Background()
	e := boot(t)
	st, err := config.NewStore(config.Options{AppID: "gotham"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if _, err := config.Register(st.Registry(), configtest.BatcaveSection()); err != nil {
		t.Fatal(err)
	}
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	st.SetImportTarget(e.store)
	if _, err := st.Attach(ctx, e.store, e.commit); err != nil {
		t.Fatal(err)
	}
	b := e.ks.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := st.Importer().Stage(b, "core.batcave.owner_email", json.RawMessage(`"joker@arkham.example"`), "m2"); err == nil || !strings.Contains(err.Error(), "before Attach") {
		t.Fatalf("Stage after Attach = %v, want a refusal", err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if rows, err := e.rows(ctx); err != nil || len(rows) != 0 {
		t.Errorf("rows %v (%v): the refused Stage wrote", rows, err)
	}
	if d, err := st.Importer().Default("core.batcave.owner_email"); err != nil || len(d) == 0 {
		t.Errorf("Default after Attach = %s, %v; the read side stays valid", d, err)
	}
}

// TestCommitter_RefusesSecretsWithoutStager: before F3-03 opens the secrets
// store, a commit with a secret write is refused, never half-applied.
func TestCommitter_RefusesSecretsWithoutStager(t *testing.T) {
	ctx := context.Background()
	e := boot(t)
	c, err := NewCommitter(CommitterOptions{Store: e.store, Audit: e.audit, Keys: e})
	if err != nil {
		t.Fatal(err)
	}
	cm := config.Commit{
		Set:     []config.Override{{Path: "core.batcave.level", Value: json.RawMessage(`"warn"`)}},
		Secrets: []secrets.Write{{Name: "core.batcave.api_key", Value: secrets.NewValue("x")}},
	}
	if err := c.Commit(ctx, cm); err == nil {
		t.Fatal("a secret write committed without a secrets store")
	}
	if rows, err := e.rows(ctx); err != nil || len(rows) != 0 {
		t.Errorf("rows %v (%v), want none", rows, err)
	}
	for _, o := range []CommitterOptions{{Audit: e.audit, Keys: e}, {Store: e.store, Keys: e}, {Store: e.store, Audit: e.audit}, {Store: e.store, Audit: e.audit, Keys: e, Attempts: -1}} {
		if _, err := NewCommitter(o); err == nil {
			t.Errorf("NewCommitter(%+v) succeeded", o)
		}
	}
}

// TestImporter_StagesOnlyValidNonDefault (05-config §1.11): Importer.Stage
// writes a c/config/ row into the caller's batch for a valid non-default
// value, nothing for a default value, ErrUnknownKey for an unregistered or
// passthrough path, ErrInvalid for a secret, ProtectOperator or boot key,
// *FieldError for a bad value; nothing commits until the caller commits.
func TestImporter_StagesOnlyValidNonDefault(t *testing.T) {
	ctx := context.Background()
	e := boot(t)
	st, err := config.NewStore(config.Options{AppID: "gotham", Passthrough: func(p config.Path) bool {
		return strings.HasPrefix(string(p), "integrations.")
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if _, err := config.Register(st.Registry(), configtest.BatcaveSection()); err != nil {
		t.Fatal(err)
	}
	imp := st.Importer()
	if err := imp.Stage(e.ks.NewBatch(), "core.batcave.level", json.RawMessage(`"warn"`), "importer"); err == nil {
		t.Fatal("the Importer staged before Load")
	}
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := imp.Stage(e.ks.NewBatch(), "core.batcave.level", json.RawMessage(`"warn"`), "importer"); err == nil {
		t.Fatal("the Importer staged with no override store handed over")
	}
	st.SetImportTarget(e.store)

	b := e.ks.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			t.Errorf("close the batch: %v", err)
		}
	}()
	refusals := []struct {
		p    config.Path
		v    string
		want error
	}{
		{"core.batcave.utility_belt", `"x"`, config.ErrUnknownKey},
		{"integrations.ai.wayne.base_url", `"x"`, config.ErrUnknownKey},
		{"core.batcave.api_key", `"wayne-tech"`, config.ErrInvalid},
		{"core.batcave.vault", `"arkham"`, config.ErrInvalid},
		{"core.batcave.mode", `"day"`, config.ErrInvalid},
	}
	for _, r := range refusals {
		if err := imp.Stage(b, r.p, json.RawMessage(r.v), "importer"); !errors.Is(err, r.want) {
			t.Errorf("Stage %s: err = %v, want %v", r.p, err, r.want)
		}
	}
	var fe *config.FieldError
	if err := imp.Stage(b, "core.batcave.workers", json.RawMessage(`0`), "importer"); !errors.As(err, &fe) || fe.Path != "core.batcave.workers" {
		t.Errorf("a bad value: err = %v, want *FieldError on core.batcave.workers", err)
	}
	if err := imp.Stage(b, "core.batcave.gadget.count", json.RawMessage(`3`), "importer"); err != nil {
		t.Errorf("a default value: %v", err)
	}
	if err := imp.Stage(b, "core.batcave.level", json.RawMessage(`"warn"`), "importer"); err != nil {
		t.Fatalf("a valid non-default value: %v", err)
	}
	if def, err := imp.Default("core.batcave.level"); err != nil || string(def) != `"info"` {
		t.Errorf("Default = %s, %v; want \"info\"", def, err)
	}
	if rows, err := e.rows(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("before the caller commits: rows %v (%v), want none", rows, err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	rows, err := e.rows(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %v (%v), want only core.batcave.level", rows, err)
	}
	var env struct {
		Value json.RawMessage `json:"value"`
		By    string          `json:"by"`
	}
	if err := json.Unmarshal(rows["core.batcave.level"], &env); err != nil || string(env.Value) != `"warn"` || env.By != "importer" {
		t.Errorf("row = %s (%v)", rows["core.batcave.level"], err)
	}
}

func records(t *testing.T, e *env) []audit.Record {
	t.Helper()
	var out []audit.Record
	for r, err := range e.audit.Query(context.Background(), audit.Filter{Limit: audit.MaxQueryLimit}) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}
