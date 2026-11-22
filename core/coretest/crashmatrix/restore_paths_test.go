// file: core/coretest/crashmatrix/restore_paths_test.go
// version: 0.1.0
// guid: 4f7d2a9e-6b13-4c85-a0d7-3e91c58b2f64
// last-edited: 2026-11-21

package crashmatrix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/bootseam/bootseamtest"
	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// This file is F4-08 B10j's cell, kept as the identity restore's permanent
// regression. An identity restore is killed at every durable operation of the
// boot that applies it, the next boots are run, and what they leave behind must
// be what a fault-free boot leaves: in all seven compared fields (admins, users,
// keys, the journal's lines, the journal head with its queue and restore_hooked
// records, the stored restore report and the core.backup health). An identity
// boot WALKS the restore journal (05-backup 5B.3.4 steps g and h: restore_line,
// hooks_done, marker_written, applied, from P4d), so a crash anywhere in the
// hooks leaves the in-flight restore, which the next boot resumes as the same
// restore: one restore line, the pre-swap view and the coverage the hooks need.
//
// The fixture is built to make the hooks act: the restored store is an earlier
// snapshot of the install, and the store it replaces has journaled, since, a key
// revocation, a promotion and the demotion of the restored store's only
// administrator (the last-admin guard), so coverage, the pre-swap pass, a kept
// admin and the restore report all have something to do. Two fixtures run: one
// with the replaced store's set-aside copy (the rbac hook's pre-swap-view
// branch) and one without (a host loss: no pre-swap store, previous == nil).

// The fixture's people and key (Batman names only).
const (
	pathsAlfred = "alfred-pennyworth" // the restored store's only administrator
	pathsBruce  = "bruce-wayne"       // promoted after the snapshot
	pathsTim    = "tim-drake"         // holds the key
	pathsKey    = "batmobile"         // the key's name
)

// pathsActor is the administrator the fixture's seeding acts as: not a stored
// user, compiled from the admin seed role as coretest's harness does.
func pathsActor(r *rbac.Service) (*auth.Principal, error) {
	perms := r.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	if perms == nil || !perms.Has(rbac.CoreAdmin) {
		return nil, errors.New("the rbac service compiled no administrator checker")
	}
	return &auth.Principal{
		User:   &auth.User{ID: "crashmatrix-actor", Username: "crashmatrix-actor", Status: auth.StatusActive},
		Method: auth.MethodSession, Perms: perms,
	}, nil
}

