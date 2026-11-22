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
	"crypto/tls"
	"crypto/x509"
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
	"strconv"
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
// the kernel only through their exported API, and the one Options edit it makes
// is the state directory.
//
// How it knows stage 13 judged a request. Under the flip books' mount resolves its
// atoms to concrete requirements when it registers (AllOf for most routes, Delegated
// for OwnAuth ones, Public and Authenticated for the rest), so stage 13 never asks
// the atom resolver at request time and a resolver call is no evidence of anything.
// What stage 13 does leave is its own problem document: a 401 or 403 carrying one of
// its codes, which no gin handler behind it writes. Router.authWrap fixes a route's
// whole chain at registration (stage 12, stage 13, the handler), so every principal
// meets the same chain, and an anonymous request to a route that stage 13 judges is
// refused by stage 13 itself. The harness believes an admit, or any answer that is
// not stage 13's refusal, only for a route where that anonymous refusal was seen
// (stage13Fronts); a route that answers the anonymous probe any other way is one
// stage 13 does not stand in front of, and a request to it is ErrNotJudged.
//
// What B10e and B10f call (the registered-path API; frozen by B10d):
//
//	h := bootRegistered(t, registeredConfig{})   // one boot per top-level test (about 6s)
//	p := h.Principal("session:admin")            // a name of verdicts.json (36 names; "authoff" needs AuthOff, Owner needs Owner)
//	h.Principals                                 // every principal of this boot: the 36 of verdicts.json in its order, then the core keys (below), Owner last
//	p.Recorded()                                 // the verdicts.json name whose R0 answers p is held to: p's own, or its imported twin's for a core key
//	h.Routes                                     // app.Routes(): every registered route (httpapi.RouteInfo)
//	h.Wired                                      // the httpapi.AtomResolver books.Options handed core
//	h.Logs()                                     // what the App's log pipeline printed
//
//	res, err := h.Do(p, "GET", "/api/v1/auth/me", nil)   // an exact path; body is JSON-encoded when not nil
//	res, err := h.DoRoute(p, ri, nil)                    // a manifest route, probed as the recorder did (probePath)
//	res := h.MustDo(t, p, "GET", "/api/v1/auth/me", nil) // Do that fails the test on an error
//	res := h.Raw(req)                                    // any request through the root handler, as no principal: the caller sets its own credential headers (ABS bearer, Access assertion)
//
//	res.Status, res.Header, res.Body                     // the answer
//	res.Route                                            // the registered route Do matched (nil for Raw or no match)
//	res.Stage13, res.Code                                // Stage13Refused and stage 13's problem code, or Stage13Unobserved
//	res.Verdict()                                        // 'D' for 401/403, else 'A': record_r0_reach.py's letters, for comparing with R0 only
//	h.Refused(res, status, code)                         // the refusal assertion for a hand-written cell: exact status AND exact code
//	res.Admitted()                                       // !(stage 13 refused)
//
// Every principal has an address of its own, stable for the whole run (a distinct one
// of 192.0.2.0/24; the auth-off boot's is loopback, the only address it serves), and
// every request of the principal comes from it. Core's limiter is on and keys on that
// address and the route's class, so two principals never share a bucket; the budget
// (registeredConfig.RateLimitPerMinute) is large enough for a table sweep of one
// principal. A 429 anywhere (Do, DoRoute, MustDo, Raw) fails the test at once with
// "rate-limited, cell proves nothing": a refusal cell that took any non-2xx, or a
// verdict letter, would otherwise pass on a throttled request and prove nothing. A test
// that is about the limiter says so with ExpectRateLimit. They return an error, with
// the Result filled, in two cases only:
//
//	ErrOwnAuth    the route is OwnAuth (the ABS surface): stages 12 and 13 skip it, so
//	              its answer is the handler's own. Take the Result and judge it as an ABS
//	              answer, or build the request yourself with Raw.
//	ErrNotJudged  the route is one stage 13 must judge and the harness has not seen stage
//	              13 refuse an anonymous caller on it, so an admit (or any non-stage-13
//	              answer) would be the handler's, not a verdict. A route stage 13 refuses
//	              the anonymous caller on is believed for every principal (see above).
//
// Two key columns. The 28 key principals of verdicts.json ("key:...") hold prod's
// shape: an abk_ value from GenerateAPIKeyToken, whose sha256 is the token_hash and
// idx:apikey:hash: row written into the HEAD corpus before the boot, so M3 imports
// them as it imports prod's keys (ABS accepts only this shape). Beside them, on an
// auth-on boot, sit 18 core-native keys ("corekey:<same tail>", Core true, Twin the
// imported name): mck_ values minted through core's own routes after the boot, with
// the owner and the scopes M3 gave the imported twin, less the ambient scopes (core:self,
// core:metrics.read, books:reading), which no creator holds to give. A core key has no
// HEAD recording, so a cell holds it to its twin's verdict (p.Recorded()). The other
// 10 keys (h.NoCoreTwin) have no twin: M3 gave each the ambient scopes only, and core
// refuses a key without a role scope. Auth-off boots have neither column.
//
// registeredConfig{Seed: func(ctx, lr storage.LegacyRange) error} writes HEAD rows
// (raw u:<id>, idx:user:..., oauth identities, keys through SeedAPIKey: a revoked
// one with Status "revoked", a Confined one for the fp-worker user) into the corpus
// before the boot, after the key rows, so M2 and M3 import them. registeredConfig{AccessAllowed: []string}
// adds addresses to the owner boot's Access allow-list. Do accepts a path with a
// query string.
//
// registeredConfig{AuthOff: true} boots with core.auth.mode disabled and the
// "authoff" principal; ABSModes ("jwt" by default) names the ABS auth modes;
// registeredConfig{Owner: true} configures the owner e-mail and Cloudflare Access
// (the real verifier over a key set the harness serves) and adds the "owner"
// principal, whose credential is a signed Access assertion.

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
	// Core marks a core-native key (an mck_ value minted through core's own route
	// after the boot, named "corekey:..."); the abk_ keys of verdicts.json are the
	// imported ones. Twin is the verdicts.json principal whose HEAD verdict a core
	// key is compared against: a core key has no HEAD recording of its own, and its
	// owner and scopes are its twin's.
	Core bool
	Twin string

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

