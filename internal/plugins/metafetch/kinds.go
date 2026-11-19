// file: internal/plugins/metafetch/kinds.go
// version: 1.0.0
// guid: 31603ffe-22e7-4c6d-beec-975c42b948ca
// last-edited: 2026-11-19

package metafetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Native books Task kinds for the metafetch plugin (F5-05, 05-tasks 5T.8.2
// group T5): its three v2 definitions, each as a tasks.Task kind named
// "books." + the HEAD def ID. They are DARK: nothing registers them before the
// tasks flip (F5-18), which also removes HEAD's v2 shells. This file does not
// import pkg/plugin/sdk (the T5 gate); the sdk-facing half of the shells
// (HEAD's Reporter, the progress sink) is in env.go.
//
// Every definition has one shared core and two shells over it: HEAD's Run (the
// v2 shell, in the definition's own file) and the native tasks.Task below. A
// core reaches its stores only through the env it is handed (rule R10): reads
// through env.Store, writes through env.Writer, the run (logging, progress,
// pools, checkpoints) through the hooks on kindEnv.

const (
	// signalMemDB is the readiness component every kind waits for (HEAD's
	// NoWarmupWait false).
	signalMemDB tasks.ReadySignal = "books.memdb"
)

// settingsManageScopes is the route requirement of HEAD's settings.manage atom
// (internal/books/atoms.go). internal/books reaches this package through its
// import graph, so the table cannot be read from here; it is the same copy
// internal/plugins/maintenance and internal/plugins/dedup keep.
var settingsManageScopes = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

// kindStartRequirement is rule R6 for every metafetch definition: none declares
// Permissions, so each takes the registry's default, settings.manage.
func kindStartRequirement() rbac.Requirement {
	return rbac.AllOf(settingsManageScopes...)
}

// kindCommon is the Common block every definition shares; the caller fills the
// per-definition fields. Every metafetch definition leaves NoWarmupWait false,
// so Ready is the memdb signal (rule R5).
func kindCommon(kind tasks.Kind, sched func(tasks.Kind) *tasks.Schedule) tasks.Common {
	c := tasks.Common{
		Perms: tasks.Permissions{Start: kindStartRequirement()},
		Ready: []tasks.ReadySignal{signalMemDB},
	}
	if sched != nil {
		c.Schedule = sched(kind)
	}
	return c
}

// res names books resources for an Effects value, from the pkg/ops constants
// only (a literal name is a drift the resource-vocabulary test cannot see).
func res(rs ...ops.Resource) []entity.Ref {
	out := make([]entity.Ref, len(rs))
	for i, r := range rs {
		out[i] = tasks.Res("books", string(r))
	}
	return out
}

// kindModeKeys are the param keys that would carry the run's mode: dry_run,
// dryRun and apply. The mode comes from the start request (05-tasks 5T.3.7),
// never from params, so a native shell refuses them rather than dropping them
// silently: a dropped {"dry_run":true} would turn a dry-run start into a live
// write.
var kindModeKeys = []string{"dry_run", "dryRun", "apply"}

// decodeKindParams is the native shells' params step: a mode key is refused
// (case-insensitively, as encoding/json matches field names), then the rest is
// decoded into v. Empty and null params decode to the zero value; params that
// are not a JSON object are refused.
func decodeKindParams(kind tasks.Kind, raw json.RawMessage, v any) error {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(t, &keys); err != nil {
		return fmt.Errorf("%s: decode params: %w", kind, err)
	}
	for key := range keys {
		for _, banned := range kindModeKeys {
			if strings.EqualFold(key, banned) {
				return fmt.Errorf("%s: params carry %q; the run's mode comes from the start request, not params", kind, key)
			}
		}
	}
	if err := json.Unmarshal(t, v); err != nil {
		return fmt.Errorf("%s: decode params: %w", kind, err)
	}
	return nil
}

// decodeNativeParams is decodeKindParams into a fresh P.
func decodeNativeParams[P any](kind tasks.Kind, raw json.RawMessage) (P, error) {
	var v P
	err := decodeKindParams(kind, raw, &v)
	return v, err
}

// kindStore is every read the three cores take: pcache-sweep's raw walk and
// book lookup, calibrate-scoring's book walk, field states and book files, and
// asin-backfill's readers. It has no write method (rule R10).
type kindStore interface {
	pcacheSweepReads
	asinBackfillReads
	pluginStore
}

// kindWriter is every write the three cores make, each named for the HEAD store
// method it wraps (rule R10): pcache-sweep's DeleteRawBatch, and asin-backfill's
// ModifyBook, RecordMetadataChange and the two SetRaw key families. calibrate-scoring
// writes nothing.
type kindWriter interface {
	pcacheSweepWriter
	asinBackfillWriter
}

// KindDeps is what the native kinds need from their owner. Nothing passes it in
// production before F5-18, which wires the store, the gated writer
// (internal/books/writer behind NewWriter), the metafetch service's candidate
// cache read and the Audible client factory; tests pass fakes.
type KindDeps struct {
	// Store is every read the kinds take.
	Store kindStore
	// NewWriter builds the run's writer over its gated tasks.Writer. A Preview
	// run never asks for one.
	NewWriter func(tasks.Writer) kindWriter
	// Candidates is the persisted candidate-cache read (calibrate-scoring):
	// metafetch.Service.GetCachedCandidates.
	Candidates calibrateCandidates
	// Scoring is the process's metadata-scoring config (calibrate-scoring);
	// HEAD read config.AppConfig.MetadataScoring.
	Scoring func() config.MetadataScoringConfig
	// NewAudible builds the Audible client per book (asin-backfill).
	NewAudible func() audibleIdentitySearcher
	// BuildSourceChain, when set, runs once at the start of an asin-backfill
	// run, before any client is built: HEAD's metafetch.Service.BuildSourceChain,
	// which installs the configured Audible budget into providerhttp.
	BuildSourceChain func()
}

// writerFor builds the run's writer, refusing a run whose owner declared no
// factory (or one that returned nothing).
func (d KindDeps) writerFor(kind tasks.Kind, rc *tasks.RC) (kindWriter, error) {
	if err := errNeed(kind, "NewWriter", d.NewWriter != nil); err != nil {
		return nil, err
	}
	w := d.NewWriter(rc.Writer())
	if w == nil {
		return nil, errNeedWriter(kind)
	}
	return w, nil
}

// Kinds returns every native kind this plugin declares, in HEAD file order of
// its OperationDefs, with Schedule: sched(kind) on each (rule R9; a nil sched
// means no schedule). Nothing calls it in production before F5-18.
func (p *Plugin) Kinds(deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return kindList(p, deps, sched)
}

// kindList is Kinds as a package-level function: tasklint's unregistered-kind
// rule counts package-level list functions only, and not a method.
func kindList(p *Plugin, deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.calibrateScoringNative(deps, sched),
		p.asinBackfillNative(deps, sched),
		p.pcacheSweepNative(deps, sched),
	}
}

