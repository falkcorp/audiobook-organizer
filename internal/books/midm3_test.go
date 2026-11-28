// file: internal/books/midm3_test.go
// version: 1.0.0
// guid: ce4981b5-6fb3-4679-ae0c-381198b68a0e
// last-edited: 2026-11-28

package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	abouth "github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/storage/keys"
)

// The mid-M3 cells (F4-08 fix-forward; 05-storage §5S.3.13, §5S.3.7; 05-authn
// §11). M3 (books.identity-cutover) is the one-way step the deploy runs on
// production: it imports HEAD's users, roles, keys and OAuth identities into
// core and deletes HEAD's rows, a chunk at a time. A process killed between two
// of its chunks leaves a store that is half books' and half core's. These cells
// build that store over a HEAD store with identity rows, twice with a real kill
// (the crash kit's KillAt: the in-memory filesystem as it stood before the
// chunk's WAL sync, unsynced data dropped) and once with a step failure between
// two families (midM3States), and then ask the four questions the deploy
// depends on:
//
//   - the kill state: every HEAD identity record is in exactly one place, books'
//     row or core's record, never both and never neither, and the store already
//     carries the raised levels (TestMidM3_KillLeavesEachRecordInOnePlace);
//   - this build, booted again, finishes M3 with every record exactly once and
//     serves the accounts (TestMidM3_ThisBuildResumesEveryRecordOnce);
//   - the build before the flip refuses the store before it writes anything
//     (TestMidM3_OlderBuildRefusesBeforeAnyWrite);
//   - the half store is never served: no boot of this build reaches Register
//     with M3 incomplete, and a build whose manifest lost M3 is refused before
//     Run, by the kernel's DAG check while M8 still names M3 and by Attach
//     (identityStoreReady) when nothing does (TestMidM3_HalfStoreIsNeverServed).
//
// M3's first chunk pulls every role and user forward (m3/step.go "Users first"),
// so "some users moved, others not" is not a state a kill can leave; the half
// states are "M3 planned, nothing committed" and "every user native, some of
// their keys, identities or sessions still HEAD's". The cells find those states
// by reading what each kill left, never by position, and fail if one is missing.

// midM3BulkKeys is how many more API keys the seed holds beyond seedHEAD's five,
// so M3 runs as several chunks. The runner commits a chunk every 2 000 items
// (the boot builds kernel.DataMigrationsConfig without ChunkItems, so the
// default holds; core.storage.data_migration.chunk_items is declared but never
// read by the boot), and every apikey: row sorts before the other families: with
// more than 2 000 of them, the first chunk commits every user with 2 000 keys
// and leaves the rest of the keys HEAD's, and the second drains the keys and
// stops among the index rows, before oauthid: and sess:. With fewer rows M3 is
// one chunk and no kill leaves anything committed.
const midM3BulkKeys = 2100

// midM3Subject is the OAuth subject of the seeded identity (an opaque Access sub).
const midM3Subject = "sub-lucius-fox-0007"

// midM3Env is the boot environment every cell's boot shares: what
// bootOverHEADStore sets (the ABS surface is environment-authoritative and the
// seed's blob turns it on).
func midM3Env() []string {
	return []string{
		"ABO_CORE_HTTP_LISTEN=127.0.0.1:0",
		"ABO_CORE_HTTP_BASIC_GATE_ENABLED=false",
		"ABO_BOOKS_ABS_API_ENABLED=true",
		"ABO_BOOKS_ABS_AUTH_MODES=jwt",
		"ABO_BOOKS_ABS_JWT_SECRET=synthetic-composed-app-secret-0123456789abcdef",
	}
}

// seedMidM3 is HEAD's _system row and seedHEAD (an admin, an editor owning four
// keys, a deactivated viewer owning one) plus midM3BulkKeys more keys of the editor (so M3 is
// several chunks), an OAuth identity and a live session (families that sort
// after apikey:, so a kill can land between drained families).
func seedMidM3(t *testing.T, h *headKeys) func(*database.PebbleStore) {
	t.Helper()
	inner := seedHEAD(t, h)
	return func(s *database.PebbleStore) {
		t.Helper()
		// HEAD's server seeds its _system pseudo-user at start, so every
		// production store holds the row (m3/systemuser.go): M3 imports it under
		// core's system user ID.
		require.NoError(t, abouth.SeedSystemUser(s))
		inner(s)
		lucius, err := s.GetUserByUsername("lucius.fox")
		require.NoError(t, err)
		require.NotNil(t, lucius)
		// An expiry, so M3 logs no one-shot stamp line for each of them.
		expires := time.Now().Add(30 * 24 * time.Hour)
		for i := range midM3BulkKeys {
			_, hash, err := database.GenerateAPIKeyToken()
			require.NoError(t, err)
			_, err = s.CreateAPIKey(&database.APIKey{UserID: lucius.ID, Name: fmt.Sprintf("batarang-%04d", i), TokenHash: hash, Status: "active", ExpiresAt: &expires})
			require.NoError(t, err)
		}
		_, err = s.CreateOAuthIdentity(&database.OAuthIdentity{Provider: cfaccessProvider, Subject: midM3Subject, Email: "lucius.fox@wayne-enterprises.example.test", UserID: lucius.ID})
		require.NoError(t, err)
		_, err = s.CreateSession(h.bruceUser, "192.0.2.10", "batcomputer", time.Hour, 0)
		require.NoError(t, err)
	}
}

