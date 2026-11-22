// file: core/backup/stagehook_test.go
// version: 0.1.0
// guid: 5d9e2a14-7b36-4c80-a1f3-c8e04b7d6a52
// last-edited: 2026-11-22

package backup

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// recStager records the order of CheckStage, the hook and Stage.
type recStager struct {
	calls    *[]string
	checkErr error
	stageErr error
}

func (r recStager) CheckStage(restorejournal.Intent) error {
	*r.calls = append(*r.calls, "check")
	return r.checkErr
}

func (r recStager) Stage(context.Context, restorejournal.Intent) error {
	*r.calls = append(*r.calls, "stage")
	return r.stageErr
}

// TestBeforeStageOrder: check, then the hook, then the create; never the hook
// for an intent the check refuses, and never the create after a failed hook.
func TestBeforeStageOrder(t *testing.T) {
	errCheck, errHook := errors.New("a rename was refused"), errors.New("the marker would not go")
	for _, c := range []struct {
		name     string
		hook     bool
		checkErr error
		hookErr  error
		want     []string
		wantErr  error
	}{
		{name: "accepted", hook: true, want: []string{"check", "hook", "stage"}},
		{name: "check refuses: no hook, no stage", hook: true, checkErr: errCheck, want: []string{"check"}, wantErr: errCheck},
		{name: "hook fails: no stage", hook: true, hookErr: errHook, want: []string{"check", "hook"}, wantErr: errHook},
		{name: "no hook goes straight to stage", want: []string{"stage"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			s := BeforeStage(recStager{calls: &calls, checkErr: c.checkErr})
			ctx := context.Background()
			if c.hook {
				ctx = WithBeforeStage(ctx, func(context.Context, restorejournal.Intent) error {
					calls = append(calls, "hook")
					return c.hookErr
				})
			}
			err := s.Stage(ctx, restorejournal.Intent{ID: "r-bane"})
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("Stage = %v, want %v", err, c.wantErr)
			}
			if !reflect.DeepEqual(calls, c.want) {
				t.Fatalf("calls = %v, want %v", calls, c.want)
			}
		})
	}
}

// TestBeforeStageHookSeesTheIntent: the hook gets the intent being staged, so
// books discards the clean-stop marker of in.DBPath and no other store.
func TestBeforeStageHookSeesTheIntent(t *testing.T) {
	var calls []string
	var got restorejournal.Intent
	ctx := WithBeforeStage(context.Background(), func(_ context.Context, in restorejournal.Intent) error {
		got = in
		return nil
	})
	if err := BeforeStage(recStager{calls: &calls}).Stage(ctx, restorejournal.Intent{ID: "r-crane", DBPath: "/var/lib/gotham/db"}); err != nil {
		t.Fatal(err)
	}
	if got.ID != "r-crane" || got.DBPath != "/var/lib/gotham/db" {
		t.Fatalf("hook got %+v", got)
	}
}