// Recorded is the verdicts.json principal whose R0 answers this one is held to: its
// own name, or its twin's for a core key.
func (p *Principal) Recorded() string {
	if p.Twin != "" {
		return p.Twin
	}
	return p.Name
}

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
	// Seed writes HEAD rows into the corpus before the boot, through the books
	// legacy range, so M2 and M3 import them like any row a HEAD build wrote. It
	// runs after the key rows are seeded; nil does nothing.
	Seed func(ctx context.Context, lr storage.LegacyRange) error
	// AccessAllowed is appended to the owner boot's Access allow-list
	// (ABO_BOOKS_CFACCESS_ALLOWED_EMAILS); the owner's own address is always on it.
	AccessAllowed []string
	// RateLimitPerMinute is core.http.rate_limit.per_minute (the burst is a fifth of
	// it, at least 10). The limiter is always on, keyed by the caller's address, and
	// each principal has its own address; zero is harnessRatePerMinute, a budget a
	// table sweep of one principal does not exhaust. A test about the limiter itself
	// sets a small number.
	RateLimitPerMinute int
	// AuthMode is the operator value of core.auth.mode (ABO_CORE_AUTH_MODE): "required"
	// is how an operator turns auth on. Empty sets none, except that AuthOff sets
	// "disabled" (unless StoredAuthOff).
	AuthMode string
	// StoredAuthOff makes an AuthOff boot an install whose operator never set a value:
	// core.auth.mode comes from the stored enable_auth false alone (M2 stores it as
	// disabled), no ABO_CORE_AUTH_MODE is set.
	StoredAuthOff bool
	// Over boots over the store an earlier boot left: that boot must have been
	// stopped (Stop), the migrations are already done, and the principals sign in with
	// the credentials the earlier boot's corpus gave them. The corpus is not copied
	// again and Seed does not run.
	Over *Registered
}

// harnessRatePerMinute is the limiter's default budget in a harness boot.
const harnessRatePerMinute = 60000

