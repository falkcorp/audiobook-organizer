// file: internal/search/per_user_values.go
// version: 1.2.0
// guid: 5b7d2c1e-84a3-4f60-9e1d-2a6c0b9f3d47
// last-edited: 2026-10-30

package search

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// PerUserValueError is returned by CheckPerUserValues for a per-user term whose
// value the per-user pass cannot evaluate as written. Reason is shown to the
// person who typed the query, so it names what to write instead.
type PerUserValueError struct {
	Reason string
}

func (e *PerUserValueError) Error() string { return e.Reason }

// IsPerUserQueryError reports whether err says that a query's per-user terms
// are the problem and why (a value from CheckPerUserValues, or text the parser
// rejected, UnparsablePerUserQuery). Its text is safe to show to the person who
// owns the query.
func IsPerUserQueryError(err error) bool {
	var ve *PerUserValueError
	return errors.As(err, &ve)
}

// UnparsablePerUserQuery is the refusal for query text the parser rejected that
// writes a per-user term (read_status:finished OR, an unbalanced parenthesis while
// the person is still typing). Such text used to fall to a whole-text substring
// match over title and author, which shows no rows and says nothing; the person
// meant a listening-state filter, so say the query could not be read. It is nil
// when the text names no per-user term, so an ordinary punctuation-heavy title
// keeps its substring fallback. The error is a *PerUserValueError, so every
// handler that answers one with a 400 answers this one the same way.
func UnparsablePerUserQuery(q string, parseErr error) error {
	if parseErr == nil || !perUserTermInText.MatchString(q) {
		return nil
	}
	return &PerUserValueError{Reason: "this search could not be read (" + parseErr.Error() +
		"), so its read_status, progress_pct or last_played term cannot be applied; " +
		"finish the last term and balance any parentheses"}
}

// PerUserQueryReason is the text to show for an error IsPerUserQueryError
// accepted, without the wrappers callers put around it ("search: ", "query: "):
// a message that adds its own lead-in must not repeat theirs.
func PerUserQueryReason(err error) string {
	var ve *PerUserValueError
	if errors.As(err, &ve) {
		return ve.Reason
	}
	return err.Error()
}

// perUserReadStatuses are the values read_status takes: the status machine's own
// words (database.UserBookStatus*), in the order they are shown to the user.
var perUserReadStatuses = []string{
	database.UserBookStatusUnstarted,
	database.UserBookStatusInProgress,
	database.UserBookStatusFinished,
	database.UserBookStatusAbandoned,
}

// CheckPerUserTerms is the whole per-user check for a query that is about to be
// saved or evaluated: the values (CheckPerUserValues). now is the instant relative
// dates resolve against. The boolean shape is no longer checked: an OR or a NOT
// over a group that holds a per-user term is evaluated (MixedQuery), not refused.
func CheckPerUserTerms(n Node, now time.Time) error {
	return CheckPerUserValues(n, now)
}

