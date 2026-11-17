// file: internal/transcribe/clip.go
// version: 1.1.1
// guid: 9c4e1b73-28a5-4d06-bf92-7a3d05e8c614
// last-edited: 2026-11-17

package transcribe

import (
	"regexp"
	"strings"
	"unicode"
)

// ClipKind is what a stored per-file intro clip IS.
//
// IntroKind answers a narrower question for the regroup classifier ("does this
// file START a book, CONTINUE one, or say nothing?"). ClipKind answers the
// question an operator asks of a stored transcript: what did Whisper hear? It
// comes from its own parser (clip_classify.go), separate from ClassifyIntro, so
// the two may disagree about a clip: ClipKind adds front/back matter,
// credits-with-no-title, silence and garbage, and reads markers the legacy
// grammar does not. Nothing that decides identity reads ClipKind.
//
// The kind is additive to a row's status. A row's status stays the vocabulary
// readers already switch on (ok / unparsed / empty / the failure statuses);
// ClipKind never replaces it and never changes which files a transcription run
// selects.
type ClipKind string

const (
	// ClipKindNone is "nothing was classified": no transcript at all. It is the
	// zero value and is never stored.
	ClipKindNone ClipKind = ""

	// ClipKindTitle is a book-opening announcement: "<TITLE> written by <AUTHOR>,
	// read by <NARRATOR>". The only kind that carries identity.
	ClipKindTitle ClipKind = "title_announcement"

	// ClipKindCredits is publisher or production boilerplate with no title:
	// "This is Audible.", "Narrated by X. Copyright 2019. All rights reserved."
	ClipKindCredits ClipKind = "credits"

	// ClipKindChapter is a chapter/track/episode marker: "Chapter 12", "12.".
	// It confirms POSITION inside a book, never which book.
	ClipKindChapter ClipKind = "chapter_marker"

	// ClipKindPart is a part/book/section/disc marker: "Part Two", "Disc 3".
	ClipKindPart ClipKind = "part_marker"

	// ClipKindMatter is front or back matter: prologue, epilogue, interlude,
	// afterword, foreword, introduction and the like.
	ClipKindMatter ClipKind = "front_back_matter"

	// ClipKindNarration is running text with no announcement and no marker.
	ClipKindNarration ClipKind = "narration"

	// ClipKindSilence is music, a sound tag, or the [SILENCE] sentinel: nothing
	// was said.
	ClipKindSilence ClipKind = "music_or_silence"

	// ClipKindGarbage is text that carries no usable content: a Whisper
	// hallucination, a repeated token, symbols, or a fragment too short to
	// classify (the absent-evidence rule: a six-word scrap is not "narration").
	ClipKindGarbage ClipKind = "garbage"
)

// AllClipKinds lists every storable kind in the order reports print them.
var AllClipKinds = []ClipKind{
	ClipKindTitle, ClipKindCredits, ClipKindChapter, ClipKindPart,
	ClipKindMatter, ClipKindNarration, ClipKindSilence, ClipKindGarbage,
}

// ClipParserVersion is bumped whenever ParseClip's grammar changes in a way that
// can change a stored result. A stored parse carries the version that produced
// it, so a re-parse can tell "never parsed" from "parsed by the current grammar".
const ClipParserVersion = 1

// ClipParse is the structured reading of one stored intro clip.
//
// Chapter and part numbers are kept apart from the identity fields on purpose:
// "Chapter 12" confirms where in a book this file sits, and says nothing about
// which book it is. Identity (Title, Author, ...) is filled only for a
// title_announcement, and only that kind may ever write a row's identity columns.
type ClipParse struct {
	Kind ClipKind
	// Reason is a machine-stable token for diagnosis and tests. It is not stored.
	Reason string

	// ChapterNumber is the announced chapter/track/episode number; 0 with
	// HasChapterNumber false when none was stated.
	ChapterNumber    int
	HasChapterNumber bool
	// ChapterTitle is the title that followed the chapter number, "" when absent
	// or when the words after it read as narration.
	ChapterTitle string
	// PartNumber is the announced part/book/section/disc number.
	PartNumber    int
	HasPartNumber bool
	// Marker is the keyword that announced a marker kind ("chapter", "prologue").
	Marker string

	Title       string
	Author      string
	Narrator    string
	Translator  string
	CoverArtist string
	// Publisher is the imprint named by "<publisher> presents", "published by",
	// "a production of" or a jingle. Stored on the parse; not an identity column.
	Publisher string

	// CollectionFrom/To are set when the title carries "books N through M" style
	// wording, so a collection is recognisable without opening its title.
	CollectionFrom int
	CollectionTo   int
}