// midM3Build is the flip boot the kit kills: books' manifest with the whole
// cutover (both level steps, M2, M3, M8) and no Register of its own, as
// TestBootCrashMatrix_ABOFlipCells boots it, but with the identity boot serve
// declares (identityOptions: core's auth and role importers bound at P4b, which
// M3 stages into).
func midM3Build(f crashmatrix.Fixture, failAt string) func(testing.TB, *crashmatrix.Injector) error {
	retired, bindings, passthrough := flipConfigNames()
	return func(_ testing.TB, inj *crashmatrix.Injector) error {
		m := flipOnAt(f.StateDir, f.DBDir)
		opts := app.Options{
			AppID: AppID, Logger: slog.New(slog.DiscardHandler), StorageStateFiles: StateFiles(f.StateDir, f.DBDir),
			RetiredConfigNames: retired, ConfigEnvBindings: bindings, ConfigPassthrough: passthrough,
			Env: append([]string{storagePathEnv + "=" + f.DBDir}, midM3Env()...),
		}
		if err := m.identityOptions(&opts); err != nil {
			return err
		}
		_, err := inj.New(context.Background(), opts, midM3Module{m: m, failAt: failAt})
		return err
	}
}

// midM3Module is aboFlipModule with the declarations the identity boot reads
// from the manifest (books' permissions, seed roles and imported role IDs: the
// catalog M3's key scopes are checked against). With failAt set, M3's step
// fails (errMidM3StepFailed) at the first source row whose key has that prefix.
type midM3Module struct {
	m      *booksModule
	failAt string
}

func (a midM3Module) Manifest() module.Manifest {
	man := aboFlipModule{m: a.m}.Manifest()
	b := a.m.Manifest()
	man.Permissions, man.Roles, man.ImportedRoleIDs = b.Permissions, b.Roles, b.ImportedRoleIDs
	if a.failAt != "" {
		inner := man.Cutover
		man.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
			steps := inner(imp)
			for i := range steps {
				if steps[i].ID != cutover.IdentityCutoverID {
					continue
				}
				step := steps[i].Step
				steps[i].Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
					if strings.HasPrefix(string(kv.Key), a.failAt) {
						return errMidM3StepFailed
					}
					return step(ctx, kv, w)
				}
			}
			return steps
		}
	}
	return man
}

func (midM3Module) Register(*module.Registrar) error { return nil }

// midM3Records are HEAD's identity records by the IDs core keeps for them.
type midM3Records struct {
	users []string // u:<id> → c/auth/ user/<id> (HEAD's _system row: user/_system)
	// coreID maps a HEAD user ID to the ID core keeps it under: its own, but
	// auth.SystemUserID for HEAD's _system row (m3/systemuser.go).
	coreID map[string]string
	keys   []string    // apikey:<id> → c/auth/ apikey/<id>
	idents [][2]string // oauthid:<id> → c/auth/ identity/<provider>/<subject>
	// identRows are the oauthid: row keys, by the same index as idents.
	identRows []string
	sessions  int // sess: rows, which M3 deletes by rule
}

// midM3RecordsOf reads HEAD's identity records from a store's rows.
func midM3RecordsOf(t *testing.T, kv map[string]string) midM3Records {
	t.Helper()
	r := midM3Records{coreID: map[string]string{}}
	for k, v := range kv {
		switch {
		case strings.HasPrefix(k, "u:"):
			id := strings.TrimPrefix(k, "u:")
			var u database.User
			require.NoError(t, json.Unmarshal([]byte(v), &u), "user row %s", k)
			r.users = append(r.users, id)
			r.coreID[id] = id
			if u.Username == abouth.SystemUserID {
				r.coreID[id] = string(coreauth.SystemUserID)
			}
		case strings.HasPrefix(k, "apikey:"):
			r.keys = append(r.keys, strings.TrimPrefix(k, "apikey:"))
		case strings.HasPrefix(k, "oauthid:"):
			var id database.OAuthIdentity
			require.NoError(t, json.Unmarshal([]byte(v), &id), "oauthid row %s", k)
			r.idents = append(r.idents, [2]string{id.Provider, id.Subject})
			r.identRows = append(r.identRows, k)
		case strings.HasPrefix(k, "sess:"):
			r.sessions++
		}
	}
	slices.Sort(r.users)
	slices.Sort(r.keys)
	return r
}

