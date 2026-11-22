// file: internal/books/cutover/registered_harness_test.go
// version: 0.1.0
// guid: 21892659-f06e-47a5-be7c-c4352e5a1d93
// last-edited: 2026-11-22

package cutover_test

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/gin-gonic/gin"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The registered-path harness (F4-08 B10d).
//
// It boots books' real Module through app.New, exactly as serve does (Module,
// Options, app.New, InstallProcessDefaults, Attach, Run), over a copy of the R2
// corpus in a temporary directory, with the identity selector on. The boot runs
// M2 (config), M3 (identity) and M8 (store rewrite) through the kernel's data
// migration runner, so every request is answered by what the flipped build would
// serve: core's stage 12 binds the principal, stage 13 decides, books' gin engine
// answers behind it. Nothing here evaluates a requirement itself: a verdict is the
// status the root handler wrote.
//
// It is an external package because books imports cutover. It reaches books and
// the kernel only through their exported API, and the Options edits it makes
// are the state directory, and the counting wrapper around ResolveAtom (the
// stage 13 witness, below).
//
// What B10e and B10f call:
//
//	h := bootRegistered(t, registeredConfig{})           // one boot per top-level test
//	p := h.Principal("session:admin")                    // any of the 36 names of verdicts.json but authoff
//	res, err := h.Do(p, "GET", "/api/v1/auth/me", nil)   // an exact path
//	res, err := h.DoRoute(p, ri, nil)                    // a manifest route, probed as the recorder did
//	h.Routes                                             // app.Routes(), the registered routes
//	h.MustDo(t, p, ...)                                  // Do that fails the test on an error
//
// registeredConfig{AuthOff: true} boots the auth-off principal ("authoff");
// registeredConfig{Owner: true} configures the owner and Access and adds the
// "owner" principal.

const (
	corpusRel = "internal/books/testdata/r2corpus/overlay"
	reachRel  = "internal/books/rbacreach"

	// seedBuild is the build ID of the short pre-boot open that adds the key rows.
	seedBuild = "batcomputer-1"
	// corpusBuilderDir is the scratch checkout the stored settings blob names.
	corpusBuilderDir = "/tmp/r2corpus/stage1"

	passwordGotham = "fixture-password-gotham-1940"
	passwordArkham = "fixture-password-arkham-asylum-1974"

	// The Access application of the owner boot. The team domain is on the reserved
	// example.test; its key set is served by a transport the harness installs.
	accessTeam  = "wayne-enterprises.example.test"
	accessAUD   = "r2reach-access-aud"
	accessKeyID = "r2reach-key-1"
	// ownerEmail is the owner_email the R0 owner-set recording used.
	ownerEmail = "bruce@example.invalid"
	ownerSub   = "r2reach-owner-sub"
)

// ---- the 37 principals ----

type reachPrincipalRow struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"` // session, key, anonymous, authoff
	User   string   `json:"user"`
	Role   string   `json:"role"`
	Scopes []string `json:"scopes"`
}

type reachPrincipalsFile struct {
	Principals []reachPrincipalRow `json:"principals"`
}

type reachVerdictsFile struct {
	Principals []string       `json:"principals"`
	Canary     map[string]int `json:"canary"`
}

// loadPrincipalTable reads the recorded principal table and R0's canary answers.
func loadPrincipalTable(t testing.TB) ([]reachPrincipalRow, map[string]int) {
	t.Helper()
	var pf reachPrincipalsFile
	var vf reachVerdictsFile
	for name, v := range map[string]any{"principals.json": &pf, "verdicts.json": &vf} {
		raw, err := os.ReadFile(filepath.Join(repoRoot(t), reachRel, name))
		if err != nil {
			t.Fatalf("the recorded R0 answers: %v", err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	names := make([]string, len(pf.Principals))
	for i, p := range pf.Principals {
		names[i] = p.Name
	}
	if !slices.Equal(names, vf.Principals) {
		t.Fatalf("principals.json and verdicts.json list different principals:\n%v\n%v", names, vf.Principals)
	}
	return pf.Principals, vf.Canary
}

// repoRoot is the module root, found from the test's working directory (a
// package directory under go test; -trimpath makes runtime.Caller useless).
func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, corpusRel)); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatalf("no %s above the working directory", corpusRel)
		}
		dir = next
	}
}