// pathsAs is a principal for a stored user: the service rules that read the
// actor's own record (a key mint, a role change) need one the store has.
func pathsAs(ctx context.Context, a *auth.Service, r *rbac.Service, name string) (*auth.Principal, error) {
	u, err := a.UserByUsername(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return &auth.Principal{User: u, Method: auth.MethodSession, Perms: r.Compile(rbac.CheckerInput{Roles: u.Roles, Interactive: true})}, nil
}

// bootRes is one boot of a path's chain: what the boot saw at readiness and how
// it ended.
type bootRes struct {
	err    error
	events []bootseamtest.Event
	clones []bootseamtest.Clone
	k      *kernel.Kernel
	live   liveObs // zero unless the boot reached readiness
	ready  bool
}

// pathCell is the restore over one fixture.
type pathCell struct {
	name  string
	f     Fixture
	cache *config.SectionCache
	// seed runs inside the boot, after app.New and before Run, with the services:
	// the fixture-building boots use it; the matrix's boots do not.
	seed func(ctx context.Context, a *auth.Service, r *rbac.Service) error
}

func pathsOptions() app.Options {
	return app.Options{AppID: "gotham", IdentityBoot: true, Logger: slog.New(slog.DiscardHandler)}
}

// bootOnce boots the fixture's app once over mem, to readiness and a clean stop.
func (c pathCell) bootOnce(ctx context.Context, mem *vfs.MemFS, opts bootseamtest.Options) (res bootRes) {
	opts.Stores = []string{c.f.dbPath()}
	rec := bootseamtest.New(mem, opts)
	inj := newInjector(c.f, rec, nil, c.cache)
	inj.seams.Built = func(k any) { res.k, _ = k.(*kernel.Kernel) }
	defer func() {
		if p := recover(); p != nil {
			res.err = fmt.Errorf("%w: %v", errPanicked, p)
		}
		res.events, res.clones = rec.Events(), rec.Clones()
	}()
	_, err := inj.New(ctx, pathsOptions(), alfred{})
	if err == nil {
		res.live, err = observeLive(ctx, res.k)
	}
	ferr := inj.finish(ctx)
	switch {
	case err != nil:
		res.err = err
	case ferr != nil:
		res.err = ferr
	default:
		res.ready = true
	}
	return res
}

// chain boots mem until a boot reaches readiness, at most maxBoots times (a
// swapped-back restore exits once). It returns the end state read after the last
// boot stopped.
func (c pathCell) chain(ctx context.Context, mem *vfs.MemFS) (end endState, boots []bootRes) {
	cur := mem
	for range maxBoots {
		r := c.bootOnce(ctx, cur, bootseamtest.Options{})
		if errors.Is(r.err, errPanicked) {
			cur = cur.CrashClone(vfs.CrashCloneCfg{})
			cur.TestingSetDiskUsage(diskUsage())
		}
		boots = append(boots, r)
		if r.ready {
			break
		}
	}
	end.Boots = len(boots)
	last := boots[len(boots)-1]
	end.Ready = last.ready
	if last.err != nil {
		end.LastErr = last.err.Error()
	}
	end.Live = last.live
	end.Raw = readRaw(cur, c.f)
	return end, boots
}

// ---------------------------------------------------------------------------
// What is observed.

// liveObs is what the services and the registry say at readiness.
type liveObs struct {
	Users  []string // "<name> <status> roles=<sorted> reset=<bool> restricted=<bool>"
	Keys   []string // "<owner> <name> <status>"
	Admins []string // the users holding the admin role, with their standing
	Health string   // core.backup: status, reason, detail
}

// rawObs is what the stopped store and the journal file hold.
type rawObs struct {
	Report  string   // c/backup/restore_report, without its time
	Head    string   // c/backup/journal_head's seq
	Queue   []string // c/backup/journal_unapplied/<seq>: the queued seqs
	Hooked  []string // c/backup/restore_hooked/<seq>: the restore lines whose hooks completed
	Lines   []string // the journal file's lines: seq, kind, restore seq, subject (no time, no MAC)
	Pending string   // restore_run: whether a hook run's ID is left
}

// endState is the observation after a chain of boots.
type endState struct {
	Boots   int
	Ready   bool
	LastErr string
	Live    liveObs
	Raw     rawObs
}

// fields are the compared fields, in the order the table prints them.
var fieldNames = []string{"admins", "users", "keys", "journal.lines", "journal.head+queue", "report", "health"}

// field renders one compared field of e; two end states are equal in a field
// exactly when the renders are.
func (e endState) field(name string) string {
	switch name {
	case "admins":
		return strings.Join(e.Live.Admins, "; ")
	case "users":
		return strings.Join(e.Live.Users, "; ")
	case "keys":
		return strings.Join(e.Live.Keys, "; ")
	case "journal.lines":
		return strings.Join(e.Raw.Lines, "; ")
	case "journal.head+queue":
		return fmt.Sprintf("head=%s queue=%v hooked=%v run=%s", e.Raw.Head, e.Raw.Queue, e.Raw.Hooked, e.Raw.Pending)
	case "report":
		return e.Raw.Report
	case "health":
		return e.Live.Health
	}
	panic("unknown field " + name)
}

// differ lists the fields in which a and b differ.
func differ(a, b endState) []string {
	var out []string
	for _, n := range fieldNames {
		if a.field(n) != b.field(n) {
			out = append(out, n)
		}
	}
	return out
}

// observeLive reads the services and the registry of a booted kernel. Standing
// is status, roles, the two reset flags and a key's status: no version, epoch or
// time, which a re-run of an idempotent hook moves without changing anyone's
// standing.
func observeLive(ctx context.Context, k *kernel.Kernel) (liveObs, error) {
	var o liveObs
	a, _, ok := k.IdentityServices()
	if !ok {
		return o, errors.New("the boot built no identity services")
	}
	for u, err := range a.Users(ctx, auth.UserFilter{}) {
		if err != nil {
			return o, fmt.Errorf("list users: %w", err)
		}
		if u.Status == auth.StatusSystem {
			continue
		}
		roles := make([]string, len(u.Roles))
		for i, r := range u.Roles {
			roles[i] = string(r)
		}
		slices.Sort(roles)
		row := fmt.Sprintf("%s %s roles=%s reset=%t restricted=%t", u.Username, u.Status, strings.Join(roles, ","), u.PasswordResetRequired, u.SignInRestricted)
		o.Users = append(o.Users, row)
		if slices.Contains(roles, string(rbac.RoleAdmin)) {
			o.Admins = append(o.Admins, row)
		}
		for key, err := range a.APIKeys(ctx, u.ID) {
			if err != nil {
				return o, fmt.Errorf("list keys of %s: %w", u.Username, err)
			}
			o.Keys = append(o.Keys, fmt.Sprintf("%s %s %s", u.Username, key.Name, key.Status))
		}
	}
	slices.Sort(o.Users)
	slices.Sort(o.Admins)
	slices.Sort(o.Keys)
	if cs, ok := k.BackupHealth(); ok {
		o.Health = fmt.Sprintf("%v %s | %s", cs.Result.Status, cs.Result.Reason, cs.Result.Detail)
	} else {
		o.Health = "core.backup not registered"
	}
	return o, nil
}

// readRaw reads the stopped store's c/backup/ and the journal file.
func readRaw(mem *vfs.MemFS, f Fixture) (o rawObs) {
	o.Report, o.Head = "none", "none"
	eng, err := ipebble.Open(f.dbPath(), ipebble.Options{FS: mem, ReadOnly: true, ErrorIfNotExists: true})
	if err != nil {
		o.Report = "store unreadable: " + err.Error()
		return o
	}
	defer func() { _ = eng.Close(10 * time.Second) }()
	it, err := eng.DB().NewIter(nil)
	if err != nil {
		o.Report = "store unreadable: " + err.Error()
		return o
	}
	defer func() { _ = it.Close() }()
	const prefix = "c/backup/"
	for valid := it.First(); valid; valid = it.Next() {
		k := string(it.Key())
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		name := strings.TrimPrefix(k, prefix)
		v, verr := it.ValueAndErr()
		if verr != nil {
			continue
		}
		switch {
		case strings.HasSuffix(name, "restore_report"):
			var rec map[string]any
			if json.Unmarshal(v, &rec) == nil {
				delete(rec, "at")
				b, _ := json.Marshal(rec) // map[string]any of decoded JSON always marshals
				o.Report = string(b)
			} else {
				o.Report = "undecodable"
			}
		case strings.HasSuffix(name, "journal_head"):
			var h map[string]any
			if json.Unmarshal(v, &h) == nil {
				o.Head = fmt.Sprint(h["seq"])
			}
		case strings.Contains(name, "journal_unapplied/"):
			o.Queue = append(o.Queue, strings.TrimLeft(name[strings.LastIndex(name, "/")+1:], "0"))
		case strings.Contains(name, "restore_hooked/"):
			o.Hooked = append(o.Hooked, name[strings.LastIndex(name, "/")+1:])
		case strings.HasSuffix(name, "restore_run"):
			o.Pending = "left"
		}
	}
	if o.Pending == "" {
		o.Pending = "clear"
	}
	o.Lines = journalLines(mem, f)
	return o
}

// journalLines is the journal file's lines as the unauthenticated JSON the file
// stores: seq, kind, restore seq and subject, without time, MAC or key ID.
func journalLines(mem *vfs.MemFS, f Fixture) []string {
	var path string
	for _, root := range f.roots() {
		_ = walkMem(mem, root, func(p string) {
			if strings.HasSuffix(p, "revocations.journal") {
				path = p
			}
		})
	}
	if path == "" {
		return []string{"no journal file"}
	}
	r, err := mem.Open(path)
	if err != nil {
		return []string{"journal unreadable: " + err.Error()}
	}
	defer func() { _ = r.Close() }()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(r); err != nil {
		return []string{"journal unreadable: " + err.Error()}
	}
	var out []string
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			out = append(out, "undecodable line")
			continue
		}
		for _, drop := range []string{"mac", "kid", "at", "started_at", "base_mac", "install_id"} {
			delete(m, drop)
		}
		keys := slices.Sorted(maps.Keys(m))
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
		}
		out = append(out, strings.Join(parts, ","))
	}
	return out
}

