// file: internal/transcribe/classify.go
// version: 1.0.0
// guid: 7d41f0a2-6c93-4b18-a5e7-2f9b6c0d3184
// last-edited: 2026-08-07

package transcribe

import (
	"regexp"
	"strconv"
	"strings"
)

// SilenceSentinel is stored in place of a transcript when every retry (longer
// clip, then the second audio file) returned zero characters from Whisper. It
// records "we tried and there is nothing here" so incremental runs skip the file
// instead of re-paying for it forever.
//
// It lives here, beside the classifier that must recognise it, because two
// packages disagreeing about this literal would silently turn "known silent"
// into "unparsed prose". internal/plugins/maintenance aliases this constant
// rather than declaring its own.
const SilenceSentinel = "[SILENCE]"

// IntroKind is what a transcript of a file's opening seconds actually IS.
//
// The old parser answered only "did this yield a title?", which conflated three
// very different states: a genuine book-opening announcement, a chapter/disc
// marker that proves the file is a CONTINUATION, and ordinary narrative prose
// that happened to contain the word "by". Those demand opposite decisions from
// the regroup classifier, so they get distinct kinds.
type IntroKind string

const (
	// IntroKindUnknown means the transcript cannot be interpreted: absent, empty,
	// the [SILENCE] sentinel, or nothing but publisher boilerplate. It NEVER
	// means "continuation" — see the absent-evidence rule below.
	IntroKindUnknown IntroKind = "unknown"

	// IntroKindCredits is a book-opening announcement:
	// "<TITLE> written by <AUTHOR>, performed by <NARRATOR>". Direct identity
	// evidence that a book STARTS at this file.
	IntroKindCredits IntroKind = "credits"

	// IntroKindChapter is a structural marker — "Chapter 12", "Part One",
	// "Disc 2", "This part includes Chapter 2". Evidence the file CONTINUES a
	// book rather than starting one.
	IntroKindChapter IntroKind = "chapter"

	// IntroKindProse is narrative text with no announcement. Weak evidence of
	// continuation: real books do open straight into prose, so this is "no
	// announcement here", not "definitely not a start".
	IntroKindProse IntroKind = "prose"
)

// 🔴 THE ABSENT-EVIDENCE RULE. An absent transcript yields IntroKindUnknown,
// never IntroKindProse and never "continuation". This codebase has been bitten
// four separate ways by reading an absent value as a specific cause:
// DurationSec==0 read as "short", a 404 body read as "zero files", memPtr==nil
// read as "nothing to do", and an empty intro_transcription read as "needs
// transcribing" when it meant "has no file". Unknown is a REFUSAL to guess, and
// callers must branch on it explicitly rather than letting it fall through to a
// default.

// IntroPosition locates a file within its book's canonical (disc, track, path)
// ordering — the discriminator that per-file storage unlocked.
//
// Ordinal MUST be derived from the same sorted slice nthAudioFile produces. A
// caller that derives it from TrackNumber directly will fabricate an order for
// the many rows whose track tags are all zero; pass OrdinalUnknown in that case
// so the position weight is skipped rather than trusted.
type IntroPosition struct {
	Ordinal int // 0-based index in (disc, track, path) order; OrdinalUnknown if untrustworthy
	Total   int // total audio files in the book; 0 when unknown
}

// OrdinalUnknown marks a position whose ordering could not be trusted.
const OrdinalUnknown = -1

// UnknownPosition is the zero-information position, for callers with no file context.
var UnknownPosition = IntroPosition{Ordinal: OrdinalUnknown}

// IntroClassification is the verdict plus the evidence behind it.
type IntroClassification struct {
	Kind IntroKind

	// Fields carries title/author/narrator and is populated ONLY for
	// IntroKindCredits. For every other kind it is deliberately zero: a parse
	// extracted from prose is exactly the false positive this type exists to
	// prevent, so it is never handed back.
	Fields IntroFields

	// Confidence is 0..1. It reflects how strongly the grammar matched, adjusted
	// by position where position is trustworthy.
	Confidence float64

	// Reason is a short machine-stable token explaining the verdict, for
	// operator diagnosis and for First Aid's enqueue-vs-skip decision. The three
	// unknown reasons are distinguished on purpose: "no-transcript" should
	// enqueue a transcription, "silence-sentinel" should NOT (it already failed
	// every retry), and "boilerplate-only" should retry with a longer clip.
	Reason string

	// ChapterNumber is the announced chapter/part number when Kind is
	// IntroKindChapter and a number was stated, else 0. A number >1 is strong
	// continuation evidence regardless of position.
	ChapterNumber int
}

