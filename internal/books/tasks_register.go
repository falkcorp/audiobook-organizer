// file: internal/books/tasks_register.go
// version: 0.1.0
// guid: 0b6f3d82-47c1-4e95-a2d8-9e5c1a7f4b63
// last-edited: 2026-11-28

package books

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/health"
	corelog "github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
)

// memdbComponent is the readiness component the kinds that read the memdb wait
// on (Common.Ready: books.memdb). It must be a registered health check, or core
// never dispatches one of them.
const memdbComponent health.ComponentID = "books.memdb"

// memdbWarmupBound is how long a run waits for the memdb before it runs on
// Pebble reads anyway. It is HEAD's registry.WarmupWaitTimeout (300 s), which
// the registry removed with the engine would otherwise have been the only copy.
const memdbWarmupBound = 300 * time.Second

// warmupStatuser is the part of the store the memdb check reads.
type warmupStatuser interface {
	WarmupStatus() (ready, done bool, ms int64)
}

// memdbProbe is the books.memdb check's probe, with HEAD's warmup gate
// (registry.WarmupGate) as its rule: a run is held while the memdb warms and
// released when the warmup is done, whether it published (Up) or fell back to
// Pebble reads (Degraded), or when memdbWarmupBound has passed since the first
// probe that found it warming (Degraded). The dispatcher treats Up and Degraded
// alike, so a failed or stuck warmup never holds a kind that waits on the memdb
// forever. src is read each call through the func so the check can be declared
// before the store opens. now is the clock.
func memdbProbe(src func() warmupStatuser, now func() time.Time) func(context.Context) health.Result {
	var firstWarming atomic.Int64 // unix nanos of the first probe that found the memdb warming
	return func(context.Context) health.Result {
		at := now()
		s := src()
		if s == nil {
			return health.Result{Status: health.StatusDown, Reason: "no_store", At: at}
		}
		ready, done, _ := s.WarmupStatus()
		switch {
		case done && ready:
			firstWarming.Store(0)
			return health.Result{Status: health.StatusUp, At: at}
		case done:
			firstWarming.Store(0)
			return health.Result{Status: health.StatusDegraded, Reason: "pebble_fallback",
				Detail: "the memdb warmup finished without publishing; reads use Pebble", At: at}
		}
		firstWarming.CompareAndSwap(0, at.UnixNano())
		if at.Sub(time.Unix(0, firstWarming.Load())) >= memdbWarmupBound {
			return health.Result{Status: health.StatusDegraded, Reason: "warmup_timeout",
				Detail: "the memdb warmup did not finish in time; runs proceed on Pebble reads", At: at}
		}
		return health.Result{Status: health.StatusDown, Reason: "warming_up", At: at}
	}
}

// memdbPollInterval is how often the watcher re-reads the warmup state.
const memdbPollInterval = 500 * time.Millisecond

// memdbSource is the store the check reads, nil before the store opens.
func (m *booksModule) memdbSource() warmupStatuser {
	if s := m.store.Load(); s != nil {
		return s
	}
	return nil
}

// memdbCheck is the books.memdb component: neither Critical nor Readiness, so
// the app's own readiness does not wait on the warmup (HEAD's server was ready
// while it warmed); only the kinds that name it in Common.Ready do. probe is
// its state (memdbProbe): the registry calls it once as the check registers, and
// memdbWatch pushes it after that, because core's probe loop is not started by
// the kernel yet and a component nothing re-reads stays at its first answer.
func memdbCheck(probe func(context.Context) health.Result) health.Check {
	return health.Check{
		ID:       memdbComponent,
		Title:    "Books in-memory index",
		Probe:    probe,
		Interval: 2 * time.Second,
		Timeout:  time.Second,
	}
}

// memdbWatch is the start hook that keeps books.memdb current: from Start it
// pushes probe's result through the module's reporter every memdbPollInterval,
// until the warmup is over (the result is Up, or Degraded for a finished warmup
// that fell back to Pebble reads: after memdbWarmupBound a Degraded
// warmup_timeout is pushed too, and the watcher keeps going until the warmup
// really ends, so a late publish still turns it Up). Stop ends it.
func memdbWatch(probe func(context.Context) health.Result, rep health.Reporters, poll time.Duration) module.Hook {
	var (
		cancel context.CancelFunc
		done   chan struct{}
	)
	return module.Hook{
		Name: "books.memdb-health",
		Start: func(context.Context) error {
			r, err := rep.Reporter(memdbComponent)
			if err != nil {
				return fmt.Errorf("books: the memdb readiness reporter: %w", err)
			}
			// Not derived from Start's ctx: it ends with the start window, and the
			// watcher must outlive Start. Stop ends it.
			var ctx context.Context
			ctx, cancel = context.WithCancel(context.Background())
			done = make(chan struct{})
			go func() {
				defer close(done)
				t := time.NewTicker(poll)
				defer t.Stop()
				for {
					res := probe(ctx)
					r.Report(res)
					if res.Status == health.StatusUp || res.Reason == "pebble_fallback" {
						return
					}
					select {
					case <-ctx.Done():
						return
					case <-t.C:
					}
				}
			}()
			return nil
		},
		Stop: func(context.Context) error {
			if cancel != nil {
				cancel()
				<-done
			}
			return nil
		},
	}
}