// Registered is one booted flipped build and the principals that call it.
type Registered struct {
	t testing.TB

	App     *app.App
	Handler http.Handler
	Routes  []httpapi.RouteInfo
	// Principals are in the order of verdicts.json; Owner, when configured, is last.
	Principals []*Principal
	// Wired is the resolver books.Options handed core: what stage 13 resolves a
	// legacy atom requirement with.
	Wired httpapi.AtomResolver

	// NoCoreTwin names the key principals that have no core-native twin: their
	// imported key holds the ambient scopes only (every atom of theirs lies outside
	// the owner's roles), and core's mint refuses a key without a role scope.
	NoCoreTwin []string
	// KnownDifferences names the principals whose canary differed from R0 as
	// knownDifferences says it may.
	KnownDifferences []string

	byName map[string]*Principal
	// work is the boot's directory (the store is work/db), raws the key principals'
	// credentials the corpus was given; a boot Over this one reuses both.
	work     string
	raws     map[string]string
	teardown []func()
	stopOnce sync.Once
	stopped  atomic.Bool
	peers    atomic.Int64
	// rateExpected counts the ExpectRateLimit calls in progress; a 429 fails the test
	// while it is zero.
	rateExpected atomic.Int32
	frontMu      sync.Mutex
	fronted      map[string]bool // route -> an anonymous request to it was refused by stage 13
	logs         *syncLog
	cfg          registeredConfig
	access       *accessApp
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

// AccessRefused lists the hosts and paths the owner boot's network (the Access key
// set server) was asked for and does not serve; empty when the boot stayed on the
// team's key set. nil outside an Owner boot.
func (h *Registered) AccessRefused() []string {
	if h.access == nil {
		return nil
	}
	h.access.mu.Lock()
	defer h.access.mu.Unlock()
	return slices.Clone(h.access.refused)
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
func diskCorpus(t testing.TB, rows []reachPrincipalRow, dir, stage string, cfg registeredConfig) map[string]string {
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
		if cfg.AuthOff {
			// The stored blob says enable_auth true and M2 carries it to
			// core.auth.mode, a layer above the environment and EnableAuth, so an
			// auth-off boot edits the blob as record_r0_reach.py's edit_config did.
			// The row is a database.Setting whose value is the blob's JSON.
			var row map[string]json.RawMessage
			if err := json.Unmarshal(blob, &row); err != nil {
				return fmt.Errorf("decode the settings row: %w", err)
			}
			var val string
			if err := json.Unmarshal(row["value"], &val); err != nil {
				return fmt.Errorf("decode the settings blob's value: %w", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(val), &fields); err != nil {
				return fmt.Errorf("decode the settings blob: %w", err)
			}
			if got := string(fields["enable_auth"]); got != "true" {
				return fmt.Errorf("the corpus stores enable_auth %q, want true", got)
			}
			fields["enable_auth"] = json.RawMessage("false")
			nv, err := json.Marshal(fields)
			if err != nil {
				return err
			}
			if row["value"], err = json.Marshal(string(nv)); err != nil {
				return err
			}
			if blob, err = json.Marshal(row); err != nil {
				return err
			}
			if err := lr.Set(ctx, []byte("setting:config_blob"), blob); err != nil {
				return fmt.Errorf("turn auth off in the settings blob: %w", err)
			}
		}
		for i, p := range rows {
			if p.Kind != "key" {
				continue
			}
			// Prod's shape: see SeedAPIKey.
			tok, err := SeedAPIKey(ctx, lr, KeySeed{ID: fmt.Sprintf("01M7R2%020d", i), User: p.User, Scopes: p.Scopes})
			if err != nil {
				return err
			}
			raws[p.Name] = tok
		}
		if cfg.Seed != nil {
			if err := cfg.Seed(ctx, lr); err != nil {
				return fmt.Errorf("the config's Seed: %w", err)
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
// the network it fetches keys from is replaced: a local TLS server, reached by a
// clone of the default transport that dials it for every host. Core's own clients
// clone http.DefaultTransport and need it to stay an *http.Transport, so the
// replacement is one.
type accessApp struct {
	key *rsa.PrivateKey
	srv *httptest.Server
	// refused lists the hosts a request reached the server for that it does not
	// serve: a boot that reached for the real network fails loudly.
	mu      sync.Mutex
	refused []string
}

func newAccessApp(t testing.TB) *accessApp {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	a := &accessApp{key: k}
	a.srv = httptest.NewTLSServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *accessApp) serve(w http.ResponseWriter, r *http.Request) {
	if r.Host != accessTeam || r.URL.Path != "/cdn-cgi/access/certs" {
		a.mu.Lock()
		a.refused = append(a.refused, r.Host+r.URL.Path)
		a.mu.Unlock()
		http.Error(w, "the harness serves no such host", http.StatusNotFound)
		return
	}
	set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &a.key.PublicKey, KeyID: accessKeyID, Algorithm: "RS256", Use: "sig"}}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(set)
}

// transport is the default transport's clone that sends every request to the
// harness's server and trusts only its certificate.
func (a *accessApp) transport() *http.Transport {
	pool := x509.NewCertPool()
	pool.AddCert(a.srv.Certificate())
	tr := http.DefaultTransport.(*http.Transport).Clone()
	addr := a.srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	// The test server's certificate names example.com, not the team's host.
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, ServerName: "example.com", MinVersion: tls.VersionTLS12}
	tr.ForceAttemptHTTP2 = false
	tr.Proxy = nil
	return tr
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
// principal signed in as the user R0 recorded: each principal's canary answers what
// R0 recorded for it (requireCanary), except where knownDifferences names the cell,
// and an auth-off boot reports auth_disabled (requireAuthMode). Process-wide state it sets
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

	var work string
	var raws map[string]string
	if cfg.Over != nil {
		if !cfg.Over.stopped.Load() {
			t.Fatal("bootRegistered over a boot that is still running: Stop it first, the store is open")
		}
		work, raws = cfg.Over.work, cfg.Over.raws
	}
	if work == "" {
		work = t.TempDir()
	}
	stage := filepath.Join(work, "stage")
	dbDir := filepath.Join(work, "db")
	if cfg.Over == nil {
		raws = diskCorpus(t, rows, dbDir, stage, cfg)
	}
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
	setenv("ABO_CORE_HTTP_RATE_LIMIT_PER_MINUTE", strconv.Itoa(cmp.Or(cfg.RateLimitPerMinute, harnessRatePerMinute)))
	switch {
	case cfg.AuthMode != "":
		setenv("ABO_CORE_AUTH_MODE", cfg.AuthMode)
	case cfg.AuthOff && !cfg.StoredAuthOff:
		setenv("ABO_CORE_AUTH_MODE", "disabled")
	}

	var access *accessApp
	if cfg.Owner {
		access = newAccessApp(t)
		setenv("ABO_BOOKS_IDENTITY_OWNER_EMAIL", ownerEmail)
		setenv("ABO_BOOKS_CFACCESS_ALLOWED_EMAILS", strings.Join(append([]string{ownerEmail}, cfg.AccessAllowed...), ","))
		setenv("ABO_BOOKS_IDENTITY_CF_ACCESS_TEAM_DOMAIN", accessTeam)
		setenv("ABO_BOOKS_IDENTITY_CF_ACCESS_AUD", accessAUD)
		orig := http.DefaultTransport
		http.DefaultTransport = access.transport()
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
	h := &Registered{t: t, cfg: cfg, logs: &syncLog{}, byName: map[string]*Principal{}, access: access, work: work, raws: raws}
	// What a boot sets and a stop (or the test's end) undoes, last first. The
	// process-wide handles a books boot sets are put back as the composed harness
	// does after every boot.
	t.Cleanup(h.Stop)
	h.onStop(func() {
		store.SetGlobalStore(nil)
		scanner.SetStore(nil)
	})

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
		h.onStop(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = a.Close(ctx)
		})
	}
	h.onStop(func() { books.Release(mod) })
	if err != nil {
		t.Fatalf("app.New over the corpus: %v", err)
	}
	restore, err := app.InstallProcessDefaults(a)
	if err != nil {
		t.Fatalf("InstallProcessDefaults: %v", err)
	}
	var once sync.Once
	h.onStop(func() { once.Do(restore) })
	if err := books.Attach(mod, a); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() { ran <- a.Run(ctx) }()
	h.onStop(func() {
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
	h.signIn(rows, raws, access)
	h.requireAuthMode()
	h.requireCanary(canary)
	return h
}

// onStop adds a step to what Stop undoes.
func (h *Registered) onStop(fn func()) { h.teardown = append(h.teardown, fn) }

// Stop shuts the boot down: the run ends, the process defaults and handles it set
// are put back and the store is closed, the steps in the reverse of the boot's. It is
// what the test's cleanup does, and may be called earlier to boot again over the
// same store (registeredConfig.Over); a second call does nothing.
func (h *Registered) Stop() {
	h.stopOnce.Do(func() {
		for i := len(h.teardown) - 1; i >= 0; i-- {
			h.teardown[i]()
		}
		h.stopped.Store(true)
	})
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

func (h *Registered) signIn(rows []reachPrincipalRow, raws map[string]string, access *accessApp) {
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
	if !h.cfg.AuthOff {
		h.mintCoreKeys(rows)
	}
	if h.cfg.Owner {
		p := &Principal{Name: Owner, Kind: "owner", User: ownerEmail, peer: "192.0.2.200:41000"}
		p.access = access.assertion(h.t, ownerSub, ownerEmail)
		h.Principals = append(h.Principals, p)
		h.byName[p.Name] = p
	}
}

// ambientScopes are the scopes ABO's cutover hands every key that is not worker
// confined (rbac.WithAmbient): core:self, core:metrics.read and books:reading.
var ambientScopes = []string{"core:self", "core:metrics.read", "books:reading"}

// coreKeyName is the name of the core key that mirrors a verdicts.json key.
func coreKeyName(key string) string { return "corekey:" + strings.TrimPrefix(key, "key:") }

// mintCoreKeys adds the second key column: for every key principal of the table a
// core-native mck_ key with the same owner and the same scopes, minted through core's
// own routes after the boot (as the admin session; its own route when the owner is
// that admin). The scopes are the ones M3 gave the imported abk_ key, read back from
// core, so the twin differs from the imported key in its credential kind and nothing
// else. The keys come after the verdicts.json principals, before Owner.
func (h *Registered) mintCoreKeys(rows []reachPrincipalRow) {
	h.t.Helper()
	admin := h.Principal("session:admin")
	var page struct {
		Items []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"items"`
	}
	h.coreJSON(admin, http.MethodGet, "/api/core/v1/users?limit=100", nil, &page)
	uids := map[string]string{}
	for _, u := range page.Items {
		uids[strings.ToLower(u.Username)] = u.ID
	}
	adminID := uids[strings.ToLower(admin.User)]
	for i, r := range rows {
		if r.Kind != "key" {
			continue
		}
		uid := uids[strings.ToLower(r.User)]
		if uid == "" {
			h.t.Fatalf("core lists no user %q (owner of %s)", r.User, r.Name)
		}
		var keys struct {
			Items []struct {
				Name     string   `json:"name"`
				Scopes   []string `json:"scopes"`
				Imported bool     `json:"imported"`
			} `json:"items"`
		}
		h.coreJSON(admin, http.MethodGet, "/api/core/v1/users/"+uid+"/api-keys?limit=100", nil, &keys)
		imported := fmt.Sprintf("key-%04d", i)
		var scopes []string
		for _, k := range keys.Items {
			if k.Name == imported {
				scopes = k.Scopes
				break
			}
		}
		if len(scopes) == 0 {
			h.t.Fatalf("core lists no imported key %q for %s: %+v", imported, r.Name, keys.Items)
		}
		// The ambient scopes M3 added to the imported key are not role scopes: no
		// creator holds them to give, and core's own mint leaves them off.
		scopes = slices.DeleteFunc(scopes, func(s string) bool { return slices.Contains(ambientScopes, s) })
		if len(scopes) == 0 {
			// The imported key's atoms all lie outside its owner's roles, so M3 gave
			// it the ambient scopes only. Core refuses a key with no role scope and
			// no creator holds an ambient one to give: there is no core twin.
			h.NoCoreTwin = append(h.NoCoreTwin, r.Name)
			continue
		}
		route := "/api/core/v1/users/" + uid + "/api-keys"
		if uid == adminID {
			route = "/api/core/v1/me/api-keys"
		}
		var made struct {
			Key string `json:"key"`
		}
		h.coreJSON(admin, http.MethodPost, route, map[string]any{"name": "corekey-" + strconv.Itoa(i), "scopes": scopes, "expires_in": "720h"}, &made)
		if !strings.HasPrefix(made.Key, "mck_") {
			h.t.Fatalf("core minted %q for %s, want an mck_ key", made.Key[:min(4, len(made.Key))], r.Name)
		}
		p := &Principal{Name: coreKeyName(r.Name), Kind: "key", User: r.User, Role: r.Role, Scopes: r.Scopes, Core: true, Twin: r.Name, bearer: made.Key}
		p.peer = fmt.Sprintf("192.0.2.%d:41000", 100+i)
		h.Principals = append(h.Principals, p)
		h.byName[p.Name] = p
	}
}

// coreJSON is one request to core as p that must answer 2xx, its body decoded into out.
func (h *Registered) coreJSON(p *Principal, method, path string, body, out any) {
	h.t.Helper()
	res, err := h.Do(p, method, path, body)
	if err != nil {
		h.t.Fatalf("%s %s as %s: %v", method, path, p.Name, err)
	}
	if res.Status/100 != 2 {
		h.t.Fatalf("%s %s as %s -> %d %s", method, path, p.Name, res.Status, res.excerpt())
	}
	if err := json.Unmarshal(res.Body, out); err != nil {
		h.t.Fatalf("%s %s: %v: %s", method, path, err, res.excerpt())
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
	// Stage13 is what the answer itself shows of stage 13.
	Stage13 Stage13Evidence
	// Code is the problem code of a refusal stage 13 wrote ("" otherwise).
	Code string
}

// Stage13Evidence says what an answer shows of stage 13.
type Stage13Evidence uint8

const (
	// Stage13Unobserved: the answer is not stage 13's refusal. It is the handler's
	// (stage 13 admitted) or a refusal of another layer; it counts as a verdict only
	// on a route the harness has seen stage 13 refuse the anonymous caller on.
	Stage13Unobserved Stage13Evidence = iota
	// Stage13Refused: stage 13 wrote the refusal, a problem document with one of its
	// codes.
	Stage13Refused
)

func (e Stage13Evidence) String() string {
	return [...]string{"unobserved", "refused"}[e]
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
// path matches (a query string is sent and not matched on).
func (h *Registered) Do(p *Principal, method, path string, body any) (Result, error) {
	h.t.Helper()
	route, _, _ := strings.Cut(path, "?")
	return h.do(h.Handler, true, p, method, path, body, h.match(method, route))
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

// judged reports whether stage 13 must judge a request to ri: every route except an
// OwnAuth one (stage 13 never runs on it) and a Public one without guards (stage 13
// admits it whatever the caller is).
func judged(ri *httpapi.RouteInfo) bool {
	if ri == nil || ri.OwnAuth {
		return false
	}
	return !(ri.Perm.Kind() == rbac.KindPublic && ri.Guards == (httpapi.Guards{}))
}

// Verdict is record_r0_reach.py's letter for the answer: 'D' for a 401 or 403,
// 'A' for anything else (the request got past the authorization decision).
//
// Use it to compare with R0's recorded letters, never as the whole refusal check of
// a hand-written cell: 'A' is every answer that is not a 401 or 403, an error among
// them, and 'D' does not say which refusal. A cell that expects a refusal asserts
// Refused (status and code, exactly).
func (r Result) Verdict() byte {
	if r.Status == http.StatusUnauthorized || r.Status == http.StatusForbidden {
		return 'D'
	}
	return 'A'
}

// problemCode is the code the answer names: the code of a problem document, else the
// last segment of its type, else the message of a plain {"error": ...} answer (the ABS
// surface's and the legacy handlers'), else "".
func problemCode(res Result) string {
	var b struct {
		Code  string `json:"code"`
		Type  string `json:"type"`
		Error string `json:"error"`
	}
	if json.Unmarshal(res.Body, &b) != nil {
		return ""
	}
	switch {
	case b.Code != "":
		return b.Code
	case b.Type != "":
		return b.Type[strings.LastIndex(b.Type, "/")+1:]
	}
	return b.Error
}

// Refused asserts res is a refusal with exactly this status and this code (see
// problemCode) and reports whether it is. It is the refusal check of a hand-written
// cell: matching the status alone, a band of statuses or Verdict() would let another
// refusal, or a throttled request, stand in for the one the cell is about.
func (h *Registered) Refused(res Result, status int, code string) bool {
	h.t.Helper()
	if got := problemCode(res); res.Status != status || got != code {
		h.t.Errorf("want the refusal %d %q, got %d %q: %s", status, code, res.Status, got, res.excerpt())
		return false
	}
	return true
}

// ExpectRateLimit runs fn with 429 answers allowed: the opt-in of a test that is
// about the limiter. Anywhere else a 429 fails the test.
func (h *Registered) ExpectRateLimit(fn func()) {
	h.rateExpected.Add(1)
	defer h.rateExpected.Add(-1)
	fn()
}

// notRateLimited fails the test on a throttled answer: the cell it belongs to
// measured the limiter, not what it asked.
func (h *Registered) notRateLimited(res Result, method, path, who string) {
	h.t.Helper()
	if res.Status == http.StatusTooManyRequests && h.rateExpected.Load() == 0 {
		h.t.Fatalf("rate-limited, cell proves nothing: %s %s as %s", method, path, who)
	}
}

// Admitted reports that stage 13 did not refuse the request.
func (r Result) Admitted() bool { return r.Stage13 != Stage13Refused }

// Raw serves req through the root handler as no principal: no credential is added
// and a request with no RemoteAddr gets a fresh one. It judges nothing, so a caller
// that reads an authorization verdict from it must check Stage13 itself; it exists
// for the routes Do refuses (ErrOwnAuth) and for credentials the table does not
// hold.
func (h *Registered) Raw(req *http.Request) Result {
	h.t.Helper()
	if req.RemoteAddr == "" || req.RemoteAddr == "192.0.2.1:1234" {
		req.RemoteAddr = h.nextPeer()
	}
	res := resultOf(h.serve(h.Handler, req), nil)
	h.notRateLimited(res, req.Method, req.URL.RequestURI(), "no principal (Raw)")
	return res
}

// excerpt is the start of the response body, for a failure message.
func (r Result) excerpt() string {
	b := r.Body
	if len(b) > 160 {
		b = b[:160]
	}
	return strings.TrimSpace(string(b))
}

// peerFor is the address a request of p is sent from: p's own, the same for the whole
// run and no other principal's (the sign-in route and the limiter both key on it). A
// caller that is not in the table (a probe) gets a fresh one. The auth-off boot serves
// loopback callers only (core's remote refusal), so its principal's is loopback.
func (h *Registered) peerFor(p *Principal) string {
	if p.peer != "" {
		return p.peer
	}
	return h.nextPeer()
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
	if judged(ri) && res.Stage13 == Stage13Unobserved && !h.cfg.AuthOff && !h.stage13Fronts(via, real, ri) {
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
	req.RemoteAddr = h.peerFor(p)
	switch {
	case p.cookie != nil:
		req.AddCookie(p.cookie)
	case p.bearer != "":
		req.Header.Set("Authorization", "Bearer "+p.bearer)
	case p.access != "":
		req.Header.Set("Cf-Access-Jwt-Assertion", p.access)
	}
	rec := h.serve(via, req)
	res := resultOf(rec, ri)
	h.notRateLimited(res, method, path, p.Name)
	return res, nil
}

// streamTimeout bounds one request. A route that streams (the SSE one) never ends
// by itself: the harness ends it at its first flush, the answer's status already
// being the verdict, and a handler that neither answers nor flushes is cut off here.
const streamTimeout = 30 * time.Second

// serve runs req through via and records the answer. The request's context is
// cancelled at the first flush, so an event stream returns once it has begun.
func (h *Registered) serve(via http.Handler, req *http.Request) *httptest.ResponseRecorder {
	ctx, cancel := context.WithTimeout(req.Context(), streamTimeout)
	defer cancel()
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	via.ServeHTTP(rec, req.WithContext(ctx))
	return rec.ResponseRecorder
}

// flushRecorder is a ResponseRecorder that ends the request's context when the
// handler first flushes.
type flushRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (f *flushRecorder) Flush() {
	f.ResponseRecorder.Flush()
	f.cancel()
}

// resultOf reads a recorded answer, naming stage 13's refusal when it is one.
func resultOf(rec *httptest.ResponseRecorder, ri *httpapi.RouteInfo) Result {
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
	return res
}

// ---- the canary ----

// knownDifference is one cell where the flipped build answers differently from R0
// and the coordinator accepted it.
type knownDifference struct {
	Method, Route string
	Principal     string
	Head, Flip    int
	// Code is the problem code the flipped build answers with.
	Code string
	// Why names the requirement that refuses and the layer that applies it.
	Why string
}

// knownDifferences is the only place the registered path may differ from R0's
// recorded answers on the canary route. Every other cell holds strict HEAD parity.
//
// Coordinator ruling 2026-11-22: a key of the fp-worker user answers 403 on
// /api/v1/auth/me whatever scope it carries, where R0 answered 200 for a key whose
// scope lay outside the worker API. It narrows access (a 200 became a 403), the
// production worker never calls the route (it uses /api/v1/fingerprint/worker), and
// the ruling is to accept it.
//
// The route's requirement is Authenticated (KindAuthenticated), not an atom: stage
// 13 refuses it through the principal's checker, because every credential of the
// fp-worker user is worker-confined (rbac.Confined: the role holds only Confining
// scopes), a confined principal gets no ambient scope (core:self), and a confined
// checker does not satisfy Authenticated. R0 judged the same key by its own scope.
var knownDifferences = []knownDifference{
	{"GET", "/api/v1/auth/me", "key:fp-worker:view", 200, 403, "permission_denied", "Authenticated, refused by the worker-confined checker (no core:self)"},
	{"GET", "/api/v1/auth/me", "key:fp-worker:settings", 200, 403, "permission_denied", "Authenticated, refused by the worker-confined checker (no core:self)"},
	{"GET", "/api/v1/auth/me", "key:fp-worker:users", 200, 403, "permission_denied", "Authenticated, refused by the worker-confined checker (no core:self)"},
}

// canaryRoute is the route whose answer R0 recorded for every principal as its
// canary: the caller's own account, or the auth status for the auth-off one.
func canaryRoute(p *Principal) string {
	if p.Kind == "authoff" {
		return "/api/v1/auth/status"
	}
	return "/api/v1/auth/me"
}

// requireAuthMode fails an auth-off boot that does not report auth_disabled: the
// stored settings carry enable_auth true and M2 turns that into core.auth.mode, so a
// boot that missed the edit answers 401 to the "authoff" principal on every route and
// every cell of its column would be about a different build.
func (h *Registered) requireAuthMode() {
	h.t.Helper()
	if !h.cfg.AuthOff {
		return
	}
	var st struct {
		Disabled bool `json:"auth_disabled"`
	}
	h.coreJSON(h.Principal(Authoff), http.MethodGet, "/api/core/v1/auth/status", nil, &st)
	if !st.Disabled {
		h.t.Fatal("the auth-off boot reports auth_disabled false: core.auth.mode is still required")
	}
}

// requireCanary asks every principal its canary and fails the boot unless the answer
// is what R0 recorded. A core key is held to its imported twin's. knownDifferences
// names the cells that may differ, each exactly (R0's status, the flipped status and
// problem code); a named difference that does not occur fails too, so a pin never
// outlives the behaviour. The cells it applied are in h.KnownDifferences.
func (h *Registered) requireCanary(canary map[string]int) {
	h.t.Helper()
	pinned := map[string]knownDifference{}
	for _, d := range knownDifferences {
		pinned[d.Principal] = d
	}
	for _, p := range h.Principals {
		if p.Name == Owner {
			continue // the owner has no R0 recording
		}
		want, ok := canary[p.Recorded()]
		if !ok {
			h.t.Fatalf("verdicts.json holds no canary for %s", p.Recorded())
		}
		route := canaryRoute(p)
		res, err := h.Do(p, http.MethodGet, route, nil)
		if err != nil {
			h.t.Fatalf("canary %s as %s: %v", route, p.Name, err)
		}
		d, known := pinned[p.Name]
		switch {
		case known:
			if want != d.Head || res.Status != d.Flip || res.Code != d.Code {
				h.t.Fatalf("canary %s %s: R0 %d, flip %d %q; the known difference says R0 %d, flip %d %q", p.Name, route, want, res.Status, res.Code, d.Head, d.Flip, d.Code)
			}
			h.KnownDifferences = append(h.KnownDifferences, p.Name)
		case res.Status == want:
		case want >= 400 && res.Status < 400:
			h.t.Fatalf("canary WIDENING: %s %s: R0 refused with %d, the flipped build answers %d", p.Name, route, want, res.Status)
		default:
			h.t.Fatalf("canary %s %s: R0 %d, flip %d (%s): not a known difference; the principal did not sign in as R0's", p.Name, route, want, res.Status, res.excerpt())
		}
	}
	for _, d := range knownDifferences {
		if _, here := h.byName[d.Principal]; here && !slices.Contains(h.KnownDifferences, d.Principal) {
			h.t.Fatalf("known difference for %s never occurred", d.Principal)
		}
	}
}

// ---- seeding HEAD keys ----

// KeySeed is one HEAD apikey row, for registeredConfig.Seed. User is a username of
// the corpus; Scopes are HEAD's atom names ("library.view", "fingerprint.worker"; an
// empty list is HEAD's "all the owner's role permissions"). Status is "active" (the
// default) or "revoked" (RevokedAt is stamped); Name defaults to the ID's tail. ID
// defaults to one derived from the key. The corpus's usernames and roles are in
// internal/books/rbacreach/principals.json; for example a Confined (fp-worker-style)
// key is User "fp.worker" with Scopes {"fingerprint.worker"}, and a revoked admin key
// is User "lucius.fox", Status "revoked". ExpiresAt is the row's expires_at: zero is a
// year from now; a past instant with Status "active" is the shape of a key HEAD never
// wrote the expiry back into the status of (it stores the time and leaves status active).
type KeySeed struct {
	ID, User, Name, Status string
	Scopes                 []string
	ExpiresAt              time.Time
}

// SeedAPIKey writes a HEAD key row in prod's shape and returns the raw key. The raw
// value is GenerateAPIKeyToken's abk_ token; the row holds its sha256 as token_hash
// and the idx:apikey:hash: and idx:apikey:user: rows HEAD's CreateAPIKey writes, so M3
// imports it like any key a HEAD build wrote. Use it inside registeredConfig.Seed:
//
//	var raw string
//	cfg := registeredConfig{Seed: func(ctx context.Context, lr storage.LegacyRange) (err error) {
//		raw, err = SeedAPIKey(ctx, lr, KeySeed{User: "lucius.fox", Status: "revoked", Scopes: []string{"library.view"}})
//		return err
//	}}
//	h := bootRegistered(t, cfg)
//	h.Raw(request with "Authorization: Bearer "+raw)
func SeedAPIKey(ctx context.Context, lr storage.LegacyRange, ks KeySeed) (string, error) {
	uidRaw, err := lr.Get(ctx, []byte("idx:user:username:"+strings.ToLower(ks.User)))
	if err != nil || len(uidRaw) == 0 {
		return "", fmt.Errorf("the corpus has no user %q: %v", ks.User, err)
	}
	uid := string(uidRaw)
	tok, hash, err := store.GenerateAPIKeyToken()
	if err != nil {
		return "", err
	}
	id := ks.ID
	if id == "" {
		id = "01M7SD" + strings.ToUpper(hash[:20])
	}
	status := cmp.Or(ks.Status, "active")
	now := time.Now().UTC()
	fields := map[string]any{
		"id": id, "user_id": uid, "name": cmp.Or(ks.Name, "key-"+id[len(id)-4:]), "token_hash": hash, "scopes": ks.Scopes,
		"status": status, "created_at": now.Add(-time.Hour).Format(time.RFC3339),
		"expires_at": cmp.Or(ks.ExpiresAt, now.AddDate(1, 0, 0)).UTC().Format(time.RFC3339), "use_count": 0,
	}
	if fields["scopes"] == nil {
		fields["scopes"] = []string{}
	}
	if status == "revoked" {
		fields["revoked_at"] = now.Add(-time.Minute).Format(time.RFC3339)
	}
	row, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	for k, v := range map[string][]byte{
		"apikey:" + id:                      row,
		"idx:apikey:hash:" + hash:           []byte(id),
		"idx:apikey:user:" + uid + ":" + id: []byte(id),
	} {
		if err := lr.Set(ctx, []byte(k), v); err != nil {
			return "", fmt.Errorf("seed %s: %w", k, err)
		}
	}
	return tok, nil
}