// kindListRoot names kindList from non-test code so tasklint's
// unregistered-kind rule counts it a registration root. F5-18 deletes it with
// the HEAD shells.
var _ = kindList

// calibrateScoringNative is the native metafetch.calibrate-scoring kind: a
// read-only report, so a Preview and a Live run do the same and write nothing.
func (p *Plugin) calibrateScoringNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(calibrateScoringKind, sched)
	c.Title = "Calibrate metadata scoring (report only)"
	c.Help = calibrateScoringHelp
	// ReadOnly: HEAD declares no Writes and the body writes nothing (it reads the
	// book walk, the field states, the book files and the candidate cache).
	c.Effects = tasks.ReadOnly()
	// HEAD ResumeDrop: no checkpoint; a re-run replays from the start.
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key(string(calibrateScoringKind))
	c.Timeout = 30 * time.Minute
	return tasks.Task(calibrateScoringKind, tasks.TaskSpec[json.RawMessage, calibrationReport]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (calibrationReport, error) {
			params, err := decodeNativeParams[calibrateScoringParams](calibrateScoringKind, raw)
			if err != nil {
				return calibrationReport{}, err
			}
			if err := errNeed(calibrateScoringKind, "Store", d.Store != nil); err != nil {
				return calibrationReport{}, err
			}
			if err := errNeed(calibrateScoringKind, "Candidates", d.Candidates != nil); err != nil {
				return calibrationReport{}, err
			}
			if err := errNeed(calibrateScoringKind, "Scoring", d.Scoring != nil); err != nil {
				return calibrationReport{}, err
			}
			return calibrateScoringCore(rc.Context(), params, calibrateScoringEnv{
				kindEnv: nativeEnv(rc), Store: d.Store, Candidates: d.Candidates, Scoring: d.Scoring,
			})
		},
	})
}