func coreAPIKeyKey(id string) string {
	return coreAuthKeyspace + string(keys.Join(keys.Str("apikey"), keys.Str(id)))
}

// coreRecordsOf counts core's auth records by kind: user/<id>, apikey/<id>,
// apikey/h/<hash>, identity/<provider>/<subject> (authstore's segments: "h" is
// the hash index, "u" the per-user indexes). A duplicate import under a
// second ID would show here as one record too many.
func coreRecordsOf(kv map[string]string) (users, apiKeys, keyHashes, idents int) {
	for k := range kv {
		rest, ok := strings.CutPrefix(k, coreAuthKeyspace)
		if !ok {
			continue
		}
		segs, err := keys.Split([]byte(rest))
		if err != nil || len(segs) == 0 {
			continue
		}
		s := func(i int) string { return string(segs[i]) }
		switch {
		case s(0) == "user" && len(segs) == 2:
			users++
		case s(0) == "apikey" && len(segs) == 2:
			apiKeys++
		case s(0) == "apikey" && len(segs) == 3 && s(1) == "h":
			keyHashes++
		case s(0) == "identity" && len(segs) == 3 && s(1) != "u":
			idents++
		}
	}
	return
}

// midM3State is a store a kill left, named by what it is.
type midM3State struct {
	name  string
	clone *vfs.MemFS
	kv    map[string]string
}

// m3Record is M3's data-migration record in kv; ok is false when M3 has not
// planned.
func m3Record(t *testing.T, kv map[string]string) (rec storage.DataMigrationRecord, ok bool) {
	t.Helper()
	raw, ok := kv[dmStateKey(cutover.IdentityCutoverID)]
	if !ok {
		return rec, false
	}
	rec, err := codec.JSON[storage.DataMigrationRecord](1).Decode([]byte(raw))
	require.NoError(t, err, "M3's record does not decode")
	return rec, true
}

// midM3Set is the fixture and the half states every mid-M3 cell runs over.
type midM3Set struct {
	fx     *flipFixture
	f      crashmatrix.Fixture
	h      *headKeys
	before midM3Records
	states []midM3State
}

// midM3Shared is the one midM3Set of the process. Building it (2,105 keys
// written, a kill series and a failed boot) is most of a cell's time, a few
// minutes under -race, so the four cells share it instead of each building its
// own. The cells never run in parallel (composedOptions sets process
// environment), and each restores the state it uses into the shared
// directories first. TestMain removes the directory (cutoverRuns.dirs).
var midM3Shared struct {
	sync.Mutex
	set *midM3Set
}

// midM3Sets is the process' midM3Set, built by the first cell that asks. A
// build that fails fails that cell and is retried by the next.
func midM3Sets(t *testing.T) *midM3Set {
	t.Helper()
	midM3Shared.Lock()
	defer midM3Shared.Unlock()
	if midM3Shared.set != nil {
		return midM3Shared.set
	}
	root, err := os.MkdirTemp("", "midm3-")
	require.NoError(t, err)
	cutoverRuns.Lock()
	cutoverRuns.dirs = append(cutoverRuns.dirs, root)
	cutoverRuns.Unlock()
	h := &headKeys{}
	fx := newFlipFixtureAt(t, filepath.Join(root, "batcave"), seedMidM3(t, h), "")
	f := crashmatrix.Fixture{Name: "mid-m3", StateDir: fx.stateDir, DBDir: fx.dbPath}
	before := midM3RecordsOf(t, rawKVs(t, fx.dbPath))
	require.Len(t, before.users, 4, "the seed's users (_system and three accounts)")
	require.Contains(t, slices.Collect(maps.Values(before.coreID)), string(coreauth.SystemUserID), "the seed holds no HEAD _system row")
	require.Len(t, before.keys, 5+midM3BulkKeys, "the seed's keys")
	require.Len(t, before.idents, 1, "the seed's OAuth identity")
	require.Equal(t, 1, before.sessions, "the seed's session")
	set := &midM3Set{fx: fx, f: f, h: h, before: before}
	set.states = midM3States(t, f, before)
	midM3Shared.set = set
	return set
}

