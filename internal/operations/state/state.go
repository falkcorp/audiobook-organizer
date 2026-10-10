// file: internal/operations/state/state.go
// version: 1.0.0
// guid: 713db576-1d8b-4ede-a52b-63e91ee2ed85
// last-edited: 2026-10-10

// Package state is the single definition of operation run statuses and what
// each one means. Go helpers and the generated web/src/generated/ops.ts both
// read this table.
//
// Every classifier that used to keep its own status list now asks this
// package: database.isTerminalV2Status / IsTerminalV2Status /
// isResumableV2Status, registry.isTerminalStatus / IsTerminalStatus /
// isInterruptedStatus / IsInterruptedStatus, scheduler.isTerminalOpV2Status,
// childop.IsTerminal, the pause handler's isTerminalOpStatus, and on the web
// side every predicate in web/src/generated/ops.ts (regenerate with
// `make generate-ops`; `make ci` fails when that file is stale).
//
// This package imports only the standard library on purpose: internal/database
// and internal/operations/registry both import it, and both sit in the
// pkg/plugin/sdk closure that tools/cmd/sdkguard ratchets.
package state

import "strings"

// State is a run status string as stored on database.OperationV2Row.Status.
type State string

const (
	Queued              State = "queued"
	WaitingDeps         State = "waiting_deps"
	Running             State = "running"
	Completed           State = "completed"
	Failed              State = "failed"
	Canceled            State = "canceled"
	InterruptedQuiesced State = "interrupted_quiesced"
	InterruptedAsk      State = "interrupted_ask"
	InterruptedDropped  State = "interrupted_dropped"
	// Legacy spellings: still accepted on read (registry.go discardableStatuses,
	// legacyStatusFor), never minted by the v2 registry.
	InterruptedRestart State = "interrupted_restart"
	Interrupted        State = "interrupted"
)

// Props says what a status means.
//
// Terminal: the run will never execute again and its completed_at must be
// stamped. This is the STRICT question, and it is an explicit allowlist, not
// the complement of the live states. Every Go caller that asks it is a writer
// (it stamps completed_at, refuses a cancel, or deletes resume state), so the
// cost of being wrong is asymmetric:
//
//   - Miss a terminal status: the repair stamps fewer rows than it could. The
//     leftover row stays visible in Active Operations and someone reports it.
//   - Miss a LIVE status: the repair stamps a row that the scheduler, the
//     dependency waiter, or the startup resume sweep still owns. That is
//     silent, and the row then reads as finished work that never ran.
//
// So an unknown status is live. interrupted_quiesced (resumable),
// interrupted_ask (waiting on a person) and interrupted_restart (a resume
// marker) are deliberately NOT terminal even though all three stamp
// completed_at at their write site: the retention job's opstate sweep deletes
// an operation's resume state on Terminal, and all three are precisely the
// states whose checkpoint the resume path is about to load.
//
// HoldsSlot: it occupies a worker slot and its exclusive key.
//
// Resumable: the startup resume sweep (registry.resumeAfterStartup, fed by
// database.ListResumableOperationsV2) picks it up. That is queued, running and
// interrupted_quiesced. interrupted_dropped and interrupted_ask are excluded on
// purpose: both are decisions a previous boot's sweep already made
// (ResumePolicy=drop, or "awaiting user decision"); re-including them would
// relitigate a settled outcome on every restart, and interrupted_ask would
// resume without the user ever answering. waiting_deps is not swept either: the
// dependency evaluator re-checks parked rows on its own path.
//
// Retryable / Discardable: the Activity page's Retry and Discard buttons.
// Discardable is also the allowlist registry.Discard hands the store.
//
// AwaitingDecision: the run waits for a person (interrupted_ask).
//
// Settled: a poller has nothing more to wait for in this session: Terminal or
// any interrupted* status. MATCH THE PREFIX, NOT A LIST. The registry mints one
// interrupted_<policy> status per ResumePolicy, and every enumerated copy of
// that family in this codebase drifted behind the side that mints it: the v1
// mirror (legacyStatusFor) left rows at "pending" forever, the pause handler
// counted interrupted_dropped as in progress and inflated the bell badge, and
// two web pollers spun at 1s on interrupted_quiesced. A poller that does not
// recognise a settled status does not fail, it spins while the UI shows the
// operation still running.
//
// Legacy: a spelling still accepted on read but never minted.
type Props struct {
	Terminal         bool
	HoldsSlot        bool
	Resumable        bool
	Retryable        bool
	Discardable      bool
	AwaitingDecision bool
	Settled          bool
	Legacy           bool
}

