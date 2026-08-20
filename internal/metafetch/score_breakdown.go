// file: internal/metafetch/score_breakdown.go
// version: 1.0.0
// guid: 6c81e35a-2b47-4d19-90fa-7e5c3d81b026
// last-edited: 2026-08-20

package metafetch

import "math"

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
