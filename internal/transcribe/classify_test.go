// file: internal/transcribe/classify_test.go
// version: 1.0.0
// guid: 5e82c1b7-4d09-42fa-96c3-8b71e0d5a2f9
// last-edited: 2026-08-07

package transcribe

import (
	"strings"
	"testing"
)

// TestClassifyIntroKinds pins the three-way verdict. Every text here is either
// verbatim production output or a minimal reduction of one.
func TestClassifyIntroKinds(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		wantKind IntroKind
		wantRsn  IntroReason
	}{
		// --- Unknown: three distinct absences, never confused for evidence. ---
		{"empty_is_unknown_not_prose", "", IntroKindUnknown, ReasonNoTranscript},
		{"whitespace_is_unknown", "   \n\t ", IntroKindUnknown, ReasonNoTranscript},
		{"silence_sentinel_is_unknown", SilenceSentinel, IntroKindUnknown, ReasonSilenceSentinel},
		// 60 of the first prod sample were this exact string. It is a jingle with
		// zero identity content — not prose, not a continuation.
		{"audible_jingle_only_is_unknown", "This is Audible.", IntroKindUnknown, ReasonBoilerplateOnly},

		// --- Credits. ---
		{
			name:     "written_by_variant_does_not_leak_verb",
			text:     "Awakened Essence 1 Written by Jacob Poole Performed by Alex Perrone",
			wantKind: IntroKindCredits, wantRsn: ReasonCreditGrammar,
		},
		{
			name:     "publisher_presents_written_by",
			text:     "This is Audible. Podium Publishing presents The Sergeant's Apprentice Written by Christopher G. Nuttall Performed by Tavia Gilbert Prologue",
			wantKind: IntroKindCredits, wantRsn: ReasonCreditGrammar,
		},
		{
			name:     "bare_title_by_author_no_narrator",
			text:     "On Writing by Stephen King No one writes a long novel alone",
			wantKind: IntroKindCredits, wantRsn: ReasonCreditGrammar,
		},
		{
			name:     "edited_by_anthology",
			text:     "Dark Angels Lesbian Vampire Erotica edited by Pam Kesey narrated by Veronica Lane",
			wantKind: IntroKindCredits, wantRsn: ReasonCreditGrammar,
		},

		// --- Chapter: continuation evidence. ---
		{
			name:     "chapter_number_opening",
			text:     "Chapter 12 Fury drove through DC. He had a lot on his mind, hidden by mirrored sunglasses.",
			wantKind: IntroKindChapter, wantRsn: ReasonChapterMarker,
		},
		{
			name:     "this_part_includes_is_continuation",
			text:     "This is a reading of Overlord Volume 7. This part includes Chapter 2.",
			wantKind: IntroKindChapter, wantRsn: ReasonContinuationPhrase,
		},

		// --- Prose: the confirmed production false positive. ---
		{
			name:     "prose_containing_by_is_not_credits",
			text:     "The morning dragged on and he wasn't mildly amused by Memphis fortunes, nor the talk that followed.",
			wantKind: IntroKindProse, wantRsn: ReasonNoAnnouncement,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyIntro(tc.text, UnknownPosition)
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q (reason %q, fields %+v)",
					got.Kind, tc.wantKind, got.Reason, got.Fields)
			}
			if IntroReason(got.Reason) != tc.wantRsn {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantRsn)
			}
			// Fields must be empty for every non-credits verdict — handing back a
			// parse extracted from prose is the bug this type exists to prevent.
			if got.Kind != IntroKindCredits {
				if got.Fields.Title != "" || got.Fields.Author != "" {
					t.Errorf("non-credits verdict leaked fields: %+v", got.Fields)
				}
			}
		})
	}
}

