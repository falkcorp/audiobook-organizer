// file: core/backup/stagehook.go
// version: 0.1.0
// guid: 8c3f1e57-2a94-4d6b-b0e8-71d5a9c4f236
// last-edited: 2026-11-22

package backup

import (
	"context"
	"errors"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// BeforeStageFunc is something an app must do, once, for a restore that is
// certain to be staged and before the intent is created: books marks its
// vector index diverged and discards its clean-stop marker, so the boot after
// the swap rebuilds the graph instead of trusting the one of the store being
// replaced. It is irreversible, so it must never run for a restore that is
// then refused, and it must run before the intent exists: an intent staged
// with no marker discarded would let a crash between the two boot on the old
// graph.
type BeforeStageFunc func(ctx context.Context, in restorejournal.Intent) error

// ErrBeforeStageUnsupported: the context carries a BeforeStageFunc and the
// Service's Stager is not a CheckedStager, so nothing could run the hook
// between the checks and the create. Nothing was staged.
var ErrBeforeStageUnsupported = errors.New("backup: the Service's Stager cannot run a before-stage hook")

type beforeStageKey struct{}

// WithBeforeStage returns a context whose restore runs f first: Svc.StageRestore
// refuses it (ErrBeforeStageUnsupported) unless its Stager is a CheckedStager,
// which New wraps with BeforeStage. It is per request, as the closure is: a
// hook is the caller's own and never ambient, so a context that carries none
// stages exactly as before.
func WithBeforeStage(ctx context.Context, f BeforeStageFunc) context.Context {
	return context.WithValue(ctx, beforeStageKey{}, f)
}

// BeforeStageFrom is the hook ctx carries, nil when none.
func BeforeStageFrom(ctx context.Context) BeforeStageFunc {
	f, _ := ctx.Value(beforeStageKey{}).(BeforeStageFunc)
	return f
}

// CheckedStager is a Stager that can say, without writing anything, whether
// it would refuse an intent: restoreswap.Swapper (CheckStage is every refusal
// Stage makes before its first write).
type CheckedStager interface {
	Stager
	CheckStage(in restorejournal.Intent) error
}

// BeforeStage wraps s so a restore whose context carries a BeforeStageFunc
// runs, in this order: s.CheckStage (a refused intent runs no hook and writes
// nothing), the hook, then s.Stage. A context with no hook goes straight to
// s.Stage, which checks for itself.
//
// The hook runs after core.restore.stage's audit record, which Svc writes
// before it calls the Stager: the ledger then shows a restore whose hook
// failed or whose check refused as rejected, as it shows any refusal after
// the record.
func BeforeStage(s CheckedStager) Stager { return beforeStager{s} }

type beforeStager struct{ s CheckedStager }

func (b beforeStager) Stage(ctx context.Context, in restorejournal.Intent) error {
	hook := BeforeStageFrom(ctx)
	if hook == nil {
		return b.s.Stage(ctx, in)
	}
	if err := b.s.CheckStage(in); err != nil {
		return err
	}
	if err := hook(ctx, in); err != nil {
		return err
	}
	return b.s.Stage(ctx, in)
}
