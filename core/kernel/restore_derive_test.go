// file: core/kernel/restore_derive_test.go
// version: 0.1.0
// guid: b3f81d57-2c49-4e06-9a7d-5e1c0f84a692
// last-edited: 2026-11-21

package kernel

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

const (
	deriveArchive = "01JBATMAN0000000000000000A"
	deriveInstall = "wayne-install"
)

// archiveSource is the staged source of one archive restore the tests build: the
// manifest signed (or not) by the live keyring, the intent that names it.
type archiveSource struct {
	manifest *backup.Manifest
	// sign says whether the manifest is signed with the install's derivation key.
	sign bool
	// tamper edits the manifest after it is signed, so its MAC no longer verifies.
	tamper func(*backup.Manifest)
	// legacy stages HEAD's legacy-manifest.json in the manifest's place.
	legacy bool
	// noManifestHash leaves ManifestSHA256 out of the intent.
	noManifestHash bool
	// intentInstall is the install ID the stager read from the store it replaced
	// ("" = a host loss).
	intentInstall string
}

func newArchiveSource() archiveSource {
	return archiveSource{
		sign: true, intentInstall: "",
		manifest: &backup.Manifest{
			Format: backup.FormatMediacore, ArchiveID: deriveArchive, App: "gotham", AppVersion: "1", CoreVersion: "1",
			InstallID: "", CreatedAt: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC), Reason: "manual",
			JournalSeq: 4, AuditHighWater: 7, PebbleFormat: 1,
			Keyspaces: map[string]backup.KSInfo{}, Modules: map[string]string{}, Levels: map[string]int{},
		},
	}
}

