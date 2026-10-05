// file: internal/applygate/transcribed_identity_test.go
// version: 1.6.0
// guid: 329bd78d-e4ca-434d-aa8d-65f766d386a1
// last-edited: 2026-10-04

package applygate

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// A candidate found by searching the book's transcribed title passes the gate
// on that match -- identity and title -- and is still refused on an ASIN
// conflict, a partial book, and identity_stale for any reason other than the
// query. Folder-title searches get no such evidence.
func TestEvaluateTranscribed(t *testing.T) {
	const hulk = "Marvel's Planet Hulk"
	stale := metafetch.ErrStaleMetadataCache
	explained := TranscribedSearch{Query: hulk, Source: "transcribed_title", ExplainsStaleIdentity: true}

	type fixture struct {
		bookTitle   string
		path        string
		transcribed *string
		bookASIN    *string
		duration    *int
		authors     Authors
		cand        metafetch.MetadataCandidate
	}
	base := func() fixture {
		return fixture{
			bookTitle:   "",
			path:        "/library/Greg Pak/Unknown Title/book.m4b",
			transcribed: strp(hulk),
			duration:    intp(36000),
			authors:     Authors{"Greg Pak"},
			cand:        metafetch.MetadataCandidate{Title: hulk, Author: "Greg Pak", Score: 0.95, DurationSec: 36000, Source: "Audible"},
		}
	}

	cases := []struct {
		name        string
		mod         func(*fixture)
		identityErr error
		ts          TranscribedSearch
		wantAllowed bool
		wantReason  string
		wantBlock   string // an evidence check that must still block
		wantEvid    bool
		wantAgree   int // -1: don't check
	}{
		{name: "transcribed match passes identity and title", identityErr: stale, ts: explained,
			wantAllowed: true, wantEvid: true, wantAgree: 3},
		{name: "same match, ASIN conflict still refused",
			mod:         func(f *fixture) { f.bookASIN = strp("B000000001"); f.cand.ASIN = "B000000002" },
			identityErr: stale, ts: explained, wantReason: ReasonASINConflict, wantBlock: ReasonASINConflict, wantEvid: true, wantAgree: -1},
		{name: "same match, partial book still refused",
			mod: func(f *fixture) {
				f.path = "/library/Greg Pak/Unknown Title/Part 2.m4b"
				f.duration, f.cand.DurationSec = nil, 0
			},
			identityErr: stale, ts: explained, wantBlock: ReasonPartialBook, wantEvid: true, wantAgree: -1},
		{name: "stale for another reason (author changed) stays stale", identityErr: stale,
			ts:         TranscribedSearch{Query: hulk, Source: "transcribed_title"},
			wantReason: ReasonIdentityStale, wantAgree: -1},
		{name: "candidate title differs from the transcription stays stale",
			mod:         func(f *fixture) { f.cand.Title = "World War Hulk" },
			identityErr: stale, ts: explained, wantReason: ReasonIdentityStale, wantAgree: -1},
		{name: "folder-title search gets no evidence and stays stale", identityErr: stale,
			wantReason: ReasonIdentityStale, wantAgree: -1},
		{name: "no transcribed search: the placeholder title blocks as before",
			wantReason: ReasonTitleDisagrees, wantBlock: ReasonTitleDisagrees, wantAgree: -1},
		{name: "file-level transcription: the title match counts once",
			mod: func(f *fixture) {
				f.transcribed = nil
				f.duration, f.cand.DurationSec = nil, 0
			},
			ts:          TranscribedSearch{Query: hulk, Source: "file_transcribed_title"},
			wantAllowed: true, wantEvid: true, wantAgree: 2},
		{name: "file-level transcription heard another author: refused",
			mod: func(f *fixture) {
				f.transcribed = nil
				f.duration, f.cand.DurationSec = nil, 0
				f.cand.Author = "Someone Else"
			},
			ts:        TranscribedSearch{Query: hulk, Author: "Greg Pak", Source: "file_transcribed_title"},
			wantBlock: ReasonTitleDisagrees, wantAgree: -1},
		{name: "file-level transcription heard the candidate's author: passes",
			mod: func(f *fixture) {
				f.transcribed = nil
				f.duration, f.cand.DurationSec = nil, 0
			},
			ts:          TranscribedSearch{Query: hulk, Author: "Greg Pak", Source: "file_transcribed_title"},
			wantAllowed: true, wantEvid: true, wantAgree: 2},
		{name: "book-level transcription alone is one fact, not two",
			mod: func(f *fixture) {
				f.duration, f.cand.DurationSec = nil, 0
				f.authors, f.cand.Author = nil, ""
			},
			identityErr: stale, ts: explained, wantReason: ReasonInsufficientEvidence, wantEvid: true, wantAgree: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := base()
			if tc.mod != nil {
				tc.mod(&f)
			}
			book := &database.Book{ID: "b1", Title: f.bookTitle, FilePath: f.path, TranscribedTitle: f.transcribed,
				ASIN: f.bookASIN, Duration: f.duration}
			cand := f.cand
			var idErr error
			if tc.identityErr != nil {
				idErr = errors.Join(tc.identityErr, errors.New("book b1 (stored x, current y)"))
			}
			v := EvaluateTranscribed(book, f.authors, database.ComputeBookRuntime(book, nil), &cand, idErr, nil, tc.ts, ManualOnlyGuard{Bulk: true})

			if v.Allowed != tc.wantAllowed {
				t.Fatalf("allowed = %v (reason %q: %s), want %v", v.Allowed, v.Reason, v.Detail, tc.wantAllowed)
			}
			if tc.wantReason != "" && v.Reason != tc.wantReason {
				t.Errorf("reason = %q (%s), want %q", v.Reason, v.Detail, tc.wantReason)
			}
			if tc.wantBlock != "" {
				blocked := false
				for _, ch := range v.Evidence.Checks {
					blocked = blocked || (ch.Outcome == OutcomeBlock && ch.Reason == tc.wantBlock)
				}
				if !blocked {
					t.Errorf("no evidence check blocks with %q: %+v", tc.wantBlock, v.Evidence.Checks)
				}
			}
			if got := v.IdentityEvidence != nil; got != tc.wantEvid {
				t.Errorf("verdict identity evidence = %+v, want present=%v", v.IdentityEvidence, tc.wantEvid)
			}
			if tc.wantEvid {
				if cand.IdentityEvidence == nil || cand.IdentityEvidence.Kind != metafetch.IdentityEvidenceTranscribedTitle ||
					cand.IdentityEvidence.Query != hulk || !strings.Contains(cand.IdentityEvidence.Detail, hulk) {
					t.Errorf("candidate evidence = %+v, want transcribed_title %q", cand.IdentityEvidence, hulk)
				}
			} else if cand.IdentityEvidence != nil {
				t.Errorf("candidate stamped with evidence it did not use: %+v", cand.IdentityEvidence)
			}
			if tc.wantAgree >= 0 && v.Evidence.Agreements != tc.wantAgree {
				t.Errorf("agreements = %d, want %d (checks %+v)", v.Evidence.Agreements, tc.wantAgree, v.Evidence.Checks)
			}
		})
	}
}

