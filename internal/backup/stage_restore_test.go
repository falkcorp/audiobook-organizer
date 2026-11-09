// file: internal/backup/stage_restore_test.go
// version: 1.0.0
// guid: e3e5dbad-e027-4168-8373-986dda885db4
// last-edited: 2026-11-09

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// stageFixture is a live install for StageRestore: a state directory, the
// live store at <parent>/wayne.pebble holding one file, and a backup
// directory holding an archive of another store (with its sidecar).
type stageFixture struct {
	state, db, backups string
	info               *BackupInfo
	verified           []string // the directories VerifyStore was asked to open
	beforeStage        []string // the paths BeforeStage was called with
}

func newStageFixture(t *testing.T) *stageFixture {
	t.Helper()
	f := &stageFixture{state: t.TempDir(), backups: t.TempDir()}
	f.db = filepath.Join(t.TempDir(), "wayne.pebble")
	if err := os.MkdirAll(f.db, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.db, "CURRENT"), []byte("the live store\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := CreateBackup(gothamStore(t), "pebble", BackupConfig{BackupDir: f.backups, MaxBackups: 5})
	if err != nil {
		t.Fatal(err)
	}
	f.info = info
	return f
}

// opts is StageRestore's options over f; VerifyStore and BeforeStage record
// their arguments.
func (f *stageFixture) opts() StageRestoreOptions {
	return StageRestoreOptions{
		BackupDir: f.backups, Filename: filepath.Base(f.info.Path),
		StateDir: f.state, DBPath: f.db, BuildID: "batcomputer-r1", InstallID: "wayne-install",
		RequestedBy: "user:alfred", LegacySuffixes: []string{".storage-format"},
		VerifyStore: func(_ context.Context, dir string) error { f.verified = append(f.verified, dir); return nil },
		BeforeStage: func(db string) error {
			if len(f.verified) == 0 {
				return errors.New("BeforeStage ran before the extracted store was opened")
			}
			f.beforeStage = append(f.beforeStage, db)
			return nil
		},
		Now: func() time.Time { return time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC) },
	}
}

// leftovers lists what a refused stage must not leave: entries of the
// swap's private directory beside the store, and journal directories in the
// slot other than keep.
func (f *stageFixture) leftovers(t *testing.T, keep string) []string {
	t.Helper()
	var out []string
	aside, _ := os.ReadDir(filepath.Join(filepath.Dir(f.db), restorejournal.AsideDirName))
	for _, e := range aside {
		out = append(out, "aside/"+e.Name())
	}
	slot, _ := os.ReadDir(restorejournal.Dir(f.state))
	for _, e := range slot {
		if e.IsDir() && e.Name() != keep {
			out = append(out, "restore/"+e.Name())
		}
	}
	return out
}

// otherIntent stages a competing archive restore under the fixture's slot.
func (f *stageFixture) otherIntent(t *testing.T, id string, inflight bool) {
	t.Helper()
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindArchive, DBPath: f.db,
		Targets: []restorejournal.Target{{From: restorejournal.StagingDir(f.db, id) + "/db", To: f.db}}}
	if err := restorejournal.CreateIntent(f.state, in); err != nil {
		t.Fatal(err)
	}
	if inflight {
		dir := restorejournal.JournalDir(f.state, id)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(restorejournal.PendingPath(f.state), filepath.Join(dir, "inflight.json")); err != nil {
			t.Fatal(err)
		}
	}
}

// otherFS is the OS probe with the staged store reported on another
// device: the placement's rename would fail with EXDEV.
type otherFS struct{ restorejournal.Probe }

func (p otherFS) ItemDev(path string) (uint64, error) {
	d, err := p.Probe.ItemDev(path)
	if strings.Contains(path, restorejournal.AsideDirName) {
		d++
	}
	return d, err
}

func (p otherFS) DirDev(path string) (uint64, error) {
	d, err := p.Probe.DirDev(path)
	if strings.Contains(path, restorejournal.AsideDirName) {
		d++
	}
	return d, err
}

// sidecarThief is the OS probe that removes the archive's checksum sidecar
// the first time the rename pre-check asks it anything: a sidecar deleted
// after StageRestore listed the backup directory.
type sidecarThief struct {
	restorejournal.Probe
	sidecar string
}

func (p sidecarThief) ItemDev(path string) (uint64, error) {
	_ = os.Remove(p.sidecar)
	return p.Probe.ItemDev(path)
}

// untouchedProbe fails the test when the rename pre-check runs: the stage
// was to be refused before it creates anything.
type untouchedProbe struct{ t *testing.T }

func (p untouchedProbe) ItemDev(path string) (uint64, error) {
	p.t.Errorf("the rename pre-check ran (%s) for a stage refused at the listing", path)
	return 0, nil
}

func (p untouchedProbe) DirDev(path string) (uint64, error) { return p.ItemDev(path) }

func (p untouchedProbe) Writable(dir string) error {
	_, err := p.ItemDev(dir)
	return err
}

