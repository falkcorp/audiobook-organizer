// file: internal/plugins/maintenance/file_intro_reparse_identity_test.go
// version: 1.0.0
// guid: 5b8e2f14-93a6-4c70-8d1e-6a4f0b7c9d23
// last-edited: 2026-11-17

package maintenance

import (
	"context"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/franchise"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// These are the review findings of PR #4687 round 1 (B1, M1, M2, M3, m1) written
// down as inputs, each asserting the one thing that must hold: a re-parse never
// changes a stored identity column or a status, and a new transcription's
// identity columns are what the legacy grammar always produced. They were first
// run against 696ab23df, where they fail; they pass once the re-parse writes only
// IntroParse and the clip grammar is a separate parser from the legacy one.
//
// Invented text only: Batman titles and characters.

// liveReparse runs the op live over s and fails the test on an error.
func liveReparse(t *testing.T, s *reparseFakeStore) reparseFileIntrosResult {
	t.Helper()
	res, err := runReparseFileIntros(context.Background(), newReparseEnv(s, &fakeReporter{}), reparseResumeKey{}, false)
	if err != nil {
		t.Fatalf("live re-parse: %v", err)
	}
	return res
}

// identityOf is every column a re-parse must leave alone.
func identityOf(f database.BookFile) [6]string {
	d := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return [6]string{d(f.TranscribedTitle), d(f.TranscribedAuthor), d(f.TranscribedNarrator), d(f.TranscribedTranslator), d(f.TranscribedCoverArtist), d(f.TranscribeStatus)}
}

func TestReparseNeverTouchesStoredIdentity(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		id         database.IntroReparseIdentity
		transcript string
	}{
		{
			// B1: "presents" is an ordinary word in the title, followed by punctuation.
			name:       "B1 title word presents with a colon",
			status:     "ok",
			id:         database.IntroReparseIdentity{Title: "Christmas Presents: A Gotham Anthology", Author: "Alfred Pennyworth"},
			transcript: "Christmas Presents: A Gotham Anthology, edited by Alfred Pennyworth, read by Selina Kyle",
		},
		{
			// M2: the stored title and the transcript disagree; a field must not be
			// filled from a parse of a different announcement.
			name:       "M2 narrator not filled from a different announcement",
			status:     "ok",
			id:         database.IntroReparseIdentity{Title: "Hush", Author: "Selina Kyle"},
			transcript: "The Long Halloween, written by Harvey Dent, read by Barbara Gordon",
		},
		{
			// M2: and an unparsed row is not promoted on the strength of it.
			name:       "M2 unparsed row is not promoted",
			status:     "unparsed",
			id:         database.IntroReparseIdentity{Title: "Hush", Author: "Selina Kyle"},
			transcript: "The Long Halloween, written by Harvey Dent, read by Barbara Gordon",
		},
		{
			// m1: a seven word narrator is not cut to six words.
			name:       "m1 long narrator not truncated",
			status:     "ok",
			id:         database.IntroReparseIdentity{Title: "Hush", Narrator: "Dick Grayson, Barbara Gordon and Selina Kyle Wayne"},
			transcript: "Hush, written by Lucius Fox, read by Dick Grayson, Barbara Gordon and Selina Kyle Wayne",
		},
		{
			// A defective-looking stored value is also kept: repairing identity is a
			// later decision, made from the dry run's disagreement counts.
			name:       "publisher lead left in a stored title is kept",
			status:     "ok",
			id:         database.IntroReparseIdentity{Title: "Gotham Audio presents, Year One", Author: "Alfred Pennyworth"},
			transcript: rpPresent,
		},
		{
			// An empty stored field is not filled either.
			name:       "empty narrator is not filled",
			status:     "ok",
			id:         database.IntroReparseIdentity{Title: "The Killing Joke", Author: "Alfred Pennyworth"},
			transcript: rpTitle,
		},
		{
			name:       "unparsed row with a clean announcement is not promoted",
			status:     "unparsed",
			id:         database.IntroReparseIdentity{},
			transcript: rpTitle,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newReparseFakeStore()
			s.put(rpIntro("b1", "f1", tc.transcript, tc.status, tc.id, nil))
			before := identityOf(s.get("b1", "f1"))

			liveReparse(t, s)

			got := s.get("b1", "f1")
			if after := identityOf(got); after != before {
				t.Fatalf("identity columns changed by a re-parse:\n before %q\n after  %q", before, after)
			}
			if got.IntroParse == nil {
				t.Error("the classification was not stored")
			}
		})
	}
}

