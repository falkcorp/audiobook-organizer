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
	"reflect"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
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

// TestPlainRestoreNeedsTheOwnerProof: a plain restore is gated like every other
// one. 05-backup §5B.3.4 names the API phase "task core.backup.stage-restore
// (POST /api/core/v1/backups/{id}/restore, permission core:backup.restore
// (Sensitive, AuthRequired, 05-rbac), credential-change route with owner
// proof", so a caller that has not proved the owner stages nothing, and the
// restart step 9 asks for never runs.
func TestPlainRestoreNeedsTheOwnerProof(t *testing.T) {
	e := newEnv(t)
	restarts := 0
	e.opts.Restart = func(context.Context) error { restarts++; return nil }
	e.rebuild()
	a := e.create("manual")
	bg := context.Background()
	if _, err := e.svc.StageRestore(bg, a.ID, backup.RestoreOptions{Restart: true}); !errors.Is(err, backup.ErrRestoreNotAuthorized) {
		t.Fatalf("a plain restore without proof: err = %v, want ErrRestoreNotAuthorized", err)
	}
	noStaging(t, e, "a plain restore without proof")
	if _, err := e.svc.StageRestore(backup.WithRestoreProof(bg, backup.RestoreProof{Admin: true}), a.ID, backup.RestoreOptions{Restart: true}); !errors.Is(err, backup.ErrRestoreNotAuthorized) {
		t.Fatalf("a plain restore by core:admin without the owner proof: err = %v, want ErrRestoreNotAuthorized", err)
	}
	noStaging(t, e, "a plain restore by core:admin without the owner proof")
	if restarts != 0 {
		t.Fatalf("a refused restore asked for %d restarts", restarts)
	}
	owner := backup.WithRestoreProof(bg, backup.RestoreProof{Owner: true})
	pr, err := e.svc.StageRestore(owner, a.ID, backup.RestoreOptions{Restart: true})
	if err != nil {
		t.Fatalf("a plain restore with the owner proof: %v", err)
	}
	if pr.ArchiveID != a.ID || len(e.stager.staged) != 1 {
		t.Fatalf("staged %+v, intents = %d; want the one authorised restore", pr, len(e.stager.staged))
	}
	if restarts != 1 {
		t.Fatalf("restarts = %d, want the one step 9 asks for", restarts)
	}
}

func TestRevertGate(t *testing.T) {
	e := newEnv(t)
	revertFixture(t, e, "20261110T030000Z-cccccccc", "applied", true)
	bg := context.Background()
	// POST /restore/revert carries the same permission and guards as a restore
	// (05-backup §5B.3.8), so a plain revert needs the owner proof too.
	if _, err := e.svc.RevertLastRestore(bg, backup.RestoreOptions{}); !errors.Is(err, backup.ErrRestoreNotAuthorized) {
		t.Fatalf("a plain revert without proof: %v", err)
	}
	if _, err := e.svc.RevertLastRestore(backup.WithRestoreProof(bg, backup.RestoreProof{Admin: true}), backup.RestoreOptions{}); !errors.Is(err, backup.ErrRestoreNotAuthorized) {
		t.Fatalf("a plain revert by core:admin without the owner proof: %v", err)
	}
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

// TestStageTaskStagesNothingWithoutAProof: a core:backup.restore holder who
// starts the stage-restore task has no route to prove anything through, and a
// task run's context carries no RestoreProof. Every restore through the task is
// refused, the plain one included, and nothing is staged.
func TestStageTaskStagesNothingWithoutAProof(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	rc := taskstest.NewFakeRun(ctx, backup.KindStageRestore).RC()
	for name, p := range map[string]backup.StageParams{
		"plain":                     {},
		"restart":                   {Restart: true},
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
}

// TestStageRestoreTaskPermissions pins the stage-restore definition to the
// permission 05-backup §5B.3.4 names, "permission core:backup.restore
// (Sensitive, AuthRequired, 05-rbac)", and keeps it off the generic task API:
// that route would start the kind without the restore route's interactive and
// owner guards.
func TestStageRestoreTaskPermissions(t *testing.T) {
	perms := tasks.CommonOf(backup.StageRestoreTask(nil)).Perms
	if !reflect.DeepEqual(perms.Start, rbac.Need("core:backup.restore")) {
		t.Fatalf("Start = %+v, want rbac.Need(\"core:backup.restore\")", perms.Start)
	}
	if perms.OwnerRouteOnly == "" {
		t.Fatal("core.backup.stage-restore is startable through the generic task API")
	}
	var spec rbac.PermissionSpec
	for _, s := range rbac.CoreScopes() {
		if s.Name == "core:backup.restore" {
			spec = s
		}
	}
	if !spec.Sensitive || !spec.AuthRequired {
		t.Fatalf("catalog row %+v: core:backup.restore must be Sensitive and AuthRequired", spec)
	}
	// The other three backup kinds keep their own start permission and stay on
	// the generic route.
	for _, d := range []tasks.Definition{backup.CreateTask(nil), backup.VerifyTask(nil), backup.PruneTask(nil)} {
		if p := tasks.CommonOf(d).Perms; p.OwnerRouteOnly != "" || !reflect.DeepEqual(p.Start, rbac.Need("core:system.manage")) {
			t.Errorf("%s: perms %+v", d.Kind(), p)
		}
	}
}
