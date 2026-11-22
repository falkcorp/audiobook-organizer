// file: core/kernel/backup_inspector_proof_test.go
// version: 0.1.0
// guid: 5c8e1d37-92a4-4b60-b7f3-1d04e6a9c825
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The proof that the Inspector the G2 adapter builds (backupWireInput) opens an
// archive a post-flip binary took. The adapter builds StorageInspector.Base by
// hand, from k.storageOwners(), the binary ID and FromRelease/ClaimsShadow, and
// leaves out what storageOptions sets for the live open (DataSteps,
// ExtendStart, JournalHead): it cannot call storageOptions, whose dataSteps
// reassigns the runner P4a built. Every other test of the adapter builds its
// Inspector by hand, so none of them could see a Base that refuses the flip's
// own archives. These tests boot the kernel as the flip does, take the backup
// through the Svc newBackup built, and hand the archive back to that Svc's own
// Inspector.
//
// The fixture is the flip's shape in miniature: the identity owners (the app
// declares identity), core/levels (a manifest declares a cutover, so core is
// stamped at level 1), a module at level 1 whose level step ran and whose
// cutover data migration is done, and an UpgradeFloor on that module that
// requires the cutover (the one input a read-only open reads for a data step
// it was not handed).

const (
	flipCutoverID = "alfred.gotham-cutover"
	flipTag       = "v0.9.0-arkham"
)

// flipModule is the module a flip build declares: a keyspace, a legacy family
// its cutover retires, level steps up to level, and a floor that requires the
// cutover. level 1 is the build under test; level 2 is the next release.
func flipModule(level int) testModule {
	migs := []storage.Migration{seedStep(1, "cave"), storage.LevelStep("alfred", 2, 1)}
	if level >= 2 {
		migs = append(migs, storage.LevelStep("alfred", 3, 2))
	}
	return storageMod("alfred", nil, migs...).with(func(m *module.Manifest) {
		m.Storage.Level = level
		m.Storage.Claims = []storage.Claim{{Prefix: "case:", Mode: storage.ClaimRetired, Description: "the GCPD's case files (a synthetic legacy family)"}}
		m.Storage.UpgradeFloor = &storage.UpgradeFloor{
			Owner: "alfred", Version: 2, InstallFirst: flipTag,
			Requires: []storage.StepRef{{DataMigration: flipCutoverID}},
		}
		m.Cutover = noRowsCutover("alfred")
	})
}

// flipBackup is a kernel booted as the flip boots, and the backup service
// newBackup built over its handles.
type flipBackup struct {
	h   *swapHost
	k   *Kernel
	in  backupWireIn
	svc *backup.Svc
	ins *backup.StorageInspector
	ctx context.Context
}

// bootFlipBackup boots the flip fixture with the identity boot declared (the
// owners come from the kernel's own storageOwners, never a hand list) and
// builds core backup over it through k.backupWireInput, the adapter under test.
func bootFlipBackup(t *testing.T, level int, extra ...module.Module) *flipBackup {
	t.Helper()
	return bootFlipBackupWith(t, func(c *Config) {
		c.Modules = append([]module.Module{flipModule(level)}, extra...)
	})
}

func bootFlipBackupWith(t *testing.T, mut func(*Config)) *flipBackup {
	t.Helper()
	h := newSwapHost(t)
	c := h.envConfig(nil)
	c.IdentityBoot = true
	mut(&c)
	k := h.mustBoot(c)
	in, err := k.backupWireInput(h.ctx)
	if err != nil {
		t.Fatalf("backupWireInput on a flip boot: %v", err)
	}
	svc, err := newBackupSvc(in)
	if err != nil {
		t.Fatalf("newBackupSvc: %v", err)
	}
	ins, ok := in.Inspector.(*backup.StorageInspector)
	if !ok {
		t.Fatalf("the adapter's Inspector is a %T, want *backup.StorageInspector", in.Inspector)
	}
	return &flipBackup{h: h, k: k, in: in, svc: svc, ins: ins, ctx: h.ctx}
}

// owned is the context of an owner who passed the route's guards.
func (f *flipBackup) owned() context.Context {
	return backup.WithRestoreProof(f.ctx, backup.RestoreProof{Owner: true, Admin: true})
}