// stageArchive stages src as restore id over h's store and returns the intent.
// The install ID of the live store is read from k (a booted kernel of h).
func stageArchive(t *testing.T, h *swapHost, k *Kernel, id string, src archiveSource) restorejournal.Intent {
	t.Helper()
	install := k.store.db.Admin().InstallID()
	staged := h.stagedCopy(id, "robin")
	staging := restorejournal.StagingDir(h.db, id)
	m := *src.manifest
	m.InstallID = install
	if src.intentInstall == "live" {
		src.intentInstall = install
	}
	if src.sign {
		if err := backup.Sign(&m, k.sec.store); err != nil {
			t.Fatal(err)
		}
	}
	if src.tamper != nil {
		src.tamper(&m)
	}
	raw, err := backup.MarshalManifest(&m)
	if err != nil {
		t.Fatal(err)
	}
	name := backup.ManifestName
	if src.legacy {
		name = "legacy-manifest.json"
		raw = []byte(`{"entries":[]}`)
	}
	if err := os.WriteFile(filepath.Join(staging, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	in := restorejournal.Intent{
		ID: id, Kind: restorejournal.KindArchive, DBPath: h.db, Targets: []restorejournal.Target{{From: staged, To: h.db}},
		StagedByBuild: swapBuild, ExpiresAt: time.Now().Add(time.Hour), RequestedBy: "alfred", RequestedAt: time.Now(),
		ArchiveID: deriveArchive, Staging: staging, InstallID: src.intentInstall,
	}
	if !src.noManifestHash {
		in.ManifestSHA256 = backup.SumManifest(raw)
	}
	sw, err := restoreswap.New(restoreswap.Options{StateDir: h.state, BuildID: swapBuild})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Stage(h.ctx, in); err != nil {
		t.Fatal(err)
	}
	return in
}

// restoreHost is an identity host whose store has booted once and is closed: the
// state a restore is staged over.
func restoreHost(t *testing.T) (h *swapHost, c Config, first *Kernel) {
	t.Helper()
	h, c = identityHost(t)
	first = h.mustBoot(c)
	// The kernel's stores stay readable after close: the install ID and the keyring
	// handle are what the manifest is signed with.
	return h, c, first
}

// derive boots the restore with the switch off (P4b journals applied with
// HooksOwed, the same journal the identity boot meets at P4d) and derives its
// restoreBoot from the kernel that holds the outcome.
func derive(t *testing.T, h *swapHost, c Config) (*restoreBoot, error) {
	t.Helper()
	c.IdentityBoot = false
	k := h.mustBoot(c)
	return k.deriveRestoreBoot(h.ctx)
}

// TestDeriveRestoreBoot_ArchiveReadsTheStagedManifest: the source's boundary and
// audit high-water mark come from the staged manifest, its provenance from the
// manifest's MAC and install, the pre-swap store from the set-aside, and the
// restore is Owed (P4b journaled applied before P4d, so there is no edge to walk).
func TestDeriveRestoreBoot_ArchiveReadsTheStagedManifest(t *testing.T) {
	h, c, first := restoreHost(t)
	src := newArchiveSource()
	src.intentInstall = "live"
	stageArchive(t, h, first, "r-derive-own", src)
	h.close(first)

	rb, err := derive(t, h, c)
	if err != nil {
		t.Fatal(err)
	}
	if rb.ID != "r-derive-own" || rb.Kind != journal.KindArchive || rb.ArchiveID != deriveArchive {
		t.Errorf("identity of the restore = %q %v %q", rb.ID, rb.Kind, rb.ArchiveID)
	}
	if rb.Foreign {
		t.Error("an archive of this install, signed under its key, is foreign")
	}
	if rb.SourceSeq != 4 || rb.AuditHighWater != 7 {
		t.Errorf("source seq %d, audit high water %d, want the manifest's 4 and 7", rb.SourceSeq, rb.AuditHighWater)
	}
	if !rb.Owed || rb.Steps != nil {
		t.Errorf("Owed=%v Steps=%v: P4b already journaled applied, so there are no edges to walk", rb.Owed, rb.Steps)
	}
	want := restorejournal.SetAsideDest(h.state, h.db, "r-derive-own", h.db)
	if rb.PreSwapDir != want {
		t.Errorf("PreSwapDir = %q, want %q", rb.PreSwapDir, want)
	}
	if len(rb.Records) == 0 || rb.Records[0].Step != restoreswap.StepStaged {
		t.Errorf("records = %+v, want the restore journal from its staged record", rb.Records)
	}
}

// TestDeriveRestoreBoot_ForeignWhenProvenanceDoesNotHold: an archive whose MAC
// does not verify, that carries none, that names another install than the one
// the stager read, or that is HEAD's legacy format is foreign: no coverage.
func TestDeriveRestoreBoot_ForeignWhenProvenanceDoesNotHold(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*archiveSource)
		seq  uint64
		hw   uint64
	}{
		{"a manifest edited after it was signed", func(s *archiveSource) {
			s.intentInstall = "live"
			s.tamper = func(m *backup.Manifest) { m.JournalSeq = 1 }
		}, 1, 7},
		{"an unsigned manifest", func(s *archiveSource) { s.intentInstall = "live"; s.sign = false }, 4, 7},
		{"another install than the stager read", func(s *archiveSource) { s.intentInstall = "joker-install" }, 4, 7},
		{"a legacy archive", func(s *archiveSource) { s.intentInstall = "live"; s.legacy = true }, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c, first := restoreHost(t)
			src := newArchiveSource()
			tc.edit(&src)
			stageArchive(t, h, first, "r-derive-foreign", src)
			h.close(first)

			rb, err := derive(t, h, c)
			if err != nil {
				t.Fatal(err)
			}
			if !rb.Foreign {
				t.Error("the restore is not foreign")
			}
			if rb.SourceSeq != tc.seq || rb.AuditHighWater != tc.hw {
				t.Errorf("source seq %d, audit high water %d, want %d and %d", rb.SourceSeq, rb.AuditHighWater, tc.seq, tc.hw)
			}
		})
	}
}

