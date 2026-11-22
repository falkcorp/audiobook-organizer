// file: core/kernel/backup_wire_test.go
// version: 0.1.0
// guid: 9e3c7b15-4a28-4d61-8f0b-d2a6c15e7384
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/internal/taskstore"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
	"github.com/falkcorp/mediacore/tasks"
)

// bwRing is a backup.Keyring over the journal fixture's derive keys.
type bwRing struct{ journal.Keys }

func (bwRing) KeyIDs() []string { return []string{"d1"} }
func (bwRing) KeyIDsReferenced(context.Context, storage.Reader) ([]string, error) {
	return nil, nil
}

// bwStager is a backup.CheckedStager that counts what reaches it.
type bwStager struct {
	checks, stages int
	checkErr       error
}

func (s *bwStager) CheckStage(restorejournal.Intent) error { s.checks++; return s.checkErr }
func (s *bwStager) Stage(context.Context, restorejournal.Intent) error {
	s.stages++
	return nil
}

// bwInput is newBackupSvc's input over the journal fixture fx: its store,
// journal component and audit store, with st as the swapper.
func bwInput(t *testing.T, fx *rxFx, st backup.CheckedStager) backupWireIn {
	t.Helper()
	k := &Kernel{ident: &identityBoot{journal: fx.comp, backupKS: fx.bks()}, audit: fx.aud}
	return backupWireIn{
		AppID: "gotham", AppVersion: "1.0.0", BuildID: "batcomputer-1", Prefix: "gotham_",
		Dir: t.TempDir(), StateDir: fx.state, DBPath: fx.dbPath,
		Store: fx.db.Admin(), Keyring: bwRing{fx.keys}, Markers: k.backupMarkers(), Stager: st,
		Prune: backupPrune(fx.comp), Clock: fx.clock.now, Logger: slog.New(slog.DiscardHandler),
	}
}

type fakeCut struct {
	hw  uint64
	err error
	n   int
}

func (c *fakeCut) HighWater(context.Context) (uint64, error) { c.n++; return c.hw, c.err }

// TestBackupMarkers_ReadsTheThreeBoundaries: the journal head and the content
// marker come from c/backup/, the audit boundary from the audit store's cut,
// and the marker is nil for a store that has none.
func TestBackupMarkers_ReadsTheThreeBoundaries(t *testing.T) {
	ctx := context.Background()
	fx := newRxFx(t, nil)
	cut := &fakeCut{hw: 77}
	m := &backupMarkers{journal: fx.comp, ks: fx.bks(), audit: cut}

	b, err := m.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b.AuditHighWater != 77 || cut.n != 1 {
		t.Fatalf("AuditHighWater = %d after %d cuts, want 77 after 1", b.AuditHighWater, cut.n)
	}
	if b.ServedBoot != nil || b.JournalSeq != 0 {
		t.Fatalf("a store with no journal and no marker read %+v", b)
	}

	pair, err := m.Advance(ctx)
	if err != nil || pair.N == 0 {
		t.Fatalf("Advance = %+v, %v", pair, err)
	}
	b, err = m.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b.ServedBoot == nil || *b.ServedBoot != pair {
		t.Fatalf("Read marker = %+v, want the pair Advance wrote, %+v", b.ServedBoot, pair)
	}
}

// TestBackupMarkers_AFailedCutFailsTheRead: no half-read boundaries reach
// Create, which would record an audit mark it never cut.
func TestBackupMarkers_AFailedCutFailsTheRead(t *testing.T) {
	fx := newRxFx(t, nil)
	boom := errors.New("the audit sequence would not commit")
	b, err := (&backupMarkers{journal: fx.comp, ks: fx.bks(), audit: &fakeCut{err: boom}}).Read(context.Background())
	if !errors.Is(err, boom) || !reflect.DeepEqual(b, backup.Boundaries{}) {
		t.Fatalf("Read = %+v, %v; want a zero Boundaries and the cut's error", b, err)
	}
}

// TestBackupMarkers_UsesTheIdentityBootsJournal (STOP 6): the markers are over
// the component P4d built, and backup_wire.go builds no journal component of
// its own (a second one over the journal file would take its lock).
func TestBackupMarkers_UsesTheIdentityBootsJournal(t *testing.T) {
	fx := newRxFx(t, nil)
	k := &Kernel{ident: &identityBoot{journal: fx.comp, backupKS: fx.bks()}, audit: fx.aud}
	if got := k.backupMarkers().journal; got != backupJournal(fx.comp) {
		t.Fatalf("markers journal = %p, want k.ident.journal %p", got, fx.comp)
	}
	src, err := os.ReadFile("backup_wire.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"journal.NewComponent", "journalfs"} {
		if strings.Contains(string(src), banned) {
			t.Errorf("backup_wire.go mentions %s: the journal handle is k.ident.journal, never a second one", banned)
		}
	}
}

