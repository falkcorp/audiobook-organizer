// file: internal/personname/personname.go
// version: 1.5.0
// guid: 8c3f6a15-2e94-4d78-b1a0-5f7e2c9d3b48
// last-edited: 2026-09-01

// Package personname answers "does this string look like a human author's
// name, rather than a title or a structural marker?" -- the heuristic that
// decides which half of "X - Y" is the author when a file or folder has to be
// parsed for one.
//
// # Why this is its own package
//
// It existed THREE times -- internal/scanner, internal/metadata and
// internal/dedup -- and the copies had DIVERGED. Measured over a corpus of real
// author names, titles and structural markers, the three disagreed on 13 of 40
// inputs, in BOTH directions:
//
//   - scanner and metadata compared capitalisation with an ASCII byte test
//     (word[0] < 'A' || word[0] > 'Z'). Any name whose FIRST letter is
//     non-ASCII was rejected: Émile Zola, Åsa Larsson, Ítalo Calvino, Øyvind
//     Torseter, Александр Пушкин, 村上 春樹. (Names like "José Saramago" passed,
//     because 'J' is ASCII -- the bug is narrower than "all accented names".)
//     Both also rejected lowercase particles, losing "Simone de Beauvoir" and
//     "Ludwig van Beethoven".
//   - dedup handled all of the above correctly, but had no validity guard at
//     all, so it answered TRUE for "Book 3", "Chapter 1", "Volume 2" and
//     "Disc 1" -- structural markers, filed as people.
//
// So no single copy was the good one. This implementation is the UNION of the
// three sets of checks, which is why it is a merge rather than a promotion of a
// winner.
//
// # The rule that matters most
//
// Capitalisation is expressed as "the first rune must not be LOWERCASE", never
// as "must be UPPERCASE". unicode.IsUpper is false for every caseless script --
// CJK, Hebrew, Arabic, Thai -- so requiring positive uppercase excludes them
// entirely. That distinction is not stylistic; it is the difference between
// supporting Japanese authors and silently dropping them.
//
// # Known limit: Georgian, and Armenian written lowercase
//
// The formulation above is right for CASELESS scripts and wrong for a CASED
// script whose DEFAULT written form is the lowercase one. Georgian Mkhedruli
// letters are Unicode Ll -- unicode.IsLower('გ') is true, because Unicode 11
// added Mtavruli capitals -- yet Mkhedruli is how Georgian is normally written.
// So LooksLikePersonName("გიორგი ბაქრაძე") is FALSE and every Georgian author is
// dropped at all 20 non-test call sites (scanner 6, metadata 6, dedup 8) --
// "five" was written from the number of SPLIT BRANCHES, not call sites. Not a
// regression (the ASCII test this package
// replaced dropped them too), but not fixed either.
//
// Do not "fix" it by accepting runes with no uppercase mapping: Go DOES map
// Mkhedruli to Mtavruli (unicode.ToUpper('გ') == 'Გ'), so that test rejects
// Georgian exactly as today. Measured 2026-09-01; see
// todo.d/20260901_georgian_dropped_by_person_name_predicate.md for the disproof
// and for why a per-script exception is needed instead.
package personname

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// nameParticles are lowercase words that legitimately appear INSIDE a person's
// name ("Simone de Beauvoir", "Ludwig van Beethoven"). Interior lowercase words
// otherwise mark a title clause ("A Game of Thrones"), so the particle list is
// what separates the two.
var nameParticles = map[string]bool{
	"de": true, "la": true, "le": true, "van": true, "von": true,
	"del": true, "della": true, "di": true, "da": true, "dos": true,
	"du": true, "den": true, "ter": true, "bin": true, "ibn": true,
	"al": true, "el": true, "st.": true, "mac": true,
}

