// file: internal/books/cutover/identity_blockers_test.go
// version: 1.0.0
// guid: 29019ee4-a461-42ec-b1de-1d9b35527141
// last-edited: 2026-11-12

package cutover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// fsDigest hashes every file under dir on fsys: the store and its directory,
// for the byte-identical assertions.
func fsDigest(t testing.TB, fsys vfs.FS, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	var walk func(p string)
	walk = func(p string) {
		names, err := fsys.List(p)
		if err != nil {
			t.Fatalf("list %s: %v", p, err)
		}
		for _, n := range names {
			full := fsys.PathJoin(p, n)
			fi, err := fsys.Stat(full)
			if err != nil {
				t.Fatalf("stat %s: %v", full, err)
			}
			if fi.IsDir() {
				walk(full)
				continue
			}
			f, err := fsys.Open(full)
			if err != nil {
				t.Fatalf("open %s: %v", full, err)
			}
			raw, err := io.ReadAll(f)
			if err := errors.Join(err, f.Close()); err != nil {
				t.Fatalf("read %s: %v", full, err)
			}
			sum := sha256.Sum256(raw)
			out[full] = hex.EncodeToString(sum[:])
		}
	}
	walk(dir)
	return out
}

// offers is storage cutover-blockers' reading of a blocker's Fix text: it
// offers an action when the text names the command followed by the actions it
// offers, separated by "|" (kernel.blockerOffers, which the command applies).
func offers(fix, action string) bool {
	const command = "storage cutover-blockers "
	rest := fix
	for {
		_, after, ok := strings.Cut(rest, command)
		if !ok {
			return false
		}
		word := after
		if i := strings.IndexFunc(after, func(r rune) bool { return !unicode.IsLetter(r) && r != '|' }); i >= 0 {
			word = after[:i]
		}
		if slices.Contains(strings.Split(word, "|"), action) {
			return true
		}
		rest = after
	}
}

// refusal runs one boot of the cutover build over fsys, which the preflight
// must refuse, and returns its blockers.
func (r *idRig) refusal(t testing.TB, fsys vfs.FS) []storage.CutoverBlocker {
	t.Helper()
	before := r.logicalState(t, fsys)
	err := r.boot(context.Background(), fsys, []datamigrate.DataMigration{r.migration()}, nil)
	var cbe *storage.CutoverBlockedError
	if !errors.Is(err, storage.ErrCutoverBlocked) || !errors.As(err, &cbe) {
		t.Fatalf("the boot returned %v, want storage.ErrCutoverBlocked\n%s", err, r.logs.String())
	}
	if code := kernel.ExitCode(err); code != 78 {
		t.Errorf("the refusal exits %d, want 78", code)
	}
	if after := r.logicalState(t, fsys); !digestsEqual(before, after) {
		t.Errorf("the refused boot changed the store: %s", digestDiff(before, after))
	}
	return cbe.Blockers
}

// logicalState is what a refused boot must leave as it found it, as a digest
// map: every row of books' legacy range, the store's directory entries (a
// checkpoint is a sibling of the store) and the sidecar's bytes. Pebble's own
// files are not compared: opening an engine rewrites its manifest and WAL
// whatever the caller writes, so physical bytes say nothing about the rows.
// Reading it opens the store as the pre-cutover build, on a clone, so the
// store under test is not the one that is read.
func (r *idRig) logicalState(t testing.TB, fsys vfs.FS) map[string]string {
	t.Helper()
	mem, ok := fsys.(*vfs.MemFS)
	if !ok {
		t.Fatalf("logicalState needs the rig's in-memory filesystem")
	}
	out := map[string]string{}
	for k, v := range r.dump(t, mem.CrashClone(vfs.CrashCloneCfg{})) {
		sum := sha256.Sum256(v)
		out["row "+k] = hex.EncodeToString(sum[:])
	}
	names, err := fsys.List(rigDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		out["entry "+n] = ""
	}
	side := rigDir + "/audiobooks.pebble.storage-format"
	f, err := fsys.Open(side)
	if err != nil {
		t.Fatalf("open the sidecar: %v", err)
	}
	raw, err := io.ReadAll(f)
	if err := errors.Join(err, f.Close()); err != nil {
		t.Fatalf("read the sidecar: %v", err)
	}
	sum := sha256.Sum256(raw)
	out["sidecar"] = hex.EncodeToString(sum[:])
	return out
}

func digestsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func digestDiff(a, b map[string]string) string {
	var out []string
	for k, v := range a {
		switch w, ok := b[k]; {
		case !ok:
			out = append(out, "removed "+k)
		case w != v:
			out = append(out, "changed "+k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, "added "+k)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

// blockerSession is `storage cutover-blockers` over one store: the store
// opened by Opened.BeginCutoverBlockers (no step run, never prepared), the
// step's Preflight over its source, and the edit the kernel's
// CutoverBlockers.edit makes through storage's retired-claim seam.
type blockerSession struct {
	t  testing.TB
	r  *idRig
	o  *storage.Opened
	db *storage.DB
	dm datamigrate.DataMigration
}

func (r *idRig) beginBlockers(t testing.TB, fsys vfs.FS) *blockerSession {
	t.Helper()
	ctx := context.Background()
	dm := r.migration()
	owners := r.owners(true)
	dms, err := kernel.NewDataMigrations(kernel.DataMigrationsConfig{ChunkItems: r.chunkItems, Build: rigBuildNew, Logger: slog.New(slog.DiscardHandler),
		Quarantine: storage.QuarantineWriter(nil)}, owners, dm)
	if err != nil {
		t.Fatal(err)
	}
	o, err := storage.Open(ctx, storage.OpenOptions{Path: rigStore, FS: storage.FSOf(fsys), Owners: owners, Build: rigBuildNew,
		FreeSpaceMargin: 1, DataSteps: dms.Steps(), Clock: crashmatrix.Now})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.BeginCutoverBlockers(ctx, []storage.CutoverStep{{Owner: dm.Owner, ID: dm.ID, RaisesLevel: dm.RaisesLevel}})
	if err != nil {
		t.Fatal(errors.Join(err, o.Close()))
	}
	lr, _ := db.Legacy(BooksOwner)
	r.h.mu.Lock()
	r.h.legacy = lr
	r.h.mu.Unlock()
	s := &blockerSession{t: t, r: r, o: o, db: db, dm: dm}
	t.Cleanup(s.close)
	return s
}

func (s *blockerSession) close() {
	if s.o != nil {
		_ = s.db.Close()
		s.o = nil
	}
}

func (s *blockerSession) list() []storage.CutoverBlocker {
	s.t.Helper()
	src, err := s.o.PreflightSource(BooksOwner)
	if err != nil {
		s.t.Fatal(err)
	}
	found, err := s.dm.Preflight(context.Background(), src)
	if err != nil {
		s.t.Fatal(err)
	}
	out := make([]storage.CutoverBlocker, 0, len(found))
	for _, b := range found {
		out = append(out, storage.CutoverBlocker{Step: b.Step, Key: b.Key, Family: b.Family, Reason: b.Reason, Fix: b.Fix})
	}
	return out
}

// edit is CutoverBlockers.edit: a key the current report names, whose Fix text
// offers the action, staged by the step's hook through the seam, rechecked
// over the overlay (the row no longer named, no row newly named) and
// committed with its record, or refused with nothing written.
func (s *blockerSession) edit(action string, key, value []byte) ([]string, error) {
	s.t.Helper()
	ctx := context.Background()
	current := s.list()
	i := slices.IndexFunc(current, func(b storage.CutoverBlocker) bool { return string(b.Key) == string(key) })
	if i < 0 {
		return nil, fmt.Errorf("the row at %s is not named by the current blocker report", storage.PrintableKey(key))
	}
	blocker := current[i]
	if !offers(blocker.Fix, action) {
		return nil, fmt.Errorf("step %s does not offer %s for the row at %s here (its way out: %s)", blocker.Step, action, storage.PrintableKey(key), blocker.Fix)
	}
	before := map[string]bool{}
	for _, b := range current {
		before[string(b.Key)] = true
	}
	var removed []string
	_, err := s.db.EditCutoverBlocker(ctx, storage.CutoverBlockerEdit{
		Step: blocker.Step, Owner: BooksOwner, Source: s.dm.Source, Key: key, Action: action, Reason: blocker.Reason, Build: rigBuildNew, At: idNow,
		Stage: func(ctx context.Context, b *storage.Batch) ([]string, error) {
			var err error
			if action == storage.CutoverBlockerFix {
				err = s.dm.FixBlocker(ctx, b, key, value)
			} else {
				removed, err = s.dm.DeleteBlocker(ctx, b, key)
			}
			return removed, err
		},
		Recheck: func(ctx context.Context, r storage.Reader) error {
			found, err := s.dm.Preflight(ctx, r)
			if err != nil {
				return err
			}
			for _, f := range found {
				switch {
				case string(f.Key) == string(key):
					return fmt.Errorf("the row at %s still blocks the cutover: %s", storage.PrintableKey(key), f.Reason)
				case !before[string(f.Key)]:
					return fmt.Errorf("the change would make the row at %s block the cutover: %s", storage.PrintableKey(f.Key), f.Reason)
				}
			}
			return nil
		},
		Audit: func(*storage.Batch, []string) error { return nil },
	})
	return removed, err
}

// blockerCell is one of F2-13's blocker stores.
type blockerCell struct {
	name   string
	store  string
	key    string // the blocking row
	family string
	// user is the user the cell's row names (a deleted user's references are orphaned).
	user string
}

func blockerCells() []blockerCell {
	return []blockerCell{
		{"corrupt-user", "blockers/blocker-identity-corrupt-user", "u:" + bUser1, "u:", bUser1},
		{"corrupt-apikey", "blockers/blocker-identity-corrupt-apikey", "apikey:" + bRowA, "apikey:", ""},
		{"corrupt-oauthid", "blockers/blocker-identity-corrupt-oauthid", "oauthid:" + bRowB, "oauthid:", ""},
		{"user-rejected", "blockers/blocker-identity-user-rejected", "u:" + bUser2, "u:", bUser2},
		{"apikey-orphan", "blockers/blocker-identity-apikey-orphan", "apikey:" + bRowC, "apikey:", ""},
		{"oauthid-orphan", "blockers/blocker-identity-oauthid-orphan", "oauthid:" + bRowD, "oauthid:", ""},
	}
}

// credentialValues are strings no blocker report may carry: the fixture
// password hash, every token hash of the cell's rows and raw rows' fragments.
func credentialValues(legacy map[string][]byte) []string {
	out := []string{"$argon2id$", idArgon}
	for k, v := range legacy {
		if strings.HasPrefix(k, "apikey:") {
			var h struct {
				TokenHash string `json:"token_hash"`
			}
			if json.Unmarshal(v, &h) == nil && h.TokenHash != "" {
				out = append(out, h.TokenHash)
			}
		}
		if strings.HasPrefix(k, "u:") {
			var h struct {
				PasswordHash string `json:"password_hash"`
			}
			if json.Unmarshal(v, &h) == nil && h.PasswordHash != "" {
				out = append(out, h.PasswordHash)
			}
		}
	}
	return out
}

// TestCutoverStopsOnUnconvertibleIdentity (05-storage §5S.3.13; 09 §9.2.3;
// F4-06 STOP 2): a corrupt u:, apikey: or oauthid: row, a decodable user the
// importer would reject, and a key or identity whose user does not exist each
// refuse the boot in the preflight with exit 78, a report that names the key
// and a reason and carries no value, the store's directory byte-identical and
// no checkpoint. A blocker is never quarantined.
func TestCutoverStopsOnUnconvertibleIdentity(t *testing.T) {
	for _, c := range blockerCells() {
		t.Run(c.name, func(t *testing.T) {
			r := newIDRig(t, c.store)
			legacy := r.before(t)
			blockers := r.refusal(t, r.newMem(t))
			if len(blockers) != 1 || string(blockers[0].Key) != c.key {
				t.Fatalf("blockers %+v, want exactly the row %s", blockers, c.key)
			}
			b := blockers[0]
			if b.Step != IdentityCutoverID || b.Family != c.family || b.Reason == "" || b.Fix == "" {
				t.Errorf("blocker %+v: want step %s, family %s, a reason and a fix", b, IdentityCutoverID, c.family)
			}
			if !offers(b.Fix, "fix") || !offers(b.Fix, "delete") {
				t.Errorf("fix text %q does not offer storage cutover-blockers fix and delete", b.Fix)
			}
			// Never a value: not in the blocker, not in the boot's log, not in the rendered report.
			text := strings.Join(storage.CutoverBlockerReport(blockers), "\n") + "\n" + b.Reason + b.Fix + r.logs.String()
			for _, v := range credentialValues(legacy) {
				if strings.Contains(text, v) {
					t.Errorf("the report carries a credential value %q...", v[:8])
				}
			}
			if strings.Contains(strings.ToLower(text), "quarantin") {
				t.Errorf("the report speaks of quarantine: a blocker is never quarantined\n%s", text)
			}
		})
	}
}

// repairedRows are a valid replacement for each blocking row, in HEAD's format.
func repairedRow(c blockerCell) []byte {
	switch c.name {
	case "corrupt-user", "user-rejected":
		return mustMarshal(map[string]any{"id": c.user, "username": "harvey.bullock", "email": "", "password_hash_algo": "argon2id", "password_hash": idArgon,
			"roles": []string{"viewer"}, "status": "active", "created_at": "2026-11-10T11:00:00Z", "updated_at": "2026-11-10T11:00:00Z", "version": 1})
	case "corrupt-apikey":
		return mustMarshal(map[string]any{"id": strings.TrimPrefix(c.key, "apikey:"), "user_id": uBarbara, "name": "repaired", "token_hash": idDigest("mck-repaired-0001"),
			"scopes": []string{"library.view"}, "status": "active", "created_at": "2026-11-10T11:00:00Z", "expires_at": "2027-11-10T11:00:00Z"})
	case "corrupt-oauthid":
		return mustMarshal(map[string]any{"id": strings.TrimPrefix(c.key, "oauthid:"), "provider": "github", "subject": "repaired-subject", "email": "", "user_id": uBarbara, "created_at": "2026-11-10T11:00:00Z"})
	}
	return nil
}

func mustMarshal(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

// TestCutoverStopsOnUnconvertibleIdentity_FixAndDelete: `storage cutover-blockers
// fix` with a repaired value, or `delete`, clears the blocker and the next
// boot completes; a fix that does not convert is refused with the store
// byte-identical; a deleted user's keys and identities go with it and its
// referencing books rows are counted in orphaned_refs.
func TestCutoverStopsOnUnconvertibleIdentity_FixAndDelete(t *testing.T) {
	for _, c := range blockerCells() {
		if repairedRow(c) != nil {
			t.Run(c.name+"/fix", func(t *testing.T) {
				r := newIDRig(t, c.store)
				mem := r.newMem(t)
				s := r.beginBlockers(t, mem)
				if _, err := s.edit(storage.CutoverBlockerFix, []byte(c.key), repairedRow(c)); err != nil {
					t.Fatalf("fix: %v", err)
				}
				if left := s.list(); len(left) != 0 {
					t.Fatalf("after the fix the report still names %+v", left)
				}
				s.close()
				snap, err := r.runOn(t, mem)
				if err != nil {
					t.Fatalf("the boot after the fix: %v\n%s", err, r.logs.String())
				}
				id := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(c.key, "u:"), "apikey:"), "oauthid:")
				switch c.family {
				case "u:":
					if _, ok := snap.Users[id]; !ok {
						t.Errorf("the repaired user %s was not imported", id)
					}
				case "apikey:":
					if _, ok := snap.Keys[id]; !ok {
						t.Errorf("the repaired key %s was not imported", id)
					}
				case "oauthid:":
					if _, ok := snap.Idents["github/repaired-subject"]; !ok {
						t.Errorf("the repaired identity was not imported")
					}
				}
			})
		}
		t.Run(c.name+"/delete", func(t *testing.T) {
			rows := []seedRow(nil)
			if c.user != "" {
				// Books rows that name the user the delete removes.
				rows = []seedRow{{"bookmark:" + c.user + ":book-1", "{}"}, {"ubs:" + c.user + ":book-1", "{}"}, {"ubs:" + c.user + ":book-2", "{}"}}
			}
			r := newIDRig(t, c.store, idRows(rows...))
			mem := r.newMem(t)
			s := r.beginBlockers(t, mem)
			ids, err := s.edit(storage.CutoverBlockerDelete, []byte(c.key), nil)
			if err != nil {
				t.Fatalf("delete: %v", err)
			}
			if len(ids) == 0 {
				t.Errorf("the delete reports no removed ID")
			}
			s.close()
			snap, err := r.runOn(t, mem)
			if err != nil {
				t.Fatalf("the boot after the delete: %v\n%s", err, r.logs.String())
			}
			if c.user != "" {
				if _, ok := snap.Users[c.user]; ok {
					t.Errorf("the deleted user %s was imported", c.user)
				}
				if got := snap.Verify.OrphanedRefs["ubs:"]; got != 2 {
					t.Errorf("orphaned ubs: refs = %d, want 2 (%v)", got, snap.Verify.OrphanedRefs)
				}
				if got := snap.Verify.OrphanedRefs["bookmark:"]; got != 1 {
					t.Errorf("orphaned bookmark: refs = %d, want 1 (%v)", got, snap.Verify.OrphanedRefs)
				}
			}
			if hasPrefix(snap.Legacy, c.key) {
				t.Errorf("the deleted row %s is still in the store", c.key)
			}
		})
	}
}

// TestCutoverBlockers_UserDeleteTakesItsRows: deleting a user takes its keys,
// its identities and their index rows with it, and returns every ID.
func TestCutoverBlockers_UserDeleteTakesItsRows(t *testing.T) {
	const (
		user   = bUser2
		key1   = "01M7ZZZDELKEY00000000001A"
		key2   = "01M7ZZZDELKEY00000000002B"
		ident  = "01M7ZZZDELIDENT0000000003"
		other  = "01M7ZZZOTHERKEY00000000C"
		before = "setting:bootstrap_token_hash"
	)
	_ = before
	r := newIDRig(t, "blockers/blocker-identity-user-rejected", idRows(rowsOf(
		headKeyRows(key1, user, "mck-two-face-1", []string{"library.view"}, "active"),
		headKeyRows(key2, user, "mck-two-face-2", []string{"library.view"}, "active"),
		headKeyRows(other, uBarbara, "mck-oracle-1", []string{"library.view"}, "active"),
		[]seedRow{
			{"oauthid:" + ident, map[string]any{"id": ident, "provider": "github", "subject": "two-face", "user_id": user, "created_at": "2026-11-10T11:00:00Z"}},
			{"idx:oauth:sub:github:two-face", ident},
			{"idx:oauth:user:" + user + ":" + ident, ident},
		},
	)...))
	mem := r.newMem(t)
	s := r.beginBlockers(t, mem)
	// The seeded user's key rows name a user whose row is a blocker, not an orphan.
	ids, err := s.edit(storage.CutoverBlockerDelete, []byte("u:"+user), nil)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	want := sortedStrings(user, key1, key2, ident)
	got := slices.Clone(ids)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the delete removed %v, want %v", got, want)
	}
	legacy := map[string][]byte{}
	lr, _ := s.db.Legacy(BooksOwner)
	for kv, err := range lr.Scan(context.Background(), storage.ScanOptions{}) {
		if err != nil {
			t.Fatal(err)
		}
		legacy[string(kv.Key)] = kv.Value
	}
	for _, gone := range []string{"u:" + user, "apikey:" + key1, "apikey:" + key2, "oauthid:" + ident, "idx:apikey:user:" + user + ":" + key1,
		"idx:apikey:hash:" + idDigest("mck-two-face-1"), "idx:apikey:hash:" + idDigest("mck-two-face-2"), "idx:oauth:sub:github:two-face", "idx:oauth:user:" + user + ":" + ident} {
		if _, ok := legacy[gone]; ok {
			t.Errorf("%s survives the user's delete", gone)
		}
	}
	for _, kept := range []string{"apikey:" + other, "idx:apikey:hash:" + idDigest("mck-oracle-1"), "u:" + uBarbara} {
		if _, ok := legacy[kept]; !ok {
			t.Errorf("%s was deleted with the user", kept)
		}
	}
}

// TestCutoverBlockers_RefusedFixWritesNothing: a fix whose value still does not
// convert, a fix that makes another row block, and a key the report does not
// name are each refused, and the store's directory is byte-identical.
func TestCutoverBlockers_RefusedFixWritesNothing(t *testing.T) {
	r := newIDRig(t, "blockers/blocker-identity-apikey-orphan")
	mem := r.newMem(t)
	s := r.beginBlockers(t, mem)
	before := r.logicalState(t, mem)
	key := "apikey:" + bRowC
	for name, value := range map[string][]byte{
		"a value that is not a key":         []byte("not json"),
		"a key whose owner is still absent": mustMarshal(map[string]any{"id": bRowC, "user_id": "01M7ZZZNOSUCHUSER0000000X", "name": "x", "token_hash": idDigest("mck-x"), "scopes": []string{"library.view"}, "status": "active", "created_at": "2026-11-10T11:00:00Z", "expires_at": "2027-11-10T11:00:00Z"}),
		"a key with another ID":             mustMarshal(map[string]any{"id": "01M7ZZZOTHERID000000000Y", "user_id": uBarbara, "name": "x", "token_hash": idDigest("mck-x"), "scopes": []string{"library.view"}, "status": "active", "created_at": "2026-11-10T11:00:00Z", "expires_at": "2027-11-10T11:00:00Z"}),
	} {
		if _, err := s.edit(storage.CutoverBlockerFix, []byte(key), value); err == nil {
			t.Errorf("fix with %s was accepted", name)
		}
	}
	if _, err := s.edit(storage.CutoverBlockerDelete, []byte("u:"+uBarbara), nil); err == nil {
		t.Errorf("a delete of a row the report does not name was accepted")
	}
	s.close()
	if after := r.logicalState(t, mem); !digestsEqual(before, after) {
		t.Errorf("refused edits changed the store: %s", digestDiff(before, after))
	}
}

// TestCutoverRefusedWhileMergeInProgress (09 §9.2.3, 05-storage §5S.3.13): a
// user-merge journal at applying or undoing refuses the boot with nothing
// written and a fix that says to finish or undo the merge on the pre-freeze
// release, one that storage cutover-blockers does not offer; a terminal
// journal does not block, its merge is imported, and M3 rewrites the journal
// once with its invite tokens hashed. The merge-undo 409 merge_permanent cell
// lands with F4-08.
func TestCutoverRefusedWhileMergeInProgress(t *testing.T) {
	for _, c := range []struct{ name, store string }{{"applying", "blockers/blocker-merge-applying"}, {"undoing", "blockers/blocker-merge-undoing"}} {
		t.Run(c.name, func(t *testing.T) {
			r := newIDRig(t, c.store)
			mem := r.newMem(t)
			blockers := r.refusal(t, mem)
			if len(blockers) != 1 {
				t.Fatalf("blockers %d, want only the journal: %+v", len(blockers), blockers)
			}
			b := blockers[0]
			if b.Family != m3.JournalFamily || !strings.HasPrefix(string(b.Key), m3.JournalPrefix) || b.Step != IdentityCutoverID {
				t.Errorf("blocker %+v is not a merge journal's", b)
			}
			if !strings.Contains(b.Reason, c.name) {
				t.Errorf("reason %q does not name the journal's status %s", b.Reason, c.name)
			}
			if !strings.Contains(b.Fix, "finish or undo") || strings.Contains(b.Fix, "cutover-blockers") || offers(b.Fix, "fix") || offers(b.Fix, "delete") {
				t.Errorf("fix %q must tell the operator to finish or undo the merge and offer neither fix nor delete", b.Fix)
			}
			s := r.beginBlockers(t, mem)
			for _, action := range []string{storage.CutoverBlockerFix, storage.CutoverBlockerDelete} {
				if _, err := s.edit(action, b.Key, []byte("{}")); err == nil {
					t.Errorf("%s of a merge journal was accepted", action)
				}
			}
		})
	}
	t.Run("terminal", func(t *testing.T) {
		r := newIDRig(t, "blockers/blocker-merge-terminal")
		before := r.before(t)
		var rawTokens []string
		for k := range before {
			for _, p := range []string{"invite:", "sess:"} {
				if tok, ok := strings.CutPrefix(k, p); ok {
					rawTokens = append(rawTokens, tok)
				}
			}
		}
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("a terminal merge blocks: %v\n%s", err, r.logs.String())
		}
		journals := 0
		for k, v := range snap.Legacy {
			if !strings.HasPrefix(k, m3.JournalPrefix) {
				continue
			}
			journals++
			if strings.Contains(string(v), "\"invite:") || strings.Contains(string(v), "invite:r2corpus") {
				t.Errorf("journal %s keeps a raw invite key image", k)
			}
		}
		if journals == 0 {
			t.Errorf("the terminal journals were not kept")
		}
		changed := 0
		for k, v := range before {
			if strings.HasPrefix(k, m3.JournalPrefix) && string(snap.Legacy[k]) != string(v) {
				changed++
			}
		}
		if changed == 0 {
			t.Errorf("no journal was rewritten: the corpus's terminal journals carry invite tokens")
		}
		// No raw token anywhere in the live store: the legacy range or core's stores.
		if len(rawTokens) == 0 {
			t.Fatalf("the corpus holds no session or invite token")
		}
		var hay []string
		for k, v := range snap.Legacy {
			hay = append(hay, k, string(v))
		}
		for _, rows := range snap.raw {
			for k, v := range rows {
				hay = append(hay, k, string(v))
			}
		}
		text := strings.Join(hay, "\n")
		for _, tok := range rawTokens {
			if strings.Contains(text, tok) {
				t.Errorf("a raw token survives in the live store: %.14s...", tok)
			}
		}
	})
}