// TestBackupWireInput_RefusesBeforeItsHandlesExist: a kernel that has not
// reached P4d (or has no swapper, secrets, store or audit) builds nothing and
// says which handle is missing.
func TestBackupWireInput_RefusesBeforeItsHandlesExist(t *testing.T) {
	_, err := (&Kernel{}).newBackup(context.Background())
	if err == nil || !strings.Contains(err.Error(), "journal component") {
		t.Fatalf("a bare kernel built a backup service: %v", err)
	}
}

// TestBackupWireInput_RefusesAHalfBuiltIdentityBoot: an identity boot whose
// P4d has not built the journal component is as unready as no boot at all, and
// is refused before the service is built on a nil handle.
func TestBackupWireInput_RefusesAHalfBuiltIdentityBoot(t *testing.T) {
	_, err := (&Kernel{ident: &identityBoot{}}).newBackup(context.Background())
	if err == nil || !strings.Contains(err.Error(), "journal component") {
		t.Fatalf("an identity boot with no journal built a backup service: %v", err)
	}
}

// TestNewBackupSvc_RefusesNoSwapper: with nothing to stage a restore through,
// no service is built, so a restore can never reach a nil Stager.
func TestNewBackupSvc_RefusesNoSwapper(t *testing.T) {
	fx := newRxFx(t, nil)
	if svc, err := newBackupSvc(bwInput(t, fx, nil)); err == nil || svc != nil {
		t.Fatalf("newBackupSvc with no swapper = (%v, %v), want a refusal", svc, err)
	}
}

// TestBackupRestart_IsRequestRestart (blocker 16): a staged restore's restart
// goes through Lifecycle.RequestRestart, the path whose stop order sends hub
// A's system.shutdown frame; while the app is not ready it reports that
// instead of logging and returning success.
func TestBackupRestart_IsRequestRestart(t *testing.T) {
	ctx := context.Background()
	if backupRestart(nil) != nil {
		t.Fatal("a nil lifecycle yields a restart")
	}
	l := newLifecycle()
	restart := backupRestart(l)
	if err := restart(ctx); !errors.Is(err, errBackupNotReady) {
		t.Fatalf("restart before ready = %v, want errBackupNotReady", err)
	}
	select {
	case <-l.requested:
		t.Fatal("a restart before ready requested a stop")
	default:
	}
	l.ready.Store(true)
	if err := restart(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.requested:
	default:
		t.Fatal("a ready lifecycle was not asked to restart")
	}
	if l.reason != backupRestartReason {
		t.Fatalf("reason = %q", l.reason)
	}
}

// TestBackupOptions_Wiring: the options the constructor binds are the ones
// this slice owes, each reaching the handle it names.
func TestBackupOptions_Wiring(t *testing.T) {
	fx := newRxFx(t, nil)
	st := &bwStager{}
	in := bwInput(t, fx, st)
	l := newLifecycle()
	l.ready.Store(true)
	in.Restart = backupRestart(l)
	o := backupOptions(in)

	if o.Markers == nil || o.Stager == nil || o.PruneJournal == nil || o.Restart == nil {
		t.Fatalf("an option is unbound: %+v", o)
	}
	if err := o.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-l.requested:
	default:
		t.Fatal("Options.Restart did not reach Lifecycle.RequestRestart")
	}

	// The stager handed to the Service is the checked swapper itself, which
	// backup.New wraps (TestStageRestoreBeforeStageHook in core/backup holds the
	// order); a kernel input with no swapper builds no Service.
	if o.Stager != in.Stager {
		t.Fatalf("Stager = %v, want the input's swapper", o.Stager)
	}
}

// TestBackupPrune_IsTheComponentsTrim: retention's PruneJournal reaches
// Component.Trim with the seq and returns its refusal.
func TestBackupPrune_IsTheComponentsTrim(t *testing.T) {
	j := &fakeJournal{}
	if err := backupPrune(j)(context.Background(), 41); err != nil || j.trimmed != 41 {
		t.Fatalf("Prune = %v, trimmed %d", err, j.trimmed)
	}
	j.trimErr = journal.ErrTrimRefused
	if err := backupPrune(j)(context.Background(), 42); !errors.Is(err, journal.ErrTrimRefused) {
		t.Fatalf("Prune = %v, want the refusal", err)
	}
}