// Principal is one caller of the table and the credential the harness holds for
// it. The credentials are made at boot: a session by signing in through core's
// login route with the corpus user's fixture password, a key by a fresh mck_ value
// whose legacy row the pre-boot open wrote (M3 imports it like any HEAD key).
type Principal struct {
	Name, Kind, User, Role string
	Scopes                 []string

	cookie *http.Cookie
	bearer string
	peer   string
	access string // the Access assertion header of the owner
}

// Authoff is the principal of the auth-off boot, Anonymous the one that holds no
// credential, Owner the Access-verified owner of the owner boot.
const (
	Anonymous = "anonymous"
	Authoff   = "authoff"
	Owner     = "owner"
)

// registeredConfig selects the boot.
type registeredConfig struct {
	// AuthOff boots with core.auth.mode disabled (enable_auth false at R0).
	AuthOff bool
	// Owner configures the owner (modules.books.identity.owner_email) and
	// Cloudflare Access (team domain, AUD, the allow-list), and adds the Owner
	// principal. The verifier is the real one, over a key set the harness serves.
	Owner bool
	// ABSModes is the ABS auth modes ("jwt" when empty).
	ABSModes string
}

// Registered is one booted flipped build and the principals that call it.
type Registered struct {
	t testing.TB

	App     *app.App
	Handler http.Handler
	Routes  []httpapi.RouteInfo
	// Principals are in the order of verdicts.json; Owner, when configured, is last.
	Principals []*Principal
	// Wired is the resolver books.Options handed core, before the harness wrapped
	// it to count calls: what stage 13 resolves atoms with.
	Wired httpapi.AtomResolver

	byName   map[string]*Principal
	resolved atomic.Int64
	peers    atomic.Int64
	frontMu  sync.Mutex
	fronted  map[string]bool // route -> an anonymous request to it was refused by stage 13
	logs     *syncLog
	cfg      registeredConfig
	signer   jose.Signer
}

type syncLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// Principal returns the named principal, failing the test when the boot has none.
func (h *Registered) Principal(name string) *Principal {
	h.t.Helper()
	p, ok := h.byName[name]
	if !ok {
		h.t.Fatalf("this boot has no principal %q", name)
	}
	return p
}

// Logs is what the App's log pipeline printed.
func (h *Registered) Logs() string { return h.logs.String() }

// ---- the corpus on disk ----

// diskCorpus copies the R2 overlay store into dir/db and adds one HEAD key row per
// key principal, owned by the principal's user. It returns each key principal's
// raw credential.
//
// It is a real directory, not an in-memory filesystem: M8 swaps the rewritten
// store in through the kernel's restore-swap, which runs on the boot's own
// filesystem (bootseam.FS), and an unseamed boot (the only kind outside
// core/coretest, which an arch test enforces) is the real one. A store that lived
// in memory would boot, run M2 and M3, and fail M8 trying to create its journal on
// the host. The overlay is 1.3 MB, so the copy costs nothing a MemFS would save.
func diskCorpus(t testing.TB, rows []reachPrincipalRow, dir, stage string) map[string]string {
	t.Helper()
	src := filepath.Join(repoRoot(t), corpusRel, "db")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"audiobooks.pebble", "audiobooks.pebble.storage-format"} {
		if _, err := vfs.Clone(vfs.Default, vfs.Default, filepath.Join(src, n), filepath.Join(dir, n)); err != nil {
			t.Fatalf("copy the corpus store %s: %v", n, err)
		}
	}
	dbPath := filepath.Join(dir, "audiobooks.pebble")

	raws := map[string]string{}
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: dbPath, Build: seedBuild, FreeSpaceMargin: 1,
		Owners: []storage.OwnerSpec{{Owner: cutover.BooksOwner, Spec: store.BooksSpec(nil), Enabled: true}},
	})
	if err != nil {
		t.Fatalf("open the corpus copy to seed the keys: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("prepare the corpus copy: %v", errors.Join(err, o.Close()))
	}
	lr, ok := db.Legacy(cutover.BooksOwner)
	if !ok {
		t.Fatal(errors.Join(errors.New("the corpus store has no books legacy range"), db.Close()))
	}
	seed := func() error {
		// The stored settings blob names the builder's scratch checkout; the
		// flipped boot validates the paths it holds, so point them at the stage.
		blob, err := lr.Get(ctx, []byte("setting:config_blob"))
		if err != nil {
			return fmt.Errorf("read the settings blob: %w", err)
		}
		if bytes.Contains(blob, []byte(corpusBuilderDir)) {
			blob = bytes.ReplaceAll(blob, []byte(corpusBuilderDir), []byte(stage))
			if err := lr.Set(ctx, []byte("setting:config_blob"), blob); err != nil {
				return fmt.Errorf("point the settings blob at the stage: %w", err)
			}
		}
		owners := map[string]string{}
		expires := time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339)
		created := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
		for i, p := range rows {
			if p.Kind != "key" {
				continue
			}
			uid, ok := owners[p.User]
			if !ok {
				raw, err := lr.Get(ctx, []byte("idx:user:username:"+strings.ToLower(p.User)))
				if err != nil || len(raw) == 0 {
					return fmt.Errorf("the corpus has no user %q: %v", p.User, err)
				}
				uid = string(raw)
				owners[p.User] = uid
			}
			tok, err := auth.MintCredential(auth.KindKey)
			if err != nil {
				return err
			}
			raws[p.Name] = string(tok)
			sum := sha256.Sum256([]byte(tok))
			hash := hex.EncodeToString(sum[:])
			id := fmt.Sprintf("01M7R2%020d", i)
			row, err := json.Marshal(map[string]any{
				"id": id, "user_id": uid, "name": "key-" + id[len(id)-4:], "token_hash": hash, "scopes": p.Scopes,
				"status": "active", "created_at": created, "expires_at": expires, "use_count": 0,
			})
			if err != nil {
				return err
			}
			for k, v := range map[string][]byte{
				"apikey:" + id:                      row,
				"idx:apikey:hash:" + hash:           []byte(id),
				"idx:apikey:user:" + uid + ":" + id: []byte(id),
			} {
				if err := lr.Set(ctx, []byte(k), v); err != nil {
					return fmt.Errorf("seed %s: %w", k, err)
				}
			}
		}
		return nil
	}
	if err := seed(); err != nil {
		t.Fatal(errors.Join(err, db.Close()))
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the seeded corpus copy: %v", err)
	}
	return raws
}

