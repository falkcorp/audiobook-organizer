// file: internal/applygate/refusal.go
// version: 1.0.0
// guid: f3b7bf9e-36b3-4b4a-a634-beaf4cf1a2fa
// last-edited: 2026-10-30

package applygate

// StructuralRefusal reports whether a gate refusal reason is STRUCTURAL: it is
// a judgement about the book's own files, path, tags and the candidate record,
// so the same inputs refuse the same way on every later run. A bulk apply that
// remembers such a refusal (internal/server batch_apply_refusal.go) may skip the
// book until those inputs change.
//
// This is an ALLOWLIST on purpose. A new reason defaults to "not structural"
// and is re-evaluated on every run until someone classifies it here;
// TestStructuralRefusal_EveryReasonClassified fails when a Reason* constant
// exists in this package that appears in neither list below.
//
// Deliberately not structural, with the reason:
//
//   - ReasonIdentityStale: depends on the cache row's query, the folder-parse
//     config flag and the sibling rows in the folder -- not on the book alone.
//   - ReasonPartialBook: depends on every OTHER book in the batch (the claim
//     index), so it changes when a sibling is applied.
//   - ReasonOwnerRejected, ReasonOwnerManualOnly: the owner can undo a
//     rejection or edit a manual-only series/tag without touching the book.
//   - ReasonOwnerRejectionCheckFailed, ReasonOwnerManualCheckFailed: a store
//     read failed. That is a transient fault, not a verdict.
//   - ReasonOwnerReviewed: an override marker, never a refusal.
//
// A caller must also bind the memory to a fingerprint of the inputs and to the
// build that judged them (see the server file), because the legs listed here
// are rules, and a relaxed rule must not stay suppressed.
func StructuralRefusal(reason string) bool {
	_, ok := structuralRefusals[reason]
	return ok
}

// structuralRefusals is the allowlist behind StructuralRefusal.
var structuralRefusals = map[string]struct{}{
	// Score leg.
	ReasonTranscriptionMismatch:          {},
	ReasonTranscriptionRuntimeUnverified: {},
	ReasonScoreBelowFloor:                {},
	ReasonReviewOnlySource:               {},
	// Sequence leg.
	ReasonSequenceMismatch:           {},
	ReasonSequenceMissingOnCandidate: {},
	ReasonBookSequenceConflict:       {},
	ReasonCandidateSequenceConflict:  {},
	ReasonSeriesNumberLost:           {},
	ReasonSeriesRenamed:              {},
	// Evidence leg.
	ReasonRuntimeMismatch:         {},
	ReasonRuntimeUnknownOverwrite: {},
	ReasonAuthorRoleCredit:        {},
	ReasonAuthorNotInPath:         {},
	ReasonTitleDisagrees:          {},
	ReasonNarratorMismatch:        {},
	ReasonASINConflict:            {},
	ReasonInsufficientEvidence:    {},
	// Candidate shape.
	ReasonCastInAuthor: {},
}