// walkMem calls fn for every regular file under root.
func walkMem(mem *vfs.MemFS, root string, fn func(string)) error {
	names, err := mem.List(root)
	if err != nil {
		return err
	}
	for _, n := range names {
		p := mem.PathJoin(root, n)
		fi, err := mem.Stat(p)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			_ = walkMem(mem, p, fn)
			continue
		}
		fn(p)
	}
	return nil
}

// ---------------------------------------------------------------------------
// The fixture.

// pathsFixture builds the restore: the install's earlier snapshot is staged as a
// revert over the install's current store, or, with hostLoss, over no store.
//
// Snapshot (what is restored): alfred is the only administrator, bruce is a
// viewer, tim is an editor holding one active key. Since the snapshot the live
// store journaled three lines: bruce promoted to administrator (his viewer role
// removed), alfred demoted (his admin role removed, which the restored store
// cannot take without a lockout), and tim's key revoked.
func pathsFixture(t *testing.T, hostLoss bool) Fixture {
	t.Helper()
	f := Fixture{Name: "wayne-manor-hooks", StateDir: t.TempDir()}
	if hostLoss {
		f.Name = "wayne-manor-host-loss"
	}
	seed := pathCell{name: "seed", f: f, seed: func(ctx context.Context, a *auth.Service, r *rbac.Service) error {
		actor, err := pathsActor(r)
		if err != nil {
			return err
		}
		for _, u := range []auth.NewUser{
			{Username: pathsAlfred, Roles: []rbac.RoleName{rbac.RoleAdmin}},
			{Username: pathsBruce, Roles: []rbac.RoleName{rbac.RoleViewer}},
			{Username: pathsTim, Roles: []rbac.RoleName{rbac.RoleEditor}},
		} {
			if _, err := a.CreateUser(ctx, actor, u); err != nil {
				return fmt.Errorf("create %s: %w", u.Username, err)
			}
		}
		tim, err := a.UserByUsername(ctx, pathsTim)
		if err != nil {
			return err
		}
		admin, err := pathsAs(ctx, a, r, pathsAlfred)
		if err != nil {
			return err
		}
		if _, _, err = a.CreateAPIKey(ctx, admin, auth.KeySpec{Owner: tim.ID, Name: pathsKey, Scopes: []rbac.Permission{"core:tasks.view"}}); err != nil {
			return fmt.Errorf("mint %s: %w", pathsKey, err)
		}
		return nil
	}}
	if err := seed.build(t); err != nil {
		t.Fatalf("seeding the snapshot: %v", err)
	}
	const id = "r-wayne-manor-hooks"
	staged := filepath.Join(restorejournal.StagingDir(f.dbPath(), id), "main.pebble")
	copyStoreDir(t, f.dbPath(), staged)
	tail := pathCell{name: "tail", f: f, seed: func(ctx context.Context, a *auth.Service, r *rbac.Service) error {
		alfredP, err := pathsAs(ctx, a, r, pathsAlfred)
		if err != nil {
			return err
		}
		bruce, err := a.UserByUsername(ctx, pathsBruce)
		if err != nil {
			return err
		}
		tim, err := a.UserByUsername(ctx, pathsTim)
		if err != nil {
			return err
		}
		if err := a.SetRoles(ctx, alfredP, bruce.ID, []rbac.RoleName{rbac.RoleAdmin}); err != nil {
			return fmt.Errorf("promote %s: %w", pathsBruce, err)
		}
		// bruce is the administrator now and demotes the one the snapshot knows.
		bruceP, err := pathsAs(ctx, a, r, pathsBruce)
		if err != nil {
			return err
		}
		if err := a.SetRoles(ctx, bruceP, alfredP.User.ID, []rbac.RoleName{rbac.RoleViewer}); err != nil {
			return fmt.Errorf("demote %s: %w", pathsAlfred, err)
		}
		for key, err := range a.APIKeys(ctx, tim.ID) {
			if err != nil {
				return err
			}
			if err := a.RevokeAPIKey(ctx, bruceP, key.ID); err != nil {
				return fmt.Errorf("revoke %s: %w", key.Name, err)
			}
		}
		return nil
	}}
	if err := tail.build(t); err != nil {
		t.Fatalf("journaling the tail: %v", err)
	}
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindRevert, Targets: []restorejournal.Target{{From: staged, To: f.dbPath()}}}
	if err := Stage(f, in); err != nil {
		t.Fatalf("staging the restore: %v", err)
	}
	if hostLoss {
		// The host lost its store: the restore lands on no live store, so no
		// pre-swap copy is set aside and the hooks have no previous view.
		if err := os.RemoveAll(f.dbPath()); err != nil {
			t.Fatalf("losing the host's store: %v", err)
		}
	}
	return f
}