// ---- the Access application of the owner boot ----

// accessApp is the Cloudflare Access application the owner boot trusts: an RSA key
// whose public half is served as the team's key set, and the minting of assertions
// it signs. The verifier under test is books' real one (cfaccess), unchanged; only
// the network it fetches keys from is replaced.
type accessApp struct {
	key *rsa.PrivateKey
}

func newAccessApp(t testing.TB) *accessApp {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &accessApp{key: k}
}

// RoundTrip serves the team's key set and refuses every other URL, so a boot that
// reached for the real network fails loudly instead of leaving the harness.
func (a *accessApp) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != accessTeam || r.URL.Path != "/cdn-cgi/access/certs" {
		return nil, fmt.Errorf("the harness serves no %s", r.URL)
	}
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &a.key.PublicKey, KeyID: accessKeyID, Algorithm: "RS256", Use: "sig"}}}
	body, err := json.Marshal(set)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)), Request: r,
	}, nil
}

// assertion is an Access JWT for email, issued a minute ago, valid for an hour.
func (a *accessApp) assertion(t testing.TB, sub, email string) string {
	t.Helper()
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: a.key, KeyID: accessKeyID}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw, err := jwt.Signed(sig).Claims(map[string]any{
		"iss": "https://" + accessTeam, "aud": accessAUD, "sub": sub, "email": email,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ---- the boot ----

// bootRegistered boots the flipped build over the corpus and signs the principals
// in. It fails the test unless M2, M3 and M8 ran at that boot and every
// principal's canary answers what R0 recorded for it. Process-wide state it sets
// (the selector, the environment, the HTTP transport when Owner is set, the
// process defaults) is restored by the test's cleanup, so a test boots one
// registered build at a time and none runs in parallel.
func bootRegistered(t *testing.T, cfg registeredConfig) *Registered {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rows, canary := loadPrincipalTable(t)

	// The one switch the flipped build has until B10g (an arch test forbids any
	// non-test caller). It stays on for the app's whole life: it is read at
	// Options, at Register and by the handlers.
	t.Cleanup(books.SetIdentityFlipForTest(true))

	work := t.TempDir()
	stage := filepath.Join(work, "stage")
	dbDir := filepath.Join(work, "db")
	raws := diskCorpus(t, rows, dbDir, stage)
	library := filepath.Join(stage, "library")
	for _, d := range []string{dbDir, library, filepath.Join(stage, "state")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	c := config.Config{
		DatabaseType:                    "pebble",
		DatabasePath:                    filepath.Join(dbDir, "audiobooks.pebble"),
		RootDir:                         library,
		PlaylistDir:                     filepath.Join(stage, "playlists"),
		OpenLibraryDumpDir:              filepath.Join(library, "openlibrary-dumps"),
		FingerprintRemoteWorkersEnabled: true,
		EnableAuth:                      !cfg.AuthOff,
		// The corpus was written by an R0 serving the ABS surface, its stored
		// settings say so, and a boot that names it differently is refused.
		ABSAPIEnabled: true,
		ABSAuthModes:  cmp.Or(cfg.ABSModes, "jwt"),
		ABSJWTSecret:  "synthetic-registered-harness-secret-0123456789abcdef",
	}
	origCfg := config.AppConfig
	config.AppConfig = c
	t.Cleanup(func() { config.AppConfig = origCfg })

	setenv := func(k, v string) { t.Setenv(k, v) }
	setenv("ABO_BOOKS_ABS_API_ENABLED", "true")
	setenv("ABO_BOOKS_ABS_AUTH_MODES", c.ABSAuthModes)
	setenv("ABO_BOOKS_ABS_JWT_SECRET", c.ABSJWTSecret)
	setenv("ABO_BOOKS_PLAYLIST_DIR", c.PlaylistDir)
	setenv("ABO_BOOKS_OPENLIBRARY_DUMP_DIR", c.OpenLibraryDumpDir)
	setenv("ABO_CORE_STORAGE_PATH", c.DatabasePath)
	setenv("ABO_CORE_HTTP_LISTEN", "127.0.0.1:0")
	setenv("ABO_CORE_HTTP_BASIC_GATE_ENABLED", "false")
	if cfg.AuthOff {
		setenv("ABO_CORE_AUTH_MODE", "disabled")
	}

	var access *accessApp
	if cfg.Owner {
		access = newAccessApp(t)
		setenv("ABO_BOOKS_IDENTITY_OWNER_EMAIL", ownerEmail)
		setenv("ABO_BOOKS_CFACCESS_ALLOWED_EMAILS", ownerEmail)
		setenv("ABO_BOOKS_IDENTITY_CF_ACCESS_TEAM_DOMAIN", accessTeam)
		setenv("ABO_BOOKS_IDENTITY_CF_ACCESS_AUD", accessAUD)
		orig := http.DefaultTransport
		http.DefaultTransport = access
		t.Cleanup(func() { http.DefaultTransport = orig })
	}

	// The secure state directory holds the corpus' own settings key: its secrets
	// are encrypted under it.
	stateDir := filepath.Join(stage, "state")
	key, err := os.ReadFile(filepath.Join(repoRoot(t), corpusRel, "state", ".encryption_key"))
	if err != nil {
		t.Fatalf("the corpus holds no settings key: %v", err)
	}
	config.ResetStateDirForTest()
	t.Cleanup(config.ResetStateDirForTest)
	setenv(config.SecureStateDirEnv, stateDir)
	if err := os.WriteFile(store.EncryptionKeyPath(stateDir), key, 0o600); err != nil {
		t.Fatal(err)
	}
	// The process-wide handles a books boot sets, put back as the composed
	// harness does after every boot.
	t.Cleanup(func() {
		store.SetGlobalStore(nil)
		scanner.SetStore(nil)
	})

	h := &Registered{t: t, cfg: cfg, logs: &syncLog{}, byName: map[string]*Principal{}}

	mod := books.Module()
	opts, err := books.Options(mod, books.ServeInputs{Server: server.GetDefaultServerConfig(), Telemetry: config.Snapshot(), LogOut: h.logs})
	if err != nil {
		t.Fatalf("books.Options: %v", err)
	}
	opts.StateDir = filepath.Join(stage, "state")
	h.Wired = opts.ResolveAtom
	if h.Wired == nil {
		t.Fatal("books.Options wires no ResolveAtom under the identity selector: stage 13 would refuse every legacy route")
	}
	inner := h.Wired
	opts.ResolveAtom = func(atom string, readSide bool) (rbac.Requirement, bool) {
		h.resolved.Add(1)
		return inner(atom, readSide)
	}
	addrs := make(chan net.Addr, 1)
	listen := opts.R1Listen
	opts.R1Listen = func(lo httpapi.ListenOptions, hd http.Handler, l *slog.Logger) (app.R1Server, error) {
		srv, err := listen(lo, hd, l)
		if err == nil {
			addrs <- srv.Addr()
		}
		return srv, err
	}

	a, err := app.New(t.Context(), opts, mod)
	if a != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = a.Close(ctx)
		})
	}
	t.Cleanup(func() { books.Release(mod) })
	if err != nil {
		t.Fatalf("app.New over the corpus: %v", err)
	}
	restore, err := app.InstallProcessDefaults(a)
	if err != nil {
		t.Fatalf("InstallProcessDefaults: %v", err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(restore) })
	if err := books.Attach(mod, a); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() { ran <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-ran:
			once.Do(restore)
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(60 * time.Second):
			t.Error("Run did not return after its context was cancelled")
		}
	})
	select {
	case <-addrs:
	case err := <-ran:
		t.Fatalf("Run returned before the listener opened: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("the App never opened its listener")
	}

	h.App, h.Handler = a, a.Handler()
	h.Routes = slices.Collect(a.Routes())
	h.requireCutoverRan()
	h.signIn(rows, raws, access, canary)
	return h
}

