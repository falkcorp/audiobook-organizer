// file: core/backup/stagehook_svc_test.go
// version: 0.1.0
// guid: 2e7a4c19-6d83-4f05-b9a1-d30c58e7f164
// last-edited: 2026-11-22

package backup_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// orderStager is the fake swapper with a CheckStage, recording the order of its
// calls in the env's shared log.
type orderStager struct {
	*fakeStager
	calls    *[]string
	checkErr error
}

func (o orderStager) CheckStage(restorejournal.Intent) error {
	*o.calls = append(*o.calls, "check")
	return o.checkErr
}

func (o orderStager) Stage(ctx context.Context, in restorejournal.Intent) error {
	*o.calls = append(*o.calls, "stage")
	return o.fakeStager.Stage(ctx, in)
}

// TestStageRestoreBeforeStageHook: through the Service, a BeforeStageFunc runs
// after every refusal the Stager can make without writing and before the
// create, with the intent the create will write; a restore that is refused (no
// owner proof, a check refusal) or whose hook fails runs it never, or stages
// nothing; and a Service whose Stager cannot run one refuses a context that
// asks for it, before it reads anything.
func TestStageRestoreBeforeStageHook(t *testing.T) {
	ctx := context.Background()
	errCheck, errHook := errors.New("the swap would not rename the sidecar"), errors.New("the marker would not go")

	setup := func(t *testing.T) (*env, *[]string, *orderStager, backup.Archive) {
		e := newEnv(t)
		a := e.create("manual")
		var calls []string
		st := &orderStager{fakeStager: e.stager, calls: &calls}
		e.opts.Stager = st
		e.rebuild()
		return e, &calls, st, a
	}
	hookCtx := func(c context.Context, calls *[]string, err error, seen *restorejournal.Intent) context.Context {
		return backup.WithBeforeStage(c, func(_ context.Context, in restorejournal.Intent) error {
			*calls = append(*calls, "hook")
			if seen != nil {
				*seen = in
			}
			return err
		})
	}

	t.Run("accepted: check, hook, stage", func(t *testing.T) {
		e, calls, _, a := setup(t)
		var seen restorejournal.Intent
		if _, err := e.svc.StageRestore(hookCtx(asOwner(ctx), calls, nil, &seen), a.ID, backup.RestoreOptions{}); err != nil {
			t.Fatal(err)
		}
		if want := []string{"check", "hook", "stage"}; !reflect.DeepEqual(*calls, want) {
			t.Fatalf("calls = %v, want %v", *calls, want)
		}
		if len(e.stager.staged) != 1 || seen.ID == "" || seen.ID != e.stager.staged[0].ID {
			t.Fatalf("the hook saw intent %q, the create wrote %+v", seen.ID, e.stager.staged)
		}
	})
	t.Run("check refuses: no hook, nothing staged", func(t *testing.T) {
		e, calls, st, a := setup(t)
		st.checkErr = errCheck
		_, err := e.svc.StageRestore(hookCtx(asOwner(ctx), calls, nil, nil), a.ID, backup.RestoreOptions{})
		if !errors.Is(err, errCheck) {
			t.Fatalf("StageRestore = %v, want the check's refusal", err)
		}
		if want := []string{"check"}; !reflect.DeepEqual(*calls, want) {
			t.Fatalf("calls = %v, want %v", *calls, want)
		}
		nothingStaged(t, e)
	})
	t.Run("hook fails: nothing staged", func(t *testing.T) {
		e, calls, _, a := setup(t)
		_, err := e.svc.StageRestore(hookCtx(asOwner(ctx), calls, errHook, nil), a.ID, backup.RestoreOptions{})
		if !errors.Is(err, errHook) {
			t.Fatalf("StageRestore = %v, want the hook's error", err)
		}
		if want := []string{"check", "hook"}; !reflect.DeepEqual(*calls, want) {
			t.Fatalf("calls = %v, want %v", *calls, want)
		}
		nothingStaged(t, e)
	})
	t.Run("no owner proof: no hook", func(t *testing.T) {
		e, calls, _, a := setup(t)
		_, err := e.svc.StageRestore(hookCtx(ctx, calls, nil, nil), a.ID, backup.RestoreOptions{})
		if !errors.Is(err, backup.ErrRestoreNotAuthorized) {
			t.Fatalf("StageRestore = %v, want ErrRestoreNotAuthorized", err)
		}
		if len(*calls) != 0 {
			t.Fatalf("an unauthorized restore reached %v", *calls)
		}
		nothingStaged(t, e)
	})
	t.Run("a Stager that cannot check takes no hook", func(t *testing.T) {
		e := newEnv(t)
		a := e.create("manual")
		var calls []string
		_, err := e.svc.StageRestore(hookCtx(asOwner(ctx), &calls, nil, nil), a.ID, backup.RestoreOptions{})
		if !errors.Is(err, backup.ErrBeforeStageUnsupported) {
			t.Fatalf("StageRestore = %v, want ErrBeforeStageUnsupported", err)
		}
		if len(calls) != 0 {
			t.Fatalf("the hook ran: %v", calls)
		}
		nothingStaged(t, e)
		// The same Service stages a context with no hook, as before.
		if _, err := e.svc.StageRestore(asOwner(ctx), a.ID, backup.RestoreOptions{}); err != nil {
			t.Fatalf("a restore with no hook was refused: %v", err)
		}
	})
}