// build boots the fixture once with c.seed, writing the result back over the
// real directories (Boot's contract).
func (c pathCell) build(t *testing.T) error {
	t.Helper()
	build := func(_ testing.TB, inj *Injector) error {
		inj.seams.Built = nil
		var k *kernel.Kernel
		inj.seams.Built = func(x any) { k, _ = x.(*kernel.Kernel) }
		if _, err := inj.New(context.Background(), pathsOptions(), alfred{}); err != nil {
			return err
		}
		a, r, ok := k.IdentityServices()
		if !ok {
			return errors.New("the boot built no identity services")
		}
		return c.seed(context.Background(), a, r)
	}
	return Boot(t, build, c.f)
}

// ---------------------------------------------------------------------------
// The matrix.

// killRow is one kill point of one path: where it fell and the end state its
// chain reached.
type killRow struct {
	key   string // "<label> #<ordinal>"
	desc  string
	end   endState
	boots []bootRes
}

// census boots the fixture once with a clone before every kill point and
// returns those clones in order, and the fault-free chain's end state.
func (c pathCell) census(t *testing.T, ctx context.Context) (free endState, clones []bootseamtest.Clone) {
	t.Helper()
	mem, err := load(c.f)
	if err != nil {
		t.Fatal(err)
	}
	r := c.bootOnce(ctx, mem, bootseamtest.Options{Clones: true})
	if !r.ready {
		t.Fatalf("%s: the fault-free boot did not reach readiness: %v", c.name, r.err)
	}
	mem2, err := load(c.f)
	if err != nil {
		t.Fatal(err)
	}
	free, _ = c.chain(ctx, mem2)
	return free, r.clones
}