// requireCutoverRan fails unless M2, M3 and M8 committed at this boot: a boot that
// skipped them would answer from an unmigrated store and every verdict would be
// about something else.
func (h *Registered) requireCutoverRan() {
	h.t.Helper()
	adm := h.App.StorageAdmin()
	if adm == nil {
		h.t.Fatal("the App has no storage admin")
	}
	for _, id := range []string{cutover.ConfigCutoverID, cutover.IdentityCutoverID, cutover.RewriteID} {
		done, err := adm.DataMigrationDone(context.Background(), id)
		if err != nil {
			h.t.Fatalf("%s: %v", id, err)
		}
		if !done {
			h.t.Fatalf("%s did not run at this boot: the registered path is not over a migrated store", id)
		}
	}
}

// loginRoute is core's sign-in route: the books gin one reads a store M3 emptied.
const loginRoute = "/api/core/v1/auth/login"

func (h *Registered) signIn(rows []reachPrincipalRow, raws map[string]string, access *accessApp, canary map[string]int) {
	h.t.Helper()
	for i, r := range rows {
		if (r.Kind == "authoff") != h.cfg.AuthOff {
			continue
		}
		p := &Principal{Name: r.Name, Kind: r.Kind, User: r.User, Role: r.Role, Scopes: r.Scopes}
		// One peer address each: the sign-in route is rate limited per address.
		p.peer = fmt.Sprintf("192.0.2.%d:41000", 10+i)
		if r.Kind == "authoff" {
			p.peer = "127.0.0.1:41000"
		}
		switch r.Kind {
		case "session":
			p.cookie = h.login(p, passwordFor(r.User))
		case "key":
			p.bearer = raws[r.Name]
		}
		h.Principals = append(h.Principals, p)
		h.byName[p.Name] = p
	}
	if h.cfg.Owner {
		p := &Principal{Name: Owner, Kind: "owner", User: ownerEmail, peer: "192.0.2.200:41000"}
		p.access = access.assertion(h.t, ownerSub, ownerEmail)
		h.Principals = append(h.Principals, p)
		h.byName[p.Name] = p
	}
}