// structuralWords mark a volume/part token rather than a person. Without this
// guard "Book 3" and "Chapter 1" parse as two capitalised words and are
// indistinguishable from a name -- which is exactly what the dedup copy did.
//
// These are matched as WHOLE FIRST WORDS, never as bare prefixes. A
// strings.HasPrefix test here rejects real authors whose names merely begin with
// the same letters -- Booker T. Washington, Volker Kutscher, Volney Beckner,
// Volodymyr Zelensky, Voltaire, Partha Chatterjee, Partridge -- and the damage
// does not stop at a refusal. SplitCompositeAuthorName's comma branch falls
// THROUGH on refusal (internal/dedup/author.go:270) to a weaker semicolon gate
// with no shape check, so refusing "Volker Kutscher, Niall Sellar" does not
// merely drop a split, it can mint the whole composite as one author name.
// Measured against the real splitter: 886 distinct author strings that a bare
// prefix test would newly mint, and 33,580 of 195,245 realistic composites
// silently losing their split.
//
// Plurals are listed explicitly so widening the match does not also start
// admitting "Parts Unknown", which the prefix test caught by accident.
var structuralWords = map[string]bool{
	"book": true, "books": true,
	"chapter": true, "chapters": true,
	"part": true, "parts": true,
	"vol": true, "vols": true,
	"volume": true, "volumes": true,
	"disc": true, "discs": true,
}

// IsValidAuthor rejects strings that cannot be an author at all: empty, purely
// numeric, or led by a structural marker word.
func IsValidAuthor(author string) bool {
	if author == "" {
		return false
	}
	// Purely numeric ("01", "1984") is a disc or a year, not a person.
	if _, err := strconv.Atoi(author); err == nil {
		return false
	}
	// Test the first WORD, not a prefix. Trailing punctuation and digits are
	// stripped so the label forms that actually occur -- "Vol. 2", "Book3",
	// "Disc 1" -- still match, while "Volker" and "Booker" do not.
	fields := strings.Fields(strings.ToLower(author))
	if len(fields) > 0 && structuralWords[strings.TrimRight(fields[0], ".,-_0123456789")] {
		return false
	}
	return true
}

// IsNameParticle reports whether w is a name particle ("de", "van", "Le") in any
// casing. Exported because internal/dedup needs the SAME set to decide that a
// trailing particle is not a surname -- a second copy of this list in dedup is
// precisely the duplication this package exists to remove.
func IsNameParticle(w string) bool {
	return nameParticles[strings.ToLower(strings.TrimSpace(w))]
}

// editionSuffixRe matches a trailing edition/format marker -- "(Unabridged)",
// "[Dramatized Adaptation]". These attach to the WORK, so in a filename they
// trail whichever field happens to come last, author or title alike.
var editionSuffixRe = regexp.MustCompile(`\s*(?:[\(\[][^\)\]]*[\)\]]\s*)+$`)

// creditSeparatorRe splits an author credit into the individual names it lists.
var creditSeparatorRe = regexp.MustCompile(`(?i)\s*(?:,|;|&|\+|/|\band\b|\bwith\b)\s*`)

// NOTE on "/" and on "with", because these two look like inconsistencies with
// internal/dedup and are deliberate.
//
// "/" was MISSING here at first, and that is not a miss but an inversion: with
// no "/" in this list, "Good Omens / Neil Gaiman / Terry Pratchett" is not a
// credit, so the caller concludes the right side is a title and files "Good
// Omens" as the author. internal/dedup/author.go treats "/" as an author
// separator -- and as the FIRST branch tried -- so omitting it here gave one
// repo two answers to "is this an author credit?", which is precisely what this
// package exists to abolish. It was missed the same way dedup's slash branch
// was: the corpus that measured this predicate contained no "/".
//
// "with" is in THIS list but is deliberately NOT a separator in dedup's
// splitter. That is not a divergence, because the two answer different
// questions. The splitter must decide WHERE TO CUT a credit into separate
// author rows, and "A with B" has no reliable cut point -- it was minting
// "Volker Kutscher with Bob". This predicate only decides WHETHER the field is
// a credit at all, and "Neil Gaiman with Terry Pratchett" plainly is one. Same
// string, two questions, two correct answers.