// restoreResult is the census and the kill rows of one fixture.
type restoreResult struct {
	free  endState
	rows  map[string]*killRow
	order []string
}

// runRestore kills the restore at every kill point and boots the chain after it.
func runRestore(t *testing.T, f Fixture) restoreResult {
	t.Helper()
	ctx := context.Background()
	c := pathCell{name: "restore", f: f, cache: config.NewSectionCache()}
	free, clones := c.census(t, ctx)
	res := restoreResult{free: free, rows: map[string]*killRow{}}
	var mu sync.Mutex
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	// A kill point is named by its label and ordinal; Pebble's engine
	// operations share one (the ordinal counts only the others), so the nth
	// clone of a name carries ".n" and none is dropped.
	seenKey := map[string]int{}
	for _, cl := range clones {
		base := fmt.Sprintf("%s #%d", cl.Event.Label, cl.Event.Ordinal)
		key := base
		if n := seenKey[base]; n > 0 {
			key = fmt.Sprintf("%s.%d", base, n)
		}
		seenKey[base]++
		row := &killRow{key: key, desc: describe(cl.Event)}
		res.rows[key] = row
		res.order = append(res.order, key)
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			cl.FS.TestingSetDiskUsage(diskUsage())
			end, boots := c.chain(ctx, cl.FS)
			mu.Lock()
			row.end, row.boots = end, boots
			mu.Unlock()
		}()
	}
	wg.Wait()
	return res
}