// midM3States builds the half states M3 can leave on the fixture, each checked
// to be the state it names:
//
//   - "killed-planned": a kill before M3's first chunk commit. M3's record is
//     written and not done, every u: row is still HEAD's and core holds no user.
//   - "killed-users-native": a kill before the second chunk commit. Every user
//     is core's, no u: row is left, and keys are still HEAD's: the users and
//     roles families drained, the key, identity and session families not.
//   - "failed-between-families": M3's step fails at the first oauthid: row (a
//     defensive refusal of a later chunk, CutoverBlockedError's shape). Every
//     apikey: row is drained, and the oauthid: and sess: rows are still HEAD's.
//
// The two kills are real (crashmatrix.KillAt: the in-memory filesystem before
// the chunk's WAL sync, unsynced data dropped). The kills are found by scanning
// the storage.dm.chunk kill points in order and reading what each left, never
// by position: a large chunk's commit can carry more than one such kill point,
// and how many varies with Pebble's background work, so a later chunk's kill
// point is not reliably there. The third state is therefore an error return,
// which is deterministic. The fixture's real directories end holding it, so
// the caller restores a state before each use.
func midM3States(t *testing.T, f crashmatrix.Fixture, before midM3Records) []midM3State {
	t.Helper()
	build := midM3Build(f, "")
	found := map[string]midM3State{}
	for nth := 0; len(found) < 2; nth++ {
		require.Less(t, nth, 16, "no kill before the 16th chunk commit left both killed states: found %v", mapKeysOf(found))
		clone, err := crashmatrix.KillAt(t, f, build, "storage.dm.chunk", nth)(nil)
		require.NoError(t, err)
		st, err := readAboFlipState(clone, f)
		require.NoError(t, err)
		require.NotNil(t, st, "the kill before chunk commit %d left no store", nth)
		rec, planned := m3Record(t, st.kv)
		if !planned {
			continue // M2's chunk: M3 has not started
		}
		require.NotEqual(t, storage.DataMigrationDone, rec.State, "M3 is done at the kill before chunk commit %d: the boot left no half state", nth)
		now := midM3RecordsOf(t, st.kv)
		users, _, _, _ := coreRecordsOf(st.kv)
		var name string
		switch {
		case len(now.users) == len(before.users) && users == 0 && len(now.keys) == len(before.keys):
			name = "killed-planned"
		case len(now.users) == 0 && users >= len(before.users) && len(now.keys) > 0:
			name = "killed-users-native"
		default:
			continue
		}
		if _, ok := found[name]; !ok {
			found[name] = midM3State{name: name, clone: clone, kv: st.kv}
			t.Logf("the kill before chunk commit %d left the %s state: %d u:, %d apikey:, %d oauthid:, %d sess: rows; %d core users", nth, name, len(now.users), len(now.keys), len(now.idents), now.sessions, users)
		}
	}

	err := crashmatrix.Boot(t, midM3Build(f, "oauthid:"), f)
	require.ErrorIs(t, err, errMidM3StepFailed, "the boot whose M3 fails at the first oauthid: row")
	clone := snapshotFixture(t, f)
	st, err := readAboFlipState(clone, f)
	require.NoError(t, err)
	require.NotNil(t, st)
	rec, ok := m3Record(t, st.kv)
	require.True(t, ok, "M3 has no record after its step failed")
	require.NotEqual(t, storage.DataMigrationDone, rec.State)
	now := midM3RecordsOf(t, st.kv)
	require.Empty(t, now.users, "the failed boot left u: rows: the state is not between later families")
	// Whether every apikey: row went with its import is the cells' question
	// (each record in one place), not this builder's: only that the step got
	// past the keys and stopped before oauthid: and sess: is checked here.
	_, nativeKeys, _, _ := coreRecordsOf(st.kv)
	require.Equal(t, len(before.keys), nativeKeys, "the failed boot did not import every key: the state is not between later families")
	require.Len(t, now.idents, len(before.idents), "the failed boot drained the oauthid: family")
	require.Equal(t, before.sessions, now.sessions, "the failed boot drained the sess: family")
	t.Logf("the step failure at the first oauthid: row left the failed-between-families state: M3 %s", rec.State)

	return []midM3State{found["killed-planned"], found["killed-users-native"], {name: "failed-between-families", clone: clone, kv: st.kv}}
}

// errMidM3StepFailed is the failure midM3Build's failAt injects into M3's step.
var errMidM3StepFailed = errors.New("the test failed M3's step")

// snapshotFixture copies f's real directories into a new in-memory filesystem
// at the same paths, so restoreAboFlip can put them back later.
func snapshotFixture(t *testing.T, f crashmatrix.Fixture) *vfs.MemFS {
	t.Helper()
	mem := vfs.NewMem()
	for _, root := range []string{f.StateDir, filepath.Dir(f.DBDir)} {
		require.NoError(t, mem.MkdirAll(filepath.Dir(root), 0o755))
		_, err := vfs.Clone(vfs.Default, mem, root, root)
		require.NoError(t, err, "snapshot %s", root)
	}
	return mem
}

func mapKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// midM3Violations is what must hold of any store a kill mid-M3 leaves: each HEAD
// record in exactly one place, books stamped at the identity level with the
// sidecars at or above the stamps, and the legacy projection HEAD refuses.
func midM3Violations(t *testing.T, st *aboFlipState, before midM3Records) []string {
	t.Helper()
	var out []string
	one := func(what, legacyKey, coreKey string) {
		_, legacy := st.kv[legacyKey]
		_, native := st.kv[coreKey]
		switch {
		case legacy && native:
			out = append(out, fmt.Sprintf("%s is in both places: %s and %s", what, legacyKey, coreKey))
		case !legacy && !native:
			out = append(out, fmt.Sprintf("%s is lost: neither %s nor %s exists", what, legacyKey, coreKey))
		}
	}
	for _, id := range before.users {
		one("user "+id, "u:"+id, coreUserKey(before.coreID[id]))
	}
	for _, id := range before.keys {
		one("API key "+id, "apikey:"+id, coreAPIKeyKey(id))
	}
	for i, pv := range before.idents {
		one("OAuth identity "+pv[1], before.identRows[i], coreIdentityKey(pv[0], pv[1]))
	}
	if got := st.stamps["books"]; got != cutover.BooksIdentityLevel {
		out = append(out, fmt.Sprintf("books' stamp is %d with M3 under way, want %d (books.level-2 runs before M3)", got, cutover.BooksIdentityLevel))
	}
	for o, l := range st.stamps {
		if st.levels == nil || st.levels[o] < l {
			out = append(out, fmt.Sprintf(".storage-levels has %s at %d, below its stamp %d", o, st.levels[o], l))
		}
	}
	if st.format <= database.SupportedStorageFormat {
		out = append(out, fmt.Sprintf(".storage-format is %d, not above R0's supported %d: R0 would open the half store", st.format, database.SupportedStorageFormat))
	}
	return out
}

// identityRows are the rows a boot over a half store must not touch: HEAD's
// identity families and core's auth and role records.
func identityRows(kv map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range kv {
		for _, p := range []string{"u:", "role:", "apikey:", "oauthid:", "sess:", "invite:", "inviteallow:", "idx:", coreAuthKeyspace, coreRBACKeyspace} {
			if strings.HasPrefix(k, p) {
				out[k] = v
				break
			}
		}
	}
	return out
}

// midM3Outcome is what one midM3Boot did.
type midM3Outcome struct {
	newErr    error // app.New's
	attachErr error // Attach's, when New succeeded
	// registered: books' Register ran (it built the store or the server).
	registered bool
	// listed and listErr are the store's ListUsers answer, read once Register
	// built the store and before anything is released: what every per-user
	// fan-out and delete gate would see.
	listed  int
	listErr error
}

// midM3Boot is app.New and Attach of the composition over the fixture with wrap
// applied, the way serve runs them, never Run. The app, when built, is closed
// and books released.
func midM3Boot(t *testing.T, fx *flipFixture, wrap ...func(module.Module) module.Module) midM3Outcome {
	t.Helper()
	mod, opts, _ := composedOptions(t, false, fx.stateDir, midM3Config(fx), nil)
	opts.StorageFS = storage.FS{}
	opts.StateDir = fx.stateDir
	opts.Env = withLoopbackListen(append([]string{storagePathEnv + "=" + fx.dbPath}, midM3Env()...))
	opts.ProcessDefaults = false
	require.NoError(t, os.WriteFile(database.EncryptionKeyPath(fx.stateDir), fx.key, 0o600))
	var m module.Module = mod
	for _, w := range wrap {
		m = w(m)
	}
	var out midM3Outcome
	a, err := app.New(t.Context(), opts, m)
	out.registered = mod.store.Load() != nil || mod.srv.Load() != nil
	if err != nil {
		Release(mod)
		out.newErr = err
		return out
	}
	out.attachErr = Attach(mod, a)
	if st := mod.store.Load(); st != nil {
		users, lerr := st.ListUsers()
		out.listed, out.listErr = len(users), lerr
	}
	Release(mod)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, a.Close(ctx))
	return out
}

// midM3Config is bootOverHEADStore's config edit.
func midM3Config(fx *flipFixture) func(*config.Config) {
	return func(c *config.Config) {
		c.DatabasePath = fx.dbPath
		c.RootDir = fx.dbDir
		c.EnableAuth = true
		c.ABSAPIEnabled = true
		c.ABSJWTSecret = "synthetic-composed-app-secret-0123456789abcdef"
		c.ABSAuthModes = "jwt"
	}
}

