// file: core/kernel/offline_users_test.go
// version: 0.1.0
// guid: d52b8e31-6f07-4a9c-b1e4-93c7a0f58d16
// last-edited: 2026-11-21

package kernel

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The offline identity commands (F4-08 B9b): reset-password and the journal
// queue's list and clear, at the kernel layer. Every fixture is a serve boot
// of identityHost that seeds the store and closes; the command then runs over
// the closed store, as an operator's would after stopping the service.

// oiFx is a seeded, closed identity store.
type oiFx struct {
	h *swapHost
	c Config
	// journalPath is where the serve boot's journal component bound.
	journalPath string
}

// seed boots identityHost, commits the given users and any extra rows, then
// closes. The users: a restricted admin with an external identity (bruce), a
// plain active user (selina), a disabled one (harvey) and a pending one
// (oswald).
func seedIdentity(t *testing.T, extra func(k *Kernel, b *storage.Batch, jks storage.Keyspace)) *oiFx {
	t.Helper()
	h, c := identityHost(t)
	k := h.mustBoot(c)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	users := []*auth.User{
		{ID: "u-bruce", Username: "bruce", Status: auth.StatusActive, Roles: []rbac.RoleName{"admin"}, HasPassword: true,
			PasswordResetRequired: true, SignInRestricted: true, CredentialEpoch: 4, CreatedAt: now, UpdatedAt: now, Version: 1},
		{ID: "u-selina", Username: "selina", Status: auth.StatusActive, HasPassword: true, CredentialEpoch: 1, CreatedAt: now, UpdatedAt: now, Version: 1},
		{ID: "u-harvey", Username: "harvey", Status: auth.StatusDisabled, CreatedAt: now, UpdatedAt: now, Version: 1},
		{ID: "u-oswald", Username: "oswald", Status: auth.StatusPending, CreatedAt: now, UpdatedAt: now, Version: 1},
	}
	b := k.ident.users.NewBatch()
	for _, u := range users {
		if err := k.ident.users.PutUser(b, u); err != nil {
			t.Fatal(err)
		}
	}
	for _, owner := range []auth.UserID{"u-bruce", "u-selina"} {
		if _, err := k.ident.users.IssueKey(b, &auth.APIKey{Owner: owner, Name: "batsignal", Scopes: []rbac.Permission{"gotham:patrol"},
			Status: auth.KeyActive, ExpiresAt: now.AddDate(1, 0, 0)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := k.ident.users.PutIdentity(b, &auth.ExternalIdentity{Provider: "gotham", Subject: "bruce-w", UserID: "u-bruce", Email: "bruce@example.invalid", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(t.Context(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	if extra != nil {
		jks, ok := k.store.db.Keyspace(journal.Owner)
		if !ok {
			t.Fatal("no journal keyspace")
		}
		eb := jks.NewBatch()
		extra(k, eb, jks)
		if err := eb.Commit(t.Context(), storage.Sync); err != nil {
			t.Fatal(err)
		}
		_ = eb.Close()
	}
	fx := &oiFx{h: h, c: c, journalPath: k.ident.journal.Path()}
	h.close(k)
	return fx
}

// inspect opens the closed store the way an operator command does and hands fn
// the open; the open is closed after.
func (fx *oiFx) inspect(t *testing.T, fn func(oi *offlineIdentity)) {
	t.Helper()
	oi, err := openOfflineIdentity(t.Context(), fx.c, "test")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := oi.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	fn(oi)
}

func (fx *oiFx) user(t *testing.T, name string) (u *auth.User, hash string) {
	t.Helper()
	fx.inspect(t, func(oi *offlineIdentity) {
		users, err := oi.users(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if u, err = users.UserByUsername(t.Context(), name); err != nil {
			t.Fatalf("user %s: %v", name, err)
		}
		hash, _ = users.PasswordHash(t.Context(), u.ID)
	})
	return u, hash
}

// journalLines is the verified lines of the journal file at path, read with the
// open's keyring; the chain must not be broken.
func journalLines(t *testing.T, oi *offlineIdentity, path string) []journal.Line {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the journal: %v", err)
	}
	chain := journal.ReadChain(raw, oi.k.sec.store)
	if chain.Break != nil {
		t.Fatalf("the journal chain is broken: %+v", chain.Break)
	}
	return chain.Lines
}

func (fx *oiFx) lines(t *testing.T) (out []journal.Line) {
	t.Helper()
	fx.inspect(t, func(oi *offlineIdentity) { out = journalLines(t, oi, oi.journalPath()) })
	return out
}

func (fx *oiFx) auditRecords(t *testing.T, prefix string) (out []audit.Record) {
	t.Helper()
	fx.inspect(t, func(oi *offlineIdentity) {
		for r, err := range oi.k.audit.Query(t.Context(), audit.Filter{ActionPrefix: prefix, Limit: 100}) {
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
	})
	return out
}

// keyStatuses is every API key's status by its owner's user ID (one key each
// in the fixture).
func (fx *oiFx) keyStatuses(t *testing.T) map[string]auth.KeyStatus {
	t.Helper()
	out := map[string]auth.KeyStatus{}
	fx.inspect(t, func(oi *offlineIdentity) {
		users, err := oi.users(t.Context())
		must(t, err)
		for k, err := range users.ScanAPIKeys(t.Context(), "") {
			must(t, err)
			out[string(k.Owner)] = k.Status
		}
	})
	return out
}

// epochs is every ordinary user's credential epoch by username.
func (fx *oiFx) epochs(t *testing.T) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	fx.inspect(t, func(oi *offlineIdentity) {
		users, err := oi.users(t.Context())
		must(t, err)
		for u, err := range users.ScanUsers(t.Context()) {
			must(t, err)
			if u.Status != auth.StatusSystem {
				out[u.Username] = u.CredentialEpoch
			}
		}
	})
	return out
}

func TestOfflineResetPassword_Effect(t *testing.T) {
	fx := seedIdentity(t, nil)
	before, oldHash := fx.user(t, "bruce")
	keysBefore := fx.keyStatuses(t)
	res, err := ResetPasswordOffline(t.Context(), fx.c, "  BRUCE ", "ops")
	if err != nil {
		t.Fatalf("ResetPasswordOffline: %v", err)
	}
	if !maps.Equal(fx.keyStatuses(t), keysBefore) {
		t.Errorf("a reset changed API keys: %v -> %v", keysBefore, fx.keyStatuses(t))
	}
	if len(res.Password) < 24 {
		t.Fatalf("the generated password has %d characters", len(res.Password))
	}
	if !res.ClearedRestriction || !res.ClearedResetRequired || res.JournalGap {
		t.Errorf("result = %#v, want both flags cleared and no gap", res)
	}
	after, hash := fx.user(t, "bruce")
	if after.SignInRestricted || after.PasswordResetRequired {
		t.Errorf("flags after the reset: restricted=%v reset_required=%v", after.SignInRestricted, after.PasswordResetRequired)
	}
	if after.CredentialEpoch != before.CredentialEpoch+1 || res.Epoch != after.CredentialEpoch {
		t.Errorf("epoch = %d (result %d), want %d", after.CredentialEpoch, res.Epoch, before.CredentialEpoch+1)
	}
	if !after.HasPassword || hash == "" || hash == oldHash {
		t.Errorf("hash after the reset: has=%v same=%v", after.HasPassword, hash == oldHash)
	}
	if ok, _ := password.New(password.Options{}).Verify(password.AlgoOf(hash), hash, res.Password); !ok {
		t.Error("the stored hash does not verify the printed password")
	}
	if after.Status != before.Status || !slices.Equal(after.Roles, before.Roles) {
		t.Errorf("status/roles changed: %v %v -> %v %v", before.Status, before.Roles, after.Status, after.Roles)
	}
	// An external identity link is neither re-created nor deleted by the reset:
	// what a restore unlinked stays unlinked, and a link it kept stays.
	fx.inspect(t, func(oi *offlineIdentity) {
		users, err := oi.users(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var n int
		for id, err := range users.ScanIdentities(t.Context(), "u-bruce") {
			if err != nil {
				t.Fatal(err)
			}
			n++
			if id.Provider != "gotham" {
				t.Errorf("identity %+v", id)
			}
		}
		if n != 1 {
			t.Errorf("the reset left %d external identity link(s), want the 1 seeded", n)
		}
	})
	// Another user is untouched.
	if sel, _ := fx.user(t, "selina"); sel.Version != 1 || sel.CredentialEpoch != 1 {
		t.Errorf("selina changed: %+v", sel)
	}
}

func TestOfflineResetPassword_Refusals(t *testing.T) {
	fx := seedIdentity(t, nil)
	linesBefore := len(fx.lines(t))
	auditBefore := fx.auditRecords(t, "")
	for _, r := range auditBefore {
		t.Logf("audit record present before any command ran: %s (%s)", r.Action, r.Actor.Method)
	}
	for _, tc := range []struct {
		name, user string
		want       error
	}{
		{"unknown", "joker", auth.ErrNotFound},
		{"empty", "  ", auth.ErrBadInput},
		{"disabled", "harvey", auth.ErrResetPasswordTarget},
		{"pending", "oswald", auth.ErrResetPasswordTarget},
		{"system user", "_system", auth.ErrResetPasswordTarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ResetPasswordOffline(t.Context(), fx.c, tc.user, "ops")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if res.Password != "" {
				t.Error("a refused reset returned a password")
			}
		})
	}
	if got := len(fx.lines(t)); got != linesBefore {
		t.Errorf("refused resets appended %d journal line(s)", got-linesBefore)
	}
	if recs := fx.auditRecords(t, ""); len(recs) != len(auditBefore) {
		t.Errorf("refused resets, or the opens around them, changed the audit log: %d -> %d records", len(auditBefore), len(recs))
	}
	for _, name := range []string{"harvey", "oswald", "bruce", "_system"} {
		u, _ := fx.user(t, name)
		if name == "bruce" && (!u.SignInRestricted || u.CredentialEpoch != 4) {
			t.Errorf("bruce changed by a refused reset: %+v", u)
		}
		if name != "bruce" && (u.CredentialEpoch != 0 || u.Version != 1) {
			t.Errorf("%s changed: %+v", name, u)
		}
	}
}

func TestOfflineResetPassword_JournalAndAudit(t *testing.T) {
	fx := seedIdentity(t, nil)
	before := fx.lines(t)
	res, err := ResetPasswordOffline(t.Context(), fx.c, "bruce", "ops")
	if err != nil {
		t.Fatal(err)
	}
	after := fx.lines(t)
	if len(after) != len(before)+2 {
		t.Fatalf("journal lines %d -> %d, want +2", len(before), len(after))
	}
	got := after[len(before):]
	if got[0].Kind != journal.PasswordChanged || got[1].Kind != journal.CredentialEpochBumped {
		t.Errorf("kinds = %s, %s", got[0].Kind, got[1].Kind)
	}
	for _, l := range got {
		if l.Subject.ID != "u-bruce" || l.Epoch != res.Epoch {
			t.Errorf("line %+v: want subject u-bruce epoch %d", l, res.Epoch)
		}
	}
	recs := fx.auditRecords(t, "core.user.")
	if len(recs) != 1 {
		t.Fatalf("%d audit record(s), want 1: %+v", len(recs), recs)
	}
	r := recs[0]
	if r.Actor.Method != "offline" || r.Actor.Username != "ops" || r.Outcome != audit.Success || r.Target.ID != "u-bruce" {
		t.Errorf("audit record = %+v", r)
	}
	if v, _ := r.Details["offline"].(bool); !v {
		t.Errorf("details = %v, want offline=true", r.Details)
	}
	if strings.Contains(strings.ToLower(strings.Join(detailStrings(r.Details), " ")), strings.ToLower(res.Password)) {
		t.Error("the audit record carries the password")
	}
}

func detailStrings(m map[string]any) (out []string) {
	for k, v := range m {
		out = append(out, k)
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// TestOfflineResetPassword_NoPlaintextOnDisk: the printed password is in no file
// under the state directory, the store, or the backup directory.
func TestOfflineResetPassword_NoPlaintextOnDisk(t *testing.T) {
	fx := seedIdentity(t, nil)
	res, err := ResetPasswordOffline(t.Context(), fx.c, "bruce", "ops")
	if err != nil {
		t.Fatal(err)
	}
	needle := []byte(res.Password)
	roots := []string{fx.c.StateDir, filepath.Dir(fx.c.StoragePath), filepath.Dir(fx.journalPath)}
	scanned := 0
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			scanned++
			if bytes.Contains(raw, needle) {
				t.Errorf("the plaintext password is in %s", path)
			}
			return nil
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no files: the roots are wrong")
	}
	if got := res.String() + " " + res.GoString(); strings.Contains(got, res.Password) {
		t.Error("the result formats its password")
	}
}

// TestOfflineIdentity_RunningServiceRefused: a store a serving kernel holds is
// refused by every identity command, and nothing is written. The refusal is
// OpenOffline's, whose own tests classify it (ErrStoreHeld across processes);
// inside this process it is Pebble's lock error, which is all this pins: the
// commands open the store before they read or write anything.
func TestOfflineIdentity_RunningServiceRefused(t *testing.T) {
	h, c := identityHost(t)
	k := h.mustBoot(c)
	defer h.close(k)
	linesBefore := len(readLines(t, k))
	if _, err := ResetPasswordOffline(t.Context(), c, "bruce", "ops"); err == nil {
		t.Error("reset-password ran against a running service")
	}
	if _, err := JournalQueueList(t.Context(), c); err == nil {
		t.Error("journal-queue list ran against a running service")
	}
	if _, err := JournalQueueClear(t.Context(), c, 1, "ops"); err == nil {
		t.Error("journal-queue clear ran against a running service")
	}
	if got := len(readLines(t, k)); got != linesBefore {
		t.Errorf("a refused command changed the journal: %d -> %d lines", linesBefore, got)
	}
}

func readLines(t *testing.T, k *Kernel) []journal.Line {
	t.Helper()
	raw, err := os.ReadFile(k.ident.journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	return journal.ReadChain(raw, k.sec.store).Lines
}

func TestOfflineIdentity_NotDeclaredRefuses(t *testing.T) {
	fx := seedIdentity(t, nil)
	c := fx.c
	c.IdentityBoot = false
	if _, err := ResetPasswordOffline(t.Context(), c, "bruce", "ops"); !errors.Is(err, ErrIdentityNotDeclared) {
		t.Errorf("reset err = %v, want ErrIdentityNotDeclared", err)
	}
	if _, err := JournalQueueList(t.Context(), c); !errors.Is(err, ErrIdentityNotDeclared) {
		t.Errorf("list err = %v, want ErrIdentityNotDeclared", err)
	}
	if _, err := JournalQueueClear(t.Context(), c, 1, "x"); !errors.Is(err, ErrIdentityNotDeclared) {
		t.Errorf("clear err = %v, want ErrIdentityNotDeclared", err)
	}
}

// TestOfflineResetPassword_UnusableJournalRecordsGap: a journal that cannot take
// a line never blocks the way back in; the reset commits and a gap is recorded.
func TestOfflineResetPassword_UnusableJournalRecordsGap(t *testing.T) {
	fx := seedIdentity(t, nil)
	if err := os.Remove(fx.journalPath); err != nil {
		t.Fatal(err)
	}
	// A directory at the journal's path: Bind cannot open it.
	if err := os.Mkdir(fx.journalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	// The recorded location names the journal, so the open uses that path.
	res, err := ResetPasswordOffline(t.Context(), fx.c, "bruce", "ops")
	if err != nil {
		t.Fatalf("a broken journal blocked the reset: %v", err)
	}
	if !res.JournalGap {
		t.Error("the result does not report the journal gap")
	}
	if u, _ := fx.user(t, "bruce"); u.SignInRestricted {
		t.Error("the reset did not commit")
	}
}

// TestOfflineIdentity_JournalPathIsTheServeBoots pins the offline open's journal
// file name to the one runIdentityP4d binds, which this build builds in another
// file: a drift would make an offline reset append to a second journal.
func TestOfflineIdentity_JournalPathIsTheServeBoots(t *testing.T) {
	fx := seedIdentity(t, nil)
	fx.inspect(t, func(oi *offlineIdentity) {
		if want, got := fx.journalPath, filepath.Join(oi.backupDir, journalFileName(oi.prefix)); got != want {
			t.Errorf("offline journal path %q, serve boot's %q", got, want)
		}
		if got := oi.journalPath(); got != fx.journalPath {
			t.Errorf("journalPath() = %q, serve boot's %q", got, fx.journalPath)
		}
	})
}

// queueFx seeds a journal with one real line plus the queue rows the tests
// name: a pending entry (the line is in the journal), an orphan (no such line),
// a poison row (not JSON), one whose key and entry disagree, and a malformed key.
func queueFx(t *testing.T) (fx *oiFx, pendingSeq uint64) {
	t.Helper()
	fx = seedIdentity(t, func(k *Kernel, b *storage.Batch, jks storage.Keyspace) {
		ctx := t.Context()
		at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
		if err := k.ident.journal.Append(ctx, b, journal.Revocation{Kind: journal.CredentialEpochBumped, At: at, Epoch: 9,
			Subject: rxUserRef("u-selina")}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(k.ident.journal.Path())
		if err != nil {
			t.Fatal(err)
		}
		lines := journal.ReadChain(raw, k.sec.store).Lines
		last := lines[len(lines)-1]
		pendingSeq = last.Seq
		stage := func(u journal.Unapplied) {
			if err := journal.StageUnapplied(ctx, b, jks, u); err != nil {
				t.Fatal(err)
			}
		}
		stage(journal.Unapplied{Seq: last.Seq, MAC: last.MAC, Kind: "batcave.future", At: at})
		stage(journal.Unapplied{Seq: 500, MAC: "00ff", Kind: "batcave.future", At: at})
		must := func(err error) {
			if err != nil {
				t.Fatal(err)
			}
		}
		must(b.In(jks).Set([]byte("journal_unapplied/00000000000000000007"), []byte("{not json")))
		must(b.In(jks).Set([]byte("journal_unapplied/00000000000000000009"), []byte(`{"seq":8,"mac":"aa","kind":"x"}`)))
		// A key that names another seq than its entry, the entry naming the live line:
		// the boot reads the entry, so this is a pending line, not poison.
		must(b.In(jks).Set([]byte("journal_unapplied/00000000000000000011"),
			[]byte(`{"seq":`+strconv.FormatUint(last.Seq, 10)+`,"mac":"`+last.MAC+`","kind":"x"}`)))
		must(b.In(jks).Set([]byte("journal_unapplied/zzz"), []byte(`{"seq":3,"mac":"aa","kind":"x"}`)))
	})
	return fx, pendingSeq
}

func entryStates(es []JournalQueueEntry) map[string]JournalQueueState {
	m := map[string]JournalQueueState{}
	for _, e := range es {
		m[e.Key[strings.LastIndex(e.Key, "/")+1:]] = e.State
	}
	return m
}

func TestJournalQueue_ListClassifies(t *testing.T) {
	fx, pending := queueFx(t)
	es, err := JournalQueueList(t.Context(), fx.c)
	if err != nil {
		t.Fatal(err)
	}
	got := entryStates(es)
	pk := keyOf(pending)
	want := map[string]JournalQueueState{
		pk: QueuePending, "00000000000000000500": QueueOrphan,
		"00000000000000000007": QueuePoison, "00000000000000000009": QueueOrphan, "00000000000000000011": QueuePending,
		"zzz": QueueMalformedKey,
	}
	if len(got) != len(want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %s, want %s", k, got[k], w)
		}
	}
	for _, e := range es {
		if e.Reason == "" {
			t.Errorf("%s has no reason", e.Key)
		}
	}
}

func TestJournalQueue_ClearOnlyOrphanAndPoison(t *testing.T) {
	fx, pending := queueFx(t)
	ctx := t.Context()
	// A pending entry is never cleared.
	if _, err := JournalQueueClear(ctx, fx.c, pending, "ops"); !errors.Is(err, ErrJournalQueueNotClearable) {
		t.Fatalf("clear pending = %v, want ErrJournalQueueNotClearable", err)
	}
	if _, err := JournalQueueClear(ctx, fx.c, 4242, "ops"); !errors.Is(err, ErrJournalQueueNoEntry) {
		t.Fatalf("clear absent = %v, want ErrJournalQueueNoEntry", err)
	}
	// A malformed key has no seq to name.
	if _, err := JournalQueueClear(ctx, fx.c, 3, "ops"); !errors.Is(err, ErrJournalQueueNoEntry) {
		t.Fatalf("clear by a malformed key's entry seq = %v, want ErrJournalQueueNoEntry", err)
	}
	if _, err := JournalQueueClear(ctx, fx.c, 11, "ops"); !errors.Is(err, ErrJournalQueueNotClearable) {
		t.Fatalf("clear of a row whose entry names a live line = %v, want ErrJournalQueueNotClearable", err)
	}
	if es, _ := JournalQueueList(ctx, fx.c); len(es) != 6 {
		t.Fatalf("refused clears changed the queue: %d rows", len(es))
	}
	if recs := fx.auditRecords(t, "core.storage.journal_queue_"); len(recs) != 0 {
		t.Fatalf("refused clears audited %d record(s)", len(recs))
	}
	for _, seq := range []uint64{500, 9} {
		cleared, err := JournalQueueClear(ctx, fx.c, seq, "ops")
		if err != nil {
			t.Fatalf("clear %d: %v", seq, err)
		}
		if cleared.State != QueueOrphan || cleared.Clear != ClearRemove {
			t.Errorf("cleared a %s entry by %q, want an orphan removed", cleared.State, cleared.Clear)
		}
	}
	es, err := JournalQueueList(ctx, fx.c)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryStates(es); len(got) != 4 || got[keyOf(pending)] != QueuePending || got["00000000000000000011"] != QueuePending ||
		got["00000000000000000007"] != QueuePoison || got["zzz"] != QueueMalformedKey {
		t.Errorf("rows left = %v, want the two pending entries, the poison row and the malformed key", got)
	}
	// Removing an orphan changes no access: no key and no epoch moves.
	if keys := fx.keyStatuses(t); keys["u-bruce"] != auth.KeyActive || keys["u-selina"] != auth.KeyActive {
		t.Errorf("clearing orphans changed API keys: %v", keys)
	}
	recs := fx.auditRecords(t, "core.storage.journal_queue_")
	if len(recs) != 2 {
		t.Fatalf("%d audit record(s), want 2", len(recs))
	}
	for _, r := range recs {
		if r.Action != "core.storage.journal_queue_clear" || r.Actor.Method != "offline" || r.Actor.Username != "ops" || r.Outcome != audit.Success {
			t.Errorf("audit record = %+v", r)
		}
	}
}

// snapshot is every file under dir with its bytes, for before/after equality.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[path] = "<dir>"
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestJournalQueue_AJournalThatProvesNothingMakesNoOrphan: orphan means the
// journal is positively this store's whole, verified history and holds no such
// line. Each way the journal can fail to be that is made here, and in each the
// decodable entries are unverified (never orphan), clear refuses, poison is
// still poison, and listing or a refused clear leaves the journal directory
// byte for byte as it was.
func TestJournalQueue_AJournalThatProvesNothingMakesNoOrphan(t *testing.T) {
	const orphanKey = "00000000000000000500"
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, fx *oiFx)
	}{
		{"journal file missing", func(t *testing.T, fx *oiFx) { must(t, os.Remove(fx.journalPath)) }},
		{"journal file empty", func(t *testing.T, fx *oiFx) { must(t, os.WriteFile(fx.journalPath, nil, 0o600)) }},
		{"journal path is a directory", func(t *testing.T, fx *oiFx) {
			must(t, os.Remove(fx.journalPath))
			must(t, os.Mkdir(fx.journalPath, 0o700))
		}},
		{"journal truncated after its header", func(t *testing.T, fx *oiFx) {
			raw, err := os.ReadFile(fx.journalPath)
			must(t, err)
			first, _, ok := bytes.Cut(raw, []byte("\n"))
			if !ok {
				t.Fatalf("journal has no complete header line: %q", raw)
			}
			must(t, os.WriteFile(fx.journalPath, append(first, '\n'), 0o600))
		}},
		{"journal line mac corrupted", func(t *testing.T, fx *oiFx) {
			raw, err := os.ReadFile(fx.journalPath)
			must(t, err)
			i := bytes.LastIndex(raw, []byte(`"mac":"`))
			if i < 0 {
				t.Fatalf("no mac in the journal: %q", raw)
			}
			raw[i+len(`"mac":"`)] ^= 1
			must(t, os.WriteFile(fx.journalPath, raw, 0o600))
		}},
		{"another install's journal", func(t *testing.T, fx *oiFx) {
			fx.inspect(t, func(oi *offlineIdentity) {
				ks, err := oi.keyspace()
				must(t, err)
				head, err := journal.ReadHead(t.Context(), ks)
				must(t, err)
				must(t, os.Remove(fx.journalPath))
				must(t, journal.CreateBased(fx.journalPath, oi.k.sec.store,
					journal.Header{InstallID: "arkham-install", App: "gotham", StartedAt: time.Now().UTC()}, head))
			})
		}},
		{"journal path is a symlink to an intact copy", func(t *testing.T, fx *oiFx) {
			raw, err := os.ReadFile(fx.journalPath)
			must(t, err)
			real := fx.journalPath + ".real"
			must(t, os.WriteFile(real, raw, 0o600))
			must(t, os.Remove(fx.journalPath))
			must(t, os.Symlink(real, fx.journalPath))
		}},
		{"location file describes another store", func(t *testing.T, fx *oiFx) {
			loc, ok, err := journal.ReadLocation(fx.c.StateDir)
			if err != nil || !ok {
				t.Fatalf("location = %v, %v", ok, err)
			}
			loc.DBPath = filepath.Join(filepath.Dir(loc.DBPath), "arkham.db")
			must(t, journal.WriteLocation(fx.c.StateDir, loc))
		}},
		{"store head records a seq and no mac", func(t *testing.T, fx *oiFx) {
			fx.inspect(t, func(oi *offlineIdentity) {
				ks, err := oi.keyspace()
				must(t, err)
				head, err := journal.ReadHead(t.Context(), ks)
				must(t, err)
				// StageHead never moves a head sideways, so the record is written directly:
				// a head with a seq and no mac cannot be checked against the journal's line.
				b := ks.NewBatch()
				defer b.Close()
				if _, err := b.GetForUpdate(t.Context(), ks, []byte("journal_head")); err != nil {
					t.Fatal(err)
				}
				must(t, b.In(ks).Set([]byte("journal_head"), []byte(`{"seq":`+strconv.FormatUint(head.Seq, 10)+`}`)))
				must(t, b.Commit(t.Context(), storage.Sync))
			})
		}},
		{"location file unreadable", func(t *testing.T, fx *oiFx) {
			must(t, os.WriteFile(filepath.Join(fx.c.StateDir, journal.LocationFile), []byte("{not json"), 0o600))
		}},
		{"location file missing", func(t *testing.T, fx *oiFx) {
			must(t, os.Remove(filepath.Join(fx.c.StateDir, journal.LocationFile)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx, pending := queueFx(t)
			tc.damage(t, fx)
			dir := filepath.Dir(fx.journalPath)
			before := snapshot(t, dir)
			loc := filepath.Join(fx.c.StateDir, journal.LocationFile)
			locBefore, locErr := os.ReadFile(loc)
			es, err := JournalQueueList(t.Context(), fx.c)
			if err != nil {
				t.Fatal(err)
			}
			got := entryStates(es)
			if got[orphanKey] != QueueUnverified {
				t.Errorf("orphan candidate = %s, want unverified (%v)", got[orphanKey], got)
			}
			if st := got[keyOf(pending)]; st == QueueOrphan {
				t.Errorf("the live line's entry was called an orphan")
			}
			if got["00000000000000000009"] != QueueUnverified {
				t.Errorf("entry naming a missing line = %s, want unverified", got["00000000000000000009"])
			}
			if got["00000000000000000007"] != QueuePoison {
				t.Errorf("poison = %s, want poison whatever the journal says", got["00000000000000000007"])
			}
			for _, seq := range []uint64{500, 9} {
				if _, err := JournalQueueClear(t.Context(), fx.c, seq, "ops"); !errors.Is(err, ErrJournalQueueNotClearable) {
					t.Errorf("clear %d = %v, want ErrJournalQueueNotClearable", seq, err)
				}
			}
			if after := snapshot(t, dir); !maps.Equal(before, after) {
				t.Errorf("list and refused clears changed the journal directory:\nbefore %v\nafter  %v", fileNames(before), fileNames(after))
			}
			// A read never rewrites the location file that anchors the journal.
			if locAfter, err := os.ReadFile(loc); string(locAfter) != string(locBefore) || (err == nil) != (locErr == nil) {
				t.Errorf("list and refused clears changed %s", journal.LocationFile)
			}
		})
	}
}

func fileNames(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func keyOf(seq uint64) string {
	const w = 20
	d := []byte("00000000000000000000")
	for i := w - 1; seq > 0 && i >= 0; i-- {
		d[i] = byte('0' + seq%10)
		seq /= 10
	}
	return string(d)
}

// TestOfflineResetPassword_RefusalCreatesNothing: the eligibility check runs
// before the backup directory or a journal is created, so a refused reset in a
// fresh state leaves neither behind (the kernel's own check, not auth's second
// one, is what this pins).
func TestOfflineResetPassword_RefusalCreatesNothing(t *testing.T) {
	fx := seedIdentity(t, nil)
	dir := filepath.Dir(fx.journalPath)
	must(t, os.RemoveAll(dir))
	for _, name := range []string{"harvey", "oswald", "_system", "joker"} {
		if _, err := ResetPasswordOffline(t.Context(), fx.c, name, "ops"); err == nil {
			t.Fatalf("%s: a refused target was reset", name)
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: a refused reset created the backup directory (%v)", name, err)
		}
	}
	// A reset that goes through creates it, and a journal in it.
	if _, err := ResetPasswordOffline(t.Context(), fx.c, "bruce", "ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fx.journalPath); err != nil {
		t.Errorf("the successful reset left no journal at %s: %v", fx.journalPath, err)
	}
}

// TestOfflineIdentity_NeverSetsABootRecordAside: the offline open resolves the
// effective settings without reading config.boot.json, so a record a serve
// boot would set aside is left exactly where it is, with no .bad beside it.
func TestOfflineIdentity_NeverSetsABootRecordAside(t *testing.T) {
	fx := seedIdentity(t, nil)
	rec := filepath.Join(fx.c.StateDir, "config.boot.json")
	must(t, os.WriteFile(rec, []byte("{this is not a boot record"), 0o600))
	if _, err := ResetPasswordOffline(t.Context(), fx.c, "bruce", "ops"); err != nil {
		t.Fatalf("a bad boot record blocked the reset: %v", err)
	}
	if _, err := JournalQueueList(t.Context(), fx.c); err != nil {
		t.Fatalf("a bad boot record blocked the list: %v", err)
	}
	if raw, err := os.ReadFile(rec); err != nil || string(raw) != "{this is not a boot record" {
		t.Errorf("the boot record was changed (%q, %v)", raw, err)
	}
	if _, err := os.Stat(rec + ".bad"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the boot record was set aside (%v)", err)
	}
}

// TestJournalQueue_EmptyJournalUnderAnEmptyHeadProvesNothing: a store that has
// journaled nothing (head 0) with a zero-byte journal file is still not proof:
// with no verified header there is no history to hold or lack a line, so an
// entry is unverified and not clearable, not an orphan.
func TestJournalQueue_EmptyJournalUnderAnEmptyHeadProvesNothing(t *testing.T) {
	fx := seedIdentity(t, func(k *Kernel, b *storage.Batch, jks storage.Keyspace) {
		must(t, journal.StageUnapplied(t.Context(), b, jks, journal.Unapplied{
			Seq: 500, MAC: "00ff", Kind: "batcave.future", At: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}))
	})
	must(t, os.WriteFile(fx.journalPath, nil, 0o600))
	es, err := JournalQueueList(t.Context(), fx.c)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryStates(es); got["00000000000000000500"] != QueueUnverified {
		t.Errorf("states = %v, want the entry unverified", got)
	}
	if _, err := JournalQueueClear(t.Context(), fx.c, 500, "ops"); !errors.Is(err, ErrJournalQueueNotClearable) {
		t.Errorf("clear = %v, want ErrJournalQueueNotClearable", err)
	}
}

// --- poison rows: repair when the journal can speak, fail closed when it cannot ---

// poisonFx is a store with one live journal line (seq returned), an
// undecodable queue row at the seq the key names (poisonSeq), and API keys for
// two users. damage runs on the closed store before the command.
func poisonFx(t *testing.T, poisonAt func(live uint64) uint64) (fx *oiFx, live, poisonSeq uint64, line journal.Line) {
	t.Helper()
	fx = seedIdentity(t, func(k *Kernel, b *storage.Batch, jks storage.Keyspace) {
		at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
		must(t, k.ident.journal.Append(t.Context(), b, journal.Revocation{Kind: journal.CredentialEpochBumped, At: at, Epoch: 9, Subject: rxUserRef("u-selina")}))
		raw, err := os.ReadFile(k.ident.journal.Path())
		must(t, err)
		lines := journal.ReadChain(raw, k.sec.store).Lines
		line = lines[len(lines)-1]
		live = line.Seq
		poisonSeq = poisonAt(live)
		must(t, b.In(jks).Set([]byte("journal_unapplied/"+fmt.Sprintf("%020d", poisonSeq)), []byte("{not json")))
	})
	return fx, live, poisonSeq, line
}

func (fx *oiFx) queueRows(t *testing.T) (rows []journal.UnappliedRow) {
	t.Helper()
	fx.inspect(t, func(oi *offlineIdentity) {
		ks, err := oi.keyspace()
		must(t, err)
		rows, err = journal.ScanUnapplied(t.Context(), ks)
		must(t, err)
	})
	return rows
}

func clearDetails(t *testing.T, fx *oiFx) map[string]any {
	t.Helper()
	recs := fx.auditRecords(t, "core.storage.journal_queue_")
	if len(recs) != 1 {
		t.Fatalf("%d clear audit records, want 1", len(recs))
	}
	return recs[0].Details
}

// TestJournalQueue_PoisonRepairedWhenTheJournalHoldsItsLine: the verified
// journal holds the line at the seq the poison row's key names, so clear
// replaces the row with that line's entry, not yet failed closed for, and no key
// or epoch moves. The next plain boot's fail-closed pass and drain answer it.
func TestJournalQueue_PoisonRepairedWhenTheJournalHoldsItsLine(t *testing.T) {
	fx, live, seq, line := poisonFx(t, func(live uint64) uint64 { return live })
	keysBefore, epochsBefore := fx.keyStatuses(t), fx.epochs(t)
	es, err := JournalQueueList(t.Context(), fx.c)
	must(t, err)
	if len(es) != 1 || es[0].State != QueuePoison || es[0].Clear != ClearRepair || !strings.Contains(es[0].Reason, "replaces the row") {
		t.Fatalf("list = %+v, want one poison row that a clear repairs", es)
	}
	got, err := JournalQueueClear(t.Context(), fx.c, seq, "ops")
	must(t, err)
	if got.Clear != ClearRepair {
		t.Fatalf("clear did %q, want a repair", got.Clear)
	}
	rows := fx.queueRows(t)
	if len(rows) != 1 || rows[0].Err != nil || rows[0].Entry == nil {
		t.Fatalf("rows after clear = %+v, want one decodable entry", rows)
	}
	e := rows[0].Entry
	if e.Seq != live || e.MAC != line.MAC || e.Kind != line.Kind || e.Closed {
		t.Errorf("repaired entry = %+v, want seq %d, mac %s, kind %s, Closed false", *e, live, line.MAC, line.Kind)
	}
	if !maps.Equal(fx.keyStatuses(t), keysBefore) {
		t.Errorf("a repair changed API keys: %v -> %v", keysBefore, fx.keyStatuses(t))
	}
	if !maps.Equal(fx.epochs(t), epochsBefore) {
		t.Errorf("a repair changed credential epochs: %v -> %v", epochsBefore, fx.epochs(t))
	}
	if d := clearDetails(t, fx); d["action"] != "repair" || d["fallback"] != nil {
		t.Errorf("audit details = %v, want action repair and no fallback", d)
	}
	// What list now says: a plain pending entry.
	es, err = JournalQueueList(t.Context(), fx.c)
	must(t, err)
	if len(es) != 1 || es[0].State != QueuePending {
		t.Errorf("list after a repair = %+v, want one pending entry", es)
	}
}

// TestJournalQueue_PoisonFailsClosedWhenTheJournalCannotSpeak: when the verified
// journal holds no line at the poison row's seq, or the journal cannot be
// trusted at all, clear deletes the row only after failing the store closed:
// every API key needs_confirmation and every credential epoch bumped. Each way
// is asserted on its own.
func TestJournalQueue_PoisonFailsClosedWhenTheJournalCannotSpeak(t *testing.T) {
	for _, tc := range []struct {
		name     string
		poisonAt func(live uint64) uint64
		damage   func(t *testing.T, fx *oiFx)
	}{
		{"trusted journal with no line at the seq", func(live uint64) uint64 { return live + 40 }, func(*testing.T, *oiFx) {}},
		{"journal file missing", func(live uint64) uint64 { return live }, func(t *testing.T, fx *oiFx) { must(t, os.Remove(fx.journalPath)) }},
		{"journal line mac corrupted", func(live uint64) uint64 { return live }, func(t *testing.T, fx *oiFx) {
			raw, err := os.ReadFile(fx.journalPath)
			must(t, err)
			i := bytes.LastIndex(raw, []byte(`"mac":"`))
			if i < 0 {
				t.Fatalf("no mac in the journal: %q", raw)
			}
			raw[i+len(`"mac":"`)] ^= 1
			must(t, os.WriteFile(fx.journalPath, raw, 0o600))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx, _, seq, _ := poisonFx(t, tc.poisonAt)
			tc.damage(t, fx)
			epochsBefore := fx.epochs(t)
			for owner, st := range fx.keyStatuses(t) {
				if st != auth.KeyActive {
					t.Fatalf("fixture key of %s is %s, want active", owner, st)
				}
			}
			es, err := JournalQueueList(t.Context(), fx.c)
			must(t, err)
			if len(es) != 1 || es[0].State != QueuePoison || es[0].Clear != ClearFailClosed || !strings.Contains(es[0].Reason, "failing closed") {
				t.Fatalf("list = %+v, want one poison row that a clear fails closed for", es)
			}
			got, err := JournalQueueClear(t.Context(), fx.c, seq, "ops")
			must(t, err)
			if got.Clear != ClearFailClosed || got.FailClosedCounts[auth.CountKeysUnconfirmed] != 2 {
				t.Fatalf("clear = %+v, want a fail-closed clear that unconfirmed 2 keys", got)
			}
			if rows := fx.queueRows(t); len(rows) != 0 {
				t.Errorf("rows after clear = %+v, want none", rows)
			}
			for owner, st := range fx.keyStatuses(t) {
				if st != auth.KeyNeedsConfirmation {
					t.Errorf("API key of %s is %s after the clear, want needs_confirmation", owner, st)
				}
			}
			after := fx.epochs(t)
			for name, before := range epochsBefore {
				if after[name] != before+1 {
					t.Errorf("epoch of %s = %d, want %d (bumped once)", name, after[name], before+1)
				}
			}
			d := clearDetails(t, fx)
			if d["action"] != "fail_closed" || d["fallback"] != "fail_closed" || d["fallback_counts"] == nil {
				t.Errorf("audit details = %v, want the fallback and its counts named", d)
			}
		})
	}
}

// TestJournalQueue_PoisonIsNeverDeletedWithoutTheFallback: a fail-closed clear
// whose fallback cannot run (here the backup directory is a file, so the journal
// component cannot be built) fails the command and keeps the row; no key or
// epoch moves. There is no path that deletes a poison row bare.
func TestJournalQueue_PoisonIsNeverDeletedWithoutTheFallback(t *testing.T) {
	fx, _, seq, _ := poisonFx(t, func(live uint64) uint64 { return live + 40 })
	dir := filepath.Dir(fx.journalPath)
	must(t, os.RemoveAll(dir))
	must(t, os.WriteFile(dir, []byte("the batcave is closed"), 0o600))
	keysBefore, epochsBefore := fx.keyStatuses(t), fx.epochs(t)
	if _, err := JournalQueueClear(t.Context(), fx.c, seq, "ops"); err == nil {
		t.Fatal("a fail-closed clear whose fallback could not run succeeded")
	}
	if rows := fx.queueRows(t); len(rows) != 1 {
		t.Errorf("rows after the failed clear = %+v, want the poison row kept", rows)
	}
	if !maps.Equal(fx.keyStatuses(t), keysBefore) || !maps.Equal(fx.epochs(t), epochsBefore) {
		t.Errorf("a failed clear moved keys or epochs")
	}
	if recs := fx.auditRecords(t, "core.storage.journal_queue_"); len(recs) != 0 {
		t.Errorf("a failed clear audited %d record(s)", len(recs))
	}
}