// TestWrittenByDoesNotLeakVerbIntoTitle is the single highest-frequency defect
// found in the production corpus: splitting on the bare "by" inside "written by"
// welded the verb onto the title ("Awakened Essence 1 Written").
func TestWrittenByDoesNotLeakVerbIntoTitle(t *testing.T) {
	cases := []struct{ text, wantTitle, wantAuthor, wantNarrator string }{
		{
			"Awakened Essence 1 Written by Jacob Poole Performed by Alex Perrone",
			"Awakened Essence 1", "Jacob Poole", "Alex Perrone",
		},
		{
			"Audible Frontiers presents Glass Houses written by M. J. Locke narrated by Dina Perlman",
			"Glass Houses", "M. J. Locke", "Dina Perlman",
		},
		{
			"Dark Angels Lesbian Vampire Erotica edited by Pam Kesey narrated by Veronica Lane",
			"Dark Angels Lesbian Vampire Erotica", "Pam Kesey", "Veronica Lane",
		},
	}
	for _, tc := range cases {
		t.Run(tc.wantTitle, func(t *testing.T) {
			got := ClassifyIntro(tc.text, UnknownPosition)
			if got.Kind != IntroKindCredits {
				t.Fatalf("Kind = %q, want credits", got.Kind)
			}
			if got.Fields.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", got.Fields.Title, tc.wantTitle)
			}
			if got.Fields.Author != tc.wantAuthor {
				t.Errorf("Author = %q, want %q", got.Fields.Author, tc.wantAuthor)
			}
			if got.Fields.Narrator != tc.wantNarrator {
				t.Errorf("Narrator = %q, want %q", got.Fields.Narrator, tc.wantNarrator)
			}
			// Belt and braces: no credit verb may survive anywhere in the title.
			for _, v := range []string{"written", "edited", "narrated", "performed", "read"} {
				if strings.Contains(strings.ToLower(got.Fields.Title), v) {
					t.Errorf("title %q still contains credit verb %q", got.Fields.Title, v)
				}
			}
		})
	}
}

// TestMisfiledDetection covers the second confirmed production false positive.
// The announcement was parsed CORRECTLY; the file is simply filed under the
// wrong book. That is a different defect from a bad parse and needs the opposite
// fix, so it must be distinguishable.
func TestMisfiledDetection(t *testing.T) {
	c := ClassifyIntro("Meet Me in Paradise by Libby Hubscher read by Amanda Dolan", UnknownPosition)
	if c.Kind != IntroKindCredits {
		t.Fatalf("Kind = %q, want credits — the announcement itself is valid", c.Kind)
	}
	if !c.IsLikelyMisfiled("Girls with Rebel Souls") {
		t.Errorf("expected misfiled: parsed %q vs curated %q (agreement %.2f)",
			c.Fields.Title, "Girls with Rebel Souls", c.TitleAgreement("Girls with Rebel Souls"))
	}
	if c.IsLikelyMisfiled("Meet Me in Paradise") {
		t.Error("matching curated title must not be flagged misfiled")
	}
	// Absent curated title is not evidence of disagreement.
	if c.IsLikelyMisfiled("") {
		t.Error("empty curated title must never be reported as misfiled")
	}
}

// TestAbsentEvidenceNeverImpliesContinuation is the invariant this codebase has
// violated four separate ways. It is asserted directly so a future refactor
// cannot quietly reintroduce it.
func TestAbsentEvidenceNeverImpliesContinuation(t *testing.T) {
	for _, text := range []string{"", "   ", SilenceSentinel, "This is Audible."} {
		c := ClassifyIntro(text, IntroPosition{Ordinal: 5, Total: 12})
		if c.Kind != IntroKindUnknown {
			t.Errorf("text %q -> kind %q; absent evidence must be unknown", text, c.Kind)
		}
		if c.IsVerifiable() {
			t.Errorf("text %q reported verifiable", text)
		}
		// Position must not manufacture a verdict out of nothing: ordinal 5 of 12
		// is exactly where a naive implementation would guess "continuation".
		if c.Kind == IntroKindChapter || c.Kind == IntroKindProse {
			t.Errorf("text %q at ordinal 5 was read as continuation evidence", text)
		}
	}
}

