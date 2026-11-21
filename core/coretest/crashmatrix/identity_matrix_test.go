// file: core/coretest/crashmatrix/identity_matrix_test.go
// version: 0.1.0
// guid: 8b5e1c37-4d92-4a60-9f1b-2c7a60d3e514
// last-edited: 2026-11-21

package crashmatrix

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/auth"
	backupjournal "github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// alfred is the smallest module that makes the boot open a store: one
// keyspace and nothing else. The identity boot adds core's own owners.
type alfred struct{}

func (alfred) Manifest() module.Manifest {
	return module.Manifest{
		ID: "alfred", Version: "1.0.0", DisplayName: "Alfred", MinCoreVersion: "0.1.0", DefaultEnabled: true,
		Storage: storage.Spec{Keyspace: true},
	}
}

func (alfred) Register(*module.Registrar) error { return nil }

// identityBuild boots app.New with IdentityBoot declared (F4-08 B3's switch),
// so P4d steps 2 to 4 and P7's seeds run and every label of the identity
// boot's writers is a kill point and an error point.
func identityBuild(_ testing.TB, inj *Injector) error {
	_, err := inj.New(context.Background(), app.Options{
		AppID: "gotham", IdentityBoot: true, Logger: slog.New(slog.DiscardHandler),
	}, alfred{})
	return err
}

// identityScenario is the identity boot over two starting states: a fresh
// install (the journal, the seeds, the _system user and the first bootstrap
// token are created) and a restart over a store the identity boot already
// wrote (the journal is found, the previous boot's bootstrap token is deleted
// and a new one minted, served_boot moves).
func identityScenario(t *testing.T) Scenario {
	t.Helper()
	fresh := Fixture{Name: "wayne-manor", StateDir: t.TempDir()}
	restart := Fixture{Name: "wayne-manor-restart", StateDir: t.TempDir()}
	if err := Boot(t, identityBuild, restart); err != nil {
		t.Fatalf("building the restart fixture's first boot: %v", err)
	}
	return Scenario{Build: identityBuild, Fixtures: []Fixture{fresh, restart, revertRestore(t)}}
}

// revertRestore is a store the identity boot wrote, with a copy of itself
// staged as a revert: P0a swaps the copy in, P4b journals the restore applied
// with its hooks owed, and P4d derives the restore (restore_derive.go), appends
// its restore line and runs the hooks, so the boot reaches the hooks' own
// commits (restore_hooked, the restore line, served_boot after the hooks).
func revertRestore(t *testing.T) Fixture {
	t.Helper()
	f := Fixture{Name: "wayne-manor-revert", StateDir: t.TempDir()}
	if err := Boot(t, identityBuild, f); err != nil {
		t.Fatalf("building the revert fixture's first boot: %v", err)
	}
	const id = "r-wayne-manor-revert"
	staged := filepath.Join(restorejournal.StagingDir(f.dbPath(), id), "main.pebble")
	copyStoreDir(t, f.dbPath(), staged)
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindRevert, Targets: []restorejournal.Target{{From: staged, To: f.dbPath()}}}
	if err := Stage(f, in); err != nil {
		t.Fatalf("staging the revert fixture's restore: %v", err)
	}
	return f
}

// copyStoreDir copies the closed store at src to dst.
func copyStoreDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		return errors.Join(err, out.Close())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestIdentityBootCrashMatrix is the matrix over the identity boot: every
// durable write, rename and fsync of P4d steps 2 to 4 and P7's seed commit that
// a fault-free boot reaches is killed and failed, and the boots after it must
// reach readiness with I1 to I6 holding (F4-08 B9a; 06 section 6.5.1).
func TestIdentityBootCrashMatrix(t *testing.T) {
	Run(t, identityScenario(t))
}

// identityLabels is every label the identity boot's writers carry: the rows of
// P4d steps 2 to 4 and P7, and the journal's file labels.
func identityLabels() []string {
	return slices.Concat(
		kernel.ReapplyLabels(), kernel.HookRunLabels(), auth.BootWritesLabels(),
		[]string{rbac.LabelCommitSeeds, kernel.LabelBackupDirMkdir, auth.BootstrapTokenLabel, auth.ReadOnlyKeyLabel},
		[]string{backupjournal.LabelCreate, backupjournal.LabelHeader, backupjournal.LabelDirSync, backupjournal.LabelAppend},
		[]string{backupjournal.LabelLocationTmp, backupjournal.LabelLocationRename, backupjournal.LabelLocationDirSync, backupjournal.LabelLocationCleanup},
	)
}

// TestIdentityScenario_ReachesWhatIdentityUnreachedLabelsSaysItDoes: the split
// of IdentityUnreachedLabels is true. A label whose reason says only the flip is
// missing (identityFlipOnly) is reached by this scenario's fault-free boots; a
// label whose reason names a journal state or a restore no fixture sets up is
// reached by none of them. A label of the identity boot that is in neither set
// is a writer no fixture breaks and nothing says why.
func TestIdentityScenario_ReachesWhatIdentityUnreachedLabelsSaysItDoes(t *testing.T) {
	observed := Observed(t, identityScenario(t))
	unreached := IdentityUnreachedLabels()
	for _, l := range identityLabels() {
		reason, listed := unreached[l]
		switch {
		case !listed:
			t.Errorf("identity label %s is in no allow entry of IdentityUnreachedLabels", l)
		case reason == identityFlipOnly && !observed[l]:
			t.Errorf("label %s is allowed because no fixture declares the flip, but the identity scenario does not reach it: give it the reason that says what state it needs", l)
		case reason != identityFlipOnly && observed[l]:
			t.Errorf("label %s is reached by the identity scenario, so its reason %q is stale: use identityFlipOnly", l, reason)
		}
	}
}