// create takes a backup through the Svc, as core.backup.create would.
func (f *flipBackup) create(t *testing.T) backup.Archive {
	t.Helper()
	a, err := f.svc.Create(f.ctx, backup.CreateOptions{Reason: "manual"})
	if err != nil {
		t.Fatalf("Create on a flip boot: %v", err)
	}
	return a
}

// stagedStore is the db directory of the one staged restore f holds: the
// extracted archive the Svc inspected at step 5.
func (f *flipBackup) stagedStore(t *testing.T) string {
	t.Helper()
	st := f.h.slot()
	if st.Pending == nil {
		t.Fatalf("slot = %+v, want a staged restore", st)
	}
	return filepath.Join(restorejournal.StagingDir(f.h.db, st.Pending.ID), "db")
}

// keyspaceOwners are the owners a count of the store reads: every enabled
// owner with a keyspace, which is what Inspect counts and a manifest records.
func keyspaceOwners(owners []storage.OwnerSpec) []string {
	var out []string
	for _, o := range owners {
		if o.Spec.Keyspace && o.Enabled {
			out = append(out, string(o.Owner))
		}
	}
	slices.Sort(out)
	return out
}

// TestBackupInspector_TheFlipBootProducesAnAcceptedArchive: the flip's own boot,
// its core backup Svc, and the Inspector the adapter built over the archive:
// accepted at the open level, accepted again over the extracted store, with the
// flip's levels and every owner counted, and a restore stage gets past
// inspection and reaches the swapper's intent.
func TestBackupInspector_TheFlipBootProducesAnAcceptedArchive(t *testing.T) {
	f := bootFlipBackup(t, 1)

	// The fixture must keep the flip's shape, or the proof proves nothing.
	owners := f.k.storageOwners()
	ids := ownerIDs(f.k)
	for _, want := range []storage.Owner{coreLevelsOwner, "alfred", "core/audit", "core/tasks", configOwnerSpec().Owner, secretsOwnerID} {
		if !slices.Contains(ids, want) {
			t.Fatalf("the flip fixture has no %s owner: %v", want, ids)
		}
	}
	if !f.k.identityBootDeclared() || len(ids) < 9 {
		t.Fatalf("the flip fixture did not declare identity: %v", ids)
	}
	if s, sup := f.k.store.db.Levels(); s["core"] != CoreCutoverLevel || s["alfred"] != 1 || sup["alfred"] != 1 {
		t.Fatalf("the flip fixture's levels: stamps %v, supported %v", s, sup)
	}

	a := f.create(t)
	if a.Verified == nil {
		t.Fatalf("a verify-after-create archive carries no verification: %+v", a)
	}

	// The open level: the archive extracted into scratch and opened read-only
	// by the Inspector, then matched to the manifest's levels and key counts.
	res, err := f.svc.Verify(f.ctx, a.ID, backup.VerifyOpen)
	if err != nil || !res.OK {
		t.Fatalf("Verify(open) of the flip's own archive = %+v, %v; want OK", res, err)
	}

	// A restore stage: owner proof, the real swapper, nothing mocked. It must
	// get past step 5 (the Inspector's open of the staged store) and create
	// the intent.
	pr, err := f.svc.StageRestore(f.owned(), a.ID, backup.RestoreOptions{})
	if err != nil {
		t.Fatalf("StageRestore of the flip's own archive: %v", err)
	}
	if pr.ArchiveID != a.ID || pr.StagedByBuild != f.in.BuildID {
		t.Fatalf("PendingRestore = %+v, want archive %s staged by build %s", pr, a.ID, f.in.BuildID)
	}
	if st := f.h.slot(); st.Pending == nil {
		t.Fatalf("slot = %+v, want a staged restore", st)
	}

	// The Inspector over the extracted archive, called directly.
	info, err := f.ins.Inspect(f.ctx, f.stagedStore(t))
	if err != nil {
		t.Fatalf("Inspect of the staged flip archive: %v", err)
	}
	if info.Levels["core"] != CoreCutoverLevel || info.Levels["alfred"] != 1 {
		t.Errorf("Inspect levels = %v, want core %d and alfred 1", info.Levels, CoreCutoverLevel)
	}
	if info.InstallID == "" {
		t.Error("Inspect read no install marker")
	}
	// Every enabled keyspace owner of the kernel is counted; an owner the base
	// lacks would count nothing (and a manifest would record none).
	var counted []string
	for o := range info.Keys {
		counted = append(counted, o)
	}
	slices.Sort(counted)
	if want := keyspaceOwners(owners); !slices.Equal(counted, want) {
		t.Errorf("Inspect counted %v, want every kernel keyspace owner %v", counted, want)
	}
	if info.Keys["alfred"] < 1 {
		t.Errorf("Inspect counted %d alfred keys, want the seeded row", info.Keys["alfred"])
	}
}

