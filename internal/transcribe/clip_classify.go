// file: internal/transcribe/clip_classify.go
// version: 1.0.0
// guid: 3d7b9e25-6a41-4c80-b3f2-8e1a5d0c7946
// last-edited: 2026-11-17

package transcribe

import (
	"regexp"
	"strings"
)

// This file is the classifier behind ParseClip. It is deliberately a SEPARATE
// parser from ClassifyIntro / ParseAudiobookIntro.
//
// The legacy parser (classify.go, parse.go) produces the identity columns every
// reader trusts: TranscribedTitle, TranscribedAuthor, TranscribedNarrator and
// TranscribeStatus, the book-level fills, the owner-manual guards, the metadata
// search query and the apply gate. It is byte-for-byte what it was before the
// clip classifier existed, and TestLegacyParseGolden pins that over the corpus
// and every grammar row.
//
// This parser feeds only BookFile.IntroParse, which no reader trusts yet. It is
// free to be more aggressive than the legacy grammar (it accepts a punctuated
// "<publisher> presents," lead, reads Roman numerals and spelled-out numbers in
// markers, recognises front/back matter) because a wrong answer here is a wrong
// stored kind, never a wrong identity. Changing it requires a ClipParserVersion
// bump (TestClipParserVersionIsBumped).

// clipPresentsPrefixRe is the "<publisher> (audio) presents" lead. Unlike the
// legacy presentsPrefixRe the word may be followed by punctuation instead of a
// bare space, because Whisper writes "Wayne Audio presents, The Long Halloween".
// Bounded to ~80 characters so a title that merely contains the word later is not
// eaten, and only treated as a publisher after isPublisherLead.
var clipPresentsPrefixRe = regexp.MustCompile(`(?i)^.{0,80}?\b(?:audio\s+)?presents\b[\s,.:;\x{2013}\x{2014}-]*`)

// creditVerbOnlyRe matches a title candidate that is nothing but a credit verb,
// optionally led by "this has been" and followed by "for you": the front half of
// "Narrated by X" cut at the bare "by".
var creditVerbOnlyRe = regexp.MustCompile(`(?i)^(?:(?:this (?:has been|is)|you (?:have been|are) (?:listening to|hearing))\s+)?(?:an?\s+)?(?:written|authored|narrated|read|performed|voiced|produced|directed|edited|translated|published|presented|adapted|compiled|abridged|unabridged)(?:\s+and\s+\w+)?(?:\s+for\s+you)?$`)

// clipView is a transcript after the normalisation ParseClip's classifier starts
// from: whitespace collapsed, the publisher jingle and any "<publisher> presents"
// lead removed.
type clipView struct {
	// stripped is the text with whitespace collapsed and the jingle removed.
	stripped string
	// body is stripped with the "<publisher> presents" lead removed.
	body string
	// publisher is the text that led the "presents", "" when there was none.
	publisher string
	// hadPresents reports that the lead was found and removed.
	hadPresents bool
	// boilerplateOnly reports a clip that is nothing but a publisher jingle.
	boilerplateOnly bool
	// jingle is the publisher jingle that opened the clip, "" when none.
	jingle string
}

// leadSentenceEndRe splits a lead into sentences.
var leadSentenceEndRe = regexp.MustCompile(`[.!?]+\s+`)

// isPublisherLead reports whether the text before a "presents" reads as a
// publisher: it must not open with a structural marker, carry narration
// (lowercase pronouns or past-tense verbs), or, when it spans several
// sentences, end on a clause that names no publisher ("Bruce stared at the
// screen. Alfred presents..."). A multi-sentence jingle that does end on one
// ("Gotham Audio. A tale in your mind. Gotham Audio presents") still counts.
func isPublisherLead(lead string) bool {
	// Judge the lead WITHOUT the word "presents": it is the last word of the final
	// sentence, and would make "Wayne Audio. A Division of Gotham Books.
	// Presents" end on a clause that names no publisher.
	lead = publisherFromLead(lead)
	if startsWithMarker(lead) || proseMarkerRe.MatchString(lead) || proseVerbRe.MatchString(lead) {
		return false
	}
	if titleHasSentenceBreak(lead) {
		parts := leadSentenceEndRe.Split(strings.TrimSpace(lead), -1)
		return publisherishRe.MatchString(parts[len(parts)-1])
	}
	return true
}

// viewClip normalises a transcript (already trimmed, non-empty, not the
// sentinel).
func viewClip(raw string) clipView {
	norm := collapseWS(strings.Join(splitLines(raw), " "))
	stripped := strings.TrimSpace(junkOpenerRe.ReplaceAllString(norm, ""))
	v := clipView{stripped: stripped, body: stripped}
	if jl := junkOpenerRe.FindString(norm); jl != "" {
		v.jingle = clean(jl)
	}
	if boilerplateOnlyRe.MatchString(stripped) {
		v.boilerplateOnly = true
		return v
	}
	if loc := clipPresentsPrefixRe.FindStringIndex(stripped); loc != nil {
		lead := stripped[:loc[1]]
		// The lead is a PUBLISHER only when it reads like one. A chapter heading
		// or a sentence of narration can also precede the word ("Chapter 3.
		// Alfred presents his findings..."): stripping that would delete the
		// heading and every word before the verb.
		if isPublisherLead(lead) {
			v.body = strings.TrimSpace(stripped[loc[1]:])
			v.publisher = publisherFromLead(lead)
			v.hadPresents = true
		}
	}
	return v
}

// clipClass is the classifier's verdict on one clip.
type clipClass struct {
	kind IntroKind
	// fields is set for IntroKindCredits.
	fields IntroFields
	// chapterNumber is set for IntroKindChapter.
	chapterNumber int
	reason        IntroReason
}

// classifyClip decides what a normalised clip is. It follows ClassifyIntro's
// order (credits grammar, then markers, then too-short, then prose) over the
// clip grammar's view of the text, with the clip grammar's marker reader.
func classifyClip(v clipView) clipClass {
	body := v.body

	loc := anyCreditVerbRe.FindStringIndex(body)
	hasCreditVerb := loc != nil && loc[0] <= creditVerbWindow
	if f, _, ok := extractCredits(body, hasCreditVerb); ok && !creditVerbOnlyRe.MatchString(f.Title) {
		return clipClass{kind: IntroKindCredits, fields: f, reason: ReasonCreditGrammar}
	}

	// Front and back matter ("Prologue", "Epilogue") is not a counted marker: it
	// is as likely to open a book as to continue one. ParseClip reports it as
	// its own kind.
	if mk, ok := parseMarkerAt(body); ok && mk.Class != markerMatter {
		return clipClass{kind: IntroKindChapter, chapterNumber: mk.Number, reason: ReasonChapterMarker}
	}
	if n, ok := midMarker(body); ok {
		return clipClass{kind: IntroKindChapter, chapterNumber: n, reason: ReasonContinuationPhrase}
	}
	if len(strings.Fields(body)) < minProseWords {
		return clipClass{kind: IntroKindUnknown, reason: ReasonBoilerplateOnly}
	}
	return clipClass{kind: IntroKindProse, reason: ReasonNoAnnouncement}
}
