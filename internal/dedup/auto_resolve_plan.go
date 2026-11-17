// file: internal/dedup/auto_resolve_plan.go
// version: 1.1.0
// guid: 0e8b3d57-a6c1-4f92-b5d4-7c19e2a8f630
// last-edited: 2026-11-17

package dedup

import (
	"slices"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The dry-run plan of dedup.auto-resolve.
//
// A dry run used to keep a count and fifty samples with titles, and the op
// wrapper logged only the counts, so nobody could review the exact list a run
// would merge before the flag was ever switched on. The plan is that list: one
// entry per eligible pair, in the order apply visits them (ascending candidate
// id), stored as the op's result and read back through GET /operations/:id/result.
//
// It carries ids, scores and guard names only. Titles stay out of it (a result
// is served wherever the op is readable and this repo's logs carry no titles),
// and so does Signal.Evidence, which is free text built from file and tag values.

// AutoResolveRuleID names the version of the Tier-1 rule autoResolveEligible
// implements. A plan is only comparable to another plan, or to what an apply run
// did, under the same rule: bump it whenever a guard is added, removed or
// re-weighted.
const AutoResolveRuleID = "dedup.auto-resolve/tier1-certain/v1"

// The stored plan is bounded. An entry is up to about 1 KB of JSON (a breakdown
// carries up to 13 signal kinds), so the two caps hold the stored result to about
// 2.5 MB at the very most, usually far less. Pairs beyond a cap are counted
// (PlanTruncated, RefusedTruncated), never silently dropped. The default apply
// run takes 200 merges, so the plan cap covers ten canary runs.
const (
	autoResolvePlanCap    = 2000
	autoResolveRefusedCap = 500
)

// AutoResolvePlanSignal is one signal of a pair's stored breakdown as the rule
// evaluated it.
type AutoResolvePlanSignal struct {
	Kind       string  `json:"kind"`
	Raw        float64 `json:"raw"`
	Confidence float64 `json:"confidence"`
}

// AutoResolvePlanEntry is one candidate the dry run decided: a pair a run would
// merge (Plan) or one a guard refused (Refused).
type AutoResolvePlanEntry struct {
	// Rank is the 1-based position among eligible pairs in the order apply visits
	// them (zero on a refused entry). Rank is the visit order, not a promise: an
	// apply run stops after max_merges SUCCESSFUL merges and a pair it skips or
	// that fails does not count, so it can merge a pair ranked past max_merges.
	// See AutoResolveApplyDescription.
	Rank        int     `json:"rank,omitempty"`
	CandidateID int64   `json:"candidate_id"`
	BookAID     string  `json:"book_a_id"`
	BookBID     string  `json:"book_b_id"`
	Band        string  `json:"band"`
	Score       float64 `json:"score"`
	Formula     string  `json:"formula,omitempty"`
	// Signals is the stored breakdown; PrimaryKinds is the subset the rule counts
	// as corroboration (DistinctAutoResolvePrimaryKinds).
	Signals      []AutoResolvePlanSignal `json:"signals"`
	PrimaryKinds []string                `json:"primary_kinds"`
	// Guard names the rule that decided the pair (guard* constants): for a merge
	// entry the corroboration clause that admitted it, for a refused one the guard
	// that stopped it. Reason is the same text an apply run's sample carries; Rule
	// is AutoResolveRuleID.
	Guard  string `json:"guard"`
	Reason string `json:"reason"`
	Rule   string `json:"rule"`
	// SharesBookWithRank, when non-zero, is the earlier entry that names one of
	// this entry's books. Apply merges a book at most once, so this entry would be
	// skipped if that earlier one merges first.
	SharesBookWithRank int `json:"shares_book_with_rank,omitempty"`
}

// autoResolvePlan accumulates the plan during a dry run. Every method is a no-op
// when disabled, so the apply path does nothing and allocates nothing.
type autoResolvePlan struct {
	enabled bool
	// planCap and refusedCap bound the two lists.
	planCap, refusedCap int

	entries   []AutoResolvePlanEntry
	truncated int
	eligible  int
	// plannedByGuard counts EVERY admitted pair by the clause that admitted it,
	// listed or not, so the owner sees how many planned merges rest on the
	// whole-book-signature label (guardCorroborationLabel) however long the plan.
	plannedByGuard map[string]int

	refused          []AutoResolvePlanEntry
	refusedTruncated int
	byGuard          map[string]int

	// owner maps a book id to the first entry that names it: an in-memory check,
	// no reads.
	owner map[string]int
}

func newAutoResolvePlan(enabled bool, planCap, refusedCap int) *autoResolvePlan {
	p := &autoResolvePlan{enabled: enabled, planCap: planCap, refusedCap: refusedCap}
	if enabled {
		p.byGuard = map[string]int{}
		p.plannedByGuard = map[string]int{}
		p.owner = map[string]int{}
	}
	return p
}

// entry builds the stored view of one candidate from the row as the rule saw it.
func (p *autoResolvePlan) entry(c database.DedupCandidate, d autoResolveDecision) AutoResolvePlanEntry {
	e := AutoResolvePlanEntry{
		CandidateID:  c.ID,
		BookAID:      c.EntityAID,
		BookBID:      c.EntityBID,
		Band:         c.Band,
		Guard:        d.Guard,
		Reason:       d.Reason,
		Rule:         AutoResolveRuleID,
		Signals:      []AutoResolvePlanSignal{},
		PrimaryKinds: []string{},
	}
	if sb := c.ScoreBreakdown; sb != nil {
		e.Score, e.Formula = sb.Score, sb.Formula
		for _, sig := range sb.Signals {
			e.Signals = append(e.Signals, AutoResolvePlanSignal{Kind: string(sig.Kind), Raw: sig.Raw, Confidence: sig.Confidence})
		}
		for _, k := range DistinctAutoResolvePrimaryKinds(sb.Signals) {
			e.PrimaryKinds = append(e.PrimaryKinds, string(k))
		}
	}
	return e
}

// refuse records a candidate a guard stopped. Every refusal is counted by guard;
// the first refusedCap are also listed.
func (p *autoResolvePlan) refuse(c database.DedupCandidate, d autoResolveDecision) {
	if !p.enabled {
		return
	}
	p.byGuard[d.Guard]++
	if len(p.refused) >= p.refusedCap {
		p.refusedTruncated++
		return
	}
	p.refused = append(p.refused, p.entry(c, d))
}

// add records a candidate the rule admitted, in the order apply visits them.
func (p *autoResolvePlan) add(c database.DedupCandidate, d autoResolveDecision) {
	if !p.enabled {
		return
	}
	p.eligible++
	p.plannedByGuard[d.Guard]++
	if len(p.entries) >= p.planCap {
		p.truncated++
		return
	}
	e := p.entry(c, d)
	e.Rank = p.eligible
	for _, id := range []string{c.EntityAID, c.EntityBID} {
		if first, seen := p.owner[id]; seen {
			if e.SharesBookWithRank == 0 || first < e.SharesBookWithRank {
				e.SharesBookWithRank = first
			}
		} else {
			p.owner[id] = e.Rank
		}
	}
	p.entries = append(p.entries, e)
}

// finish copies the plan into the result. It leaves an apply run's result as it
// was.
func (p *autoResolvePlan) finish(res *AutoResolveResult, maxMerges int) {
	if !p.enabled {
		return
	}
	res.MaxMerges = maxMerges
	res.Plan = p.entries
	res.PlanTruncated = p.truncated
	res.PlannedByGuard = p.plannedByGuard
	res.Refused = p.refused
	res.RefusedTruncated = p.refusedTruncated
	res.RefusedByGuard = p.byGuard
}

// AutoResolveNotEvaluated lists what a dry run does not evaluate. An apply run
// does them at merge time, so the plan is a superset of what a run merges.
var AutoResolveNotEvaluated = []string{
	"merge-time re-check of the candidate row (a pair pinned or decided since the list was taken)",
	"refusal of a pair with a file under the active iTunes library",
	"skip of a pair whose book an earlier merge in the same run already soft-deleted (see shares_book_with_rank)",
}

// AutoResolveApplyDescription states exactly what an apply run does with respect
// to a dry run's plan. It is stored in the plan so a reviewer reads it next to the
// ranks. The apply loop is unchanged by this: it counts only successful merges
// against max_merges, and it is not bound to a reviewed plan.
const AutoResolveApplyDescription = "An apply run is not bound to this plan: it re-reads the pending candidates when it runs " +
	"and visits the eligible pairs in plan order. It stops after max_merges SUCCESSFUL merges; a pair it skips " +
	"(not_evaluated below) or that fails does not count toward the cap, so it can merge pairs ranked past " +
	"max_merges that nobody reviewed here."

// AutoResolveDryRunReport is the op result of a dry run: counts and the plan,
// without titles. It is what the owner reviews before switching apply on.
type AutoResolveDryRunReport struct {
	RuleID    string `json:"rule_id"`
	DryRun    bool   `json:"dry_run"`
	MaxMerges int    `json:"max_merges"`
	Checked   int    `json:"checked"`
	Eligible  int    `json:"eligible"`
	// PlanCap and RefusedCap are the most entries stored in each list;
	// PlanTruncated and RefusedTruncated are how many came after them.
	// RefusedByGuard counts every refusal, listed or not.
	PlanCap          int                    `json:"plan_cap"`
	PlanTruncated    int                    `json:"plan_truncated"`
	Plan             []AutoResolvePlanEntry `json:"plan"`
	RefusedCap       int                    `json:"refused_cap"`
	RefusedTruncated int                    `json:"refused_truncated"`
	Refused          []AutoResolvePlanEntry `json:"refused"`
	RefusedByGuard   map[string]int         `json:"refused_by_guard"`
	// PlannedByGuard counts every pair the plan would merge by the corroboration
	// clause that admitted it (listed or not). corroboration_signature_label is
	// the pairs admitted only by a labelled example's free-text reason; see
	// todo.d for why that clause must be tightened before apply is enabled.
	PlannedByGuard map[string]int `json:"planned_by_guard"`
	NotEvaluated   []string       `json:"not_evaluated"`
	// ApplyDescription is AutoResolveApplyDescription.
	ApplyDescription string `json:"apply_description"`
}

// NewAutoResolveDryRunReport builds the op result from a dry run's result.
func NewAutoResolveDryRunReport(res AutoResolveResult) AutoResolveDryRunReport {
	plan, refusedList := res.Plan, res.Refused
	if plan == nil {
		plan = []AutoResolvePlanEntry{}
	}
	if refusedList == nil {
		refusedList = []AutoResolvePlanEntry{}
	}
	refused := res.RefusedByGuard
	if refused == nil {
		refused = map[string]int{}
	}
	planned := res.PlannedByGuard
	if planned == nil {
		planned = map[string]int{}
	}
	return AutoResolveDryRunReport{
		RuleID: AutoResolveRuleID, DryRun: res.DryRun, MaxMerges: res.MaxMerges,
		Checked: res.Checked, Eligible: res.Eligible,
		PlanCap: autoResolvePlanCap, PlanTruncated: res.PlanTruncated, Plan: plan,
		RefusedCap: autoResolveRefusedCap, RefusedTruncated: res.RefusedTruncated, Refused: refusedList,
		RefusedByGuard: refused, PlannedByGuard: planned, NotEvaluated: slices.Clone(AutoResolveNotEvaluated),
		ApplyDescription: AutoResolveApplyDescription,
	}
}