func passwordFor(user string) string {
	switch user {
	case "james.gordon", "harleen.quinzel", "lucius.fox.jr":
		return passwordArkham
	}
	return passwordGotham
}

func (h *Registered) login(p *Principal, password string) *http.Cookie {
	h.t.Helper()
	body, err := json.Marshal(map[string]string{"username": p.User, "password": password})
	if err != nil {
		h.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, loginRoute, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = p.peer
	rec := httptest.NewRecorder()
	h.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("sign %s in (%s) -> %d: %s", p.Name, p.User, rec.Code, rec.Body.String())
	}
	for _, ck := range rec.Result().Cookies() {
		if strings.HasSuffix(ck.Name, "session_id") {
			return &http.Cookie{Name: ck.Name, Value: ck.Value}
		}
	}
	h.t.Fatalf("sign %s in: no session cookie in %v", p.Name, rec.Result().Header["Set-Cookie"])
	return nil
}

// ---- requests ----

// Result is one answered request.
type Result struct {
	Status int
	Header http.Header
	Body   []byte
	// Route is the registered route the request matched; nil for a path the
	// harness could not match to one.
	Route *httpapi.RouteInfo
	// Stage13 is the evidence that stage 13 judged the request.
	Stage13 Stage13Evidence
	// Code is the problem code of a refusal stage 13 wrote ("" otherwise).
	Code string
}