// midM3IdentityBootWrite reports a key core's identity boot adds on any boot
// (auth's boot writes and rbac's seed commit): the bootstrap token, core's
// system user and its name index, the rbac generation and the seed roles.
func midM3IdentityBootWrite(key string) bool {
	switch key {
	case coreUserKey(string(coreauth.SystemUserID)),
		coreAuthKeyspace + string(keys.Join(keys.Str("user"), keys.Str("name"), keys.Str(string(coreauth.SystemUserID)))),
		coreRBACKeyspace + string(keys.Join(keys.Str("generation"))):
		return true
	}
	if strings.HasPrefix(key, coreAuthKeyspace+string(keys.Prefix(keys.Str("onetime"), keys.Str("bootstrap")))) {
		return true
	}
	for _, r := range []string{"admin", "editor", "viewer", "fp-worker"} {
		if key == coreRoleKey(r) {
			return true
		}
	}
	return false
}

// m3Dropped is books with M3 gone from its cutover list and everything else as
// shipped: a manifest that lost M3, the boot identityStoreReady exists for.
// With whole, M8's After entry naming M3 goes too, so nothing in the manifest
// refers to M3 and the kernel's DAG check has nothing to refuse.
type m3Dropped struct {
	module.Module
	whole bool
}

func (d m3Dropped) Manifest() module.Manifest {
	m := d.Module.Manifest()
	inner := m.Cutover
	m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
		return slices.DeleteFunc(inner(imp), func(s datamigrate.DataMigration) bool { return s.ID == cutover.IdentityCutoverID })
	}
	if d.whole && m.Rewrite != nil {
		rw := *m.Rewrite
		rw.After = slices.DeleteFunc(slices.Clone(rw.After), func(r storage.StepRef) bool { return r.DataMigration == cutover.IdentityCutoverID })
		m.Rewrite = &rw
	}
	return m
}

// registerProbe runs check at books' Register, before books' own.
type registerProbe struct {
	module.Module
	check func(*module.Registrar) error
}

func (p registerProbe) Register(r *module.Registrar) error {
	if err := p.check(r); err != nil {
		return err
	}
	return p.Module.Register(r)
}

// TestMidM3_KillLeavesEachRecordInOnePlace (a): at each half state a kill
// between M3's chunks leaves (planned; every user native with keys still HEAD's;
// between drained families), every HEAD user, key and OAuth identity is in
// exactly one of books' row and core's record, and the store already carries
// books' identity level and the legacy projection R0 refuses.
func TestMidM3_KillLeavesEachRecordInOnePlace(t *testing.T) {
	if testing.Short() {
		t.Skip("the boot crash matrix: extraction-gates runs it")
	}
	set := midM3Sets(t)
	f, before := set.f, set.before
	for _, s := range set.states {
		t.Run(s.name, func(t *testing.T) {
			st, err := readAboFlipState(s.clone, f)
			require.NoError(t, err)
			require.Empty(t, midM3Violations(t, st, before), "the store a kill left in the %s state", s.name)
		})
	}
}

// TestMidM3_OlderBuildRefusesBeforeAnyWrite (c): the build before the flip (books'
// storage declaration with no cutover and no raised level, what the pre-freeze
// release ships) refuses each half store with *storage.LevelTooNewError and exit
// 78, and writes no row and no file; R0's own check reads a .storage-format above
// the format it supports (midM3Violations). This build then still finishes M3 on
// the same store, so the refusal left nothing behind that blocks the resume.
func TestMidM3_OlderBuildRefusesBeforeAnyWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("the boot crash matrix: extraction-gates runs it")
	}
	set := midM3Sets(t)
	fx, f := set.fx, set.f
	for _, s := range set.states {
		t.Run(s.name, func(t *testing.T) {
			restoreAboFlip(t, s.clone, f)
			// R0's own check: the storage-format sidecar it reads is above the
			// format it supports.
			sidecar, err := os.ReadFile(fx.dbPath + database.StorageFormatSidecarSuffix)
			require.NoError(t, err)
			require.Greater(t, atoi(t, strings.TrimSpace(string(sidecar))), database.SupportedStorageFormat,
				"the storage-format sidecar R0 and `make rollback` read is not above R0's supported format")
			rows, files := rawKVs(t, fx.dbPath), dirImage(t, fx.root)
			err = crashmatrix.Boot(t, aboStorageBuild(f), f)
			// No write first, and not fatal: a pre-flip build that boots the half
			// store must also show what it wrote, so this check is one a refusal
			// regression reaches rather than one hidden behind the error below.
			assert.Equal(t, rows, rawKVs(t, fx.dbPath), "the pre-flip build wrote rows over the %s store", s.name)
			assert.Empty(t, imageDiff(files, dirImage(t, fx.root)), "the pre-flip build wrote files over the %s store", s.name)
			require.Error(t, err, "the pre-flip build booted a half-migrated store")
			var tooNew *storage.LevelTooNewError
			require.True(t, errors.As(err, &tooNew), "the pre-flip build's refusal is not a level refusal: %v", err)
			require.Greater(t, tooNew.Store, tooNew.Supported, "%v", err)
			require.Equal(t, kernel.ExitConfig, kernel.ExitCode(err), "%v", err)
			require.False(t, t.Failed(), "the pre-flip build wrote over the %s store", s.name)

			// This build resumes over what the refusal left.
			require.NoError(t, crashmatrix.Boot(t, midM3Build(f, ""), f), "this build could not finish M3 after the older build's refusal")
			rec, ok := m3Record(t, rawKVs(t, fx.dbPath))
			require.True(t, ok)
			require.Equal(t, storage.DataMigrationDone, rec.State)
		})
	}
}

