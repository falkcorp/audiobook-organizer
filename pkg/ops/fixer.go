// file: pkg/ops/fixer.go
// version: 1.3.2
// guid: 8628f5c0-14c0-4191-8884-8abe658eabf6
// last-edited: 2026-10-14

package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
)

// FixerSpec plans rows, lets a person approve some of them, then applies
// exactly those, re-checking each against the plan. It is internal/repairs
// lifted into the SDK: Plan and Replan become one Evaluate, the fingerprint
// is computed from the row's declared Inputs, and the apply runs on the Batch
// runner.
type FixerSpec[P any, C any] struct {
	Common // Effects must be Writes(...); a Fixer is never ReadOnly
	// Candidates lists what to look at (cheap: ids and core fields).
	Candidates func(rc *RC, p P) (Source[C], error)
	// Load re-reads one candidate by row id for apply-time re-evaluation.
	Load func(rc *RC, p P, rowID string) (C, bool, error)
	// Evaluate decides one candidate. Read-only: it gets no Writer. It runs at
	// plan time AND at apply time, so the plan and the apply check cannot
	// drift.
	Evaluate func(rc *ItemRC, p P, c C) (Row, error)
	// Apply writes one approved, freshly re-evaluated row through w. Its
	// complete end state must be what row.Proposed says (see Row.Proposed):
	// a resumed run treats a row whose fresh Current already equals Proposed
	// as applied by its earlier attempt and does not call Apply again.
	Apply func(rc *ItemRC, w Writer, p P, row Row) error
	// PartitionBy: rows sharing a key apply in order on one worker. Default
	// Row.ID.
	PartitionBy func(Row) string
	// AfterApply runs once with every applied row id, sorted by row id (the
	// same order with or without a resume), on a context that a cancel does
	// not end.
	AfterApply func(rc *RC, applied []string) error
	// Guards add to the framework guards. Each returns a skip reason or "".
	Guards []Guard
}

// Guard returns a reason to skip a row, or "".
type Guard func(row Row) (skipReason string)

