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

// This file is the cell F4-08 B10j asked for: an identity restore is killed at
// every durable operation of the boot that applies it, under two paths, and what
// the next boots leave behind is compared.
//
//   - owed: today's path. swap.go's P4b edge journals marker_written{HooksOwed}
//     and applied before P4d runs, so P4d derives the restore with no journal
//     edge to walk (restoreBoot.Owed) and a crash before the hooks commit leaves
//     a store the next boot meets as an ordinary one.
//   - walk: the spec's path (05-backup 5B.3.4 steps g and h). bootseam.Seams.
//     RestoreWalk makes P4b journal nothing and P4d walk restore_line,
//     hooks_done, marker_written and applied, so the restore journal and its
//     in-flight file survive a crash until the hooks have committed and the next
//     boot resumes the restore itself, with the pre-swap view and its coverage.
//
// The fixture is built to make the hooks act: the restored store is an earlier
// snapshot of the install, and the store it replaces has journaled, since, a key
// revocation, a promotion and the demotion of the restored store's only
// administrator (the last-admin guard), so coverage, the pre-swap pass, a kept
// admin and the restore report all have something to do.

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

// pathCell is one path (owed or walk) over one fixture.
type pathCell struct {
	name  string
	walk  bool
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
	inj.seams.RestoreWalk = c.walk
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

// pathsFixture builds the restore both paths apply: the install's earlier
// snapshot is staged as a revert over the install's current store.
//
// Snapshot (what is restored): alfred is the only administrator, bruce is a
// viewer, tim is an editor holding one active key. Since the snapshot the live
// store journaled three lines: bruce promoted to administrator (his viewer role
// removed), alfred demoted (his admin role removed, which the restored store
// cannot take without a lockout), and tim's key revoked.
func pathsFixture(t *testing.T) Fixture {
	t.Helper()
	f := Fixture{Name: "wayne-manor-hooks", StateDir: t.TempDir()}
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
	return f
}

// build boots the fixture once with c.seed, writing the result back over the
// real directories (Boot's contract).
func (c pathCell) build(t *testing.T) error {
	t.Helper()
	build := func(_ testing.TB, inj *Injector) error {
		inj.seams.RestoreWalk = c.walk
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

// pathsResult is both paths' census and kill rows.
type pathsResult struct {
	free  map[string]endState
	rows  map[string]map[string]*killRow // path -> key -> row
	order map[string][]string            // path -> keys in boot order
}

// runPaths kills each path at every kill point and boots the chain after it.
func runPaths(t *testing.T, f Fixture) pathsResult {
	t.Helper()
	ctx := context.Background()
	cache := config.NewSectionCache()
	res := pathsResult{free: map[string]endState{}, rows: map[string]map[string]*killRow{}, order: map[string][]string{}}
	for _, c := range []pathCell{{name: "owed"}, {name: "walk", walk: true}} {
		c.f, c.cache = f, cache
		free, clones := c.census(t, ctx)
		res.free[c.name] = free
		res.rows[c.name] = map[string]*killRow{}
		var mu sync.Mutex
		sem := make(chan struct{}, runtime.GOMAXPROCS(0))
		var wg sync.WaitGroup
		// A kill point is named by its label and ordinal; Pebble's engine
		// operations share one (the ordinal counts only the others), so the
		// nth clone of a name carries ".n" and none is dropped.
		seenKey := map[string]int{}
		for _, cl := range clones {
			key := fmt.Sprintf("%s #%d", cl.Event.Label, cl.Event.Ordinal)
			if n := seenKey[key]; n > 0 {
				key = fmt.Sprintf("%s.%d", key, n)
			}
			seenKey[fmt.Sprintf("%s #%d", cl.Event.Label, cl.Event.Ordinal)]++
			row := &killRow{key: key, desc: describe(cl.Event)}
			res.rows[c.name][key] = row
			res.order[c.name] = append(res.order[c.name], key)
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
	}
	return res
}

// TestRestorePaths_OwedVersusWalk is B10j's evidence cell. See the table it logs.
func TestRestorePaths_OwedVersusWalk(t *testing.T) {
	f := pathsFixture(t)
	before, err := snapshot(f)
	if err != nil {
		t.Fatal(err)
	}
	res := runPaths(t, f)
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
		if err := os.WriteFile(p, []byte(out.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("\n" + out.String())
	checkPathsEvidence(t, res)
}

// checkPathsEvidence holds the cell to what it shows. The fault-free ends of
// the two paths are equal in every field, and neither is empty (so equal is not
// vacuous); at every kill point both paths hit, the two paths leave the same
// admins, users, keys, stored report and health tokens. The revocation journal
// is the field that differs, and the table above is where it is read: a
// duplicated or reordered restore line and the seq the restore_hooked row
// names after a kill between P4b and the hooks' commit.
func checkPathsEvidence(t *testing.T, res pathsResult) {
	t.Helper()
	for _, p := range []string{"owed", "walk"} {
		e := res.free[p]
		switch {
		case !e.Ready, len(e.Live.Admins) == 0, len(e.Live.Keys) == 0, len(e.Raw.Lines) < 6,
			e.Raw.Report == "none", !strings.Contains(e.Live.Health, "restore_admins_review"):
			t.Errorf("%s: the fault-free end is empty of what the cell compares: %+v", p, e)
		}
	}
	if d := differ(res.free["owed"], res.free["walk"]); len(d) > 0 {
		t.Errorf("the fault-free ends differ in %v", d)
	}
	both, differing := 0, 0
	for _, k := range res.order["owed"] {
		o, w := res.rows["owed"][k], res.rows["walk"][k]
		if w == nil {
			continue
		}
		both++
		d := differ(o.end, w.end)
		if len(d) > 0 {
			differing++
		}
		for _, f := range d {
			if f != "journal.lines" && f != "journal.head+queue" {
				t.Errorf("kill point %s: owed and walk differ in %s (owed %q, walk %q)", k, f, o.end.field(f), w.end.field(f))
			}
		}
	}
	if both == 0 {
		t.Error("the two paths share no kill point")
	}
	t.Logf("kill points in both paths: %d; differing in the journal: %d", both, differing)
}

// TestRestorePaths_ComparatorSeesEachField is the comparator's control: an end
// state altered in one field is reported in that field and no other, so an
// "equal" in the table is not a comparator that cannot tell.
func TestRestorePaths_ComparatorSeesEachField(t *testing.T) {
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

// writeTable prints the evidence: the fault-free ends, then one row per kill
// point of each path against the other and against its own fault-free end.
func writeTable(w *strings.Builder, res pathsResult) {
	fmt.Fprintf(w, "FAULT-FREE ENDS\n")
	for _, p := range []string{"owed", "walk"} {
		e := res.free[p]
		fmt.Fprintf(w, "  %s: boots=%d ready=%t\n", p, e.Boots, e.Ready)
		for _, n := range fieldNames {
			fmt.Fprintf(w, "    %-20s %s\n", n, e.field(n))
		}
	}
	fmt.Fprintf(w, "  owed vs walk fault-free: differ in %v\n\n", differ(res.free["owed"], res.free["walk"]))
	var all []string
	seen := map[string]bool{}
	for _, p := range []string{"walk", "owed"} {
		for _, k := range res.order[p] {
			if !seen[k] {
				seen[k] = true
				all = append(all, k)
			}
		}
	}
	fmt.Fprintf(w, "KILL POINTS (owed vs walk at the same label+ordinal; own-free = kill end vs the same path's fault-free end)\n")
	for _, k := range all {
		o, ow := res.rows["owed"][k]
		wk, wl := res.rows["walk"][k]
		switch {
		case ow && wl:
			fmt.Fprintf(w, "  %-52s owed-vs-walk %-9s owed-vs-ownfree %-9s walk-vs-ownfree %-9s\n", k, verdict(differ(o.end, wk.end)),
				verdict(differ(o.end, res.free["owed"])), verdict(differ(wk.end, res.free["walk"])))
			if d := differ(o.end, wk.end); len(d) > 0 {
				fmt.Fprintf(w, "      differs in %v\n", d)
				for _, n := range d {
					fmt.Fprintf(w, "        %s\n          owed: %s\n          walk: %s\n", n, shortField(o.end, n), shortField(wk.end, n))
				}
			}
		case ow:
			fmt.Fprintf(w, "  %-52s owed-only: vs walk fault-free %-9s vs owed fault-free %-9s\n", k, verdict(differ(o.end, res.free["walk"])), verdict(differ(o.end, res.free["owed"])))
		default:
			fmt.Fprintf(w, "  %-52s walk-only: vs owed fault-free %-9s vs walk fault-free %-9s\n", k, verdict(differ(wk.end, res.free["owed"])), verdict(differ(wk.end, res.free["walk"])))
		}
	}
}

// shortField is a field's render with journal lines cut to their kind and seq,
// so a differing row shows what differs.
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

func verdict(d []string) string {
	if len(d) == 0 {
		return "equal"
	}
	return "DIFFERENT"
}