// TestMidM3_ThisBuildResumesEveryRecordOnce (b): the composition booted on each
// half store finishes M3 and serves: M3 is done, no HEAD identity row is left,
// core holds exactly HEAD's users, keys and identity (by ID, and one hash index
// per key, so nothing imported twice under another ID), every live key
// authenticates by the raw token HEAD minted, the admin signs in with HEAD's
// password, and a key HEAD never minted is refused.
func TestMidM3_ThisBuildResumesEveryRecordOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("the boot crash matrix: extraction-gates runs it")
	}
	set := midM3Sets(t)
	fx, f, h, before := set.fx, set.f, set.h, set.before
	for _, s := range set.states {
		t.Run(s.name, func(t *testing.T) {
			restoreAboFlip(t, s.clone, f)
			r := bootOverHEADStore(t, fx)
			loginCoreAdmin(t, r, wayne, h.password)
			for name, k := range map[string]headKey{"full": h.full, "limited": h.limited} {
				rec := withKey(r, http.MethodGet, "/api/core/v1/me", k.raw)
				require.Equalf(t, http.StatusOK, rec.Code, "%s: the key HEAD minted does not authenticate after the resume: %s", name, bodyOf(rec))
			}
			rec := withKey(r, http.MethodGet, "/api/core/v1/me", h.never)
			require.Equal(t, http.StatusUnauthorized, rec.Code, bodyOf(rec))
			require.NoError(t, r.stop())

			kv := rawKVs(t, fx.dbPath)
			m3, ok := m3Record(t, kv)
			require.True(t, ok)
			require.Equal(t, storage.DataMigrationDone, m3.State)
			left := midM3RecordsOf(t, kv)
			require.Empty(t, left.users, "u: rows left after the resume")
			require.Empty(t, left.keys, "apikey: rows left after the resume")
			require.Empty(t, left.idents, "oauthid: rows left after the resume")
			require.Zero(t, left.sessions, "sess: rows left after the resume")
			for _, id := range before.users {
				require.Containsf(t, kv, coreUserKey(before.coreID[id]), "user %s is not core's after the resume", id)
			}
			for _, id := range before.keys {
				require.Containsf(t, kv, coreAPIKeyKey(id), "key %s is not core's after the resume", id)
			}
			for _, pv := range before.idents {
				require.Containsf(t, kv, coreIdentityKey(pv[0], pv[1]), "the OAuth identity %s is not core's after the resume", pv[1])
			}
			users, apiKeys, hashes, idents := coreRecordsOf(kv)
			// HEAD's _system row is core's system user, so the counts are equal.
			require.Equal(t, len(before.users), users, "core's users")
			require.Equal(t, len(before.keys), apiKeys, "core's key records")
			require.Equal(t, len(before.keys), hashes, "core's key hash index")
			require.Equal(t, len(before.idents), idents, "core's OAuth identities")
		})
	}
}