// CheckPerUserValues refuses a per-user term whose value the per-user pass would
// evaluate as something other than what it says.
//
// The per-user pass compares exactly: read_status against one of four statuses,
// progress_pct against a number, last_played against a date. It does not know
// patterns, and a value it cannot parse is "does not match". Both used to be
// silent, and negation turns "matches nothing" into "matches every book":
//
//   - read_status:completed (not a status) found nothing, and -read_status:completed
//     ("what I have not completed") returned the whole library;
//   - read_status:fin* and read_status:fin~ were compared as the text "fin";
//   - progress_pct:>half matched nothing, and negated matched everything.
//
// A term of that kind is refused at create/update (HTTP 400) and again when a
// stored query is evaluated, so a row saved before this check reads as
// unavailable instead of as a wrong list. last_played:* ("has been played at all")
// is the one pattern that is a real form, and is accepted.
func CheckPerUserValues(n Node, now time.Time) error {
	switch v := n.(type) {
	case *AndNode:
		for _, c := range v.Children {
			if err := CheckPerUserValues(c, now); err != nil {
				return err
			}
		}
	case *OrNode:
		for _, c := range v.Children {
			if err := CheckPerUserValues(c, now); err != nil {
				return err
			}
		}
	case *NotNode:
		return CheckPerUserValues(v.Child, now)
	case *FieldNode:
		if IsPerUserField(v.Field) {
			return checkPerUserFieldNode(v, now)
		}
		if err := checkFieldCase(v.Field); err != nil {
			return err
		}
	case *ValueAltNode:
		if err := checkFieldCase(v.Field); err != nil {
			return err
		}
		if IsPerUserField(v.Field) {
			// A list of nothing is not a term the translator can place: it drops
			// it, so title:Hush -read_status:() was just title:Hush.
			if len(v.Values) == 0 {
				return &PerUserValueError{Reason: fmt.Sprintf(
					"%s:() lists no values; write %s:%s or %s:(%s)", v.Field, v.Field,
					perUserExampleValue(v.Field), v.Field, perUserExampleList(v.Field))}
			}
			for _, val := range v.Values {
				if v.Field == "progress_pct" || v.Field == "last_played" {
					if val != "" && strings.ContainsAny(val[:1], "<>=[") || strings.Contains(val, "..") {
						return &PerUserValueError{Reason: fmt.Sprintf(
							"%s takes plain values inside ( | ), not the comparison or range %q; "+
								"write the comparison on its own, as in %s:%s, or list single values, as in %s:(%s)",
							v.Field, clipValue(val), v.Field, perUserExampleValue(v.Field), v.Field, perUserExampleList(v.Field))}
					}
				}
				if err := checkPerUserFieldNode(&FieldNode{Field: v.Field, Value: val}, now); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkFieldCase refuses a per-user field written with capitals (READ_STATUS:
// finished). Field names are matched as written, so such a term is not a per-user
// term to the translator: it would be looked up as an indexed field that does not
// exist and match nothing, while the rest of the system (the text rule that makes
// a collection per-viewer) reads the name without regard to case. That rule is
// Go's (?i), which is simple case folding, so this compares the same way
// (strings.EqualFold) rather than through ToLower: the long s (U+017F) folds to s
// under one and not under the other.
func checkFieldCase(field string) error {
	if IsPerUserField(field) {
		return nil
	}
	for _, name := range perUserFieldNames() {
		if strings.EqualFold(field, name) {
			return &PerUserValueError{Reason: fmt.Sprintf(
				"field names are lowercase: write %s, not %s", name, clipValue(field))}
		}
	}
	return nil
}

// perUserFieldNames lists the per-user fields in a fixed order.
func perUserFieldNames() []string {
	names := make([]string, 0, len(perUserFieldSet))
	for name := range perUserFieldSet {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func checkPerUserFieldNode(n *FieldNode, now time.Time) error {
	bad := func(format string, args ...any) error {
		return &PerUserValueError{Reason: fmt.Sprintf(format, args...)}
	}
	if (n.Prefix || n.Wildcard || n.Fuzzy) && !isPresenceTerm(n) {
		return bad("%s takes an exact value: patterns (*, ~) are not supported on it, "+
			"so %s would be compared as plain text and match nothing%s",
			n.Field, shortTerm(n), perUserValueHint(n.Field))
	}
	switch n.Field {
	case "read_status":
		if n.Op != "" && n.Op != "=" {
			return bad("read_status takes one of its statuses, not a comparison; use %s", strings.Join(perUserReadStatuses, ", "))
		}
		for _, s := range perUserReadStatuses {
			if strings.EqualFold(n.Value, s) {
				return nil
			}
		}
		return bad("read_status must be one of %s (got %q)", strings.Join(perUserReadStatuses, ", "), clipValue(n.Value))
	case "progress_pct":
		check := func(s string) error {
			f, err := strconv.ParseFloat(s, 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return bad("progress_pct takes a number from 0 to 100, as in progress_pct:>50 or progress_pct:[10 TO 90]; "+
					"!=, == and an open end such as [* TO 50] are not supported (got %q)", clipValue(s))
			}
			return nil
		}
		switch n.Op {
		case "range":
			if err := check(n.RangeMin); err != nil {
				return err
			}
			return check(n.RangeMax)
		case "", "=", ">", "<", ">=", "<=":
			return check(n.Value)
		}
		return bad("progress_pct does not take the comparison %q", clipValue(n.Op))
	case "last_played":
		if err := CheckDateNode(n, now); err != nil {
			return &PerUserValueError{Reason: err.Error()}
		}
	}
	return nil
}

// perUserExampleValue and perUserExampleList are what a message suggests writing.
func perUserExampleValue(field string) string {
	switch field {
	case "progress_pct":
		return ">50"
	case "last_played":
		return ">-30d"
	}
	return database.UserBookStatusFinished
}

func perUserExampleList(field string) string {
	switch field {
	case "progress_pct":
		return "10|20"
	case "last_played":
		return "2026-01-01|2026-02-01"
	}
	return database.UserBookStatusFinished + "|" + database.UserBookStatusInProgress
}

func perUserValueHint(field string) string {
	if field == "read_status" {
		return "; use one of " + strings.Join(perUserReadStatuses, ", ")
	}
	return ""
}

// shortTerm renders a term for a message, with the pattern markers it was written with.
func shortTerm(n *FieldNode) string {
	s := n.Value
	if n.Wildcard {
		s = "*" + s
	}
	if n.Prefix {
		s += "*"
	}
	if n.Fuzzy {
		s += "~"
	}
	return clipValue(n.Field + ":" + s)
}

// clipValue bounds a value echoed in a message: the person typed it, but a pasted
// wall of text must not become a wall of error.
func clipValue(s string) string {
	const max = 40
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "..."
	}
	return s
}