// IntroReason is the machine-stable explanation for a verdict. It is a named
// type so consumers switch exhaustively instead of comparing string literals.
type IntroReason string

const (
	// ReasonNoTranscript — nothing stored. First Aid SHOULD enqueue a transcription.
	ReasonNoTranscript IntroReason = "no-transcript"
	// ReasonSilenceSentinel — every retry already returned zero characters.
	// First Aid must NOT re-enqueue; that work is proven futile.
	ReasonSilenceSentinel IntroReason = "silence-sentinel"
	// ReasonBoilerplateOnly — the clip caught a publisher jingle and nothing
	// else. Worth retrying with a LONGER clip, unlike the two above.
	ReasonBoilerplateOnly IntroReason = "boilerplate-only"
	// ReasonCreditGrammar — a book-opening announcement was parsed.
	ReasonCreditGrammar IntroReason = "credit-grammar"
	// ReasonChapterMarker — the clip opens with a structural announcement.
	ReasonChapterMarker IntroReason = "chapter-marker"
	// ReasonContinuationPhrase — explicit continuation wording ("This part includes…").
	ReasonContinuationPhrase IntroReason = "continuation-phrase"
	// ReasonNoAnnouncement — readable text carrying no announcement at all.
	ReasonNoAnnouncement IntroReason = "no-announcement"
)

// IsCredits reports whether this classification is usable identity evidence.
func (c IntroClassification) IsCredits() bool { return c.Kind == IntroKindCredits }

// IsVerifiable reports whether the transcript carried ANY interpretable signal.
// Callers must not treat !IsVerifiable as evidence of anything.
func (c IntroClassification) IsVerifiable() bool { return c.Kind != IntroKindUnknown }

// ShouldEnqueueTranscription reports whether re-running Whisper could plausibly
// produce a transcript where there is none. It is false for the silence
// sentinel, whose whole purpose is to record that retries were already
// exhausted — re-enqueuing those would loop forever at GPU cost.
func (c IntroClassification) ShouldEnqueueTranscription() bool {
	switch IntroReason(c.Reason) {
	case ReasonNoTranscript, ReasonBoilerplateOnly:
		return true
	default:
		return false
	}
}

// minTitleAgreement is the token-overlap floor below which a parsed credits
// title is considered to name a DIFFERENT book than the curated metadata.
const minTitleAgreement = 0.25

// TitleAgreement returns the share of curated-title tokens that also appear in
// the parsed credits title, in 0..1. Returns 0 for non-credits classifications
// and 1 when there is nothing to compare (absent curated title is not evidence
// of disagreement — the absent-evidence rule again).
func (c IntroClassification) TitleAgreement(curatedTitle string) float64 {
	if c.Kind != IntroKindCredits {
		return 0
	}
	want := titleWords(strings.ToLower(curatedTitle))
	if len(want) == 0 {
		return 1
	}
	got := strings.ToLower(c.Fields.Title)
	hit := 0
	for _, w := range want {
		if strings.Contains(got, w) {
			hit++
		}
	}
	return float64(hit) / float64(len(want))
}

// IsLikelyMisfiled reports a clip that carries a PERFECTLY VALID book-opening
// announcement naming a different book than the one this file is filed under.
//
// This is a distinct defect from a bad parse and needs the opposite fix. The
// production example: a file inside a "Girls with Rebel Souls" folder announces
// "Meet Me in Paradise by Libby Hubscher". The parser was right; the FILE is in
// the wrong place. Treating that as a parser false positive hides a real
// misfiling, so it gets its own predicate and its own review-queue bucket.
func (c IntroClassification) IsLikelyMisfiled(curatedTitle string) bool {
	if c.Kind != IntroKindCredits || strings.TrimSpace(curatedTitle) == "" {
		return false
	}
	if len(titleWords(strings.ToLower(curatedTitle))) == 0 {
		return false // no comparable tokens — cannot verify, so do not accuse
	}
	return c.TitleAgreement(curatedTitle) < minTitleAgreement
}

