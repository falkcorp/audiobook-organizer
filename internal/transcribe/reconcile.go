// file: internal/transcribe/reconcile.go
// version: 1.0.0
// guid: e2f7a846-1d35-4b9c-8c60-4a91b7d3f052
// last-edited: 2026-11-17

package transcribe

import (
	"regexp"
	"strings"
)

// StatusOK and StatusUnparsed are the two transcribe_status values a parse can
// move a row between. They are the strings the per-file op has always stored;
// internal/plugins/maintenance keeps its own constants with the same values and
// a test pins that the two agree.
const (
	StatusOK       = "ok"
	StatusUnparsed = "unparsed"
)

// Identity is the five identity columns a title announcement can fill.
type Identity struct {
	Title       string
	Author      string
	Narrator    string
	Translator  string
	CoverArtist string
}

// IsZero reports whether no identity field is set.
func (i Identity) IsZero() bool { return i == Identity{} }

// StoredIntro is what a row holds before a re-parse.
type StoredIntro struct {
	Status string
	Identity
}

// ReparseDecision is what a re-parse is allowed to write to a row's identity
// and status. It never describes the parse itself (the kind and fields of the
// parse are always written; this is only the part that can hurt).
type ReparseDecision struct {
	// Identity and Status are the values to store. They equal the stored values
	// whenever nothing is written.
	Identity Identity
	Status   string

	// IdentityWrite and StatusWrite say that Identity / Status differ from stored.
	IdentityWrite bool
	StatusWrite   bool

	// Filled counts identity fields that were empty and now have a value;
	// Repaired counts fields whose stored value was defective and was replaced by
	// a clean one.
	Filled   int
	Repaired int

	// KindConflict is set when the row already carries identity but the clip is
	// not a title announcement. The identity is KEPT; the conflict is only
	// counted, so an operator can see how many stored identities the current
	// grammar would not have produced.
	KindConflict bool
}

// THE NO-DOWNGRADE RULE.
//
// A re-parse derives a kind and fields from stored text. Re-deriving can only
// help a row's identity if it never makes one worse, and the grammar changes it
// applies are sometimes stricter than the grammar that produced the stored
// value. So a re-parse may change a row's identity and status ONLY as follows,
// and a row that does not meet a rule is left exactly as it was:
//
//  1. Eligibility. Only rows whose status is ok or unparsed are touched. A
//     failure status (whisper_error, transcribe_timeout, source_file_missing, ...)
//     and empty are left alone: their text, if any, was not the product of a
//     successful transcription.
//  2. Source. Identity is written only by a title_announcement parse. Any other
//     kind leaves identity and status untouched, and a row that already has
//     identity is counted as a KindConflict (never cleared).
//  3. Per field, with S the stored value and N the new one:
//     - N empty: keep S. A field is never emptied.
//     - S empty, N set: take N (a fill).
//     - S == N: nothing.
//     - both set and different: take N only when S is DEFECTIVE and N is not
//     (a repair). A defective value leaks credit wording or publisher text, or
//     overruns the length a legitimate value has (see isDefective). Two clean
//     values that differ are both plausible, so the stored one stays: the row
//     is not "strictly better" under the new parse, merely different.
//  4. Status. unparsed becomes ok when the resulting title is non-empty (the
//     per-file op stores ok exactly when a title was parsed). ok never changes,
//     and nothing ever becomes unparsed.
//
// Applying the rule is idempotent: running it again on its own output changes
// nothing, which is what lets a second dry run report zero changes.

// Reconcile applies the no-downgrade rule.
func Reconcile(stored StoredIntro, p ClipParse) ReparseDecision {
	d := ReparseDecision{Identity: stored.Identity, Status: stored.Status}
	if stored.Status != StatusOK && stored.Status != StatusUnparsed {
		return d
	}
	if p.Kind != ClipKindTitle {
		d.KindConflict = !stored.Identity.IsZero()
		return d
	}

	next := Identity{}
	var f, r int
	next.Title, f, r = mergeField(stored.Title, p.Title, true, f, r)
	next.Author, f, r = mergeField(stored.Author, p.Author, false, f, r)
	next.Narrator, f, r = mergeField(stored.Narrator, p.Narrator, false, f, r)
	next.Translator, f, r = mergeField(stored.Translator, p.Translator, false, f, r)
	next.CoverArtist, f, r = mergeField(stored.CoverArtist, p.CoverArtist, false, f, r)

	d.Identity = next
	d.Filled, d.Repaired = f, r
	d.IdentityWrite = next != stored.Identity
	if stored.Status == StatusUnparsed && next.Title != "" {
		d.Status = StatusOK
		d.StatusWrite = true
	}
	return d
}

// mergeField is rule 3 for one field, threading the two counters.
func mergeField(stored, parsed string, isTitle bool, filled, repaired int) (string, int, int) {
	switch {
	case parsed == "":
		return stored, filled, repaired
	case stored == "":
		return parsed, filled + 1, repaired
	case stored == parsed:
		return stored, filled, repaired
	case isDefective(stored, isTitle) && !isDefective(parsed, isTitle):
		return parsed, filled, repaired + 1
	default:
		return stored, filled, repaired
	}
}

var (
	// leakedCreditRe is credit wording inside a value: "Maruyama Translated by
	// Emily", "Title Narrated by X".
	leakedCreditRe = regexp.MustCompile(`(?i)\b(?:written|authored|narrated|read|performed|voiced|translated|produced|edited|published|presented)(?:\s+for\s+you)?\s+by\b`)

	// danglingVerbRe is a credit verb welded onto the END of a value: the
	// "Awakened Essence 1 Written" defect, where the split landed on the bare "by".
	danglingVerbRe = regexp.MustCompile(`(?i)\b(?:written|authored|narrated|performed|voiced|translated|read)[.,]?\s*$`)

	// leakedPublisherRe is the publisher's lead left on the front of a title.
	leakedPublisherRe = regexp.MustCompile(`(?i)\bpresents\b`)
)

// maxNameWords is the word cap truncateName applies to a name, so a stored name
// longer than this was produced by a parser that had no cap.
const maxNameWords = 6

// isDefective reports a stored value that carries text a clean parse would not.
// It is deliberately a SHORT list of concrete, measured leaks and not a quality
// score: the rule it serves must be decidable from the two strings alone, and a
// score would let two plausible values displace each other.
func isDefective(v string, isTitle bool) bool {
	if leakedCreditRe.MatchString(v) || danglingVerbRe.MatchString(v) || leakedPublisherRe.MatchString(v) {
		return true
	}
	if isTitle {
		return len(strings.Fields(v)) > maxTitleWords || len(v) > maxTitleChars
	}
	// A name is cut at the first structural/acknowledgement boundary and capped
	// at six words; a stored one that still contains a boundary or exceeds the
	// cap was never cut.
	return nameBoundaryRe.MatchString(v) || len(strings.Fields(v)) > maxNameWords
}