// otherUserLogData is the predicate Registrar.LogOtherUserData takes: it says
// whether a journal record names another user's data. It hides the line (core
// shows it to no reader without users.manage, the viewer's own included, which
// is what HEAD does; viewer is not consulted) when any of HEAD's rules would
// have removed or altered something of it:
//
//   - the subsystem or message (activity.NamesOthersUserLog);
//   - an attribute whose key names a user and whose value is not a count or a
//     flag, at any depth through groups (activity.NamesUserAttr, the one key
//     rule WithoutUserAttrs applies too), so user_id=42 and an opaque owner are
//     caught though neither value looks like user text;
//   - an attribute whose value, flattened to "key=value" text, names a user.
//
// This is stricter than HEAD, on purpose: HEAD serves the line and redacts the
// attribute, and core's contract is a yes or no for the whole line, so the line
// goes. It is pure and, like the rules, errs towards true (an attribute value
// it cannot read as a Go value is judged by its text).
func otherUserLogData(rec corelog.Record, _ string) bool {
	if activity.NamesOthersUserLog(rec.Subsystem, rec.Message) {
		return true
	}
	return attrsNameUser(rec.Attrs)
}

// attrsNameUser is otherUserLogData's attribute half.
func attrsNameUser(attrs []slog.Attr) bool {
	for _, a := range attrs {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			// An empty key inlines the group's attributes into the line; a named
			// group is a map under its key, which the key rule reads whole.
			if a.Key == "" {
				if attrsNameUser(v.Group()) {
					return true
				}
				continue
			}
			if activity.NamesUserAttr(a.Key, groupValue(v.Group())) {
				return true
			}
			continue
		}
		if activity.NamesUserAttr(a.Key, attrGoValue(v)) || activity.NamesUserText(a.Key+"="+v.String()) {
			return true
		}
	}
	return false
}

// groupValue is a slog group as the map json.Unmarshal would have made of it.
func groupValue(attrs []slog.Attr) map[string]any {
	m := make(map[string]any, len(attrs))
	for _, a := range attrs {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			if a.Key == "" {
				for k, e := range groupValue(v.Group()) {
					m[k] = e
				}
				continue
			}
			m[a.Key] = groupValue(v.Group())
			continue
		}
		m[a.Key] = attrGoValue(v)
	}
	return m
}

// attrGoValue is a resolved, non-group slog value as a Go value the key rule
// reads: numbers and booleans as such (a count stays a count), a map or list as
// the any form NamesUserAttr walks, and anything else as its text, so an error
// or a struct is judged by what it prints.
func attrGoValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindAny:
		switch t := v.Any().(type) {
		case map[string]any, []any:
			return t
		}
	}
	return v.String()
}

// registerTaskFlip is Register's part of the tasks flip, run only with
// tasksFlipOn. It declares what does not need the server: the readiness
// component the memdb kinds wait on and the journal's other-user rule. The
// kinds themselves, the change sink and the lanes are separate, below.
//
// No lane is registered: HEAD declares pkg/ops.LaneMac but no definition lists
// it in Uses and no native kind in Common.Lanes, so a lane would be a name
// nothing leases (core refuses a kind that names an undeclared lane, never the
// reverse).
//
// No ChangeSink is registered here, and that is a decision, not an omission: a
// module that has one is asked Stands instead of the run's marks for every
// repair row on a resume, and the books evidence rule it must answer with is the
// opswriter's (RowApplied), which the contracts move (flip commit 1) has not
// yet brought under internal/books/writer.
func registerTaskFlip(r *module.Registrar, m *booksModule) {
	probe := memdbProbe(m.memdbSource, time.Now)
	r.Health(memdbCheck(probe))
	r.Hook(memdbWatch(probe, r.Deps().Health, memdbPollInterval))
	r.LogOtherUserData(otherUserLogData)
}

// taskLedgerForManifest is Manifest.TaskLedger: every kind the task lists
// carry, with the predicate on, and none otherwise.
func taskLedgerForManifest() []tasks.Kind {
	if !tasksFlipOn() {
		return nil
	}
	return append([]tasks.Kind(nil), taskLedger()...)
}