var (
	// creditVerbByRe separates AUTHOR from NARRATOR: "read|narrated|performed by",
	// plus the "read for you by" variant seen in production transcripts.
	creditVerbByRe = regexp.MustCompile(`(?i)\b(?:read|narrated|performed|voiced)(?:\s+for\s+you)?\s+by\b`)

	// translatorByRe separates AUTHOR from TRANSLATOR. Kept distinct from
	// creditVerbByRe so each role is anchored on its own verb and the order of
	// credits in the announcement does not matter.
	translatorByRe = regexp.MustCompile(`(?i)\b(?:translated|translation)\s+by\b`)

	// combinedCreditByRe matches a SINGLE credit covering both roles:
	// "Written and Narrated by James Gould", "authored and narrated by ...".
	// Without it the "<verb> by" patterns miss (the verbs are conjoined, so
	// "written by" never appears literally) and the split falls back to the bare
	// "by" that sits AFTER both verbs — welding them onto the title:
	//
	//	'Bronze-ranked Brewer, Hawkins Magic Beers, Written and Narrated'
	//	'... a dark fantasy-lit RPG Adventure 7, authored and narrated'
	//
	// Matching it also recovers real information: one person holds both roles,
	// so Author and Narrator are the same name rather than the narrator being
	// unknown. Both orderings appear in the wild.
	// coverArtByRe anchors the album/cover art credit. Like translatorByRe it is
	// kept separate so the role is found wherever it appears in the credit run.
	coverArtByRe = regexp.MustCompile(`(?i)\b(?:cover\s+art|cover\s+design|cover\s+illustration|artwork|illustrated)\s+by\b`)

	combinedCreditByRe = regexp.MustCompile(`(?i)\b(?:` +
		`(?:written|authored|created|adapted|produced)\s*(?:,\s*)?(?:and|&)\s*(?:read|narrated|performed|voiced)|` +
		`(?:read|narrated|performed|voiced)\s*(?:,\s*)?(?:and|&)\s*(?:written|authored|created|adapted|produced)` +
		`)\s+by\b`)

	// titleSepRe separates TITLE from AUTHOR. The authorship-verb forms are
	// listed FIRST so Go's leftmost-match rule consumes "written by" whole rather
	// than matching the bare "by" inside it — which is what welded a credit verb
	// onto 24.8% of stored titles ("Awakened Essence 1 Written").
	//
	// DELIBERATE SEMANTIC CHOICE: "edited by" and "translated by" are treated as
	// authorship separators, so an anthology's EDITOR lands in Author ("Dark
	// Angels … edited by Pam Kesey" -> Author = Pam Kesey). Strictly the editor
	// is not the author, but this field feeds fuzzy identity matching, where the
	// editor is the closest available handle on an anthology and a blank author
	// is strictly worse. Do not "fix" this without changing what consumes it.
	titleSepRe = regexp.MustCompile(`(?i)\b(?:written|authored|edited|translated|adapted|compiled)\s+by\b|\bby\b`)

	// anyCreditVerbRe is the presence test for announcement grammar of any kind.
	anyCreditVerbRe = regexp.MustCompile(`(?i)\b(?:read|narrated|performed|voiced|written|authored|edited|translated|adapted|compiled|produced|presented|directed)(?:\s+for\s+you)?\s+by\b`)

	// chapterMarkerRe matches a structural announcement. Anchored forms only —
	// "chapter" appearing mid-prose is not a marker.
	chapterMarkerRe = regexp.MustCompile(`(?i)^(?:chapter|part|book|disc|track|section|episode)\s+([0-9]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|twenty)\b`)

	// midChapterMarkerRe catches the explicit continuation phrasing anywhere in
	// the clip: "This part includes Chapter 2".
	midChapterMarkerRe = regexp.MustCompile(`(?i)\bthis part (?:includes|contains)\b|\bend of (?:the )?(?:part|chapter|disc|book)\b|\bcontinued\b`)

	// chapterNumRe pulls a chapter/part number out of a structural announcement.
	chapterNumRe = regexp.MustCompile(`(?i)\b(?:chapter|part|disc|track|section)\s+([0-9]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|twenty)\b`)

	// boilerplateOnlyRe matches clips that are nothing but a publisher jingle.
	// "This is Audible." is 12% of the sampled corpus and carries zero identity
	// information — it is UNKNOWN, not prose.
	boilerplateOnlyRe = regexp.MustCompile(`(?i)^(?:this is audible|audible hopes you enjoy[^.]*|an audible original|brilliance audio|welcome to [\w' ]{0,30}audio ?books?|[\w' ]{0,40}audio presents)?[.,!]?$`)

	// proseMarkerRe flags narrative text. Deliberately CASE-SENSITIVE: the
	// discriminator is not which pronoun appears but whether it is lowercase.
	// Whisper title-cases announced titles and lowercases mid-sentence prose, so
	// "Me" in "Meet Me in Paradise" is a title word while "he" in "...and he
	// wasn't mildly amused by Memphis fortunes" is narration. A case-insensitive
	// version of this rule rejected "Meet Me in Paradise" outright — suppressing
	// an entire class of real titles (Call Me By Your Name, Take Me With You).
	proseMarkerRe = regexp.MustCompile(`\b(?:he|she|they|him|her|his|their|me|my|we|us|our|its)\b`)

	// proseVerbRe catches narration that carries no pronoun at all: contractions
	// and past-tense auxiliaries do not occur in announced titles.
	proseVerbRe = regexp.MustCompile(`\b\w+n't\b|\b(?:was|were|had|would|could|should|said|went|came)\b`)
)

