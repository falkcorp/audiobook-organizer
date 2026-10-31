// file: internal/search/per_user_shape.go
// version: 2.0.0
// guid: eb623c78-1b62-4ff8-9a50-e192e62ee16b
// last-edited: 2026-10-30

package search

// perUserNeedsTree reports whether a query's per-user terms sit somewhere the flat
// per-user list cannot give them their boolean meaning, so the query must be
// translated with the viewer's rows (MixedQuery) instead of through TranslateAt.
//
// Per-user terms (read_status, progress_pct, last_played) are not sent to the
// index: TranslateAt peels them off into one flat list that is applied afterwards
// as an AND over every term. That is right for a plain conjunction and for a term
// negated on its own (-read_status:finished), and for one field's alternation
// (read_status:(finished|in_progress)), which is one filter that ORs inside itself.
// It is wrong in two shapes, and in both the wrong answer used to be returned
// silently, and later refused with a 400 (search.CheckPerUserBoolean, removed
// 2026-10-30):
//
//   - under an OR with a sibling: read_status:finished OR title:Hush was evaluated
//     as read_status:finished AND title:Hush, so the OR's other side contributed
//     nothing and a collection or playlist showed far fewer books than its query says;
//   - under a NOT over a group: -(read_status:finished title:Hush) is "not both",
//     but the translator negated each term on its own, answering "neither".
//
// Both shapes are now answered correctly by MixedQuery, which turns each per-user
// term into an index query over the viewer's rows and lets the index evaluate the
// whole boolean tree. Every other shape keeps the flat path, byte for byte.
//
// A nil node and a query with no per-user term never need the tree.
func perUserNeedsTree(n Node) bool {
	return shapeNeedsTree(n, false)
}

// shapeNeedsTree walks the tree carrying whether an odd number of NOTs sits above
// n. The translator cancels two negations of a group, so -(-(a b)) is a plain group
// and only an odd count makes a per-user term inside it inexpressible as a flat list.
func shapeNeedsTree(n Node, negated bool) bool {
	switch v := n.(type) {
	case *AndNode:
		if negated && containsPerUserTerm(v) {
			return true
		}
		for _, c := range v.Children {
			if shapeNeedsTree(c, negated) {
				return true
			}
		}
	case *OrNode:
		if negated && containsPerUserTerm(v) {
			return true
		}
		if len(v.Children) > 1 {
			for _, c := range v.Children {
				if containsPerUserTerm(c) {
					return true
				}
			}
		}
		for _, c := range v.Children {
			if shapeNeedsTree(c, negated) {
				return true
			}
		}
	case *NotNode:
		return shapeNeedsTree(v.Child, !negated)
	}
	return false
}

// containsPerUserTerm reports whether any term under n is a per-user field.
func containsPerUserTerm(n Node) bool {
	switch v := n.(type) {
	case *AndNode:
		for _, c := range v.Children {
			if containsPerUserTerm(c) {
				return true
			}
		}
	case *OrNode:
		for _, c := range v.Children {
			if containsPerUserTerm(c) {
				return true
			}
		}
	case *NotNode:
		return containsPerUserTerm(v.Child)
	case *FieldNode:
		return IsPerUserField(v.Field)
	case *ValueAltNode:
		return IsPerUserField(v.Field)
	}
	return false
}