type entry struct {
	state State
	props Props
}

// table is the one definition. Order is the stable order All returns and the
// generator emits.
var table = []entry{
	{Queued, Props{Resumable: true}},
	{WaitingDeps, Props{}},
	{Running, Props{HoldsSlot: true, Resumable: true}},
	{Completed, Props{Terminal: true, Discardable: true, Settled: true}},
	{Failed, Props{Terminal: true, Retryable: true, Discardable: true, Settled: true}},
	{Canceled, Props{Terminal: true, Retryable: true, Discardable: true, Settled: true}},
	{InterruptedDropped, Props{Terminal: true, Retryable: true, Discardable: true, Settled: true}},
	{InterruptedQuiesced, Props{Resumable: true, Retryable: true, Discardable: true, Settled: true}},
	{InterruptedAsk, Props{Retryable: true, Discardable: true, AwaitingDecision: true, Settled: true}},
	{InterruptedRestart, Props{Retryable: true, Discardable: true, Settled: true, Legacy: true}},
	{Interrupted, Props{Retryable: true, Discardable: true, Settled: true, Legacy: true}},
}

var byName = func() map[string]Props {
	m := make(map[string]Props, len(table))
	for _, e := range table {
		m[string(e.state)] = e.props
	}
	return m
}()

// unknownInterrupted is the fallback for an "interrupted_" status that is not
// in the table: a future ResumePolicy is covered the day it is minted.
var unknownInterrupted = Props{Retryable: true, Discardable: true, Settled: true}

// UnknownInterrupted returns the Props Classify gives an "interrupted_*"
// status that is not in the table. Exported for the TypeScript generator so
// both sides share one fallback.
func UnknownInterrupted() Props { return unknownInterrupted }

// Classify returns the properties of a status string. An unknown string is
// treated as live (not terminal, not settled): the safe direction for a cancel,
// see Props. Any string with the prefix "interrupted_" that is not in the table
// is an interrupted status (Retryable, Discardable, Settled) so a future policy
// is covered the day it is minted. It is not Resumable: the boot sweep reads
// only interrupted_quiesced.
func Classify(s string) Props {
	if p, ok := byName[s]; ok {
		return p
	}
	if strings.HasPrefix(s, "interrupted_") {
		return unknownInterrupted
	}
	return Props{}
}

// All returns every known status in a stable order (for the generator and tests).
func All() []State {
	out := make([]State, len(table))
	for i, e := range table {
		out[i] = e.state
	}
	return out
}

// IsInterrupted reports whether s belongs to the interrupted family: the
// legacy bare "interrupted" plus every "interrupted_*". Prefix-matched for the
// reason given on Props.Settled.
func IsInterrupted(s string) bool {
	return s == string(Interrupted) || strings.HasPrefix(s, "interrupted_")
}

// IsTerminal reports Classify(s).Terminal.
func IsTerminal(s string) bool { return Classify(s).Terminal }

// IsSettled reports Classify(s).Settled.
func IsSettled(s string) bool { return Classify(s).Settled }

// IsResumable reports Classify(s).Resumable.
func IsResumable(s string) bool { return Classify(s).Resumable }

// WithProp returns, in table order, every known status for which pick returns
// true. The registry builds its Discard allowlist from it.
func WithProp(pick func(Props) bool) []string {
	var out []string
	for _, e := range table {
		if pick(e.props) {
			out = append(out, string(e.state))
		}
	}
	return out
}
