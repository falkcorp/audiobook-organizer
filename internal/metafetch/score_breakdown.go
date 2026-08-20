// file: internal/metafetch/score_breakdown.go
// version: 1.0.0
// guid: 6c81e35a-2b47-4d19-90fa-7e5c3d81b026
// last-edited: 2026-08-20

package metafetch

import (
	"fmt"
	"math"
)

// Score breakdown types for the review UI's evidence panel.
//
// The metadata score is NOT a weighted sum. It is
//
//	(base × compilationPenalty × lengthPenalty) + richMetadataBonus
//
// a mixed multiplicative/additive pipeline. A multiplicative factor has no
// "share of the total", so this deliberately does not expose weights: shaping it
// that way would let the frontend draw a stacked contribution bar whose segments
// sum to nothing meaningful, which reads as complete while being false. The
// dedup lane's DedupSignal shape is genuinely a weighted sum and keeps its bar;
// this one is an ordered replay. See docs/evidence-panel-audit.md.
//
// These types mirror WaterfallStep / WaterfallEvidence in
// web/src/components/review/evidence/types.ts. Keep the JSON tags and the Op
// vocabulary in sync with that file.

// Operations a ScoreStep can apply to the running total.
const (
	ScoreOpBase     = "base"
	ScoreOpMultiply = "multiply"
	ScoreOpAdd      = "add"
	// ScoreOpReplace overwrites the running total instead of adjusting it. The
	// LLM reranker does exactly this -- it does not scale or offset the pipeline
	// score, it substitutes its own judgement, rescaled into the window the
	// surrounding candidates occupy. Forcing that into a multiply (by
	// new/old) would be arithmetically equivalent but a lie about what
	// happened, and would show a meaningless factor like "x 3.71".
	ScoreOpReplace = "replace"
)

// ScoreStep is one operation in the scoring pipeline, recorded as it is applied.
type ScoreStep struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Op is one of ScoreOpBase, ScoreOpMultiply, ScoreOpAdd.
	Op string `json:"op"`
	// Operand is the base value, the multiplier, or the addend, per Op.
	Operand float64 `json:"operand"`
	// Running is the total AFTER this step. Recomputing it from Operand is what
	// RecomposeScore does — the field is for display, never for verification.
	Running float64 `json:"running"`
	Detail  string  `json:"detail,omitempty"`
	// Capped marks an operand clamped by a configured cap.
	Capped bool `json:"capped,omitempty"`
}

// ScoreBreakdown is a score together with the ordered operations that produced
// it. Score is the value the pipeline actually acts on; Steps must replay to it.
type ScoreBreakdown struct {
	Score float64     `json:"score"`
	Steps []ScoreStep `json:"steps"`
}

// RecomposeScore replays the steps from the base and returns the resulting
// total, ignoring each step's stored Running.
//
// This exists so "the breakdown explains the score" is a property that can be
// TESTED rather than assumed. Passing the golden fixtures only proves the cases
// someone already wrote down still agree; recomposition holds for any input. If
// it ever fails, the breakdown is annotations sitting near the score rather than
// the arithmetic that produced it, and must not be presented as a derivation.
func RecomposeScore(steps []ScoreStep) float64 {
	total := 0.0
	for _, st := range steps {
		switch st.Op {
		case ScoreOpBase:
			total = st.Operand
		case ScoreOpMultiply:
			total *= st.Operand
		case ScoreOpAdd:
			total += st.Operand
		case ScoreOpReplace:
			total = st.Operand
		}
	}
	return total
}

// IsConsistent reports whether the steps reproduce the score within epsilon.
// An empty breakdown is never consistent: a zero score with no steps means
// "nothing was recorded", not "zero was proven".
func (b ScoreBreakdown) IsConsistent(epsilon float64) bool {
	if len(b.Steps) == 0 {
		return false
	}
	return math.Abs(RecomposeScore(b.Steps)-b.Score) <= epsilon
}

// scoreRecorder accumulates a running score together with the operations that
// produced it.
//
// It OWNS the score rather than sitting alongside a separate variable. That is
// deliberate: the alternative -- leaving `score *= f` in place and adding a
// parallel `rec.note(f)` call -- writes every factor twice, and the two copies
// drift the first time someone tunes one of them. Here a factor cannot be
// applied without being recorded, because applying it IS recording it.
type scoreRecorder struct {
	score float64
	steps []ScoreStep
}

func newScoreRecorder(base float64, label, detail string) *scoreRecorder {
	return &scoreRecorder{
		score: base,
		steps: []ScoreStep{{
			ID: "base", Label: label, Op: ScoreOpBase,
			Operand: base, Running: base, Detail: detail,
		}},
	}
}

// mul applies a multiplicative factor. Factors of exactly 1 are applied but not
// recorded: they are identity, so recomposition is unaffected, and a panel row
// reading "x 1.00" is noise rather than evidence.
func (sr *scoreRecorder) mul(id, label string, factor float64, detail string) {
	sr.score *= factor
	if factor == 1 {
		return
	}
	sr.steps = append(sr.steps, ScoreStep{
		ID: id, Label: label, Op: ScoreOpMultiply,
		Operand: factor, Running: sr.score, Detail: detail,
	})
}