// TestDeriveRestoreBoot_RefusesASourceItCannotRead: no staged manifest, one that
// does not hash to the intent's, or an intent that names none refuses with
// errRestoreSourceUnreadable, never a guessed boundary.
func TestDeriveRestoreBoot_RefusesASourceItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(t *testing.T, h *swapHost, in restorejournal.Intent)
		src  func(*archiveSource)
	}{
		{"no manifest in staging", func(t *testing.T, h *swapHost, in restorejournal.Intent) {
			if err := os.Remove(filepath.Join(in.Staging, backup.ManifestName)); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"a manifest that is not the one the intent hashed", func(t *testing.T, h *swapHost, in restorejournal.Intent) {
			if err := os.WriteFile(filepath.Join(in.Staging, backup.ManifestName), []byte(`{"format":"mediacore-backup/1"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"an intent that names no manifest hash", nil, func(s *archiveSource) { s.noManifestHash = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c, first := restoreHost(t)
			src := newArchiveSource()
			src.intentInstall = "live"
			if tc.src != nil {
				tc.src(&src)
			}
			in := stageArchive(t, h, first, "r-derive-refuse", src)
			h.close(first)
			if tc.edit != nil {
				tc.edit(t, h, in)
			}
			_, err := derive(t, h, c)
			if !errors.Is(err, errRestoreSourceUnreadable) {
				t.Fatalf("derive = %v, want errRestoreSourceUnreadable", err)
			}
		})
	}
}

// TestDeriveRestoreBoot_RevertTakesTheStoresOwnHead: a revert has no manifest; the
// swapped-in store is a former live store, so its boundary is its own journal head
// (SourceSeq is left unbounded for min(SourceSeq, head)), and it is never foreign.
func TestDeriveRestoreBoot_RevertTakesTheStoresOwnHead(t *testing.T) {
	h, c, first := restoreHost(t)
	h.close(first)
	h.stage("r-derive-revert", restorejournal.KindRevert, h.stagedCopy("r-derive-revert", "robin"))
	rb, err := derive(t, h, c)
	if err != nil {
		t.Fatal(err)
	}
	if rb.Kind != journal.KindRevert || rb.ArchiveID != "" || rb.Foreign {
		t.Errorf("revert restore = %v %q foreign=%v", rb.Kind, rb.ArchiveID, rb.Foreign)
	}
	if rb.SourceSeq != math.MaxUint64 {
		t.Errorf("SourceSeq = %d, want unbounded (the store's own head bounds it)", rb.SourceSeq)
	}
	if rb.AuditHighWater != 0 {
		t.Errorf("audit high water = %d, want 0 for a revert", rb.AuditHighWater)
	}
}

// TestIdentityBoot_ARestoreBootRunsItsHooksAtTheSourceBoundary: an identity boot
// that is also a restore boot serves, appends the restore line at
// min(the manifest's journal seq, the store's head) and runs the hooks once, and
// the restore journal reads applied. The same restore with the switch off runs no
// hook (TestRestoreBootWithoutHooksReachesApplied).
func TestIdentityBoot_ARestoreBootRunsItsHooksAtTheSourceBoundary(t *testing.T) {
	h, c, first := restoreHost(t)
	// A revocation, so the store's head is above the manifest's journal seq of 1.
	actor := &auth.Principal{Method: auth.MethodSession, Perms: holdsAll{}, User: &auth.User{ID: "u-alfred", Username: "alfred"}}
	u, err := first.ident.svc.Auth.CreateUser(t.Context(), actor, auth.NewUser{Username: "lucius", Password: fixturePassword})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ident.svc.Auth.SetStatus(t.Context(), actor, u.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	// An admin holder: with the pre-swap store open the hooks take the most
	// restrictive of both and list nobody; with none (no PreSwapDir) every admin
	// holder is listed for review.
	if _, err := first.ident.svc.Auth.CreateUser(t.Context(), actor, auth.NewUser{Username: "oracle", Password: fixturePassword, Roles: []rbac.RoleName{rbac.RoleAdmin}}); err != nil {
		t.Fatal(err)
	}
	src := newArchiveSource()
	src.intentInstall = "live"
	src.manifest.JournalSeq = 1
	stageArchive(t, h, first, "r-identity-restore", src)
	h.close(first)

	k, err := h.boot(c)
	if err != nil {
		t.Fatalf("an identity restore boot did not serve: %v", err)
	}
	if !k.ident.p4d.Hooks.Ran {
		t.Error("the restore hooks did not run")
	}
	if got := k.ident.p4d.Hooks.Report.AdminsToReview; len(got) != 0 {
		t.Errorf("admins to review = %v: the hooks did not open the pre-swap store the derivation found", got)
	}
	if got := lastStepOf(h.journal("r-identity-restore")); got != restoreswap.StepApplied {
		t.Errorf("restore journal ends at %q, want applied", got)
	}
	chain, err := journal.ReadFile(k.ident.journal.Path(), k.sec.store)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, l := range chain.Lines {
		if info, ok := l.RestoreInfo(); ok && info.RestoreID == "r-identity-restore" {
			found = true
			if info.Boundary != 1 || info.Kind != journal.RestoreArchive || info.Source != deriveArchive {
				t.Errorf("restore line = %+v, want archive %s at boundary 1", info, deriveArchive)
			}
		}
	}
	if !found {
		t.Error("no restore line for the restore in the revocation journal")
	}
}

func lastStepOf(recs []restorejournal.Record) string {
	if len(recs) == 0 {
		return ""
	}
	return recs[len(recs)-1].Step
}

// TestDeriveRestoreBoot_CheckpointTakesTheRecordsJournalSeq: a checkpoint restore's
// boundary is the journal seq its record carries, found by the name of the
// checkpoint directory the intent's store target lies in; a checkpoint with no
// record refuses.
func TestDeriveRestoreBoot_CheckpointTakesTheRecordsJournalSeq(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record bool
		want   uint64
	}{{"a recorded checkpoint", true, 6}, {"a checkpoint with no record", false, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			h, c, first := restoreHost(t)
			h.close(first)
			const cpName = "20261121T000000Z-ba7c4ae1"
			cpDir := filepath.Join(filepath.Dir(h.db), ".mediacore-checkpoints")
			staged := filepath.Join(cpDir, cpName, "db")
			copyTree(t, h.db, staged)
			if tc.record {
				rec := `{"install_id":"wayne-install","epoch":1,"journal_seq":6,"status":"open","taken_at":"2026-11-21T00:00:00Z"}`
				if err := os.WriteFile(filepath.Join(cpDir, cpName+".json"), []byte(rec), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			h.stage("r-derive-checkpoint", restorejournal.KindCheckpoint, staged)
			rb, err := derive(t, h, c)
			if !tc.record {
				if !errors.Is(err, errRestoreSourceUnreadable) {
					t.Fatalf("derive = %v, want errRestoreSourceUnreadable", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if rb.Kind != journal.KindCheckpoint || rb.SourceSeq != tc.want || rb.Foreign {
				t.Errorf("checkpoint restore = %v seq %d foreign=%v, want seq %d", rb.Kind, rb.SourceSeq, rb.Foreign, tc.want)
			}
		})
	}
}

// TestIdentityBoot_ARefusedRestoreIsNotServedByTheNextBoot: a checkpoint restore
// whose record is gone refuses the identity boot at P4d (errRestoreSourceUnreadable),
// the restore having been swapped in and journaled applied at P4b. The swapped-in
// store has lost a revocation the journal holds. The next boot must not serve it
// with that revocation undone: it either refuses again or re-applies it.
func TestIdentityBoot_ARefusedRestoreIsNotServedByTheNextBoot(t *testing.T) {
	h, c, first := restoreHost(t)
	actor := &auth.Principal{Method: auth.MethodSession, Perms: holdsAll{}, User: &auth.User{ID: "u-alfred", Username: "alfred"}}
	u, err := first.ident.svc.Auth.CreateUser(t.Context(), actor, auth.NewUser{Username: "lucius", Password: fixturePassword})
	if err != nil {
		t.Fatal(err)
	}
	h.close(first)
	const cpName = "20261121T000000Z-ba7c4ae1"
	cpDir := filepath.Join(filepath.Dir(h.db), ".mediacore-checkpoints")
	staged := filepath.Join(cpDir, cpName, "db")
	copyTree(t, h.db, staged) // lucius is active in the copy

	later := h.mustBoot(c)
	if err := later.ident.svc.Auth.SetStatus(t.Context(), actor, u.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	h.close(later)
	h.stage("r-refused", restorejournal.KindCheckpoint, staged)

	if _, err := h.boot(c); !errors.Is(err, errRestoreSourceUnreadable) || !errors.Is(err, errP4dRefuse) {
		t.Fatalf("first boot = %v, want errRestoreSourceUnreadable under errP4dRefuse", err)
	}
	k, err := h.boot(c)
	if err != nil {
		t.Logf("second boot refuses too: %v", err)
		return
	}
	defer h.close(k)
	got, err := k.ident.svc.Auth.User(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != auth.StatusDisabled {
		t.Fatalf("second boot serves the refused restore with lucius %q: the disable the restore lost is undone", got.Status)
	}
}