const (
	// maxTitleWords caps a plausible announced title. The sampled corpus put a
	// real title at well under this; the confirmed prose false positive
	// ("Chapter 12 Fury drove through DC...") ran to ~150 words before its "by".
	maxTitleWords = 20

	// maxTitleWordsNoVerb is the tighter cap applied when no credit verb
	// corroborates the announcement. With "written by"/"read by" present the
	// grammar is self-evidencing; on a bare "<title> by <author>" the title's
	// length is the only thing standing between a real intro and a sentence.
	maxTitleWordsNoVerb = 12

	// minProseWords is the floor below which a clip carrying no announcement and
	// no structural marker is UNKNOWN rather than prose.
	//
	// Prose is a positive claim — "this file opens mid-narrative" — and a
	// six-word fragment cannot support it. The corpus surfaced publisher
	// taglines ("Big Finish. For the love of stories.") that a naive fallthrough
	// labelled prose, which downstream reads as weak continuation evidence. They
	// are boilerplate: the clip caught the label and stopped. Absent evidence
	// must not become an inference about the file.
	minProseWords = 12

	// creditVerbWindow is how far into the clip an announcement may begin and
	// still be an OPENING. Publisher preambles run long, but a credit verb 1,000
	// characters in belongs to an advertisement or the next book, not this one.
	creditVerbWindow = 400
)

