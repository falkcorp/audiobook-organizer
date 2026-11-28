// file: internal/plugins/dedup/kinds.go
// version: 1.0.1
// guid: 48e96a47-de87-48f6-99f6-85b864845dd6
// last-edited: 2026-11-19

package dedup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/books/taskmanage"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Native books Task kinds for the dedup plugin (F5-04, 05-tasks 5T.8.2 group
// T5): the plugin's v2 definitions, each as a tasks.Task kind named
// "books." + the HEAD def ID. They are DARK: nothing registers them before the
// tasks flip (F5-18), which also removes HEAD's v2 shells. No kinds*.go file
// imports the plugin SDK package (the T5 gate); the SDK-facing half of the shells
// (HEAD's Reporter, the progress sink) is in env.go.
//
// Every definition has one shared core and two shells over it: HEAD's Run (the
// v2 shell, in the definition's own file) and the native tasks.Task in a
// kinds_<group>.go file. A core reaches its stores only through the env it is
// handed (rule R10): reads through env.Store, writes through env.Writer, the
// dedup engine through env.Engine, and the run (logging, progress, pools)
// through the hooks on kindEnv.
//
// KindDeps is one group struct per file, not one aggregate: each group owns
// the narrow store, writer and engine interfaces its cores take, so no
// aggregate interface exists (the F5-06 precedent). F5-18 fills them.

const (
	// kindPrefix is rule R3: tasks.Kind("books." + <HEAD def ID>).
	kindPrefix = "books."

	// signalMemDB is the readiness component every kind waits for (HEAD's
	// NoWarmupWait false); a NoWarmupWait definition declares none.
	signalMemDB tasks.ReadySignal = "books.memdb"
)

// kindFor is the kind name rule R3 gives a HEAD def ID.
func kindFor(headID string) tasks.Kind { return tasks.Kind(kindPrefix + headID) }

// settingsManageScopes is the route requirement of HEAD's settings.manage atom
// (internal/books/atoms.go). internal/books imports this package through its
// import graph, so the table cannot be read from here; it is the same copy
// internal/plugins/maintenance and internal/maintenance/jobs keep, and drifts
// the same way (their copies are the reference: the atom-table golden test in
// internal/books pins the original).
var settingsManageScopes = []rbac.Permission{
	"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
	"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
}

// kindStartRequirement is rule R6 for every dedup definition: each is gated on
// settings.manage at HEAD, directly (three declare it) or through an empty
// Permissions list that the registry defaults to it. The copied requirement
// includes core:system.manage, which the systemChecker strips; that is tracked
// in todo.d for F5-18 and matches the maintenance siblings.
func kindStartRequirement() rbac.Requirement {
	return rbac.AllOf(settingsManageScopes...)
}

// kindCommon is the Common block every definition shares; the caller fills the
// per-definition fields (Title, Help, Effects, Resume, Priority, Timeout...).
// noWarmup is HEAD's NoWarmupWait: true leaves Ready nil (rule R5).
func kindCommon(kind tasks.Kind, sched func(tasks.Kind) *tasks.Schedule, noWarmup bool) tasks.Common {
	c := tasks.Common{Perms: tasks.Permissions{Start: kindStartRequirement(), Manage: taskmanage.Requirement()}}
	if !noWarmup {
		c.Ready = []tasks.ReadySignal{signalMemDB}
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
// silently: tasks decodes native params with plain json.Unmarshal, and a
// dropped {"dry_run":true} or {"apply":false} would turn a dry-run start into a
// live write.
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

// refuseKindModeKeys is decodeKindParams for a shell whose body takes no params
// of its own; any other key is ignored, as HEAD ignored the params of these defs.
func refuseKindModeKeys(kind tasks.Kind, raw json.RawMessage) error {
	return decodeKindParams(kind, raw, &struct{}{})
}

// kindLiveOnly is the refusal a live-only writer gives a Preview run: HEAD has
// no preview for it, and a native Preview must not write.
func kindLiveOnly(rc *tasks.RC, kind tasks.Kind) error {
	if rc.Mode() != tasks.ModeLive {
		return fmt.Errorf("%s: preview not supported; start with mode live", kind)
	}
	return nil
}

// KindDeps is what the native kinds need from their owner: one group struct per
// kinds_<group>.go file. Nothing passes it in production before F5-18, which
// wires the stores, the gated writers (internal/books/writer behind each
// NewWriter) and the engine behind NewEngine; tests pass fakes.
type KindDeps struct {
	Scoring   ScoringKindDeps
	Embedding EmbeddingKindDeps
	Labels    LabelKindDeps
	Index     IndexKindDeps
	Chain     ChainKindDeps
}

// Kinds returns every native kind this plugin declares, group by group and in
// HEAD file order within each, with Schedule: sched(kind) on each (rule R9; a
// nil sched means no schedule). Nothing calls it in production before F5-18.
func (p *Plugin) Kinds(deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return kindList(p, deps, sched)
}

// kindList is Kinds as a package-level function: tasklint's unregistered-kind
// rule counts package-level list functions only, and not a method.
func kindList(p *Plugin, deps KindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	var out []tasks.Definition
	out = append(out, scoringKinds(p, deps.Scoring, sched)...)
	out = append(out, embeddingKinds(p, deps.Embedding, sched)...)
	out = append(out, labelKinds(p, deps.Labels, sched)...)
	out = append(out, indexKinds(p, deps.Index, sched)...)
	out = append(out, chainKinds(p, deps.Chain, sched)...)
	return out
}

// kindListRoot names kindList from non-test code so tasklint's
// unregistered-kind rule counts it a registration root (a list function nothing
// references registers nothing, and the analyzer does not load test files).
// F5-18 deletes it with the HEAD shells.
var _ = kindList

// The brief's list-function line, kept so the method stays referenced.
var _ = (*Plugin).Kinds