// The transcription never lifts two title blocks: a candidate whose title is
// the series name ("Discworld" heard, "Discworld" of series Discworld
// returned), and a book whose folder names another work ("Guards! Guards!"
// heard in ".../Mort/"). A placeholder folder or an author folder is no
// evidence and does not stop the lift.
func TestEvaluateTranscribed_BlocksTheTranscriptionCannotLift(t *testing.T) {
	stale := errors.Join(metafetch.ErrStaleMetadataCache, errors.New("x"))
	cases := []struct {
		name, title, path, heard string
		firstFile                string // TranscribedSearch.FirstFilePath
		cand                     metafetch.MetadataCandidate
		wantAllowed              bool
		wantInDetail             string
	}{
		{name: "candidate title is the series name", path: "/library/Terry Pratchett/Unknown Title/book.m4b", heard: "Discworld",
			cand:         metafetch.MetadataCandidate{Title: "Discworld", Series: "Discworld"},
			wantInDetail: "is the series name"},
		{name: "blank title, folder names another work", path: "/library/Terry Pratchett/Mort/Mort.m4b", heard: "Guards! Guards!",
			cand:         metafetch.MetadataCandidate{Title: "Guards! Guards!"},
			wantInDetail: `folder names "Mort"`},
		{name: "chapter title, folder names another work", title: "Chapter 1", path: "/library/Terry Pratchett/Mort/01.mp3", heard: "Guards! Guards!",
			cand:         metafetch.MetadataCandidate{Title: "Guards! Guards!", SeriesPosition: "1"},
			wantInDetail: `folder names "Mort"`},
		// A multi-file book's FilePath is its work folder, a directory:
		// read as a file path it named the author folder or nothing.
		{name: "directory path, folder names another work", path: "/library/Terry Pratchett/Mort", heard: "Guards! Guards!",
			cand:         metafetch.MetadataCandidate{Title: "Guards! Guards!"},
			wantInDetail: `folder names "Mort"`},
		{name: "directory path with a trailing slash", path: "/library/Terry Pratchett/Mort/", heard: "Guards! Guards!",
			cand:         metafetch.MetadataCandidate{Title: "Guards! Guards!"},
			wantInDetail: `folder names "Mort"`},
		{name: "directory path under a placeholder author folder", path: "/library/Unknown Author/Mort", heard: "Guards! Guards!",
			cand:         metafetch.MetadataCandidate{Title: "Guards! Guards!"},
			wantInDetail: `folder names "Mort"`},
		{name: "first present file in a disc folder names another work", path: "/library/Unknown Author/Unknown Title",
			firstFile: "/library/Terry Pratchett/Mort/CD1/01.mp3", heard: "Guards! Guards!",
			cand:         metafetch.MetadataCandidate{Title: "Guards! Guards!"},
			wantInDetail: `folder names "Mort"`},
		{name: "directory path agreeing with the transcription: lifted", path: "/library/Terry Pratchett/Guards Guards", heard: "Guards! Guards!",
			cand: metafetch.MetadataCandidate{Title: "Guards! Guards!"}, wantAllowed: true},
		{name: "folder agrees with the transcription: lifted", path: "/library/Terry Pratchett/Guards Guards/book.m4b", heard: "Guards! Guards!",
			cand: metafetch.MetadataCandidate{Title: "Guards! Guards!"}, wantAllowed: true},
		{name: "author folder is no evidence: lifted", path: "/library/Terry Pratchett/book.m4b", heard: "Guards! Guards!",
			cand: metafetch.MetadataCandidate{Title: "Guards! Guards!"}, wantAllowed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := &database.Book{ID: "b1", Title: tc.title, FilePath: tc.path, TranscribedTitle: strp(tc.heard), Duration: intp(36000)}
			cand := tc.cand
			cand.Author, cand.Score, cand.DurationSec, cand.Source = "Terry Pratchett", 0.95, 36000, "Audible"
			ts := TranscribedSearch{Query: tc.heard, Source: "transcribed_title", ExplainsStaleIdentity: true, FirstFilePath: tc.firstFile}
			v := EvaluateTranscribed(book, Authors{"Terry Pratchett"}, database.ComputeBookRuntime(book, nil), &cand, stale, nil, ts, ManualOnlyGuard{Bulk: true})
			if v.Allowed != tc.wantAllowed {
				t.Fatalf("allowed = %v (reason %q: %s), want %v", v.Allowed, v.Reason, v.Detail, tc.wantAllowed)
			}
			if tc.wantAllowed {
				return
			}
			if v.Reason != ReasonIdentityStale && v.Reason != ReasonTitleDisagrees {
				t.Errorf("reason = %q (%s)", v.Reason, v.Detail)
			}
			if cand.IdentityEvidence != nil {
				t.Errorf("evidence stamped on a block the transcription did not lift: %+v", cand.IdentityEvidence)
			}
			found := false
			for _, ch := range v.Evidence.Checks {
				if ch.Name == "title" {
					found = ch.Outcome == OutcomeBlock && strings.Contains(ch.Detail, tc.wantInDetail)
					if !found {
						t.Errorf("title check = %s (%s), want a block naming %q", ch.Outcome, ch.Detail, tc.wantInDetail)
					}
				}
			}
			if !found {
				t.Error("no blocking title check")
			}
		})
	}
}