// TestIdentityRestore_KilledThenBootedEqualsFaultFree is the regression: at every
// kill point of the boot that applies an identity restore, the killed-then-booted
// restore ends as the fault-free one does, in all seven fields, with exactly one
// restore line that restore_hooked names. Both fixtures run: the replaced store's
// set-aside copy present (the rbac hook's pre-swap-view branch) and absent (a
// host loss).
func TestIdentityRestore_KilledThenBootedEqualsFaultFree(t *testing.T) {
	for _, tc := range []struct {
		name     string
		hostLoss bool
	}{{"with_previous", false}, {"previous_nil", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := pathsFixture(t, tc.hostLoss)
			before, err := snapshot(f)
			if err != nil {
				t.Fatal(err)
			}
			res := runRestore(t, f)
			after, err := snapshot(f)
			if err != nil {
				t.Fatal(err)
			}
			if d := before.diff(after); len(d) > 0 {
				t.Fatalf("the cell wrote to the real disk: %v", d)
			}
			var out strings.Builder
			writeTable(&out, res)
			if p := os.Getenv("B10J_TABLE"); p != "" {
				if err := os.WriteFile(p+"."+tc.name, []byte(out.String()), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Log("\n" + out.String())
			checkRestoreConverges(t, res, tc.hostLoss)
		})
	}
}

// restoreLines is the fault-free-comparable restore lines of an end state: the
// journal lines of kind "restore".
func restoreLines(e endState) []string {
	var out []string
	for _, l := range e.Raw.Lines {
		if strings.Contains(l, "kind=restore,") || strings.HasSuffix(l, "kind=restore") {
			out = append(out, l)
		}
	}
	return out
}

// knownRow is a kill point that does not converge for a reason outside the walk,
// named and held to exactly that: the row must still differ from the fault-free
// end (so a fix makes the entry stale and the test says so), and must be equal to
// it once normalise removes the one thing the reason names.
type knownRow struct {
	why       string
	normalise func(endState) endState
	// spec is text 05-backup.md must still hold for the row to stand as an
	// exemption: the clause the row is measured against. The test fails when the
	// file or a phrase is gone.
	spec []string
}

// specPath is docs/specs/platform-core/05-backup.md, found from the package
// directory (os.Getwd, not runtime.Caller: CI builds with -trimpath).
func specPath() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, "docs", "specs", "platform-core", "05-backup.md")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("docs/specs/platform-core/05-backup.md not found above the package directory")
		}
		dir = parent
	}
}

// hostLossForwardRows (a host-loss restore: no live store, so no set-aside copy):
// the swap itself, before P4b and so before any step of the walk, cannot prove
// that the two names a crashed place rename leaves (restoreswap's memGhost model:
// the staged store at both its source and its destination) are one store. With
// no set-aside to prove the twin by, restoreswap.mv refuses (ErrSwapAmbiguous),
// the restore is swapped back and the next boot is a fresh install. A swap
// property reached only on a filesystem that can keep both names of a rename.
// Spec: 05-backup 5B.3.4 step x sends a failure of a forward step to the
// swap-back (restoreswap.forward -> SwapBack), and says "On a host with **no**
// previous store, the journal records `no_previous` and storage refuses to
// create a fresh store at that path until the operator resolves it, so a failed
// host-loss restore never leaves an empty store behind": the row leaves a fresh
// install, which the clause forbids, so it is a defect of the swap and not a
// pass. The unprovable-twin rule is restoreswap/fsops.go mv (ErrSwapAmbiguous).
// Granted as an exemption by the F4-08 coordinator on condition that it stays
// stale-checked and cites this text: the test fails when the row converges or
// when the cited clause leaves the spec.
var hostLossForwardRows = map[string]knownRow{
	"swap.place.mv #2": {why: "the dir sync after the staged store's rename into an empty place: the swap cannot prove the twin", normalise: nil,
		spec: []string{
			"On a host with **no** previous store, the journal records `no_previous`",
			"so a failed host-loss restore never leaves an empty store behind",
			"any failure after the in-flight rename that ends step b",
		}},
}

// epochDriftRows (a restore with a pre-swap store): auth.PostRestore bumps every
// user's credential epoch on each run, so a boot resumed after the auth hook
// committed bumps it again, and the kept admin's password.changed line, which the
// rbac hook journals after it, records the epoch of its run (2, not 1). The
// standing is the same: the users field leaves the epoch out for this reason.
var epochDriftRows = map[string]knownRow{
	"kernel.hooks.rbac #1": {why: "the resumed auth hook bumped the credential epoch again before the rbac hook's restriction line", normalise: dropEpochs},
}

// dropEpochs is e with the epoch of every journal line removed.
func dropEpochs(e endState) endState {
	lines := make([]string, len(e.Raw.Lines))
	for i, l := range e.Raw.Lines {
		parts := strings.Split(l, ",")
		parts = slices.DeleteFunc(parts, func(p string) bool { return strings.HasPrefix(p, "epoch=") })
		lines[i] = strings.Join(parts, ",")
	}
	e.Raw.Lines = lines
	return e
}

