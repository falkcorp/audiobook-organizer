// file: core/outbox/definition.go
// version: 1.0.0
// guid: 9f0fd1b8-0a2e-4c41-8f0a-7a8f5b8b0d52
// last-edited: 2026-11-12

package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// relayTimeoutSlack is how far the relay definition's Timeout is past the
// rotation: the run ends successfully at relay_rotate, and the timeout is only
// a backstop, so a rotation never ends timed_out.
const relayTimeoutSlack = 5 * time.Minute

// RelayDefinition is the task kind core.outbox.relay (05-events §5E.5.3): a
// Task, Writes(Res("core","outbox")), ResumeFromZero (all progress lives in
// intake rows and cursors, so restarting from zero is exact), one relay at a
// time, PrioritySystem, Every(1m).OnStart().Live() (the one kind allowed
// Every), Timeout = relay_rotate + 5 minutes (capped at tasks.MaxTimeout) read
// once, at registration.
//
// Perms.Start is core:tasks.manage, not core:system.manage: the relay is
// started by core's own system actor (the nudge, and the scheduler's Every
// tick through StartIn), and the executor strips every AuthRequired scope
// from a system actor's set (SEC2-15), core:system.manage among them. A kind
// that needed it could be started by a person alone, and the relay would
// never come back after a cancel or a restart.
//
// The kernel registers it with the executor; until F6-03 wires it into the
// App's boot, nothing does.
//
//tasklint:constructor the kernel passes the result to the executor's definition list when F6-03 wires core.outbox into boot (it also binds the executor); this package registers nothing
func (o *Outbox) RelayDefinition() tasks.Definition {
	return tasks.Task(RelayKind, tasks.TaskSpec[struct{}, struct{}]{
		Title:          "Outbox relay",
		Help:           "Sequences staged outbox messages in commit order and delivers them to their consumers, retrying and dead-lettering. It runs for about an hour, then a new run takes over.",
		Effects:        tasks.Writes(tasks.Res("core", "outbox")),
		Perms:          tasks.Permissions{Start: rbac.Need("core:tasks.manage")},
		Resume:         tasks.ResumeFromZero,
		ConcurrencyKey: tasks.Key(RelayKind),
		Priority:       tasks.PrioritySystem,
		Timeout:        min(o.config().RelayRotate+relayTimeoutSlack, tasks.MaxTimeout),
		Schedule:       tasks.Every(time.Minute).OnStart().Live(),
		Run:            o.relayBody,
	})
}

// Wake nudges the relay: it rescans intake now instead of at the next poll,
// and, when no relay is running, asks RunNudger to start one. Stage calls it;
// it never blocks and never fails.
func (o *Outbox) Wake() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
	if !o.running.Load() {
		select {
		case o.startReq <- struct{}{}:
		default:
		}
	}
}

// RunNudger is the one goroutine that starts the relay when Stage nudges and
// none runs, rate-limited to one start per second, with
// tasks.StartRequest{Mode: ModeRequestLive}: the Preview default for writers
// never applies (05-events §5E.5.3). It returns when ctx ends; the kernel runs
// it once, after the executor exists and BindRuns was called, on the App's
// lifecycle context. A nudge with the relay disabled by its dial starts
// nothing.
func (o *Outbox) RunNudger(ctx context.Context) {
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-o.startReq:
		}
		runs := o.runsOrNil()
		if o.running.Load() || runs == nil || !o.config().RelayEnabled {
			continue
		}
		if wait := time.Second - o.now().Sub(last); wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		last = o.now()
		_, err := runs.Start(ctx, RelayKind, tasks.StartRequest{
			Mode: tasks.ModeRequestLive, Trigger: tasks.TriggerOutbox,
			Actor: tasks.Actor{System: "core", AuthMethod: "system"},
		})
		if err != nil && ctx.Err() == nil {
			o.log.Debug("outbox: relay nudge start refused", slog.Any("err", err))
		}
	}
}