func TestTranscribedSearchConfirms(t *testing.T) {
	cases := []struct {
		query, heard, title, author string
		want                        bool
	}{
		{"Marvel's Planet Hulk", "", "Marvel's Planet Hulk", "Greg Pak", true},
		{"marvel's planet hulk", "", "Marvel's Planet Hulk", "Greg Pak", true},
		{"Planet Hulk", "", "Planet Hulk: Gladiator", "Greg Pak", false},
		{"Planet Hulk", "", "World War Hulk", "Greg Pak", false},
		{"", "", "Planet Hulk", "Greg Pak", false},
		{"Planet Hulk", "", "", "Greg Pak", false},
		// "X, by A" heard: the candidate must credit A.
		{"Planet Hulk", "Greg Pak", "Planet Hulk", "Greg Pak", true},
		{"Planet Hulk", "Greg Pak", "Planet Hulk", "Someone Else", false},
		// A heard author of 3 characters or fewer is too short to judge by
		// (util.MainTranscriptionConfirms), as on main.
		{"Planet Hulk", "Pak", "Planet Hulk", "Someone Else", true},
	}
	for _, tc := range cases {
		c := &metafetch.MetadataCandidate{Title: tc.title, Author: tc.author}
		ts := TranscribedSearch{Query: tc.query, Author: tc.heard}
		if got := TranscribedSearchConfirms(ts, c); got != tc.want {
			t.Errorf("TranscribedSearchConfirms(%q by %q, %q by %q) = %v, want %v", tc.query, tc.heard, tc.title, tc.author, got, tc.want)
		}
	}
}