type fakeJournal struct {
	trimmed uint64
	trimErr error
}

func (j *fakeJournal) AdvanceServedBoot(context.Context) (journal.ServedBoot, error) {
	return journal.ServedBoot{}, nil
}
func (j *fakeJournal) Trim(_ context.Context, seq uint64) (journal.TrimResult, error) {
	j.trimmed = seq
	return journal.TrimResult{}, j.trimErr
}

// TestBackupSettingsOf maps the live section to the service's settings.
func TestBackupSettingsOf(t *testing.T) {
	s := &backupSettings{Enabled: true, Compression: "zstd", VerifyKeep: 4, HashWorkers: 2,
		Retention: backupRetention{MaxCount: 7, MaxTotalBytes: 9, MaxAge: 5}, Restore: backupRestore{RehashOnBoot: true}}
	got := backupSettingsOf(func() *backupSettings { return s })()
	if !got.Enabled || got.Compression != "zstd" || got.VerifyKeep != 4 || got.HashWorkers != 2 ||
		got.MaxCount != 7 || got.MaxTotalBytes != 9 || got.MaxAge != 5 || !got.RehashOnBoot {
		t.Fatalf("settings = %+v", got)
	}
	s.Retention.MaxCount = 8
	if backupSettingsOf(func() *backupSettings { return s })().MaxCount != 8 {
		t.Fatal("the settings are not read live")
	}
}

type fakeRecorder struct{ got []audit.Event }

func (r *fakeRecorder) Record(_ context.Context, e audit.Event) error {
	r.got = append(r.got, e)
	return nil
}

// TestBackupAudit: core.restore.stage is a success record carrying the
// archive and the flags, and the refusal after it the same action as a
// failure with its fixed reason; a failed write is the caller's error.
func TestBackupAudit(t *testing.T) {
	if backupAudit(nil) != nil {
		t.Fatal("no recorder yields an audit function")
	}
	rec := &fakeRecorder{}
	f := backupAudit(rec)
	ctx := context.Background()
	if err := f(ctx, "core.restore.stage", map[string]string{"archive": "arch_gotham_01", "foreign": "false"}); err != nil {
		t.Fatal(err)
	}
	if err := f(ctx, "core.restore.stage", map[string]string{"archive": "arch_gotham_01", "outcome": "rejected", "reason": "swap_pending"}); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 2 || rec.got[0].Outcome != audit.Success || rec.got[1].Outcome != audit.Failure || rec.got[1].Reason != "swap_pending" ||
		rec.got[0].Action != "core.restore.stage" || rec.got[0].Details["archive"] != "arch_gotham_01" || rec.got[0].Target != backup.Resource() {
		t.Fatalf("events = %+v", rec.got)
	}
}

// bwTaskStores boots a store holding only core/tasks, as the executor's own
// tests do (coretest cannot be imported from the kernel).
func bwTaskStores(t *testing.T) tasks.Stores {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:   "/batcave/main.pebble",
		Owners: []storage.OwnerSpec{{Owner: taskstore.Owner, Spec: taskstore.StorageSpec(), Enabled: true}},
		Build:  "coretest+fixture", FreeSpaceMargin: 1, FS: storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, _ := db.Keyspace(taskstore.Owner)
	return tasks.NativeStores(ks)
}

// TestBackupSvc_PlainRestoreThroughTheTaskIsRefused (I8): a restore asked for
// through core.backup.stage-restore, run by the real executor over the service
// this file builds, carries no owner proof, so it is refused whatever its
// options, and nothing reaches the swapper: not its check, not its create.
func TestBackupSvc_PlainRestoreThroughTheTaskIsRefused(t *testing.T) {
	fx := newRxFx(t, nil)
	st := &bwStager{}
	svc, err := newBackupSvc(bwInput(t, fx, st))
	if err != nil {
		t.Fatal(err)
	}
	stores := bwTaskStores(t)
	x, err := executor.New(executor.Options{
		Stores: stores, Definitions: []tasks.Definition{backup.StageRestoreTask(svc)}, InstrumentsOff: true,
		Config: executor.Config{DispatchTick: 20 * time.Millisecond, WatchdogInterval: time.Hour},
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = x.Shutdown(context.Background()) })
	if err := x.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]backup.StageParams{
		"plain":   {ArchiveID: "arch_gotham_01"},
		"foreign": {ArchiveID: "arch_gotham_01", Foreign: true},
		"restart": {ArchiveID: "arch_gotham_01", Restart: true},
	} {
		raw, _ := json.Marshal(p)
		ref, err := x.Start(context.Background(), backup.KindStageRestore, tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: raw, Trigger: tasks.TriggerManual})
		if err != nil {
			t.Fatalf("%s: start: %v", name, err)
		}
		var rec tasks.RunRecord
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if rec, err = stores.Runs.Get(context.Background(), ref.ID); err != nil {
				t.Fatal(err)
			}
			if rec.State == tasks.StateFailed || rec.State == tasks.StateSucceeded {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: run is %s", name, rec.State)
			}
		}
		if rec.State != tasks.StateFailed || !strings.Contains(rec.Error, backup.ErrRestoreNotAuthorized.Error()) {
			t.Errorf("%s: run %s, error %q; want failed with %q", name, rec.State, rec.Error, backup.ErrRestoreNotAuthorized)
		}
	}
	if st.checks != 0 || st.stages != 0 {
		t.Fatalf("an unproven restore reached the swapper: %d checks, %d creates", st.checks, st.stages)
	}
}

