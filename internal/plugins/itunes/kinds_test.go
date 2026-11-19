// file: internal/plugins/itunes/kinds_test.go
// version: 1.0.0
// guid: a1fcd2d0-c62f-43f2-93fa-03255721f374
// last-edited: 2026-11-19

package itunes

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// modeHost is a taskstest.FakeRun in the mode a test chooses (FakeRun is pinned
// to ModeLive).
type modeHost struct {
	*taskstest.FakeRun
	mode tasks.Mode
}

func (h *modeHost) Mode() tasks.Mode { return h.mode }

// runNative runs d's own Run, the function the executor would call, in mode.
func runNative(t *testing.T, d tasks.Definition, mode tasks.Mode, params string) error {
	t.Helper()
	r, ok := d.(interface {
		ExecutorRunners() (func(json.RawMessage) (any, error), *tasks.TaskSpec[any, json.RawMessage], *tasks.BatchSpec[any, any, json.RawMessage], *tasks.FixerSpec[any, any])
	})
	require.True(t, ok, "%T has no ExecutorRunners", d)
	decode, task, _, _ := r.ExecutorRunners()
	require.NotNil(t, task, "%s is not a Task", d.Kind())
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	in, err := decode(raw)
	if err != nil {
		return err
	}
	h := &modeHost{FakeRun: taskstest.NewFakeRun(context.Background(), d.Kind()), mode: mode}
	_, err = task.Run(tasks.NewRC(h), in)
	return err
}

func TestITunesKinds_Conformance(t *testing.T) {
	taskstest.Conformance(t, (&Plugin{}).Kinds(KindDeps{}, nil)...)
}

// TestITunesKinds_MatchHEADDefs: the native kind compared with the HEAD
// definition it came from and with literal values (rule R8). A drift of either
// side fails.
func TestITunesKinds_MatchHEADDefs(t *testing.T) {
	p := &Plugin{}
	defs := p.Kinds(KindDeps{}, nil)
	require.Len(t, defs, 1, "itunes converts one definition; itunes.import is an excluded stub (F5-13)")
	head := p.positionSyncDef()
	require.Equal(t, "itunes.position-sync", head.ID)

	d := defs[0]
	require.Equal(t, tasks.Kind("books."+head.ID), d.Kind())
	_, err := tasks.ParseKind(string(d.Kind()))
	require.NoError(t, err)
	c := tasks.CommonOf(d)

	require.Equal(t, "iTunes Position Sync", head.DisplayName)
	require.Equal(t, head.DisplayName, c.Title)
	require.Equal(t, "Sync reading positions between iTunes bookmarks and the app.", head.Description)
	require.Equal(t, head.Description, c.Help, "Help is HEAD's Description verbatim")

	// HEAD ResumeRequeue maps to FromZero (rule R5).
	require.Equal(t, sdk.ResumeRequeue, head.ResumePolicy)
	require.Equal(t, tasks.ResumeFromZero, c.Resume)
	require.Equal(t, registry.PriorityNormal, head.DefaultPriority)
	require.Equal(t, tasks.PriorityNormal, c.Priority)
	require.Equal(t, 30*time.Minute, head.Timeout)
	require.Equal(t, 30*time.Minute, c.Timeout)
	require.Zero(t, c.ProgressTimeout)
	require.False(t, head.NoWarmupWait)
	require.Equal(t, []tasks.ReadySignal{"books.memdb"}, c.Ready)
	require.Equal(t, "itunes.position-sync", head.ConcurrencyKey)
	require.Equal(t, tasks.Key("books.itunes.position-sync"), c.ConcurrencyKey)
	require.Empty(t, c.NotWhile)
	require.Nil(t, c.Schedule, "sched is nil in the test; no conversion declares a schedule itself")

	// HEAD declares no Writes, no Preview and no write capability, and the body
	// writes nothing: ReadOnly, no declared reads (rule R7).
	require.Empty(t, head.Writes)
	require.True(t, c.Effects.IsReadOnly())
	require.Empty(t, c.Effects.WriteSet())
	require.Empty(t, c.Effects.ReadSet())
	require.Empty(t, c.Effects.DeleteSet())

	// HEAD has no Permissions: settings.manage (rule R6).
	require.Empty(t, head.Permissions)
	require.Equal(t, rbac.KindAllOf, c.Perms.Start.Kind())
	require.Equal(t, rbac.Requirement(kindStartRequirement()), c.Perms.Start)
	// The scopes of the settings.manage atom, spelled out: the requirement above
	// is compared with the code's own copy, which would pass a dropped scope.
	require.Equal(t, rbac.AllOf(
		"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
		"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
	), c.Perms.Start)
	require.Empty(t, c.Perms.StartableByViewers)
}

// TestITunesKinds_Schedule: the list function hands each kind its schedule from
// sched (rule R9), and a nil result means none.
func TestITunesKinds_Schedule(t *testing.T) {
	want := tasks.Every(10 * time.Minute)
	var asked []tasks.Kind
	defs := (&Plugin{}).Kinds(KindDeps{}, func(k tasks.Kind) *tasks.Schedule {
		asked = append(asked, k)
		return want
	})
	require.Equal(t, []tasks.Kind{"books.itunes.position-sync"}, asked)
	require.Equal(t, want.Interval(), tasks.CommonOf(defs[0]).Schedule.Interval())

	defs = (&Plugin{}).Kinds(KindDeps{}, func(tasks.Kind) *tasks.Schedule { return nil })
	require.Nil(t, tasks.CommonOf(defs[0]).Schedule)
}

// TestITunesKinds_WritesThroughShell: every kind of this package is ReadOnly, so
// there is no write to route through a writer (rule R10); the case asserts that,
// and that the stub fails in every mode, with HEAD's own error: a stub that
// returned nil would report a green run for work that did not happen.
func TestITunesKinds_WritesThroughShell(t *testing.T) {
	p := &Plugin{}
	for _, d := range p.Kinds(KindDeps{}, nil) {
		require.True(t, tasks.CommonOf(d).Effects.IsReadOnly(), "%s must declare ReadOnly", d.Kind())
	}
	want := p.runPositionSync(context.Background(), nil, nil)
	require.Error(t, want)
	require.Equal(t, errNotImplemented("itunes.position-sync").Error(), want.Error())

	d := p.Kinds(KindDeps{}, nil)[0]
	for name, mode := range map[string]tasks.Mode{"live": tasks.ModeLive, "preview": tasks.ModePreview} {
		t.Run(name, func(t *testing.T) {
			err := runNative(t, d, mode, "")
			require.Error(t, err, "the stub must never report success")
			require.Equal(t, want.Error(), err.Error(), "native and HEAD shells refuse with the same error")
		})
	}
	// HEAD ignored its params: so does the native shell, mode keys included (the
	// stub cannot write, so a dropped dry_run changes nothing).
	require.EqualError(t, runNative(t, d, tasks.ModeLive, `{"dry_run":false,"x":1}`), want.Error())
}