var (
	// soundTagRe is what Whisper writes for non-speech. Only bracketed tags and a
	// short list of parenthesised ones: "(Part One)" is speech, "(music)" is not.
	soundTagRe = regexp.MustCompile(`(?i)\[[^\]]{0,40}\]|\((?:music|applause|laughter|laughs|silence|inaudible|blank[ _]audio|noise|sighs?|coughs?)[^)]{0,20}\)|[\x{266A}\x{266B}\x{266C}\x{2669}]+`)

	// hallucinationRe is what Whisper emits for near-silent audio: stock phrases
	// from its training subtitles. Whole-clip only; the same words inside real
	// text are untouched.
	hallucinationRe = regexp.MustCompile(`^(?:thank you(?: so much)?(?: for (?:watching|listening))?|thanks(?: for (?:watching|listening))?|you|bye|okay|hmm+|uh+|um+|please subscribe[a-z ]*|subtitles by .*|transcribed by .*|www\.[a-z0-9.]+)$`)

	// creditsOnlyRe recognises the opening of a credits/production run that has
	// no title in front of it.
	creditsOnlyRe = regexp.MustCompile(`(?i)^(?:(?:this (?:has been|is)|you (?:have been|are) (?:listening to|hearing))\s+)?(?:` +
		`(?:written|authored|narrated|read|performed|voiced|produced|directed|edited|translated|published|presented)(?:\s+and\s+\w+)?(?:\s+for\s+you)?\s+by\b|` +
		`copyright\b|\x{00A9}|all rights reserved|` +
		`(?:an?\s+)?(?:[\w']+\s+){0,2}production(?:\s+of)?\b|` +
		`(?:un)?abridged\b)`)

	// publisherishRe flags a short fragment as publisher boilerplate.
	publisherishRe = regexp.MustCompile(`(?i)\b(?:audio|audible|audiobooks?|books|publishing|publishers?|studios?|productions?|press|records|entertainment|enterprises|company|network|media|originals?|library|copyright|rights reserved)\b`)

	presentsTailRe = regexp.MustCompile(`(?i)\s*presents\b[\s,.:;\x{2013}\x{2014}-]*$`)

	publishedByRe = regexp.MustCompile(`(?i)\b(?:published by|a production of)\s+`)
	writtenByRe   = regexp.MustCompile(`(?i)\b(?:written|authored)\s+by\s+`)
)

// stripSoundTags removes non-speech tags from a transcript and reports whether
// any were present.
func stripSoundTags(s string) (string, bool) {
	if !soundTagRe.MatchString(s) {
		return s, false
	}
	return strings.TrimSpace(soundTagRe.ReplaceAllString(s, " ")), true
}

// publisherFromLead turns the text that led a "presents" into the publisher.
func publisherFromLead(lead string) string {
	return clean(presentsTailRe.ReplaceAllString(lead, ""))
}

// publisherFromJingle names the imprint behind a stock jingle ("This is
// Audible." -> "Audible").
func publisherFromJingle(jingle string) string {
	j := strings.TrimSpace(strings.Trim(jingle, ".,! "))
	low := strings.ToLower(j)
	switch {
	case strings.HasPrefix(low, "this is "):
		return clean(j[len("this is "):])
	case strings.HasPrefix(low, "welcome to "):
		return clean(j[len("welcome to "):])
	case strings.HasPrefix(low, "audible hopes"), strings.HasPrefix(low, "an audible original"):
		return "Audible"
	default:
		return publisherFromLead(j)
	}
}

// ParseClip classifies one stored intro transcript and extracts its fields.
//
// It is a pure function of the text: same text, same parse, on every run, with
// no position, store or Whisper involved. That is what makes the re-parse op
// safe to repeat and lets a freshly transcribed file and a re-parsed one agree.
func ParseClip(text string) ClipParse {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return ClipParse{Kind: ClipKindNone, Reason: string(ReasonNoTranscript)}
	}
	if raw == SilenceSentinel {
		return ClipParse{Kind: ClipKindSilence, Reason: string(ReasonSilenceSentinel)}
	}
	cleaned, hadTags := stripSoundTags(raw)
	if !hasContent(cleaned) {
		if hadTags {
			return ClipParse{Kind: ClipKindSilence, Reason: "sound-tag-only"}
		}
		return ClipParse{Kind: ClipKindGarbage, Reason: "no-content"}
	}
	if reason := garbageReason(cleaned); reason != "" {
		return ClipParse{Kind: ClipKindGarbage, Reason: reason}
	}

	v := viewClip(cleaned)
	if v.boilerplateOnly {
		text := v.stripped
		if text == "" {
			text = v.jingle
		}
		return ClipParse{Kind: ClipKindCredits, Reason: string(ReasonBoilerplateOnly), Publisher: publisherFromJingle(text)}
	}

	c := classifyClip(v)
	switch c.kind {
	case IntroKindCredits:
		return titleParse(c, v)
	case IntroKindChapter:
		return markerParse(c, v)
	}

	// Neither an announcement nor a counted marker: front/back matter, a credits
	// run with no title, a fragment, or narration.
	if mk, ok := parseMarkerAt(v.body); ok && mk.Class == markerMatter {
		return ClipParse{Kind: ClipKindMatter, Reason: "matter-marker", Marker: mk.Keyword}
	}
	if creditsOnlyRe.MatchString(v.body) {
		p := creditsOnlyParse(v)
		p.Reason = "credits-only"
		return p
	}
	if len(strings.Fields(v.body)) < minProseWords {
		if publisherishRe.MatchString(v.body) {
			return ClipParse{Kind: ClipKindCredits, Reason: "publisher-fragment", Publisher: v.publisher}
		}
		return ClipParse{Kind: ClipKindGarbage, Reason: "fragment-too-short"}
	}
	return ClipParse{Kind: ClipKindNarration, Reason: string(ReasonNoAnnouncement)}
}

