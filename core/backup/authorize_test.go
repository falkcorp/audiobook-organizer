// file: core/backup/authorize_test.go
// version: 1.0.0
// guid: 1e7c4a92-8d05-4b36-a9f1-6c3b0d5e7a28
// last-edited: 2026-11-12

package backup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// privilegedOptions are the options of StageRestore that need proof.
var privilegedOptions = map[string]backup.RestoreOptions{
	"foreign":                   {Foreign: true},
	"replace_config":            {ReplaceConfig: true},
	"accept_unreadable_secrets": {AcceptUnreadableSecrets: true},
	"allow_weak_verify":         {AllowWeakVerify: true},
}

func noStaging(t *testing.T, e *env, what string) {
	t.Helper()
	if len(e.stager.staged) != 0 {
		t.Fatalf("%s: an intent was created", what)
	}
	if _, err := os.Stat(filepath.Join(e.dbParent, ".mediacore-restore")); !os.IsNotExist(err) {
		t.Fatalf("%s: staging was touched before the gate: %v", what, err)
	}
}

// TestRestoreOptionsGate: the one gate every caller of StageRestore passes. An
// option that plants or accepts something an ordinary restore does not needs
// the interactive owner proof, and Foreign needs core:admin on top.
func TestRestoreOptionsGate(t *testing.T) {
	for name, opts := range privilegedOptions {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			a := e.create("manual")
			bg := context.Background()
			if _, err := e.svc.StageRestore(bg, a.ID, opts); !errors.Is(err, backup.ErrRestoreNotAuthorized) {
				t.Fatalf("no proof: err = %v, want ErrRestoreNotAuthorized", err)
			}
			noStaging(t, e, "no proof")
			// A zero proof is no proof; the gate reads Owner, not the presence of a value.
			if _, err := e.svc.StageRestore(backup.WithRestoreProof(bg, backup.RestoreProof{Admin: true}), a.ID, opts); !errors.Is(err, backup.ErrRestoreNotAuthorized) {
				t.Fatalf("admin without owner proof: err = %v, want ErrRestoreNotAuthorized", err)
			}
			noStaging(t, e, "admin without the owner proof")
			owner := backup.WithRestoreProof(bg, backup.RestoreProof{Owner: true})
			_, err := e.svc.StageRestore(owner, a.ID, opts)
			if opts.Foreign {
				if !errors.Is(err, backup.ErrRestoreNotAuthorized) {
					t.Fatalf("foreign with the owner but not core:admin: err = %v", err)
				}
				noStaging(t, e, "foreign without admin")
				err = nil
				if _, err = e.svc.StageRestore(asOwner(bg), a.ID, opts); err != nil {
					t.Fatalf("foreign with owner and admin: %v", err)
				}
			} else if err != nil {
				t.Fatalf("with the owner proof: %v", err)
			}
			if len(e.stager.staged) != 1 {
				t.Fatalf("intents = %d, want the one authorised restore", len(e.stager.staged))
			}
		})
	}
}

// TestPlainRestoreNeedsNoProofFromThePackage: the caller's own gate decides who
// may restore at all; only the privileged options are gated here.
func TestPlainRestoreNeedsNoProofFromThePackage(t *testing.T) {
	e := newEnv(t)
	a := e.create("manual")
	if _, err := e.svc.StageRestore(context.Background(), a.ID, backup.RestoreOptions{Restart: true}); err != nil {
		t.Fatalf("a plain restore without proof: %v", err)
	}
}

func TestRevertGate(t *testing.T) {
	e := newEnv(t)
	revertFixture(t, e, "20261110T030000Z-cccccccc", "applied", true)
	bg := context.Background()
	if _, err := e.svc.RevertLastRestore(bg, backup.RestoreOptions{ReplaceConfig: true}); !errors.Is(err, backup.ErrRestoreNotAuthorized) {
		t.Fatalf("revert replacing the config without proof: %v", err)
	}
	if len(e.stager.staged) != 0 {
		t.Fatal("an unauthorised revert was staged")
	}
	if _, err := e.svc.RevertLastRestore(backup.WithRestoreProof(bg, backup.RestoreProof{Owner: true}), backup.RestoreOptions{ReplaceConfig: true}); err != nil {
		t.Fatalf("revert with the owner proof: %v", err)
	}
}

// TestStageTaskRefusesPrivilegedOptions: a core:backup.restore holder who starts
// the stage-restore task has no route to prove anything through. Each privileged
// option is refused, a plain restore still runs.
func TestStageTaskRefusesPrivilegedOptions(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	rc := taskstest.NewFakeRun(ctx, backup.KindStageRestore).RC()
	for name, p := range map[string]backup.StageParams{
		"foreign":                   {Foreign: true},
		"replace_config":            {ReplaceConfig: true},
		"accept_unreadable_secrets": {AcceptUnreadableSecrets: true},
		"allow_weak_verify":         {AllowWeakVerify: true, Verify: "checksum"},
	} {
		p.ArchiveID = a.ID
		if _, err := backup.TaskStage(e.svc, rc, p); !errors.Is(err, backup.ErrRestoreNotAuthorized) {
			t.Errorf("%s through the task: err = %v, want ErrRestoreNotAuthorized", name, err)
		}
		noStaging(t, e, name)
	}
	res, err := backup.TaskStage(e.svc, rc, backup.StageParams{ArchiveID: a.ID})
	if err != nil || res.Pending.ArchiveID != a.ID {
		t.Fatalf("a plain restore through the task: %+v, %v", res, err)
	}
}