// checkSpecCites fails when the spec no longer holds the text a known row cites.
func checkSpecCites(t *testing.T, k string, kr knownRow) {
	t.Helper()
	if len(kr.spec) == 0 {
		return
	}
	p, err := specPath()
	if err != nil {
		t.Errorf("kill point %s cites the spec: %v", k, err)
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Errorf("kill point %s cites the spec: %v", k, err)
		return
	}
	for _, phrase := range kr.spec {
		if !strings.Contains(string(raw), phrase) {
			t.Errorf("kill point %s: the spec no longer says %q: the exemption's citation is gone", k, phrase)
		}
	}
}

// known is the known row for kill point k in the fixture.
func known(k string, hostLoss bool) (knownRow, bool) {
	m := epochDriftRows
	if hostLoss {
		m = hostLossForwardRows
	}
	kr, ok := m[k]
	return kr, ok
}

// checkRestoreConverges holds the cell to its bar. The fault-free end is not
// empty of what the cell compares (so equal is not vacuous); at every kill point
// the chain reaches readiness and ends equal to it in every field, with exactly
// one restore line and restore_hooked naming it.
func checkRestoreConverges(t *testing.T, res restoreResult, hostLoss bool) {
	t.Helper()
	e := res.free
	switch {
	case !e.Ready, len(e.Live.Admins) == 0, len(e.Live.Keys) == 0, len(e.Raw.Lines) < 6, e.Raw.Report == "none":
		t.Fatalf("the fault-free end is empty of what the cell compares: %+v", e)
	case !hostLoss && !strings.Contains(e.Live.Health, "restore_last_admin_kept"):
		// With the pre-swap view the rbac hook keeps the restored store's only
		// administrator (the demotion would lock out) and restricts it.
		t.Fatalf("the fault-free end of a restore with a pre-swap store did not take the pre-swap branch: %s", e.Live.Health)
	case hostLoss && !strings.Contains(e.Live.Health, "restore_admins_review"):
		// With no pre-swap store (previous == nil) every administrator is listed
		// for review.
		t.Fatalf("the fault-free end of a restore with no pre-swap store did not take the previous == nil branch: %s", e.Live.Health)
	}
	if got := restoreLines(e); len(got) != 1 {
		t.Fatalf("the fault-free end has %d restore lines, want 1: %v", len(got), got)
	}
	if len(e.Raw.Hooked) != 1 {
		t.Fatalf("the fault-free end has restore_hooked %v, want one record", e.Raw.Hooked)
	}
	if len(res.order) < 20 {
		t.Fatalf("the census found %d kill points: the cell is not killing the boot", len(res.order))
	}
	bad := 0
	for _, k := range res.order {
		r := res.rows[k]
		if kr, ok := known(k, hostLoss); ok {
			checkSpecCites(t, k, kr)
			// The row must still differ, so a fix that closes it makes this entry
			// stale; and it may differ in nothing the reason does not name.
			if r.end.Ready && len(differ(r.end, e)) == 0 {
				t.Errorf("kill point %s converges now: remove it from the known rows (%s)", k, kr.why)
			}
			if kr.normalise != nil {
				if d := differ(kr.normalise(r.end), kr.normalise(e)); len(d) > 0 {
					t.Errorf("kill point %s: differs in %v beyond what its known reason names (%s)", k, d, kr.why)
				}
			}
			continue
		}
		if !r.end.Ready {
			t.Errorf("kill point %s: the chain of %d boots never reached readiness: %s", k, r.end.Boots, r.end.LastErr)
			bad++
			continue
		}
		for _, f := range differ(r.end, e) {
			t.Errorf("kill point %s: %s differs from the fault-free end\n  killed-then-booted: %s\n  fault-free:         %s%s", k, f, shortField(r.end, f), shortField(e, f), lineDiffNote(r.end, e, f))
			bad++
		}
		lines := restoreLines(r.end)
		if len(lines) != 1 {
			t.Errorf("kill point %s: %d restore lines, want exactly 1: %v", k, len(lines), lines)
			bad++
		}
		if !slices.Equal(r.end.Raw.Hooked, e.Raw.Hooked) {
			t.Errorf("kill point %s: restore_hooked = %v, want the restore line's seq %v", k, r.end.Raw.Hooked, e.Raw.Hooked)
			bad++
		}
	}
	t.Logf("kill points: %d; rows that did not converge: %d", len(res.order), bad)
}