// Row is what a person approves. Inputs is what the fingerprint hashes.
type Row struct {
	ID       string            `json:"id"`
	Subjects []Subject         `json:"subjects,omitempty"`
	Title    string            `json:"title,omitempty"`
	Current  map[string]string `json:"current,omitempty"`
	// Proposed must describe Apply's complete end state, in the same keys
	// and rendering as Current: once Apply has run, a fresh Evaluate of the
	// row returns Current equal to Proposed on every key. A resumed apply
	// relies on it to recognize a row its earlier attempt already wrote in a
	// chunk that never completed (applyRow); a Proposed that leaves out part
	// of what Apply writes would let that part be skipped.
	Proposed  map[string]string `json:"proposed,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Risk      Risk              `json:"risk,omitempty"`
	Class     string            `json:"class,omitempty"`
	Evidence  []string          `json:"evidence,omitempty"`
	Inputs    map[string]string `json:"inputs,omitempty"`
	Hold      string            `json:"hold,omitempty"`
	Retry     bool              `json:"retry,omitempty"`
	OwnerOnly bool              `json:"owner_only,omitempty"`
	State     json.RawMessage   `json:"state,omitempty"`
}

// Fingerprint hashes the row's Inputs as sorted, canonical JSON, plus the
// Hold unless it is a transient one (Retry). Apply refuses a row whose fresh
// fingerprint differs from the planned one.
func (r Row) Fingerprint() string {
	keys := make([]string, 0, len(r.Inputs))
	for k := range r.Inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]string, 0, len(keys)+1)
	for _, k := range keys {
		pairs = append(pairs, [2]string{k, r.Inputs[k]})
	}
	if r.Hold != "" && !r.Retry {
		pairs = append(pairs, [2]string{"\x00hold", r.Hold})
	}
	b, err := json.Marshal(pairs)
	if err != nil { // strings only; cannot fail
		b = []byte(fmt.Sprint(pairs))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// FixerPlan is a Fixer's Preview result: every evaluated row, by id.
type FixerPlan struct {
	Rows []Row `json:"rows"`
}

// FixerApply is the params of a Fixer's Live run: the approved rows of a plan
// as the plan recorded them, and the def's own params. 05-PR9 builds it from
// a plan_ref and the person's selection, and records the approval.
type FixerApply struct {
	PlanRef string          `json:"plan_ref,omitempty"`
	Rows    []Row           `json:"rows"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// FixerResult is a Fixer's Live result. Applied is sorted by row id, so it
// is the same whatever order the workers finished in and whether or not the
// run resumed.
type FixerResult struct {
	Applied []string `json:"applied"`
}

// MarkApplied is the rc.Mark name a Fixer apply records each written row under.
const MarkApplied = "applied"

// Skip reasons a Fixer apply records under skipped:<reason>.
const (
	SkipGone             = "gone"
	SkipHeld             = "held"
	SkipChangedSincePlan = "changed_since_plan"
	SkipOwnerOnly        = "owner_only"
)

// Fixer builds a Fixer definition. Preview runs plan (Candidates, Evaluate);
// Live runs apply the approved rows of a FixerApply.
func Fixer[P any, C any](id string, s FixerSpec[P, C]) Definition {
	b := newSpec(id, KindFixer, s.Common)
	if s.Candidates == nil || s.Load == nil || s.Evaluate == nil || s.Apply == nil {
		b.problem("Fixer needs Candidates, Load, Evaluate and Apply")
	}
	b.batch = func(rc *RC, params json.RawMessage) (*BatchJob, error) {
		if rc.Mode() == Live {
			return fixerApplyJob(id, s, rc, params)
		}
		return fixerPlanJob(id, s, rc, params)
	}
	return b
}

// fixerPlanJob evaluates every candidate and returns the rows as the result.
// A plan always restarts from zero: its rows live in memory until Finish.
func fixerPlanJob[P any, C any](id string, s FixerSpec[P, C], rc *RC, params json.RawMessage) (*BatchJob, error) {
	p, err := decodeParams[P](id, params)
	if err != nil {
		return nil, err
	}
	src, err := s.Candidates(rc, p)
	if err != nil {
		return nil, fmt.Errorf("%s: candidates: %w", id, err)
	}
	var mu sync.Mutex
	var rows []Row
	fromZero := ResumeFromZero
	job, err := buildJob(jobConfig[P, C]{
		id: id, p: p, src: src, resume: &fromZero,
		run: func(irc *ItemRC, p P, c C) error {
			row, err := s.Evaluate(irc, p, c)
			if err != nil {
				return err
			}
			mu.Lock()
			rows = append(rows, row)
			mu.Unlock()
			if row.Hold != "" {
				irc.Skip(SkipHeld)
			}
			return nil
		},
	}, params)
	if err != nil {
		return nil, err
	}
	job.Finish = func(*RC, Summary) (json.RawMessage, error) {
		mu.Lock()
		out := slices.Clone(rows)
		mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return json.Marshal(FixerPlan{Rows: out})
	}
	return job, nil
}

// fixerApplyJob applies the approved rows, partitioned by PartitionBy
// (default Row.ID), each one re-loaded, re-evaluated and fingerprint-checked
// first.
func fixerApplyJob[P any, C any](id string, s FixerSpec[P, C], rc *RC, params json.RawMessage) (*BatchJob, error) {
	env, err := decodeParams[FixerApply](id, params)
	if err != nil {
		return nil, err
	}
	p, err := decodeParams[P](id, env.Params)
	if err != nil {
		return nil, err
	}
	part := s.PartitionBy
	if part == nil {
		part = func(r Row) string { return r.ID }
	}
	src := Source[Row]{Items: env.Rows, Key: func(r Row) string { return r.ID }, Order: Snapshot,
		Total: Known(int64(len(env.Rows)))}
	job, err := buildJob(jobConfig[P, Row]{
		id: id, p: p, src: src, part: part, resume: s.Resume,
		label: func(r Row) string {
			if r.Title != "" {
				return r.Title
			}
			return r.ID
		},
		run: func(irc *ItemRC, p P, planned Row) error {
			ok, err := applyRow(irc, s, p, planned)
			if ok {
				// Recorded through the chunk ledger, not in memory, so rows a
				// run applied before a deploy are still listed (and handed to
				// AfterApply) when the resumed run finishes.
				irc.Mark(MarkApplied)
			}
			return err
		},
	}, env.Params)
	if err != nil {
		return nil, err
	}
	job.Finish = func(rc *RC, sum Summary) (json.RawMessage, error) {
		// Marked sorts: row ids are the item keys, so this is row-id order.
		applied := sum.Marked(MarkApplied)
		if applied == nil {
			applied = []string{}
		}
		if s.AfterApply != nil && len(applied) > 0 {
			if err := s.AfterApply(rc.WithContext(context.WithoutCancel(rc.Context())), applied); err != nil {
				return nil, fmt.Errorf("%s: after apply: %w", id, err)
			}
		}
		return json.Marshal(FixerResult{Applied: applied})
	}
	return job, nil
}

// applyRow applies one planned row. It reports whether the row was written.
func applyRow[P any, C any](irc *ItemRC, s FixerSpec[P, C], p P, planned Row) (bool, error) {
	c, found, err := s.Load(irc.RC, p, planned.ID)
	if err != nil {
		return false, err
	}
	if !found {
		irc.Skip(SkipGone)
		return false, nil
	}
	fresh, err := s.Evaluate(irc, p, c)
	if err != nil {
		return false, err
	}
	refusal := admissible(s.Guards, fresh)
	if fresh.Fingerprint() != planned.Fingerprint() {
		if irc.Resumed() && refusal == "" && alreadyApplied(planned, fresh) {
			// An earlier attempt of this run (before a crash or a deploy)
			// wrote this row in a chunk that never completed, so the ledger
			// has no mark for it and the resume re-runs it. Its fingerprint
			// changed because that write changed it; refusing it as changed
			// since plan would drop a row the run applied from Applied and
			// AfterApply. Report it applied; do not write again.
			//
			// There is no direct evidence that THIS run wrote it (the SDK has
			// no reader for the run's history or journal rows), so the claim
			// is narrowed as far as it goes: only a resumed attempt, only a
			// row whose fingerprint moved, only one still free of a hold, an
			// owner-only flag and every guard, and only when every Proposed
			// value now holds (Row.Proposed is Apply's complete end state).
			// A row someone else changed to the same values between the
			// attempts is indistinguishable and is reported applied too.
			// 05-PR5b (docs/proposals/2026-10-holistic/tasks/05/05-PR5b.md,
			// owner decision 2026-10-13, required before 05-PR7) closes that:
			// it adds a Writer read of this run's own history and requires
			// it here.
			return true, nil
		}
		irc.Skip(SkipChangedSincePlan)
		return false, nil
	}
	if refusal != "" {
		irc.Skip(refusal)
		return false, nil
	}
	w := irc.Writer()
	if w == nil {
		return false, fmt.Errorf("fixer apply of %s has no Writer", planned.ID)
	}
	if err := s.Apply(irc, w, p, fresh); err != nil {
		if errors.Is(err, ErrChangedSincePlan) {
			irc.Skip(SkipChangedSincePlan)
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// admissible returns why fresh may not be applied now -- a hold, an
// owner-only flag (owner grants arrive with the approval record, 05-PR9),
// or the first guard that objects -- or "".
func admissible(guards []Guard, fresh Row) string {
	switch {
	case fresh.Hold != "":
		return SkipHeld
	case fresh.OwnerOnly:
		return SkipOwnerOnly
	}
	for _, g := range guards {
		if reason := g(fresh); reason != "" {
			return reason
		}
	}
	return ""
}

// alreadyApplied reports whether fresh shows the change planned proposes
// already made: the plan proposed a real change (some Proposed value differs
// from the planned Current) and every Proposed field now holds its proposed
// value. A row with nothing proposed is never "already applied".
func alreadyApplied(planned, fresh Row) bool {
	if len(planned.Proposed) == 0 {
		return false
	}
	changes := false
	for k, v := range planned.Proposed {
		got, ok := fresh.Current[k]
		if !ok || got != v {
			return false
		}
		if was, ok := planned.Current[k]; !ok || was != v {
			changes = true
		}
	}
	return changes
}