// TestBackupSvc_AProvenOwnerPassesTheGate is the control for the refusal
// above: the same service, with the owner's proof on the context, gets past
// the gate (here it stops at the archive it does not have), so the refusal is
// the gate's and not a service that refuses everything.
func TestBackupSvc_AProvenOwnerPassesTheGate(t *testing.T) {
	fx := newRxFx(t, nil)
	svc, err := newBackupSvc(bwInput(t, fx, &bwStager{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := backup.WithRestoreProof(context.Background(), backup.RestoreProof{Owner: true})
	_, err = svc.StageRestore(ctx, "arch_gotham_01", backup.RestoreOptions{})
	if errors.Is(err, backup.ErrRestoreNotAuthorized) || err == nil {
		t.Fatalf("StageRestore with the owner's proof = %v, want a refusal past the gate (no such archive)", err)
	}
}

// The key-started half of I8, "a key-started run gets the non-interactive
// checker (token actor)", is TestDiagnosticsViewerFor_InteractiveOnlyForSession
// in diagnostics_wire_test.go: its method=token case compiles the user's
// roles with Interactive=false and asserts core:backup.restore is refused.
// backup compiles no checker of its own.

// bwSwapHost is a state directory and a live store path for the recheck test.
type bwSwapHost struct {
	state, db, staging string
}

// bwArchiveIntent stages a KindArchive restore whose staging holds one file of
// size 3 listed in a legacy manifest, through a plain swapper, and returns the
// host. tamper changes the file's size after staging.
func bwStageArchive(t *testing.T, tamper bool) bwSwapHost {
	t.Helper()
	root := t.TempDir()
	h := bwSwapHost{state: filepath.Join(root, "state"), db: filepath.Join(root, "main.pebble")}
	for _, d := range []string{h.state, h.db} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.db, "CURRENT"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := "r-20261122-090000-0123456789ab"
	h.staging = restorejournal.StagingDir(h.db, id)
	staged := filepath.Join(h.staging, "db")
	if err := os.MkdirAll(staged, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(staged, "CURRENT")
	if err := os.WriteFile(file, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"format": "legacy-abo/0", "entries": []backup.Entry{{Path: "db/CURRENT", Size: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.staging, "legacy-manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sw, err := restoreswap.New(restoreswap.Options{StateDir: h.state, BuildID: "batcomputer-1"})
	if err != nil {
		t.Fatal(err)
	}
	in := restorejournal.Intent{
		ID: id, Kind: restorejournal.KindArchive, DBPath: h.db, Targets: []restorejournal.Target{{From: staged, To: h.db}},
		StagedByBuild: "batcomputer-1", ExpiresAt: time.Now().Add(time.Hour), RequestedBy: "alfred_pennyworth", RequestedAt: time.Now(),
		ArchiveID: "legacy-gotham", Staging: h.staging, ManifestSHA256: backup.SumManifest(raw),
	}
	if err := sw.Stage(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if tamper {
		if err := os.WriteFile(file, []byte("newer"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// TestBackupRecheck_ReachesStepB: through a real swapper's P0a, a staged
// archive whose file changed size after staging is rejected at step b
// (restore_rejected, the live store untouched) when the swapper carries the
// composed check, and is not rejected by the plain swapper P0a builds today.
// The untampered archive is not rejected by either, so the check rejects the
// change and nothing else. The fixture's store is a directory, not a Pebble
// store, so a swap that gets past step b fails later at the sidecar rewrite
// (restore_failed, swapped back): "not rejected" is all this can show, and all
// it needs to.
func TestBackupRecheck_ReachesStepB(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name                  string
		tamper, aware, reject bool
	}{
		{name: "aware swapper, untouched archive: passes step b", aware: true},
		{name: "aware swapper, tampered archive: rejected", tamper: true, aware: true, reject: true},
		{name: "plain swapper, tampered archive: passes step b", tamper: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := bwStageArchive(t, c.tamper)
			o := restoreswap.Options{StateDir: h.state, BuildID: "batcomputer-1"}
			var sw *restoreswap.Swapper
			var err error
			if c.aware {
				sw, err = newBackupSwapper(o, func() bool { return false })
			} else {
				sw, err = restoreswap.New(o)
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := sw.RunP0a(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if rejected := out.Degraded == restoreswap.DegradedRestoreRejected; rejected != c.reject {
				t.Fatalf("boot = %v, degraded %q (%s); rejected at step b = %v, want %v", out.Boot, out.Degraded, out.Reason, rejected, c.reject)
			}
			cur, _ := os.ReadFile(filepath.Join(h.db, "CURRENT"))
			if c.reject && string(cur) != "old" {
				t.Fatalf("a rejected restore touched the live store: CURRENT = %q", cur)
			}
		})
	}
}

// TestBackupRecheck_RunsTheDefaultAndTheArchiveCheck: both halves run, the
// swapper's default first, and either one's refusal refuses the restore
// without the other being needed.
func TestBackupRecheck_RunsTheDefaultAndTheArchiveCheck(t *testing.T) {
	ctx := context.Background()
	errDefault := errors.New("the rename pre-check refuses")
	var calls []string
	check := backupRecheck(func(context.Context, restorejournal.Intent) error {
		calls = append(calls, "default")
		return errDefault
	}, nil)
	if err := check(ctx, restorejournal.Intent{}); !errors.Is(err, errDefault) || !slices.Equal(calls, []string{"default"}) {
		t.Fatalf("a refused default: err %v, calls %v", err, calls)
	}

	// With the default passing, the archive half runs: a KindArchive intent
	// naming no staging directory has nothing to read, and is refused.
	check = backupRecheck(func(context.Context, restorejournal.Intent) error { return nil }, nil)
	if err := check(ctx, restorejournal.Intent{Kind: restorejournal.KindArchive}); err == nil {
		t.Fatal("an archive intent with no manifest passed the composed check")
	}
}

// TestMountBackup: a nil service mounts nothing; a built one mounts core
// backup's routes.
func TestMountBackup(t *testing.T) {
	r := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{Log: slog.New(slog.DiscardHandler)})
	mountBackup(r.CoreGroup(), backupMount{})
	if n := countRoutes(r); n != 0 {
		t.Fatalf("a nil service mounted %d routes", n)
	}

	fx := newRxFx(t, nil)
	svc, err := newBackupSvc(bwInput(t, fx, &bwStager{}))
	if err != nil {
		t.Fatal(err)
	}
	mountBackup(r.CoreGroup(), backupMount{Svc: svc})
	var names []string
	for ri := range r.Routes() {
		names = append(names, ri.Method+" "+ri.Pattern)
	}
	for _, want := range []string{"GET /api/core/v1/backups", "POST /api/core/v1/backups"} {
		if !slices.Contains(names, want) {
			t.Errorf("%s was not mounted; routes: %q", want, names)
		}
	}
}

// TestRegisterBackupTasks: a built service records the four core.backup.* kinds
// on the registrar, in that order, with no problem; a nil one records none.
func TestRegisterBackupTasks(t *testing.T) {
	newReg := func() *module.Registrar {
		return module.NewRegistrar(module.RegistrarConfig{Deps: module.Deps{Module: "core"}})
	}
	r := newReg()
	registerBackupTasks(r, nil)
	if got := r.Close().Tasks; len(got) != 0 {
		t.Fatalf("a nil service recorded %d definitions", len(got))
	}

	fx := newRxFx(t, nil)
	svc, err := newBackupSvc(bwInput(t, fx, &bwStager{}))
	if err != nil {
		t.Fatal(err)
	}
	r = newReg()
	registerBackupTasks(r, svc)
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	var kinds []tasks.Kind
	for _, d := range r.Close().Tasks {
		kinds = append(kinds, d.Kind())
	}
	want := []tasks.Kind{backup.KindCreate, backup.KindVerify, backup.KindPrune, backup.KindStageRestore}
	if !slices.Equal(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
}

func countRoutes(r *httpapi.Router) int {
	n := 0
	for range r.Routes() {
		n++
	}
	return n
}