// A Doctor Who / Big Finish / Torchwood book is refused by every bulk apply,
// found by any field -- here a blank-titled book whose intro transcription
// names it and whose candidate matches that transcription exactly, i.e. a
// book the transcribed-title lift would otherwise pass -- and no bulk owner
// pin lifts it. A single-book caller (zero guard) is not refused.
func TestEvaluateTranscribed_OwnerManualOnly(t *testing.T) {
	const chimes = "Doctor Who: The Chimes of Midnight"
	cases := []struct {
		name          string
		path          string
		title         string // the book's stored title ("" = blank)
		transcribed   string
		candTitle     string
		candSeries    string
		candPublisher string
		// author is the candidate's (and the book's live) author; "" means a
		// neutral one, so each case is held by the ONE field it names and a
		// mutation dropping that field's check fails it.
		author      string
		guard       ManualOnlyGuard
		wantAllowed bool
		wantReason  string
		// wantDetail must appear in the refusal's detail: it names which
		// check fired, so a case cannot pass on some other field.
		wantDetail string
	}{
		{name: "bulk: transcription and candidate name Doctor Who", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: chimes, candTitle: chimes, guard: ManualOnlyGuard{Bulk: true}, wantReason: ReasonOwnerManualOnly,
			wantDetail: `search query "Doctor Who: The Chimes of Midnight"`},
		{name: "bulk: only the candidate's series names Big Finish", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: "The Chimes of Midnight", candTitle: "The Chimes of Midnight", candSeries: "Big Finish Main Range",
			guard: ManualOnlyGuard{Bulk: true}, wantReason: ReasonOwnerManualOnly, wantDetail: `candidate series "Big Finish Main Range"`},
		{name: "bulk: only the path names Torchwood", path: "/library/Torchwood/Unknown Title/book.m4b",
			transcribed: "Border Princes", candTitle: "Border Princes", guard: ManualOnlyGuard{Bulk: true}, wantReason: ReasonOwnerManualOnly,
			wantDetail: `path "/library/Torchwood/Unknown Title/book.m4b"`},
		{name: "bulk: the store found a Doctor Who file", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: "Spare Parts", candTitle: "Spare Parts",
			guard:      ManualOnlyGuard{Bulk: true, StoreDetail: "file \"/library/Doctor Who/Spare Parts/01.mp3\""},
			wantReason: ReasonOwnerManualOnly, wantDetail: `file "/library/Doctor Who/Spare Parts/01.mp3"`},
		// A store read fault is its own reason: the check could not be done.
		// It still refuses (fail closed), and no bulk pin lifts it.
		{name: "bulk: the store read failed", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: "Marvel's Planet Hulk", candTitle: "Marvel's Planet Hulk",
			guard:      ManualOnlyGuard{Bulk: true, ReadErr: "could not read the files for the owner-manual check: disk"},
			wantReason: ReasonOwnerManualCheckFailed, wantDetail: "could not read the files"},
		{name: "single-book caller: allowed", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: chimes, candTitle: chimes, wantAllowed: true},
		{name: "bulk: an unrelated book is not caught", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: "Marvel's Planet Hulk", candTitle: "Marvel's Planet Hulk",
			guard: ManualOnlyGuard{Bulk: true}, wantAllowed: true},
		{name: "bulk: only the candidate's author names Big Finish", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: "Marvel's Planet Hulk", candTitle: "Marvel's Planet Hulk", author: "Big Finish Productions",
			guard: ManualOnlyGuard{Bulk: true}, wantReason: ReasonOwnerManualOnly, wantDetail: `candidate author "Big Finish Productions"`},
		// The book's own title is the only signal: query and candidate are
		// neutral, so no earlier or later leg can hold it.
		{name: "bulk: only the book's title names Doctor Who", path: "/library/Unknown Author/Unknown Title/book.m4b",
			title: "Doctor Who: Spare Parts", transcribed: "Spare Parts", candTitle: "Spare Parts",
			guard: ManualOnlyGuard{Bulk: true}, wantReason: ReasonOwnerManualOnly, wantDetail: `title "Doctor Who: Spare Parts"`},
		// The candidate's title is the only signal: the transcription (and so
		// ts.Query, which is checked first) stays neutral.
		{name: "bulk: only the candidate's title names Doctor Who", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: "Spare Parts", candTitle: "Doctor Who: Spare Parts",
			guard: ManualOnlyGuard{Bulk: true}, wantReason: ReasonOwnerManualOnly, wantDetail: `candidate title "Doctor Who: Spare Parts"`},
		{name: "bulk: only the candidate's publisher names Big Finish", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: "The Chimes of Midnight", candTitle: "The Chimes of Midnight", candPublisher: "Big Finish Productions",
			guard: ManualOnlyGuard{Bulk: true}, wantReason: ReasonOwnerManualOnly, wantDetail: `candidate publisher "Big Finish Productions"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			author := tc.author
			if author == "" {
				author = "Robert Shearman"
			}
			book := &database.Book{ID: "b1", Title: tc.title, FilePath: tc.path, TranscribedTitle: strp(tc.transcribed), Duration: intp(36000)}
			cand := metafetch.MetadataCandidate{Title: tc.candTitle, Series: tc.candSeries, Publisher: tc.candPublisher, Author: author,
				Score: 0.95, DurationSec: 36000, Source: "Audible"}
			ts := TranscribedSearch{Query: tc.transcribed, Source: "transcribed_title", ExplainsStaleIdentity: true}
			v := EvaluateTranscribed(book, Authors{author}, database.ComputeBookRuntime(book, nil), &cand,
				errors.Join(metafetch.ErrStaleMetadataCache), nil, ts, tc.guard)
			if v.Allowed != tc.wantAllowed {
				t.Fatalf("allowed = %v (reason %q: %s), want %v", v.Allowed, v.Reason, v.Detail, tc.wantAllowed)
			}
			if tc.wantReason == "" {
				return
			}
			if tc.wantDetail == "" {
				t.Fatal("a refusing case must set wantDetail: without it the case passes whichever field held it")
			}
			if v.Reason != tc.wantReason {
				t.Errorf("reason = %q (%s), want %q", v.Reason, v.Detail, tc.wantReason)
			}
			if !strings.Contains(v.Detail, tc.wantDetail) {
				t.Errorf("detail %q does not name %q: the case was held by some other field", v.Detail, tc.wantDetail)
			}
			if v.OwnerReviewOverridable() {
				t.Error("an owner-manual-only refusal must not be overridable by a bulk owner pin")
			}
			if cand.IdentityEvidence != nil || v.IdentityEvidence != nil {
				t.Errorf("evidence stamped on a manual-only refusal: %+v", cand.IdentityEvidence)
			}
		})
	}
}