// ClassifyIntro decides what a transcript is, using position as a supporting
// discriminator.
//
// Position is a WEIGHT, never a veto. Credits at ordinal >0 is not an error to
// suppress — it is precisely the shattered/anthology signal the review queue
// exists to surface ("files 5-8 open a second book"). Vetoing it would hide the
// finding this whole signal was built to produce.
//
// ⚠️ The position weighting is UNCALIBRATED. The 600-transcript corpus it was
// tuned against is book-level — every row came from firstAudioFile, so it is
// ~entirely ordinal-0-or-unknown and contains no known ordinal>0 examples. The
// grammar rules below are corpus-backed; the position adjustments are a
// deliberate, conservative guess awaiting the per-file backfill (#22).
func ClassifyIntro(text string, pos IntroPosition) IntroClassification {
	raw := strings.TrimSpace(text)

	// --- Unknown: three distinct absences, reported distinctly. ---
	if raw == "" {
		return IntroClassification{Kind: IntroKindUnknown, Reason: string(ReasonNoTranscript)}
	}
	if raw == SilenceSentinel {
		return IntroClassification{Kind: IntroKindUnknown, Reason: string(ReasonSilenceSentinel)}
	}

	norm := collapseWS(strings.Join(splitLines(raw), " "))
	stripped := strings.TrimSpace(junkOpenerRe.ReplaceAllString(norm, ""))
	if boilerplateOnlyRe.MatchString(strings.TrimSpace(stripped)) {
		// Publisher jingle and nothing else — the clip captured no content.
		return IntroClassification{Kind: IntroKindUnknown, Reason: string(ReasonBoilerplateOnly)}
	}

	body := strings.TrimSpace(presentsPrefixRe.ReplaceAllString(stripped, ""))
	hadPresents := body != stripped

	// --- Credits: announcement grammar with a plausible title. ---
	//
	// A credit verb ("read by", "written by") is a strong CONFIRMATION but must
	// not be a precondition: "On Writing by Stephen King" is a legitimate
	// announcement with no narrator credit at all. Requiring the verb would
	// discard every title/author-only intro. Instead its absence tightens the
	// plausibility bar that the title candidate has to clear.
	loc := anyCreditVerbRe.FindStringIndex(body)
	hasCreditVerb := loc != nil && loc[0] <= creditVerbWindow
	if f, conf, ok := extractCredits(body, hasCreditVerb); ok {
		// Carry the struct through and set Raw, rather than re-listing fields.
		// This line previously enumerated Title/Author/Narrator by hand, so
		// Translator was silently dropped the moment it was added — a new field
		// parsed correctly and then vanished here. Assigning the whole struct
		// makes every future field additive by construction.
		f.Raw = text
		return IntroClassification{
			Kind:       IntroKindCredits,
			Fields:     f,
			Confidence: positionAdjust(conf+boolWeight(hadPresents, 0.05), IntroKindCredits, pos),
			Reason:     string(ReasonCreditGrammar),
		}
	}

	// --- Chapter: structural marker, no usable announcement. ---
	if m := chapterMarkerRe.FindStringSubmatch(body); m != nil {
		n := parseSpokenNumber(m[1])
		return IntroClassification{
			Kind:          IntroKindChapter,
			Confidence:    positionAdjust(chapterConfidence(n), IntroKindChapter, pos),
			Reason:        string(ReasonChapterMarker),
			ChapterNumber: n,
		}
	}
	if midChapterMarkerRe.MatchString(body) {
		n := 0
		if m := chapterNumRe.FindStringSubmatch(body); m != nil {
			n = parseSpokenNumber(m[1])
		}
		return IntroClassification{
			Kind:          IntroKindChapter,
			Confidence:    positionAdjust(chapterConfidence(n), IntroKindChapter, pos),
			Reason:        string(ReasonContinuationPhrase),
			ChapterNumber: n,
		}
	}

	// --- Too short to be anything: unknown, not prose. ---
	if len(strings.Fields(body)) < minProseWords {
		return IntroClassification{Kind: IntroKindUnknown, Reason: string(ReasonBoilerplateOnly)}
	}

	// --- Prose: enough text, no announcement, no structural marker. ---
	return IntroClassification{
		Kind:       IntroKindProse,
		Confidence: positionAdjust(0.6, IntroKindProse, pos),
		Reason:     string(ReasonNoAnnouncement),
	}
}

// roleFromSpans finds a secondary credit (translator, cover artist) by anchoring
// on its own verb and searching each span in turn.
//
// Searching BOTH the author and narrator spans is what makes credit ORDER
// irrelevant: a translator credited before the narrator lands in the author
// span, one credited after lands in the narrator span, and either is found.
// Checking only the author span made the parse order-dependent — caught by
// TestCreditOrderIsIrrelevant, not by inspection.
func roleFromSpans(re *regexp.Regexp, spans ...string) string {
	for _, s := range spans {
		if _, tail, ok := splitOnFirstRe(re, s); ok {
			if name := truncateName(tail); name != "" {
				return name
			}
		}
	}
	return ""
}