// TestIdentityRestore_ComparatorSeesEachField is the comparator's control: an end
// state altered in one field is reported in that field and no other, so an
// "equal" in the table is not a comparator that cannot tell.
func TestIdentityRestore_ComparatorSeesEachField(t *testing.T) {
	base := endState{
		Live: liveObs{Users: []string{"u"}, Keys: []string{"k"}, Admins: []string{"a"}, Health: "h"},
		Raw:  rawObs{Report: "r", Head: "1", Queue: []string{"1"}, Hooked: []string{"1"}, Lines: []string{"l"}, Pending: "clear"},
	}
	alter := map[string]func(e *endState){
		"admins":             func(e *endState) { e.Live.Admins = []string{"b"} },
		"users":              func(e *endState) { e.Live.Users = []string{"v"} },
		"keys":               func(e *endState) { e.Live.Keys = []string{"j"} },
		"journal.lines":      func(e *endState) { e.Raw.Lines = []string{"l", "m"} },
		"journal.head+queue": func(e *endState) { e.Raw.Queue = nil },
		"report":             func(e *endState) { e.Raw.Report = "s" },
		"health":             func(e *endState) { e.Live.Health = "i" },
	}
	if len(alter) != len(fieldNames) {
		t.Fatalf("%d alterations for %d fields", len(alter), len(fieldNames))
	}
	for name, f := range alter {
		e := base
		f(&e)
		if got := differ(base, e); !slices.Equal(got, []string{name}) {
			t.Errorf("altering %s: differ reported %v", name, got)
		}
	}
	if d := differ(base, base); len(d) != 0 {
		t.Errorf("an end state differs from itself in %v", d)
	}
}

// writeTable prints the fault-free end and, for each kill point, whether the
// chain after it ends as the fault-free one does.
func writeTable(w *strings.Builder, res restoreResult) {
	e := res.free
	fmt.Fprintf(w, "FAULT-FREE END: boots=%d ready=%t\n", e.Boots, e.Ready)
	for _, n := range fieldNames {
		fmt.Fprintf(w, "    %-20s %s\n", n, e.field(n))
	}
	fmt.Fprintf(w, "\nKILL POINTS (killed-then-booted vs the fault-free end)\n")
	for _, k := range res.order {
		r := res.rows[k]
		d := differ(r.end, e)
		fmt.Fprintf(w, "  %-52s boots=%d ready=%-5t %s\n", k, r.end.Boots, r.end.Ready, verdict(d))
		for _, n := range d {
			fmt.Fprintf(w, "        %s\n          killed: %s\n          free:   %s%s\n", n, shortField(r.end, n), shortField(e, n), lineDiffNote(r.end, e, n))
		}
	}
}

// shortField is a field's render for a differing row. The journal's lines are
// shown as the seq:kind order and, after it, the lines the other end does not
// have: a duplicated or reordered line differs in the order, a line that names
// another seq, key or detail differs in the text.
func shortField(e endState, name string) string {
	if name != "journal.lines" {
		return e.field(name)
	}
	var out []string
	for _, l := range e.Raw.Lines {
		var seq, kind string
		for _, part := range strings.Split(l, ",") {
			switch {
			case strings.HasPrefix(part, "seq="):
				seq = strings.TrimPrefix(part, "seq=")
			case strings.HasPrefix(part, "kind="):
				kind = strings.TrimPrefix(part, "kind=")
			}
		}
		out = append(out, seq+":"+kind)
	}
	return strings.Join(out, " ")
}

// lineDiffNote is the journal lines each end lacks, for the journal's field.
func lineDiffNote(killed, free endState, field string) string {
	if field != "journal.lines" {
		return ""
	}
	return "\n  only in killed-then-booted:\n                      " + lineDiff(killed, free) + "\n  only in fault-free:\n                      " + lineDiff(free, killed)
}

// lineDiff lists the journal lines of a that b lacks, in full.
func lineDiff(a, b endState) string {
	var out []string
	for _, l := range a.Raw.Lines {
		if !slices.Contains(b.Raw.Lines, l) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n                      ")
}

func verdict(d []string) string {
	if len(d) == 0 {
		return "equal"
	}
	return "DIFFERENT"
}