// add applies an additive term, recording it only when non-zero.
func (sr *scoreRecorder) add(id, label string, term float64, detail string, capped bool) {
	sr.score += term
	if term == 0 {
		return
	}
	sr.steps = append(sr.steps, ScoreStep{
		ID: id, Label: label, Op: ScoreOpAdd,
		Operand: term, Running: sr.score, Detail: detail, Capped: capped,
	})
}

// mulResult records a multiplicative layer whose RESULT is known but whose
// factor is not, e.g. transcriptionBoost, which applies a private chain of
// multiplications and returns only the product.
//
// The score is set to `result` exactly rather than to prev*factor. That
// ordering matters: the totals here are pinned bit-for-bit by golden fixtures
// and feed a sort and a threshold comparison, so the recorded score must be the
// number the untouched code produced. The displayed factor is derived for the
// panel only, which leaves recomposition off by at most one ulp of a division
// -- far inside the 1e-12 tolerance, and never inside the score itself.
func (sr *scoreRecorder) mulResult(id, label string, result float64, detail string) {
	prev := sr.score
	sr.score = result
	if prev == result {
		return
	}
	factor := 1.0
	if prev != 0 {
		factor = result / prev
	}
	sr.steps = append(sr.steps, ScoreStep{
		ID: id, Label: label, Op: ScoreOpMultiply,
		Operand: factor, Running: result, Detail: detail,
	})
}

// replace substitutes the running total outright. See ScoreOpReplace.
func (sr *scoreRecorder) replace(id, label string, value float64, detail string) {
	sr.score = value
	sr.steps = append(sr.steps, ScoreStep{
		ID: id, Label: label, Op: ScoreOpReplace,
		Operand: value, Running: value, Detail: detail,
	})
}

// adopt appends already-recorded steps (e.g. from
// ApplyNonBaseAdjustmentsWithBreakdown) after the base step this recorder
// started with, skipping their duplicate base entry.
func (sr *scoreRecorder) adopt(steps []ScoreStep, score float64) {
	for _, st := range steps {
		if st.Op == ScoreOpBase {
			continue
		}
		sr.steps = append(sr.steps, st)
	}
	sr.score = score
}

func (sr *scoreRecorder) breakdown() *ScoreBreakdown {
	return &ScoreBreakdown{Score: sr.score, Steps: sr.steps}
}

// baseTierLabel names the scorer that produced the base score. The tier is
// carried into the panel because "Title/author match" is simply false on the
// embedding path -- the base there is a vector cosine, not word overlap, and
// mislabelling it would misinform exactly the reviewer who is trying to work
// out why a candidate scored the way it did.
func baseTierLabel(tier string) string {
	switch tier {
	case "", "f1":
		return "Title/author match"
	case "embedding":
		return "Semantic similarity"
	default:
		return "Base score (" + tier + ")"
	}
}

func baseTierDetail(tier string) string {
	switch tier {
	case "", "f1":
		return "Fuzzy F1 overlap between the search title/author and this result."
	case "embedding":
		return "Cosine similarity between the book's and the result's embedding vectors."
	default:
		return "Base score from the " + tier + " scorer."
	}
}

// durationStepDetail explains the runtime comparison in the reviewer's terms.
// An unknown duration is never treated as evidence -- the multiplier is 1.0 and
// the step is therefore not recorded at all -- but when it IS recorded the row
// should say which way it cut.
func durationStepDetail(bookSec, candSec int) string {
	if bookSec <= 0 || candSec <= 0 {
		return "One side has no known runtime, so duration was not used."
	}
	return "Candidate runtime compared against the local file's duration."
}

// recordRerank appends the LLM rerank to a candidate's breakdown.
//
// Rerank is recorded as a REPLACE, not an adjustment, because that is what it
// is: the LLM does not scale the pipeline score, it substitutes its own
// judgement. Expressing that as a multiply by new/old would recompose correctly
// while telling the reviewer a falsehood -- that some signal was worth a factor
// of 3.71.
//
// The detail names the rescale window because the window bounds come from OTHER
// candidates in the result set (bestScore and the last candidate in the
// ambiguous range). A reranked score is therefore not a function of this book's
// evidence alone, and a panel that implied otherwise would be misleading in a
// way the reviewer could not detect from the row.
func recordRerank(c *MetadataCandidate, llmScore, origMin, origMax float64) {
	if c.ScoreBreakdown == nil {
		// No recorded derivation to extend. Record the rerank alone rather than
		// leaving the panel to imply the pipeline produced this number.
		c.ScoreBreakdown = &ScoreBreakdown{Steps: []ScoreStep{}}
	}
	sr := &scoreRecorder{score: c.ScoreBreakdown.Score, steps: c.ScoreBreakdown.Steps}
	sr.replace("llm_rerank", "LLM rerank", c.Score, fmt.Sprintf(
		"An LLM re-judged the top candidates and scored this one %.2f, rescaled into "+
			"[%.3f, %.3f] — the range the surrounding candidates occupy — so it stays "+
			"comparable with the results it was not asked about.",
		llmScore, origMin, origMax))
	c.ScoreBreakdown = sr.breakdown()
}