// B1, fresh row: a title that contains "presents" keeps its title on a new
// transcription, exactly as the legacy grammar stores it.
func TestNewTranscriptionIdentityIsTheLegacyParse(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name, text, status, title, author string
	}{
		{"B1 fresh row keeps a title ending in Presents", "The Joker's Christmas Presents, written by Edward Nygma", "ok", "The Joker's Christmas Presents", "Edward Nygma"},
		{"M3 marker before a publisher lead", "Part 1. Gotham Audio presents Knightfall, written by Lucius Fox", "ok", "Knightfall", "Lucius Fox"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legacy := transcribe.ParseAudiobookIntro(tc.text)
			if legacy.Title != tc.title || legacy.Author != tc.author {
				t.Fatalf("legacy parse = %q / %q, want %q / %q", legacy.Title, legacy.Author, tc.title, tc.author)
			}
			r := whisperGroupResult(fileIntroWhisperResult{Text: tc.text}, now).res
			if r.Status != tc.status || r.Title != tc.title || r.Author != tc.author {
				t.Fatalf("new row = status %q title %q author %q, want %q %q %q", r.Status, r.Title, r.Author, tc.status, tc.title, tc.author)
			}
		})
	}
}

// M1, fresh row: the franchise-bearing publisher lead is stored exactly as the
// legacy grammar stores it, so the owner-manual guard still sees it.
func TestNewTranscriptionKeepsAFranchiseLead(t *testing.T) {
	text := "Big Finish Productions presents, Hush, written by Lucius Fox, read by Selina Kyle"
	r := whisperGroupResult(fileIntroWhisperResult{Text: text}, time.Now()).res
	if r.Title != "Big Finish Productions presents, Hush" || r.Author != "Lucius Fox" || r.Narrator != "Selina Kyle" {
		t.Fatalf("new row = %q / %q / %q, want the legacy split", r.Title, r.Author, r.Narrator)
	}
	if !franchise.Matches(r.Title) {
		t.Errorf("the stored title %q no longer matches a franchise term", r.Title)
	}
}

// M3: the legacy entry points every other consumer calls give the same title as
// before the clip grammar existed.
func TestLegacyEntryPointsKeepTheirTitles(t *testing.T) {
	text := "Part 1. Gotham Audio presents Knightfall, written by Lucius Fox"
	if got := transcribe.ParseAudiobookIntro(text).Title; got != "Knightfall" {
		t.Errorf("ParseAudiobookIntro title = %q, want Knightfall", got)
	}
	c := transcribe.ClassifyIntro(text, transcribe.UnknownPosition)
	if c.Kind != transcribe.IntroKindCredits || c.Fields.Title != "Knightfall" {
		t.Errorf("ClassifyIntro = %v / %q, want credits / Knightfall", c.Kind, c.Fields.Title)
	}
}

// M1: a row whose only franchise signal is its stored title keeps the owner-manual
// verdict after a re-parse, through the real guard.
func TestReparseKeepsTheOwnerManualGuardVerdict(t *testing.T) {
	const stored = "Big Finish Productions presents, Hush"
	if !franchise.Matches(stored) {
		t.Skipf("franchise.Matches(%q) is false; the fixture no longer exercises the guard", stored)
	}
	s := newReparseFakeStore()
	s.put(rpIntro("b1", "f1", stored+", written by Lucius Fox, read by Selina Kyle", "ok",
		database.IntroReparseIdentity{Title: stored, Author: "Lucius Fox"}, nil))
	book := &database.Book{ID: "b1"}

	guard := func() string {
		f := s.get("b1", "f1")
		kind, _ := repairs.GuardBookTranscribed("b1", book, []database.BookFile{f})
		return kind
	}
	before := guard()
	if before == "" {
		t.Fatalf("fixture: the guard does not hold the book before the re-parse")
	}
	liveReparse(t, s)
	if after := guard(); after != before {
		t.Fatalf("guard verdict changed by a re-parse: %q -> %q", before, after)
	}
}