// titleLeadRe matches the articles an English title commonly opens with. A
// person's name does not start with one, so this discriminates the otherwise
// identical shape "two to four capitalised words".
var titleLeadRe = regexp.MustCompile(`(?i)^(?:the|a|an)\s`)

// StripEditionSuffix removes trailing edition/format markers -- "(Unabridged)",
// "[Dramatized Adaptation]", and repeats of them. Exported because callers that
// compare an author against a known string (authorname.IsPlaceholder) must
// compare the same normalisation this package accepts, or the decorated form of
// that string silently passes a guard written for the bare one.
func StripEditionSuffix(s string) string {
	return strings.TrimSpace(editionSuffixRe.ReplaceAllString(strings.TrimSpace(s), ""))
}

// ChooseAuthorSide decides which half of a two-part filename ("Title - Author",
// "Author_Title") is the author. It returns ok=false when it cannot tell, and
// callers MUST treat that as "no author" rather than falling back to a default
// orientation -- an empty author is re-examined by AI nomination, a wrong one is
// not.
//
// This exists because the decision was written out FOUR times -- the " - " and
// "_" paths of internal/scanner and internal/metadata -- and the copies had
// already diverged: only scanner's "_" path had the article tiebreak, only
// metadata's " - " path had the initials tiebreak, and when the " - " paths were
// fixed for the decoration inversion the "_" paths were left carrying it. Every
// copy of this decision is a place for the next one to drift.
//
// Both sides are tested with LooksLikeAuthorCredit, not LooksLikePersonName.
// Asking the strict predicate here is what produced the inversion: it answers
// false for "Neil Gaiman (Unabridged)" because of the DECORATION, and a caller
// choosing between two sides reads that false as "so the other side is the
// author" and files the title.
// TiePolicy says what ChooseAuthorSide should do when both sides are equally
// plausible and neither discriminator fires.
//
// The two separators genuinely carry different evidence, and this is the ONE
// place the four call sites legitimately differ -- so it is a named argument
// rather than a second implementation. "Title - Author" is the dominant
// audiobook filename convention and all four copies' own comments said they
// preferred author-on-the-right, so a dash tie resolves that way. An underscore
// carries no such convention, and the pre-refactor scanner code refused there
// (it fell out of its tiebreak without assigning an author); folding that into
// "prefer right" would mint an author where the old code deliberately minted
// none, and an absent author is recoverable by AI nomination while a wrong one
// is not.
type TiePolicy int

const (
	// PreferRightOnTie resolves a tie as "Title - Author". For the " - " path.
	PreferRightOnTie TiePolicy = iota
	// RefuseOnTie returns ok=false. For the "_" path.
	RefuseOnTie
)

func ChooseAuthorSide(left, right string, onTie TiePolicy) (title, author string, ok bool) {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	leftIsCredit, rightIsCredit := LooksLikeAuthorCredit(left), LooksLikeAuthorCredit(right)

	switch {
	case rightIsCredit && !leftIsCredit:
		return left, right, true
	case leftIsCredit && !rightIsCredit:
		return right, left, true
	case !leftIsCredit && !rightIsCredit:
		return "", "", false
	}

	// Both sides could be a credit. Two discriminators, both of which already
	// existed in the tree but each in only ONE of the four copies.
	leftLead, rightLead := titleLeadRe.MatchString(left), titleLeadRe.MatchString(right)
	if leftLead != rightLead {
		// A leading article marks the title.
		if leftLead {
			return left, right, true
		}
		return right, left, true
	}

	// Initials belong to a person, not to a title: "J.K. Rowling - Harry Potter".
	leftInitials, rightInitials := strings.Contains(left, "."), strings.Contains(right, ".")
	if leftInitials != rightInitials {
		if leftInitials {
			return right, left, true
		}
		return left, right, true
	}

	// Genuinely ambiguous -- a two-to-four-word capitalised phrase with no
	// article and no initials is not distinguishable from a name by structure.
	// "Good Omens" and "Neil Gaiman" are the same shape, which is why no
	// discriminator can settle it and why the caller's convention decides.
	if onTie == RefuseOnTie {
		return "", "", false
	}
	return left, right, true
}

