// file: internal/transcribe/identity_compare.go
// version: 1.0.0
// guid: 6c1e8d37-4a92-4f05-b8e3-7d2a0c9f5b14
// last-edited: 2026-11-17

package transcribe

import (
	"regexp"
	"strings"
	"unicode"
)

// This file measures, and never decides. maintenance.reparse-file-intros writes
// only BookFile.IntroParse; it does not touch the identity columns (title,
// author, narrator, translator, cover artist) or the status. What it does report
// is how the new clip parse compares with the identity a row already stores, so a
// later change can decide from counts which repairs, if any, are safe.
//
// The legacy parser (classify.go, parse.go) wrote the stored identity and is
// unchanged. The clip parser (ParseClip) is allowed to disagree with it; every
// disagreement is a number on the dry run, not an edit.

// Identity is the five identity columns a title announcement can fill.
type Identity struct {
	Title       string
	Author      string
	Narrator    string
	Translator  string
	CoverArtist string
}

// IdentityVerdict is how a clip parse relates to a row's stored identity.
type IdentityVerdict string

const (
	// VerdictNotAnnouncement: the clip is not a title announcement, so there is
	// no identity to compare.
	VerdictNotAnnouncement IdentityVerdict = "not_announcement"
	// VerdictAgree: every field the clip names equals the stored one (or is not
	// stored and the clip names nothing for it).
	VerdictAgree IdentityVerdict = "agree"
	// VerdictFill: nothing conflicts and at least one stored-empty field would be
	// filled from the clip.
	VerdictFill IdentityVerdict = "would_fill"
	// VerdictConflict: a stored field and the clip's value for it differ.
	VerdictConflict IdentityVerdict = "conflict"
)

// ConflictKind says how a conflicting title differs. A conflict on a title is
// classified by the title alone; other_field covers a title that agrees while
// another field differs.
type ConflictKind string

const (
	ConflictNone ConflictKind = ""
	// ConflictPublisherPrefix: the stored title is the clip's title with a
	// "<publisher> presents" lead in front of it.
	ConflictPublisherPrefix ConflictKind = "publisher_prefix"
	// ConflictTruncation: one title is a word-prefix of the other.
	ConflictTruncation ConflictKind = "truncation"
	// ConflictDifferentTitle: the two titles share no such relation.
	ConflictDifferentTitle ConflictKind = "different_title"
	// ConflictOtherField: the titles agree and author, narrator, translator or
	// cover artist differ.
	ConflictOtherField ConflictKind = "other_field"
)

// IdentityComparison is the outcome of CompareIdentity.
type IdentityComparison struct {
	Verdict IdentityVerdict
	// Conflict is set when Verdict is VerdictConflict.
	Conflict ConflictKind
	// Fillable counts stored-empty fields the clip would fill (reported for
	// would_fill rows and, informationally, alongside a conflict).
	Fillable int
}

// CompareIdentity compares what a stored row holds with what the clip parse
// reads. It writes nothing and decides nothing.
func CompareIdentity(stored Identity, p ClipParse) IdentityComparison {
	if p.Kind != ClipKindTitle {
		return IdentityComparison{Verdict: VerdictNotAnnouncement}
	}
	var out IdentityComparison
	conflict := ConflictNone

	switch {
	case p.Title == "":
	case stored.Title == "":
		out.Fillable++
	case normIdentity(stored.Title) != normIdentity(p.Title):
		conflict = titleConflict(stored.Title, p.Title, p.Publisher)
	}

	other := false
	for _, f := range [][2]string{
		{stored.Author, p.Author}, {stored.Narrator, p.Narrator},
		{stored.Translator, p.Translator}, {stored.CoverArtist, p.CoverArtist},
	} {
		switch {
		case f[1] == "":
		case f[0] == "":
			out.Fillable++
		case normIdentity(f[0]) != normIdentity(f[1]):
			other = true
		}
	}
	if conflict == ConflictNone && other {
		conflict = ConflictOtherField
	}

	switch {
	case conflict != ConflictNone:
		out.Verdict, out.Conflict = VerdictConflict, conflict
	case out.Fillable > 0:
		out.Verdict = VerdictFill
	default:
		out.Verdict = VerdictAgree
	}
	return out
}

// titleConflict classifies two differing titles.
func titleConflict(stored, parsed, publisher string) ConflictKind {
	s, n := normIdentity(stored), normIdentity(parsed)
	if prefix, ok := strings.CutSuffix(s, " "+n); ok {
		pub := normIdentity(publisher)
		if strings.HasSuffix(prefix, "presents") || (pub != "" && strings.Contains(prefix, pub)) {
			return ConflictPublisherPrefix
		}
	}
	if strings.HasPrefix(s, n+" ") || strings.HasPrefix(n, s+" ") {
		return ConflictTruncation
	}
	return ConflictDifferentTitle
}

// normIdentity lowercases and keeps only letters and digits, single-spaced, so
// punctuation and case never make two equal values differ.
func normIdentity(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// JunkReason names why the grammar itself calls a stored title junk, "" when it
// does not. These are values the parser's own rules reject as a title.
type JunkReason string

const (
	JunkNone JunkReason = ""
	// JunkCreditVerbOnly: the whole title is a credit verb ("Narrated", "This has
	// been narrated").
	JunkCreditVerbOnly JunkReason = "credit_verb_only"
	// JunkCreditWordingInside: the title contains "<verb> by" ("Alfred Penny
	// Narrated by Selina").
	JunkCreditWordingInside JunkReason = "credit_wording_inside"
	// JunkEndsInCreditVerb: the title ends in a credit verb ("Gotham Nights 1
	// Written"). A real title can end that way ("What Was Written"); the count is
	// a lead, not a verdict.
	JunkEndsInCreditVerb JunkReason = "ends_in_credit_verb"
)

var (
	junkInsideRe = regexp.MustCompile(`(?i)\b(?:written|authored|narrated|read|performed|voiced|translated|produced|edited|published|presented)(?:\s+for\s+you)?\s+by\b`)
	junkEndsRe   = regexp.MustCompile(`(?i)\b(?:written|authored|narrated|performed|voiced|translated|read)[.,]?\s*$`)
)

// StoredTitleJunk reports whether a stored title is one the grammar rejects.
func StoredTitleJunk(title string) JunkReason {
	t := strings.TrimSpace(title)
	switch {
	case t == "":
		return JunkNone
	case creditVerbOnlyRe.MatchString(t):
		return JunkCreditVerbOnly
	case junkInsideRe.MatchString(t):
		return JunkCreditWordingInside
	case junkEndsRe.MatchString(t):
		return JunkEndsInCreditVerb
	}
	return JunkNone
}