// Stage13Evidence says how the harness knows stage 13 ran for a request.
type Stage13Evidence uint8

const (
	// Stage13Unobserved: the request was admitted by a route whose requirement is
	// not an atom, so stage 13 left no mark (no resolver call, no refusal). Only a
	// native or public route can answer this way.
	Stage13Unobserved Stage13Evidence = iota
	// Stage13Resolved: stage 13 asked the atom resolver for the route's requirement.
	Stage13Resolved
	// Stage13Refused: stage 13 wrote the refusal, a problem document with one of its
	// codes.
	Stage13Refused
)

func (e Stage13Evidence) String() string {
	return [...]string{"unobserved", "resolved", "refused"}[e]
}

var (
	// ErrNotJudged is the harness refusing its own answer: the request was to a
	// route stage 13 must judge, and nothing shows that it did. A verdict read
	// from such a response would be the handler's, not the authorization
	// decision's.
	ErrNotJudged = errors.New("registered harness: stage 13 left no evidence on a route it must judge")
	// ErrOwnAuth is a request to a route stages 12 and 13 skip by design (an
	// OwnAuth route authenticates itself): its answer is no stage 13 verdict.
	ErrOwnAuth = errors.New("registered harness: the route is OwnAuth, stage 13 does not judge it")
)

// stage13Codes are the problem codes only stage 13 writes (stage13.go's errors).
var stage13Codes = []string{
	"authentication_required", "interactive_required", "owner_required", "permission_denied",
	auth.CodeInvalidCredential, auth.CodeExpired, auth.CodeRevoked, auth.CodeInactiveUser, auth.CodeResetRequired,
}

// probePath is a manifest pattern with every parameter replaced by the dummy the
// recorder used (record_r0_reach.py probe_path).
func probePath(pattern string) string {
	segs := strings.Split(pattern, "/")
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, ":"), strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") && !strings.HasSuffix(s, "...}"):
			segs[i] = "r2reach-missing"
		case strings.HasPrefix(s, "*"), strings.HasSuffix(s, "...}"):
			segs[i] = "r2reach/missing"
		}
	}
	return strings.Join(segs, "/")
}

// DoRoute sends the request the recorder sent for a registered route.
func (h *Registered) DoRoute(p *Principal, ri httpapi.RouteInfo, body any) (Result, error) {
	h.t.Helper()
	return h.do(h.Handler, true, p, ri.Method, probePath(ri.Pattern), body, &ri)
}

// Do sends method path as p. The route is the registered one whose pattern the
// path matches.
func (h *Registered) Do(p *Principal, method, path string, body any) (Result, error) {
	h.t.Helper()
	return h.do(h.Handler, true, p, method, path, body, h.match(method, path))
}

// MustDo is Do that fails the test on an error.
func (h *Registered) MustDo(t testing.TB, p *Principal, method, path string, body any) Result {
	t.Helper()
	res, err := h.Do(p, method, path, body)
	if err != nil {
		t.Fatalf("%s %s as %s: %v", method, path, p.Name, err)
	}
	return res
}

// match finds the registered route a concrete path belongs to: the most specific
// pattern (the one with the most literal segments) of the method.
func (h *Registered) match(method, path string) *httpapi.RouteInfo {
	var best *httpapi.RouteInfo
	bestLit := -1
	for i := range h.Routes {
		ri := &h.Routes[i]
		if ri.Method != method {
			continue
		}
		if lit, ok := patternMatches(ri.Pattern, path); ok && lit > bestLit {
			best, bestLit = ri, lit
		}
	}
	return best
}

// patternMatches reports whether path fits pattern (":x" and "{x}" match one
// segment, "*x" and "{x...}" the rest) and how many segments matched literally.
func patternMatches(pattern, path string) (literal int, ok bool) {
	ps, ss := strings.Split(strings.Trim(pattern, "/"), "/"), strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range ps {
		switch {
		case strings.HasPrefix(p, "*"), strings.HasSuffix(p, "...}"):
			return literal, i <= len(ss)
		case i >= len(ss):
			return 0, false
		case strings.HasPrefix(p, ":"), strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}"):
		case p == ss[i]:
			literal++
		default:
			return 0, false
		}
	}
	return literal, len(ps) == len(ss)
}