// titleParse builds the parse of a book-opening announcement.
func titleParse(c clipClass, v clipView) ClipParse {
	f := c.fields
	p := ClipParse{
		Kind: ClipKindTitle, Reason: string(ReasonCreditGrammar),
		Title: f.Title, Author: f.Author, Narrator: f.Narrator,
		Translator: f.Translator, CoverArtist: f.CoverArtist,
		Publisher: v.publisher,
	}
	if p.Publisher == "" {
		p.Publisher = roleFromSpans(publishedByRe, v.body)
	}
	if from, to, ok := collectionRange(f.Title); ok {
		p.CollectionFrom, p.CollectionTo = from, to
	}
	return p
}

// markerParse builds the parse of a chapter/part marker clip.
func markerParse(c clipClass, v clipView) ClipParse {
	p := ClipParse{Reason: string(c.reason)}
	rest := v.body
	var chapterRest string
	first := true
	for range 3 {
		mk, ok := parseMarkerAt(rest)
		if !ok || mk.Class == markerMatter {
			break
		}
		if first {
			p.Marker = mk.Keyword
			first = false
		}
		switch mk.Class {
		case markerChapter:
			if !p.HasChapterNumber {
				p.ChapterNumber, p.HasChapterNumber = mk.Number, true
				chapterRest = rest[mk.End:]
			}
		case markerPart:
			if !p.HasPartNumber {
				p.PartNumber, p.HasPartNumber = mk.Number, true
			}
		}
		rest = strings.TrimLeft(rest[mk.End:], " \t.,:;-–—")
	}
	if first {
		// Continuation wording ("This part includes Chapter 2"): the classifier
		// found a number but there is no marker at the start to read a title from.
		p.Marker = "continuation"
		if c.chapterNumber > 0 {
			p.ChapterNumber, p.HasChapterNumber = c.chapterNumber, true
		}
		p.Kind = ClipKindChapter
		return p
	}
	if p.HasChapterNumber {
		p.Kind = ClipKindChapter
		p.ChapterTitle = chapterTitleAfter(chapterRest)
	} else {
		p.Kind = ClipKindPart
	}
	return p
}

// creditsOnlyParse extracts what a title-less credits run names.
func creditsOnlyParse(v clipView) ClipParse {
	p := ClipParse{Kind: ClipKindCredits, Publisher: v.publisher}
	body := v.body
	if _, tail, ok := splitOnFirstRe(writtenByRe, body); ok {
		p.Author = truncateName(tail)
	}
	if _, tail, ok := splitOnFirstRe(creditVerbByRe, body); ok {
		p.Narrator = truncateName(tail)
	}
	p.Translator = roleFromSpans(translatorByRe, body)
	p.CoverArtist = roleFromSpans(coverArtByRe, body)
	if p.Publisher == "" {
		p.Publisher = roleFromSpans(publishedByRe, body)
	}
	return p
}

// hasContent reports whether s has at least one letter or digit.
func hasContent(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

const (
	// minRepeatWords is how many words a clip needs before its vocabulary is
	// judged: a short announcement legitimately repeats a word.
	minRepeatWords = 8
	// maxRepeatDistinctRatio is the share of distinct words at or below which a
	// clip of minRepeatWords or more is a stuck decoder, not speech.
	maxRepeatDistinctRatio = 0.25
	// minLetterShare is the share of non-space characters that must be letters or
	// digits for a clip to be text and not punctuation noise.
	minLetterShare = 0.4
)

// garbageReason returns why s is garbage, or "" when it is not.
func garbageReason(s string) string {
	low := strings.ToLower(strings.Trim(strings.TrimSpace(s), ".,!?;: \t\"'"))
	if hallucinationRe.MatchString(low) {
		return "hallucination"
	}
	words := strings.Fields(strings.ToLower(s))
	if len(words) >= 4 {
		distinct := map[string]bool{}
		for _, w := range words {
			distinct[strings.Trim(w, ".,!?;:\"'")] = true
		}
		if len(distinct) == 1 {
			return "repeated-token"
		}
		if len(words) >= minRepeatWords && float64(len(distinct))/float64(len(words)) <= maxRepeatDistinctRatio {
			return "repeated-vocabulary"
		}
	}
	letters, others := 0, 0
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			letters++
		default:
			others++
		}
	}
	if letters+others >= 5 && float64(letters)/float64(letters+others) < minLetterShare {
		return "symbol-noise"
	}
	return ""
}
