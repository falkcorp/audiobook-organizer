// file: internal/dedup/unified/provenance.go
// version: 0.1.0
// guid: c41b7e90-2d58-4a6f-9e03-b8f5a1d72c46
// last-edited: 2026-11-21

package unified

// Provenance kinds (SigExactRule and SigSamePath, declared with the other
// kinds in score.go) are NON-SCORING signals: Confidence is always 0 and no
// configured boost exists for them, so they never change a score or a band.
// They record WHY a candidate row exists for the paths that write a candidate
// without composing a score (the exact-layer rules), so the review UI can
// answer "why is this pair here?" instead of "No score breakdown recorded".

// IsProvenanceKind reports whether k is a non-scoring provenance kind.
func IsProvenanceKind(k SignalKind) bool {
	return k == SigExactRule || k == SigSamePath
}

// IsProvenanceOnly reports whether u records only WHY a pair exists and no
// score: at least one signal, every signal a zero-confidence provenance kind,
// and no score or band. Such a breakdown is not a scoring result — the rescore
// and breakdown-backfill paths treat it as "not yet scored", and the candidate
// store merges it into an existing breakdown instead of replacing one.
func (u *UnifiedDedupScore) IsProvenanceOnly() bool {
	if u == nil || len(u.Signals) == 0 || u.Score != 0 || u.Band != "" {
		return false
	}
	for _, s := range u.Signals {
		if !IsProvenanceKind(s.Kind) || s.Confidence != 0 {
			return false
		}
	}
	return true
}

// provenanceKey identifies one provenance fact, so re-emitting the same rule
// for the same pair replaces its evidence rather than stacking duplicates.
func provenanceKey(s Signal) string { return string(s.Kind) + "\x00" + s.Rule }

// MergeProvenance returns existing with every provenance signal from incoming
// folded in: a provenance signal with the same kind+rule is replaced (its
// evidence may have been refreshed), a new one is appended. Score, Band,
// Suppressors, Formula and every scoring signal of existing are kept exactly
// as they were, so merging never changes what a row scores.
//
// A nil existing yields a copy of incoming. Neither argument is mutated.
func MergeProvenance(existing, incoming *UnifiedDedupScore) *UnifiedDedupScore {
	if incoming == nil {
		return existing
	}
	if existing == nil {
		cp := *incoming
		cp.Signals = append([]Signal(nil), incoming.Signals...)
		return &cp
	}
	out := *existing
	out.Signals = append([]Signal(nil), existing.Signals...)
	index := make(map[string]int, len(out.Signals))
	for i, s := range out.Signals {
		if IsProvenanceKind(s.Kind) {
			index[provenanceKey(s)] = i
		}
	}
	for _, s := range incoming.Signals {
		if !IsProvenanceKind(s.Kind) {
			continue
		}
		if i, ok := index[provenanceKey(s)]; ok {
			out.Signals[i] = s
			continue
		}
		index[provenanceKey(s)] = len(out.Signals)
		out.Signals = append(out.Signals, s)
	}
	return &out
}

// CarryProvenance returns next with the provenance signals of prev that next
// lacks appended. Used when a scorer REPLACES a row's breakdown (rescore,
// breakdown backfill): the composed score is new, but the record of which
// exact rule created the row must survive it. A nil next is returned as is.
func CarryProvenance(prev, next *UnifiedDedupScore) *UnifiedDedupScore {
	if next == nil || prev == nil {
		return next
	}
	have := make(map[string]bool, len(next.Signals))
	for _, s := range next.Signals {
		if IsProvenanceKind(s.Kind) {
			have[provenanceKey(s)] = true
		}
	}
	var extra []Signal
	for _, s := range prev.Signals {
		if IsProvenanceKind(s.Kind) && !have[provenanceKey(s)] {
			extra = append(extra, s)
		}
	}
	if len(extra) == 0 {
		return next
	}
	out := *next
	out.Signals = append(append([]Signal(nil), next.Signals...), extra...)
	return &out
}