// judged reports whether stage 13 must judge a request to ri, and whether it
// leaves a mark on an admit (an atom requirement is resolved).
func judged(ri *httpapi.RouteInfo) (must, resolvesAtom bool) {
	if ri == nil || ri.OwnAuth {
		return false, false
	}
	public := ri.Perm.Kind() == rbac.KindPublic && ri.Guards == (httpapi.Guards{})
	if public {
		return false, false
	}
	k := ri.Perm.Kind()
	return true, k == rbac.KindLegacyAtom || k == rbac.KindLegacyCheck
}

// excerpt is the start of the response body, for a failure message.
func (r Result) excerpt() string {
	b := r.Body
	if len(b) > 160 {
		b = b[:160]
	}
	return strings.TrimSpace(string(b))
}

// nextPeer is an address no principal has: core rate limits by source address, so
// a probe sweep that must not be throttled sends each request from its own (the
// 198.18.0.0/15 benchmarking range).
func (h *Registered) nextPeer() string {
	n := h.peers.Add(1)
	return fmt.Sprintf("198.18.%d.%d:41000", (n/250)%256, n%250+1)
}

// stage13Fronts reports whether stage 13 stands in front of ri on via: an
// anonymous request to it is refused with stage 13's own problem
// (authentication_required). Nothing else tells an admit of stage 13 from an
// answer of the handler behind it, which is why an admit is believed only for a
// route this holds for. The answer for the real handler is kept per route.
func (h *Registered) stage13Fronts(via http.Handler, real bool, ri *httpapi.RouteInfo) bool {
	key := ri.Method + " " + ri.Pattern
	if real {
		h.frontMu.Lock()
		ok, seen := h.fronted[key]
		h.frontMu.Unlock()
		if seen {
			return ok
		}
	}
	res, _ := h.send(via, &Principal{Name: Anonymous, peer: h.nextPeer()}, ri.Method, probePath(ri.Pattern), nil, ri)
	ok := res.Stage13 == Stage13Refused && res.Code == "authentication_required"
	if real {
		h.frontMu.Lock()
		if h.fronted == nil {
			h.fronted = map[string]bool{}
		}
		h.fronted[key] = ok
		h.frontMu.Unlock()
	}
	return ok
}

// do sends the request through via (real: via is the registered handler, so what
// it learns about a route can be kept) and judges the answer.
func (h *Registered) do(via http.Handler, real bool, p *Principal, method, path string, body any, ri *httpapi.RouteInfo) (Result, error) {
	h.t.Helper()
	res, err := h.send(via, p, method, path, body, ri)
	if err != nil {
		return res, err
	}
	if ri != nil && ri.OwnAuth {
		return res, fmt.Errorf("%w: %s %s", ErrOwnAuth, method, path)
	}
	// An admit (or any answer that is not stage 13's refusal) is a verdict only
	// for a route stage 13 is in front of. An auth-off boot has no anonymous
	// refusal to find: stage 13 admits the auth-off principal on every route.
	if must, _ := judged(ri); must && res.Stage13 == Stage13Unobserved && !h.cfg.AuthOff && !h.stage13Fronts(via, real, ri) {
		return res, fmt.Errorf("%w: %s %s as %s answered %d", ErrNotJudged, method, ri.Pattern, p.Name, res.Status)
	}
	return res, nil
}

// send is one request as p, with what the answer shows of stage 13.
func (h *Registered) send(via http.Handler, p *Principal, method, path string, body any, ri *httpapi.RouteInfo) (Result, error) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return Result{}, err
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = p.peer
	switch {
	case p.cookie != nil:
		req.AddCookie(p.cookie)
	case p.bearer != "":
		req.Header.Set("Authorization", "Bearer "+p.bearer)
	case p.access != "":
		req.Header.Set("Cf-Access-Jwt-Assertion", p.access)
	}
	before := h.resolved.Load()
	rec := httptest.NewRecorder()
	via.ServeHTTP(rec, req)
	res := Result{Status: rec.Code, Header: rec.Header().Clone(), Body: rec.Body.Bytes(), Route: ri}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/problem+json") {
		var pr struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(res.Body, &pr) == nil && slices.Contains(stage13Codes, pr.Code) &&
			(res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden) {
			res.Code, res.Stage13 = pr.Code, Stage13Refused
		}
	}
	if res.Stage13 == Stage13Unobserved && h.resolved.Load() > before {
		res.Stage13 = Stage13Resolved
	}
	return res, nil
}