// LooksLikeAuthorCredit reports whether s could be the AUTHOR field of a
// "Title - Author" pair: one name, a name carrying an edition marker, or
// several names joined by a conjunction.
//
// LooksLikePersonName answers a NARROWER question -- "is this one bare human
// name?" -- and returns false for "Neil Gaiman (Unabridged)" and for
// "Neil Gaiman and Terry Pratchett" because of the DECORATION, not because
// they are not people. A caller that reads that false as "so this side must be
// the title" does not merely miss the author; it INVERTS and files the title
// AS the author. Measured: parseFilenameForAuthor stored
// "Good Omens - Neil Gaiman (Unabridged)" with Author = "Good Omens", where the
// pre-refactor code stored "Neil Gaiman (Unabridged)".
//
// That is why this is a separate predicate rather than a looser
// LooksLikePersonName: one bit cannot answer a two-way question. Any caller
// deciding an ORIENTATION -- which side is the author -- must ask this one.
// Callers deciding whether to ACCEPT a single string as a name still want the
// strict predicate, because a credit list is not a person.
func LooksLikeAuthorCredit(s string) bool {
	s = strings.TrimSpace(s)
	if LooksLikePersonName(s) {
		return true
	}
	bare := strings.TrimSpace(editionSuffixRe.ReplaceAllString(s, ""))
	if bare != s && LooksLikePersonName(bare) {
		return true
	}
	// A credit list: EVERY clause must be a name. One title clause poisons the
	// whole credit rather than half-matching it -- the same fail-closed rule
	// internal/dedup's composite splitter uses, and for the same reason:
	// refusing leaves the field visibly wrong for repair, while a partial match
	// launders a title fragment into an author.
	clauses := creditSeparatorRe.Split(bare, -1)
	if len(clauses) < 2 {
		return false
	}
	for _, c := range clauses {
		if !LooksLikePersonName(strings.TrimSpace(c)) {
			return false
		}
	}
	return true
}

// LooksLikePersonName reports whether s reads as a human name: two to four
// words, none of them beginning with a lowercase letter unless it is a name
// particle, carrying no sentence punctuation and no trailing parenthetical.
func LooksLikePersonName(s string) bool {
	if !IsValidAuthor(s) {
		return false
	}
	// Sentence punctuation belongs to titles ("Do Androids Dream?",
	// "Fear and Loathing!"), never to names.
	if strings.ContainsAny(s, ":!?") {
		return false
	}
	// A trailing parenthetical is an edition marker ("... (Unabridged)").
	if strings.HasSuffix(strings.TrimSpace(s), ")") {
		return false
	}

	fields := strings.Fields(s)
	if len(fields) < 2 || len(fields) > 4 {
		return false
	}

	for i, w := range fields {
		// strings.Fields never yields an empty field, so r[0] is always safe here
		// and a len(r)==0 guard would be unreachable code that no test can kill.
		r := []rune(w)
		// Every word must START WITH A LETTER. Checking only "is not lowercase"
		// is not enough: digits and punctuation are neither upper nor lower, so
		// "Pratchett 036" would pass as a name and get filed as a real author.
		// (That placeholder laundering is what
		// TestExtractFromFilenameDoesNotLaunderThePlaceholder guards.)
		if !unicode.IsLetter(r[0]) {
			return false
		}
		// And it must not be LOWERCASE -- expressed that way, never as "must be
		// uppercase". unicode.IsUpper is false for every caseless script (CJK,
		// Hebrew, Arabic, Thai), so requiring positive uppercase drops them all.
		// Interior lowercase is allowed only for name particles.
		if unicode.IsLower(r[0]) {
			if i == 0 || !nameParticles[strings.ToLower(w)] {
				return false
			}
		}
	}
	return true
}