// extractCredits runs the staged title/author/narrator split and validates that
// the result looks like an announcement rather than a sentence. Returns ok=false
// when the "title" is prose — the check the old parser lacked entirely.
func extractCredits(body string, hasCreditVerb bool) (f IntroFields, confidence float64, ok bool) {
	// A combined "written and narrated by" credit must be tried FIRST: its verbs
	// are conjoined, so the single-verb patterns miss and the split would fall
	// back to the bare "by" that follows both, welding them onto the title.
	combinedTitle, combinedRest, isCombined := splitOnFirstRe(combinedCreditByRe, body)

	title, rest, hasSep := splitOnFirstRe(titleSepRe, body)
	if isCombined && (!hasSep || len(combinedTitle) < len(title)) {
		title, rest, hasSep = combinedTitle, combinedRest, true
	} else {
		isCombined = false
	}
	if !hasSep {
		return IntroFields{}, 0, false
	}
	f.Title = clean(title)
	if f.Title == "" {
		return IntroFields{}, 0, false
	}

	// A title is short and contains no narration. Both confirmed production
	// false positives fail here: "...he wasn't mildly amused by Memphis
	// fortunes" carries pronouns, and the Marvel box-set clip ran ~150 words
	// before its "by".
	//
	// Without a credit verb to corroborate, the only thing separating an
	// announcement from a sentence is the title's shape, so the cap tightens.
	wordCap := maxTitleWords
	if !hasCreditVerb {
		wordCap = maxTitleWordsNoVerb
	}
	words := strings.Fields(f.Title)
	if len(words) > wordCap {
		return IntroFields{}, 0, false
	}
	if proseMarkerRe.MatchString(f.Title) || proseVerbRe.MatchString(f.Title) {
		return IntroFields{}, 0, false
	}

	author, narrator, hasReadBy := splitOnFirstRe(creditVerbByRe, rest)

	// 🔴 Split the translator off BEFORE truncating the author. The credit order
	// is "<AUTHOR>. Translated by <TRANSLATOR>. Narrated by <NARRATOR>", so the
	// narrator split above leaves the translator sitting INSIDE the author span.
	// Without its own anchor the author absorbs it — measured on prod
	// 2026-08-07, ~half of all translated works were corrupted this way:
	//
	//	'Kugane Maruyama Translated by Emily Balistreri'
	//	'Alexei Asadchuk. Translated by Andrew Douglas'
	//	'Yuri Vinokurov and Oleg Sapphire Translated'
	//
	// Splitting here rather than adding "translated by" to creditVerbByRe keeps
	// the narrator anchored on its own verb, so credit ORDER stays irrelevant:
	// a translator credit appearing before or after the narrator is handled the
	// same way.
	// The translator can appear on EITHER side of the narrator credit, so look
	// in both spans. Checking only the author span made the parse depend on
	// credit order — "Translated by X. Narrated by Y" worked while
	// "Narrated by Y. Translated by X" left the translator welded onto the
	// narrator instead. Anchoring each role on its own verb, and searching both
	// spans, is what actually makes order irrelevant.
	f.Translator = roleFromSpans(translatorByRe, author, narrator)
	f.CoverArtist = roleFromSpans(coverArtByRe, author, narrator)

	// Author and Narrator are whatever precedes the FIRST secondary credit in
	// their span; truncateName cuts at those anchors via nameBoundaryRe.
	f.Author = truncateName(author)
	if hasReadBy {
		f.Narrator = truncateName(narrator)
	}
	if f.Author == "" {
		return IntroFields{}, 0, false
	}

	// "Written and Narrated by X" credits one person with both roles. The
	// announcement carries no second name, so the narrator is known, not
	// missing — recording it is a recovery, not an assumption.
	if isCombined && f.Narrator == "" {
		f.Narrator = f.Author
		hasReadBy = true
	}

	// Confidence rises with how complete the announcement is: a title+author
	// pair is decent, an explicit narrator credit makes it unmistakable.
	confidence = 0.7
	if hasReadBy && f.Narrator != "" {
		confidence = 0.9
	}
	if !hasCreditVerb {
		confidence -= 0.2 // bare "<title> by <author>" — plausible, not corroborated
	}
	if len(words) <= 12 {
		confidence += 0.05
	}
	return f, confidence, true
}

// chapterConfidence scores a structural marker. A number >1 is near-conclusive
// continuation evidence; chapter 1 / part 1 is consistent with a book START and
// so is deliberately weaker.
func chapterConfidence(n int) float64 {
	switch {
	case n > 1:
		return 0.9
	case n == 1:
		return 0.5
	default:
		return 0.6
	}
}

// positionAdjust nudges confidence by where the file sits in its book.
// ⚠️ Uncalibrated — see the note on ClassifyIntro. Deliberately small so a
// wrong ordinal can never flip a verdict, only shade it.
func positionAdjust(conf float64, kind IntroKind, pos IntroPosition) float64 {
	if pos.Ordinal == OrdinalUnknown {
		return clamp01(conf) // no trustworthy order — position contributes nothing
	}
	switch kind {
	case IntroKindCredits:
		if pos.Ordinal == 0 {
			conf += 0.05 // a genuine opening belongs at track 1
		} else {
			// Credits deeper in the book stay CREDITS — that is the shattered-book
			// signal — but flagged as less certain so #23 routes it to review.
			conf -= 0.10
		}
	case IntroKindChapter:
		if pos.Ordinal > 0 {
			conf += 0.05
		}
	}
	return clamp01(conf)
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func boolWeight(b bool, w float64) float64 {
	if b {
		return w
	}
	return 0
}

// spokenNumbers maps the number words Whisper emits in chapter announcements.
var spokenNumbers = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7,
	"eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"fourteen": 14, "fifteen": 15, "twenty": 20,
}

// parseSpokenNumber converts "12" or "twelve" to 12. Returns 0 when unparseable.
func parseSpokenNumber(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return spokenNumbers[s]
}