// TestBackupInspector_BaseCarriesWhatTheLiveOpenDid: the fields of Base the
// read-only open does not act on (the binary ID, the claims dial and the
// release tag only matter to a store that is written) are still the live
// open's, so an Inspector one day given a write path, or a later check on
// them, cannot disagree with the open that made the archive. The oracles are
// the kernel's own state, not a copy of the adapter's expression.
func TestBackupInspector_BaseCarriesWhatTheLiveOpenDid(t *testing.T) {
	for _, shadow := range []bool{false, true} {
		t.Run(map[bool]string{false: "strict", true: "shadow"}[shadow], func(t *testing.T) {
			f := bootFlipBackupWith(t, func(c *Config) {
				c.Modules = []module.Module{flipModule(1)}
				c.StorageClaimsShadow, c.FromRelease = shadow, flipTag
			})
			b := f.ins.Base
			if b.ClaimsShadow != f.k.store.db.ClaimsShadow() || b.ClaimsShadow != shadow {
				t.Errorf("Base.ClaimsShadow = %v; the live store runs shadow=%v (configured %v)", b.ClaimsShadow, f.k.store.db.ClaimsShadow(), shadow)
			}
			if b.Build != swapBuild || b.Build != f.in.BuildID {
				t.Errorf("Base.Build = %q, want the binary ID %q the swap and the service use", b.Build, swapBuild)
			}
			if b.FromRelease != flipTag {
				t.Errorf("Base.FromRelease = %q, want %q", b.FromRelease, flipTag)
			}
			if b.FS != f.k.cfg.StorageFS {
				t.Errorf("Base.FS is not the kernel's filesystem")
			}
			// The owners are the kernel's, in the kernel's order.
			var got, want []string
			for _, o := range b.Owners {
				got = append(got, string(o.Owner)+"/"+boolWord(o.Enabled))
			}
			for _, o := range f.k.storageOwners() {
				want = append(want, string(o.Owner)+"/"+boolWord(o.Enabled))
			}
			if !slices.Equal(got, want) {
				t.Errorf("Base.Owners = %v, want the kernel's storageOwners %v", got, want)
			}
		})
	}
}

func boolWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// TestBackupInspector_ABaseThatLacksAnOwnerRefusesTheFlipArchive is the
// negative twin: the Inspector over the same archive, with core/levels dropped
// from its Base, refuses the store (core is stamped at 1 and the base serves
// 0). That is the refusal every flip archive would meet at the fold if
// backupWireInput lost an owner, so it is the shape the positive test above
// must not show.
func TestBackupInspector_ABaseThatLacksAnOwnerRefusesTheFlipArchive(t *testing.T) {
	f := bootFlipBackup(t, 1)
	a := f.create(t)
	if _, err := f.svc.StageRestore(f.owned(), a.ID, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	dir := f.stagedStore(t)

	short := *f.ins
	short.Base.Owners = slices.DeleteFunc(slices.Clone(f.ins.Base.Owners), func(o storage.OwnerSpec) bool { return o.Owner == coreLevelsOwner })
	_, err := short.Inspect(f.ctx, dir)
	if _, ok := errors.AsType[*storage.LevelTooNewError](err); !ok {
		t.Fatalf("Inspect with core/levels dropped = %v, want a *storage.LevelTooNewError", err)
	}
	if _, err := f.ins.Inspect(f.ctx, dir); err != nil {
		t.Fatalf("the adapter's own Inspector refuses what it staged: %v", err)
	}
}

// bootNewer boots the next release of the flip fixture (alfred at level 2,
// plus the extra modules) on its own host and takes a backup of it, then
// copies that archive and its sidecars into f's backup directory, as an
// operator importing an archive from another install would.
func (f *flipBackup) importFrom(t *testing.T, n *flipBackup) backup.Archive {
	t.Helper()
	a := n.create(t)
	entries, err := os.ReadDir(n.in.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), a.Name) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(n.in.Dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.in.Dir, e.Name()), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

// TestBackupInspector_ANewerBuildsArchiveIsRefusedAsTooNew: an archive taken by
// the next release (alfred's level 2) is refused by this build's service with
// the sentinel backuphttp maps (ErrBackupTooNew), and the Inspector alone
// refuses its extracted store.
func TestBackupInspector_ANewerBuildsArchiveIsRefusedAsTooNew(t *testing.T) {
	f := bootFlipBackup(t, 1)
	n := bootFlipBackup(t, 2)
	na := f.importFrom(t, n)

	res, err := f.svc.Verify(f.ctx, na.ID, backup.VerifyOpen)
	if res.OK || res.Reason != backup.VerifyReasonNeedsNewerBuild || !errors.Is(err, backup.ErrBackupTooNew) {
		t.Fatalf("Verify(open) of a newer build's archive = %+v, %v; want %s and ErrBackupTooNew", res, err, backup.VerifyReasonNeedsNewerBuild)
	}
	_, err = f.svc.StageRestore(f.owned(), na.ID, backup.RestoreOptions{Foreign: true})
	if !errors.Is(err, backup.ErrBackupTooNew) {
		t.Fatalf("StageRestore of a newer build's archive = %v, want backup.ErrBackupTooNew", err)
	}
	if st := f.h.slot(); st.Busy() {
		t.Fatalf("a refused restore left %+v in the slot", st)
	}

	// The same store, extracted by its own service, and the older build's
	// Inspector over it.
	if _, err := n.svc.StageRestore(n.owned(), na.ID, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err = f.ins.Inspect(f.ctx, n.stagedStore(t))
	if _, ok := errors.AsType[*storage.LevelTooNewError](err); !ok {
		t.Fatalf("Inspect of a store at alfred level 2 = %v, want a *storage.LevelTooNewError", err)
	}
}

// TestBackupInspector_AnOwnerTheBaseDoesNotKnowIsRefused: an archive that
// carries keys of an owner this build does not declare is refused at the open
// level, as a manifest mismatch (the Inspector counts the base's owners only, so
// the owner holds "0 keys" where the manifest says its count). That is the
// refusal a Base that lost an owner would give for that owner's own archives,
// and it is the sentinel backuphttp maps to archive_verification_failed.
//
// It is not what compat's "restored dormant" warning describes: that warning
// is reachable only for an owner with no keys, because verification runs
// first. The test pins what happens; core/backup is not changed here.
func TestBackupInspector_AnOwnerTheBaseDoesNotKnowIsRefused(t *testing.T) {
	f := bootFlipBackup(t, 1)
	robin := storageMod("robin", nil, seedStep(1, "nest"))
	n := bootFlipBackup(t, 1, robin)
	na := f.importFrom(t, n)

	res, err := f.svc.Verify(f.ctx, na.ID, backup.VerifyOpen)
	if !errors.Is(err, backup.ErrManifest) || res.OK || res.Reason != backup.VerifyReasonManifestMismatch {
		t.Fatalf("Verify(open) of an archive with an owner this build lacks = %+v, %v; want ErrManifest and %s", res, err, backup.VerifyReasonManifestMismatch)
	}
	_, err = f.svc.StageRestore(f.owned(), na.ID, backup.RestoreOptions{Foreign: true})
	if !errors.Is(err, backup.ErrManifest) || !strings.Contains(err.Error(), "robin") {
		t.Fatalf("StageRestore of an archive with an owner this build lacks = %v, want ErrManifest naming robin", err)
	}
	if st := f.h.slot(); st.Busy() {
		t.Fatalf("a refused restore left %+v in the slot", st)
	}
}
