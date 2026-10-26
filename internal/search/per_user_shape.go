// file: internal/search/per_user_shape.go
// version: 1.0.0
// guid: eb623c78-1b62-4ff8-9a50-e192e62ee16b
// last-edited: 2026-10-26

package search

import "errors"

// ErrPerUserBoolean is returned by CheckPerUserBoolean for a query the per-user
// pass cannot evaluate as written. The message is shown to the person who typed the
// query, so it says what to write instead.
var ErrPerUserBoolean = errors.New(
	"a per-user field (read_status, progress_pct, last_played) cannot be combined with other terms using OR, " +
		"and cannot be negated as part of a group: those terms are always required together with the rest of the query. " +
		"To match several states of one field, write read_status:(finished|in_progress)")

// CheckPerUserBoolean refuses a query whose per-user terms sit somewhere the
// translator cannot give them their boolean meaning.
//
// Per-user terms (read_status, progress_pct, last_played) are not sent to the
// index: the translator peels them off into one flat list that is applied afterwards
// as an AND over every term. That is right for a plain conjunction and for a term
// negated on its own (-read_status:finished), and for one field's alternation
// (read_status:(finished|in_progress)), which is one filter that ORs inside itself.
// It is wrong in two shapes, and in both the wrong answer used to be returned
// silently:
//
//   - under an OR with a sibling: read_status:finished OR title:Hush was evaluated
//     as read_status:finished AND title:Hush, so the OR's other side contributed
//     nothing and a collection or playlist showed far fewer books than its query says;
//   - under a NOT over a group: -(read_status:finished title:Hush) is "not both",
//     but the translator negated each term on its own, answering "neither".
//
// Both are refused here instead, at create/update (HTTP 400) and again when a stored
// query is evaluated, so a row saved before this check reads as unavailable rather
// than as a wrong list. Real OR/NOT support needs the per-user pass to evaluate the
// boolean tree itself; it is tracked in todo.d.
//
// A nil node and a query with no per-user term are always accepted.
func CheckPerUserBoolean(n Node) error {
	switch v := n.(type) {
	case *AndNode:
		for _, c := range v.Children {
			if err := CheckPerUserBoolean(c); err != nil {
				return err
			}
		}
	case *OrNode:
		if len(v.Children) > 1 {
			for _, c := range v.Children {
				if containsPerUserTerm(c) {
					return ErrPerUserBoolean
				}
			}
		}
		for _, c := range v.Children {
			if err := CheckPerUserBoolean(c); err != nil {
				return err
			}
		}
	case *NotNode:
		switch v.Child.(type) {
		case *AndNode, *OrNode:
			if containsPerUserTerm(v.Child) {
				return ErrPerUserBoolean
			}
		}
		return CheckPerUserBoolean(v.Child)
	}
	return nil
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
