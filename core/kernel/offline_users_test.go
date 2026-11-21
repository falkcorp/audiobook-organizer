// file: core/kernel/offline_users_test.go
// version: 0.1.0
// guid: d52b8e31-6f07-4a9c-b1e4-93c7a0f58d16
// last-edited: 2026-11-21

package kernel

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

func TestOfflineResetPassword_Effect(t *testing.T) {
	fx := seedIdentity(t, nil)
	before, oldHash := fx.user(t, "bruce")
	res, err := ResetPasswordOffline(t.Context(), fx.c, "  BRUCE ")
	if err != nil {
		t.Fatalf("ResetPasswordOffline: %v", err)
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
	for _, tc := range []struct {
		name, user string
		want       error
	}{
		{"unknown", "joker", auth.ErrNotFound},
		{"empty", "  ", auth.ErrBadInput},
		{"disabled", "harvey", auth.ErrResetPasswordTarget},
		{"pending", "oswald", auth.ErrResetPasswordTarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ResetPasswordOffline(t.Context(), fx.c, tc.user)
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
	if recs := fx.auditRecords(t, "core.user.reset_password"); len(recs) != 0 {
		t.Errorf("refused resets audited %d record(s)", len(recs))
	}
	for _, name := range []string{"harvey", "oswald", "bruce"} {
		u, _ := fx.user(t, name)
		if name == "bruce" && (!u.SignInRestricted || u.CredentialEpoch != 4) {
			t.Errorf("bruce changed by a refused reset: %+v", u)
		}
		if name != "bruce" && u.CredentialEpoch != 0 {
			t.Errorf("%s changed: %+v", name, u)
		}
	}
}

func TestOfflineResetPassword_JournalAndAudit(t *testing.T) {
	fx := seedIdentity(t, nil)
	before := fx.lines(t)
	res, err := ResetPasswordOffline(t.Context(), fx.c, "bruce")
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
	if r.Actor.Method != "offline" || r.Outcome != audit.Success || r.Target.ID != "u-bruce" {
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
	res, err := ResetPasswordOffline(t.Context(), fx.c, "bruce")
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
// refused by every identity command, and nothing is written. Inside one process
// the refusal is Pebble's own lock (a second process gets ErrStoreHeld from
// OpenOffline's exclusion tests); either way the command returns before it
// builds anything.
func TestOfflineIdentity_RunningServiceRefused(t *testing.T) {
	h, c := identityHost(t)
	k := h.mustBoot(c)
	defer h.close(k)
	linesBefore := len(readLines(t, k))
	if _, err := ResetPasswordOffline(t.Context(), c, "bruce"); err == nil {
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
	if _, err := ResetPasswordOffline(t.Context(), c, "bruce"); !errors.Is(err, ErrIdentityNotDeclared) {
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
	res, err := ResetPasswordOffline(t.Context(), fx.c, "bruce")
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
		"00000000000000000007": QueuePoison, "00000000000000000009": QueuePoison, "zzz": QueueMalformedKey,
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
	if es, _ := JournalQueueList(ctx, fx.c); len(es) != 5 {
		t.Fatalf("refused clears changed the queue: %d rows", len(es))
	}
	if recs := fx.auditRecords(t, "core.journal_queue."); len(recs) != 0 {
		t.Fatalf("refused clears audited %d record(s)", len(recs))
	}
	for _, seq := range []uint64{500, 7, 9} {
		cleared, err := JournalQueueClear(ctx, fx.c, seq, "ops")
		if err != nil {
			t.Fatalf("clear %d: %v", seq, err)
		}
		if !cleared.Clearable() {
			t.Errorf("cleared a %s entry", cleared.State)
		}
	}
	es, err := JournalQueueList(ctx, fx.c)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryStates(es); len(got) != 2 || got[keyOf(pending)] != QueuePending || got["zzz"] != QueueMalformedKey {
		t.Errorf("rows left = %v, want the pending entry and the malformed key", got)
	}
	recs := fx.auditRecords(t, "core.journal_queue.")
	if len(recs) != 3 {
		t.Fatalf("%d audit record(s), want 3", len(recs))
	}
	for _, r := range recs {
		if r.Action != "core.journal_queue.clear" || r.Actor.Method != "offline" || r.Actor.Username != "ops" || r.Outcome != audit.Success {
			t.Errorf("audit record = %+v", r)
		}
	}
}

// TestJournalQueue_NoJournalNothingIsOrphan: with the journal file gone the
// entries cannot be shown to be unreachable, so none is clearable but poison.
func TestJournalQueue_NoJournalNothingIsOrphan(t *testing.T) {
	fx, pending := queueFx(t)
	if err := os.Remove(fx.journalPath); err != nil {
		t.Fatal(err)
	}
	es, err := JournalQueueList(t.Context(), fx.c)
	if err != nil {
		t.Fatal(err)
	}
	got := entryStates(es)
	if got[keyOf(pending)] != QueueUnverified || got["00000000000000000500"] != QueueUnverified {
		t.Fatalf("states = %v, want the decodable entries unverified", got)
	}
	for _, seq := range []uint64{pending, 500} {
		if _, err := JournalQueueClear(t.Context(), fx.c, seq, "ops"); !errors.Is(err, ErrJournalQueueNotClearable) {
			t.Errorf("clear %d without a journal = %v, want ErrJournalQueueNotClearable", seq, err)
		}
	}
	if got["00000000000000000007"] != QueuePoison {
		t.Errorf("poison = %s, want poison whatever the journal says", got["00000000000000000007"])
	}
}

// TestJournalQueue_BrokenJournalNothingIsOrphan: a journal that does not
// verify to its end proves no absence either.
func TestJournalQueue_BrokenJournalNothingIsOrphan(t *testing.T) {
	fx, _ := queueFx(t)
	raw, err := os.ReadFile(fx.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the last line's mac so the chain breaks at the line the entry names.
	i := bytes.LastIndex(raw, []byte(`"mac":"`))
	if i < 0 {
		t.Fatalf("no mac in the journal: %q", raw)
	}
	raw[i+len(`"mac":"`)] ^= 1
	if err := os.WriteFile(fx.journalPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	es, err := JournalQueueList(t.Context(), fx.c)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryStates(es)["00000000000000000500"]; got != QueueUnverified {
		t.Errorf("orphan candidate over a broken journal = %s, want unverified", got)
	}
	if _, err := JournalQueueClear(t.Context(), fx.c, 500, "ops"); !errors.Is(err, ErrJournalQueueNotClearable) {
		t.Errorf("clear over a broken journal = %v, want ErrJournalQueueNotClearable", err)
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