// asinBackfillNative is the native metafetch.asin-backfill kind. Preview is
// HEAD's dry run (the proposals, no write, no marker, no answer-cache row);
// Live writes the ASIN and ISBN-13 through the run's writer.
func (p *Plugin) asinBackfillNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(asinBackfillKind, sched)
	c.Title = "Backfill missing ASINs and ISBNs from Audible"
	c.Help = asinBackfillHelp
	// Writes: HEAD declares {books}; the body also writes, through the writer:
	// ModifyBook (the whole book row of internal/maintenance/resources: books,
	// authors, metadata_cache, indexes), the shared answer cache pcache: rows
	// through CachedIdentitySearch (metadata_cache) and the asinbackfill:miss:
	// and asinbackfill:isbnmiss: no-match markers (asin_misses). The metadata
	// history rows RecordMetadataChange writes have no Table() row; they ride
	// with the book (the HEAD declaration covered them under books).
	c.Effects = tasks.Writes(res(ops.ResBooks, ops.ResAuthors, ops.ResMetadataCache, ops.ResIndexes, ops.ResASINMisses)...)
	// HEAD ResumeRestart with a page cursor: the registry merges the checkpoint
	// into the params, so HEAD reads it on restart (R5: Continue).
	c.Resume = tasks.ResumeContinue
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books." + asinBackfillOpID)
	c.Timeout = 24 * time.Hour
	// HEAD DependsOn catalog.harvest-authors and identification.advance (they
	// share Audible's token bucket), mapped by rule R3.
	c.NotWhile = []tasks.Kind{"books.catalog.harvest-authors", "books.identification.advance"}
	return tasks.Task(asinBackfillKind, tasks.TaskSpec[json.RawMessage, asinBackfillResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (asinBackfillResult, error) {
			params, err := decodeNativeParams[asinBackfillNativeParams](asinBackfillKind, raw)
			if err != nil {
				return asinBackfillResult{}, err
			}
			if err := errNeed(asinBackfillKind, "Store", d.Store != nil); err != nil {
				return asinBackfillResult{}, err
			}
			if err := errNeed(asinBackfillKind, "NewAudible", d.NewAudible != nil); err != nil {
				return asinBackfillResult{}, err
			}
			env := asinBackfillEnv{kindEnv: nativeEnv(rc), Store: d.Store, NewClient: d.NewAudible}
			if env.Live {
				w, err := d.writerFor(asinBackfillKind, rc)
				if err != nil {
					return asinBackfillResult{}, err
				}
				env.Writer = w
			}
			if d.BuildSourceChain != nil {
				d.BuildSourceChain()
			}
			return asinBackfillCore(rc.Context(), params.headParams(), env)
		},
	})
}

// pcacheSweepNative is the native metafetch.pcache-sweep kind. Preview is
// HEAD's dry run (the walk and the accounting, no delete); Live deletes the dead
// rows through the run's writer.
func (p *Plugin) pcacheSweepNative(d KindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(pcacheSweepKind, sched)
	c.Title = "Sweep the shared metadata answer cache"
	c.Help = pcacheSweepHelp
	// Writes found: DeleteRawBatch deletes pcache: rows (the question, product
	// and pointer families), which internal/plugins/maintenance's store-diff
	// vocabulary files under metadata_cache. HEAD declares none. The shared
	// Table() row for a bare DeleteRawBatch is authority_lists, which is the wrong
	// family for pcache.
	c.Effects = tasks.Writes(res(ops.ResMetadataCache)...)
	// HEAD ResumeDrop: an interrupted delete pass is not picked back up.
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books." + pcacheSweepOpID)
	return tasks.Task(pcacheSweepKind, tasks.TaskSpec[json.RawMessage, pcacheSweepResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (pcacheSweepResult, error) {
			if err := decodeKindParams(pcacheSweepKind, raw, &struct{}{}); err != nil {
				return pcacheSweepResult{}, err
			}
			if err := errNeed(pcacheSweepKind, "Store", d.Store != nil); err != nil {
				return pcacheSweepResult{}, err
			}
			env := pcacheSweepEnv{kindEnv: nativeEnv(rc), Store: d.Store}
			if env.Live {
				w, err := d.writerFor(pcacheSweepKind, rc)
				if err != nil {
					return pcacheSweepResult{}, err
				}
				env.Writer = w
			}
			return pcacheSweepCore(rc.Context(), env)
		},
	})
}