// TestMidM3_HalfStoreIsNeverServed (d): no half state is ever served or read as
// "no users". (1) The boot of this build that resumes M3 reaches books' Register
// only once M3 is done: at Register no HEAD identity row is readable through
// books' legacy handle, so nothing built from Register on (the store, its user
// directory, the server, every gate) ever runs over a half store. (2) A build
// whose manifest lost M3 is refused before Run: by the kernel at P2 while M8's
// After still names M3 (Register never runs), and by Attach's
// identityStoreReady when the manifest lost every mention of it (Register ran
// over the half store, so Attach is the only thing between it and serve's Run).
// Neither boot changes a HEAD identity row. The second one does write core's
// own identity boot records first: see the FINDING on that cell.
func TestMidM3_HalfStoreIsNeverServed(t *testing.T) {
	if testing.Short() {
		t.Skip("the boot crash matrix: extraction-gates runs it")
	}
	set := midM3Sets(t)
	fx, f, before, states := set.fx, set.f, set.before, set.states
	for _, s := range states {
		// M3 gone from the list while M8 still names it: the kernel refuses the
		// DAG at P2, before Register.
		t.Run(s.name+"/lost-m3-dag-refused", func(t *testing.T) {
			restoreAboFlip(t, s.clone, f)
			ids := identityRows(rawKVs(t, fx.dbPath))
			out := midM3Boot(t, fx, func(m module.Module) module.Module { return m3Dropped{Module: m} })
			require.Error(t, out.newErr, "a boot whose cutover list lost M3 (M8 still after it) booted over the %s store", s.name)
			require.Contains(t, out.newErr.Error(), cutover.IdentityCutoverID)
			require.False(t, out.registered, "books' Register ran over the %s store", s.name)
			require.Empty(t, imageDiff(ids, identityRows(rawKVs(t, fx.dbPath))), "the refused boot changed identity rows")
		})
		// M3 gone from the manifest entirely: the kernel has nothing to refuse,
		// the boot reaches Register over the half store, and Attach
		// (identityStoreReady) is what keeps serve from Running it. HEAD's
		// identity rows are untouched.
		//
		// FINDING (F4-08 fix-forward, reported to the coordinator, NOT accepted):
		// the refusal comes after the kernel's identity boot writes (P4d/P5:
		// core's system user and its name index when core holds none, the seed
		// roles, the rbac generation, a fresh bootstrap token). On the
		// killed-planned store that seeds core's user/_system beside HEAD's
		// u: _system row, and the next boot of the correct build then stops in
		// M3 with one cutover blocker (HEAD's _system collides with the seeded
		// one) until an operator repairs it; between Register and Attach the
		// store's ListUsers answers core's lone system user while HEAD's
		// accounts are still u: rows. The shipped manifest cannot get here (the
		// lost-m3-dag-refused cell and TestManifest_FlipDeclaresM2M7M8). The
		// additions below are what the boot writes today, pinned so a change
		// shows; the fix is the coordinator's call (refuse before P4d).
		t.Run(s.name+"/lost-m3-attach-refused", func(t *testing.T) {
			restoreAboFlip(t, s.clone, f)
			ids := identityRows(rawKVs(t, fx.dbPath))
			out := midM3Boot(t, fx, func(m module.Module) module.Module { return m3Dropped{Module: m, whole: true} })
			require.NoError(t, out.newErr, "the kernel refused the boot whose manifest lost M3 entirely: Attach is not what this cell reaches")
			require.ErrorIs(t, out.attachErr, errStoreNotAtIdentityLevel, "a boot whose manifest lost M3 passed Attach over the %s store: serve would Run it", s.name)
			after := identityRows(rawKVs(t, fx.dbPath))
			var unexpected []string
			for _, d := range imageDiff(ids, after) {
				key, added := strings.CutPrefix(d, "added ")
				if !added || !midM3IdentityBootWrite(key) {
					unexpected = append(unexpected, d)
				}
			}
			require.Empty(t, unexpected, "the lost-M3 boot changed identity rows beyond core's own boot writes")
		})
	}
	for _, s := range states {
		t.Run(s.name+"/register-after-m3", func(t *testing.T) {
			restoreAboFlip(t, s.clone, f)
			var checked int
			probe := func(m module.Module) module.Module {
				return registerProbe{Module: m, check: func(r *module.Registrar) error {
					checked++
					legacy := r.Deps().Storage.Legacy
					for _, id := range before.users {
						if _, err := legacy.Get(context.Background(), []byte("u:"+id)); !errors.Is(err, storage.ErrNotFound) {
							return fmt.Errorf("Register runs with the HEAD row u:%s readable (%v): M3 is not done", id, err)
						}
					}
					for _, id := range before.keys {
						if _, err := legacy.Get(context.Background(), []byte("apikey:"+id)); !errors.Is(err, storage.ErrNotFound) {
							return fmt.Errorf("Register runs with the HEAD row apikey:%s readable (%v): M3 is not done", id, err)
						}
					}
					return nil
				}}
			}
			out := midM3Boot(t, fx, probe)
			require.NoError(t, out.newErr)
			require.NoError(t, out.attachErr)
			require.Equal(t, 1, checked, "books' Register did not run once")
		})
	}
}