// TestStageRestore (05-backup §5B.3.4 steps 1-9; R1-25 Goal item 3): the
// live restore stages an archive restore in the one slot and touches
// neither the live store nor anything else: pending.json holds a
// KindArchive intent that places the extracted store at the live path,
// under this binary's ID, with the live install ID and the archive's
// SHA-256, and the extracted store sits in the swap's staging directory.
// Every refusal leaves the slot as it was and removes everything the call
// made: verification asked of an archive without a sidecar, a sidecar that
// disagrees, a verified restore whose sidecar is removed after the listing
// (refused after the extraction, before the store is opened), a swap already staged or in flight (refused before any
// extraction), a store that does not open, a rename the swap could not
// make (refused before any extraction), a stager that loses the slot's
// link race after extracting, and an archive that is not listed.
func TestStageRestore(t *testing.T) {
	ctx := context.Background()

	t.Run("staged", func(t *testing.T) {
		f := newStageFixture(t)
		got, err := StageRestore(ctx, f.opts())
		if err != nil {
			t.Fatal(err)
		}
		if !restorejournal.ValidID(got.ID) || got.ArchiveID != f.info.Checksum || !got.Verified {
			t.Fatalf("staged %+v, want a valid id, archive ID %s and verified (the archive has a sidecar)", got, f.info.Checksum)
		}
		raw, err := os.ReadFile(restorejournal.PendingPath(f.state))
		if err != nil {
			t.Fatalf("no intent in the slot: %v", err)
		}
		var in restorejournal.Intent
		if err := json.Unmarshal(raw, &in); err != nil {
			t.Fatal(err)
		}
		staged := filepath.Join(restorejournal.StagingDir(f.db, got.ID), "db")
		want := restorejournal.Intent{
			ID: got.ID, Kind: restorejournal.KindArchive, DBPath: f.db,
			Targets:       []restorejournal.Target{{From: staged, To: f.db}},
			StagedByBuild: "batcomputer-r1", RequestedBy: "user:alfred", InstallID: "wayne-install", ArchiveID: f.info.Checksum,
			RequestedAt: time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC),
			ExpiresAt:   time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC),
		}
		if g, w := mustJSON(t, in), mustJSON(t, want); g != w {
			t.Fatalf("intent\n got %s\nwant %s", g, w)
		}
		for name, body := range map[string]string{"CURRENT": "MANIFEST-000001\n", "000001.sst": "the long halloween"} {
			if b, err := os.ReadFile(filepath.Join(staged, name)); err != nil || string(b) != body {
				t.Fatalf("staged store's %s = %q, %v", name, b, err)
			}
		}
		if len(f.verified) != 1 || f.verified[0] != staged {
			t.Fatalf("VerifyStore opened %v, want only the staged store %s", f.verified, staged)
		}
		if len(f.beforeStage) != 1 || f.beforeStage[0] != f.db {
			t.Fatalf("BeforeStage ran with %v, want once with the live store %s", f.beforeStage, f.db)
		}
		if b, err := os.ReadFile(filepath.Join(f.db, "CURRENT")); err != nil || string(b) != "the live store\n" {
			t.Fatalf("the live store changed: %q, %v", b, err)
		}
		if _, err := os.Stat(restorejournal.JournalDir(f.state, got.ID)); err != nil {
			t.Fatalf("no journal directory: %v", err)
		}
		if _, err := StageRestore(ctx, f.opts()); !errors.Is(err, restorejournal.ErrSwapPending) {
			t.Fatalf("a second stage = %v, want ErrSwapPending", err)
		}
	})

	t.Run("unverified archive without a sidecar", func(t *testing.T) {
		f := newStageFixture(t)
		if err := os.Remove(checksumSidecarPath(f.info.Path)); err != nil {
			t.Fatal(err)
		}
		o := f.opts()
		o.Verify = true
		// Refused at the listing: the rename pre-check never runs, so
		// nothing is created or extracted.
		o.Probe = untouchedProbe{t}
		if _, err := StageRestore(ctx, o); !errors.Is(err, ErrVerificationUnsupported) {
			t.Fatalf("verify without a sidecar = %v, want ErrVerificationUnsupported", err)
		}
		if left := f.leftovers(t, ""); len(left) != 0 || len(f.verified) != 0 {
			t.Fatalf("a refused stage left %v (verified %v)", left, f.verified)
		}
		o.Verify, o.Probe = false, nil
		if got, err := StageRestore(ctx, o); err != nil || got.Verified {
			t.Fatalf("no verification asked, no sidecar = %+v, %v; want it staged unverified", got, err)
		}
	})

	t.Run("verified", func(t *testing.T) {
		f := newStageFixture(t)
		o := f.opts()
		o.Verify = true
		got, err := StageRestore(ctx, o)
		if err != nil {
			t.Fatalf("verified stage of an archive that matches its sidecar: %v", err)
		}
		if got.ArchiveID != f.info.Checksum || !got.Verified {
			t.Fatalf("staged %+v, want archive ID %s, verified", got, f.info.Checksum)
		}
	})

	refusals := []struct {
		name    string
		setup   func(t *testing.T, f *stageFixture, o *StageRestoreOptions)
		want    error // errors.Is target; nil: any error
		extract bool  // whether the refusal comes after the extraction
		keep    string
	}{
		{"sidecar mismatch", func(t *testing.T, f *stageFixture, _ *StageRestoreOptions) {
			bad := strings.Repeat("0", 64) + "  " + filepath.Base(f.info.Path) + "\n"
			if err := os.WriteFile(checksumSidecarPath(f.info.Path), []byte(bad), 0o600); err != nil {
				t.Fatal(err)
			}
		}, ErrChecksumMismatch, false, ""},
		{"verified, sidecar gone after the listing", func(_ *testing.T, f *stageFixture, o *StageRestoreOptions) {
			// The rename pre-check runs between the listing and the
			// extraction: the sidecar goes there, so ExtractStore finds none
			// and would extract unverified.
			o.Verify = true
			o.Probe = sidecarThief{restorejournal.OSProbe(), checksumSidecarPath(f.info.Path)}
		}, ErrVerificationUnsupported, false, ""},
		{"staged swap", func(t *testing.T, f *stageFixture, _ *StageRestoreOptions) {
			f.otherIntent(t, "20261107T000000Z-joker001", false)
		}, restorejournal.ErrSwapPending, false, ""},
		{"swap in flight", func(t *testing.T, f *stageFixture, _ *StageRestoreOptions) {
			f.otherIntent(t, "20261107T000000Z-joker002", true)
		}, restorejournal.ErrSwapPending, false, "20261107T000000Z-joker002"},
		{"store does not open", func(_ *testing.T, f *stageFixture, o *StageRestoreOptions) {
			o.VerifyStore = func(_ context.Context, dir string) error {
				f.verified = append(f.verified, dir)
				return errors.New("bane broke its back")
			}
		}, nil, true, ""},
		{"the HNSW marker cannot be removed", func(_ *testing.T, f *stageFixture, o *StageRestoreOptions) {
			o.BeforeStage = func(string) error { return errors.New("the clean-stop marker is held by Mr. Freeze") }
		}, nil, true, ""},
		{"rename refused", func(_ *testing.T, _ *stageFixture, o *StageRestoreOptions) {
			o.Probe = otherFS{restorejournal.OSProbe()}
		}, restorejournal.ErrRenameRefused, false, ""},
		{"store holds the state directory", func(t *testing.T, f *stageFixture, o *StageRestoreOptions) {
			o.StateDir = filepath.Join(f.db, "state")
			f.state = o.StateDir
			if err := os.MkdirAll(o.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
		}, restorejournal.ErrTargetsRefused, false, ""},
		{"lost the link race", func(t *testing.T, f *stageFixture, o *StageRestoreOptions) {
			o.VerifyStore = func(_ context.Context, dir string) error {
				f.verified = append(f.verified, dir)
				f.otherIntent(t, "20261107T000000Z-penguin3", false)
				return nil
			}
		}, restorejournal.ErrSwapPending, true, ""},
		{"not listed", func(_ *testing.T, _ *stageFixture, o *StageRestoreOptions) {
			o.Filename = "arkham-asylum.tar.gz"
		}, os.ErrNotExist, false, ""},
		{"relative state directory", func(_ *testing.T, _ *stageFixture, o *StageRestoreOptions) {
			o.StateDir = "state"
		}, nil, false, ""},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			f := newStageFixture(t)
			o := f.opts()
			c.setup(t, f, &o)
			before, _ := restorejournal.ReadSlot(vfs.Default, f.state)
			_, err := StageRestore(ctx, o)
			if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
				t.Fatalf("StageRestore = %v, want a refusal (%v)", err, c.want)
			}
			if extracted := len(f.verified) > 0; extracted != c.extract {
				t.Fatalf("extracted and opened %v; want an extraction %v", f.verified, c.extract)
			}
			if c.name != "lost the link race" && len(f.beforeStage) != 0 {
				t.Fatalf("BeforeStage ran (%v) for a refused stage", f.beforeStage)
			}
			if left := f.leftovers(t, c.keep); len(left) != 0 {
				t.Fatalf("a refused stage left %v", left)
			}
			after, err := restorejournal.ReadSlot(vfs.Default, f.state)
			if err != nil {
				t.Fatal(err)
			}
			if c.name != "lost the link race" && mustJSON(t, before) != mustJSON(t, after) {
				t.Fatalf("the slot changed: %+v -> %+v", before, after)
			}
			if b, err := os.ReadFile(filepath.Join(f.db, "CURRENT")); err != nil || string(b) != "the live store\n" {
				t.Fatalf("the live store changed: %q, %v", b, err)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