// TestShouldEnqueueTranscription pins the three unknown reasons to different
// First Aid actions — the whole reason they are distinguished.
func TestShouldEnqueueTranscription(t *testing.T) {
	cases := map[string]bool{
		"":                 true,  // never transcribed — do the work
		"This is Audible.": true,  // clip too short — retry longer
		SilenceSentinel:    false, // retries already exhausted — never re-enqueue
	}
	for text, want := range cases {
		if got := ClassifyIntro(text, UnknownPosition).ShouldEnqueueTranscription(); got != want {
			t.Errorf("text %q: ShouldEnqueueTranscription = %v, want %v", text, got, want)
		}
	}
}

// TestPositionIsWeightNotVeto guards the design decision that credits deep in a
// book stay credits. Suppressing them would hide the shattered/anthology signal
// the per-file transcription exists to surface.
func TestPositionIsWeightNotVeto(t *testing.T) {
	const text = "Meet Me in Paradise by Libby Hubscher read by Amanda Dolan"

	first := ClassifyIntro(text, IntroPosition{Ordinal: 0, Total: 8})
	deep := ClassifyIntro(text, IntroPosition{Ordinal: 6, Total: 8})

	if first.Kind != IntroKindCredits || deep.Kind != IntroKindCredits {
		t.Fatalf("position changed the VERDICT: first=%q deep=%q — must only shade confidence",
			first.Kind, deep.Kind)
	}
	if !(deep.Confidence < first.Confidence) {
		t.Errorf("credits at ordinal 6 (%.2f) should be less confident than at ordinal 0 (%.2f)",
			deep.Confidence, first.Confidence)
	}
	// An untrustworthy ordinal must contribute nothing rather than be guessed at.
	unknown := ClassifyIntro(text, UnknownPosition)
	if unknown.Confidence != clamp01(unknown.Confidence) || unknown.Kind != IntroKindCredits {
		t.Errorf("unknown position mishandled: %+v", unknown)
	}
}

// TestChapterNumberDiscriminatesStartFromContinuation: a chapter 1 announcement
// is consistent with a book START; chapter >1 is not.
func TestChapterNumberDiscriminatesStartFromContinuation(t *testing.T) {
	one := ClassifyIntro("Chapter 1 It was the best of times", UnknownPosition)
	twelve := ClassifyIntro("Chapter 12 It was the worst of times", UnknownPosition)

	if one.ChapterNumber != 1 || twelve.ChapterNumber != 12 {
		t.Fatalf("chapter numbers not parsed: %d / %d", one.ChapterNumber, twelve.ChapterNumber)
	}
	if !(one.Confidence < twelve.Confidence) {
		t.Errorf("chapter 1 (%.2f) must be weaker continuation evidence than chapter 12 (%.2f)",
			one.Confidence, twelve.Confidence)
	}
	// Spoken-word numerals appear in real Whisper output.
	if got := ClassifyIntro("Chapter Twelve The road went ever on", UnknownPosition); got.ChapterNumber != 12 {
		t.Errorf("spoken numeral: ChapterNumber = %d, want 12", got.ChapterNumber)
	}
}

// FuzzClassifyIntro checks that no input panics and that the invariants hold for
// arbitrary text — cheap insurance against pathological Whisper output.
func FuzzClassifyIntro(f *testing.F) {
	f.Add("Dune by Frank Herbert read by Scott Brick")
	f.Add(SilenceSentinel)
	f.Add("")
	f.Add("Chapter 3")
	f.Add("by by by by by")

	f.Fuzz(func(t *testing.T, text string) {
		for _, pos := range []IntroPosition{UnknownPosition, {Ordinal: 0}, {Ordinal: 3, Total: 9}} {
			c := ClassifyIntro(text, pos)
			if c.Confidence < 0 || c.Confidence > 1 {
				t.Fatalf("confidence %.3f out of range for %q", c.Confidence, text)
			}
			if c.Kind != IntroKindCredits && (c.Fields.Title != "" || c.Fields.Author != "") {
				t.Fatalf("non-credits kind %q leaked fields for %q", c.Kind, text)
			}
			if c.Kind == IntroKindCredits && c.Fields.Author == "" {
				t.Fatalf("credits with no author for %q", text)
			}
			if c.Reason == "" {
				t.Fatalf("empty reason for %q", text)
			}
		}
	})
}
