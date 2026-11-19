// file: internal/plugins/dedup/kinds_scoring.go
// version: 1.0.0
// guid: 87767b32-a764-471a-bc6d-1b845dc5a45d
// last-edited: 2026-11-18

package dedup

import (
	"context"
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The scoring group (F5-04): the definitions that drive the dedup engine's
// candidate scorers and reviewers. Each native shell is a tasks.Task over the
// same core HEAD's shell calls.

// scoringEngine is the engine surface the scoring cores take (rule R10: reached
// through the env, never a receiver). Every method is one the HEAD body called
// on p.engine, with its signature. The engine writes candidates, labels and
// merges through its own stores; the native run's engine is built over the
// run's gated writer by ScoringKindDeps.NewEngine, so those writes are the
// writer's, which is F5-18's wiring (seams.d/F5-04.md).
type scoringEngine interface {
	PurgeStaleCandidates(ctx context.Context) (int, error)
}

// ScoringKindDeps is what the scoring group's native kinds need from their owner.
type ScoringKindDeps struct {
	// NewEngine builds the dedup engine over the run's gated writer.
	NewEngine func(tasks.Writer) scoringEngine
}

// scoringEngineFor builds the run's engine, refusing a run whose owner declared
// no factory (or one that returned nothing).
func scoringEngineFor(kind tasks.Kind, d ScoringKindDeps, rc *tasks.RC) (scoringEngine, error) {
	if err := errNeed(kind, "Scoring.NewEngine", d.NewEngine != nil); err != nil {
		return nil, err
	}
	e := d.NewEngine(rc.Writer())
	if e == nil {
		return nil, errNeedEngine(kind)
	}
	return e, nil
}

// scoringKinds lists the scoring group's native kinds in HEAD file order.
func scoringKinds(p *Plugin, d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.purgeStaleNative(d, sched),
	}
}

// purgeStaleNative is the native dedup.purge-stale kind. HEAD runs it live only
// (no Writes, no Preview), so a native Preview run is refused.
func (p *Plugin) purgeStaleNative(d ScoringKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(purgeStaleKind, sched, true) // HEAD: NoWarmupWait
	c.Title = "Cleanup stale dedup candidates"
	c.Help = "Deletes pending dedup candidates that are no longer valid (chapter files in same folder, same version-group, distinct series volumes)."
	// Writes found: Engine.PurgeStaleCandidates deletes candidate rows, which live
	// in the embedding store's keyspace (embeddings).
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityHigh
	c.ConcurrencyKey = tasks.Key("books.dedup.purge-stale")
	c.Timeout = 10 * time.Minute
	return tasks.Task(purgeStaleKind, tasks.TaskSpec[json.RawMessage, purgeStaleResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (purgeStaleResult, error) {
			if err := refuseKindModeKeys(purgeStaleKind, raw); err != nil {
				return purgeStaleResult{}, err
			}
			if err := kindLiveOnly(rc, purgeStaleKind); err != nil {
				return purgeStaleResult{}, err
			}
			eng, err := scoringEngineFor(purgeStaleKind, d, rc)
			if err != nil {
				return purgeStaleResult{}, err
			}
			return purgeStaleCore(rc.Context(), purgeStaleEnv{kindEnv: nativeEnv(rc), Engine: eng})
		},
	})
}
