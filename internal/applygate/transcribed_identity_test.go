// file: internal/applygate/transcribed_identity_test.go
// version: 1.1.0
// guid: 329bd78d-e4ca-434d-aa8d-65f766d386a1
// last-edited: 2026-09-28

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
		name        string
		path        string
		transcribed string
		candTitle   string
		candSeries  string
		guard       ManualOnlyGuard
		wantAllowed bool
		wantReason  string
	}{
		{name: "bulk: transcription and candidate name Doctor Who", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: chimes, candTitle: chimes, guard: ManualOnlyGuard{Bulk: true}, wantReason: ReasonOwnerManualOnly},
		{name: "bulk: only the candidate's series names Big Finish", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: "The Chimes of Midnight", candTitle: "The Chimes of Midnight", candSeries: "Big Finish Main Range",
			guard: ManualOnlyGuard{Bulk: true}, wantReason: ReasonOwnerManualOnly},
		{name: "bulk: only the path names Torchwood", path: "/library/Torchwood/Unknown Title/book.m4b",
			transcribed: "Border Princes", candTitle: "Border Princes", guard: ManualOnlyGuard{Bulk: true}, wantReason: ReasonOwnerManualOnly},
		{name: "bulk: the store found a Doctor Who file", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: "Spare Parts", candTitle: "Spare Parts",
			guard: ManualOnlyGuard{Bulk: true, StoreDetail: "file \"/library/Doctor Who/Spare Parts/01.mp3\""}, wantReason: ReasonOwnerManualOnly},
		{name: "single-book caller: allowed", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: chimes, candTitle: chimes, wantAllowed: true},
		{name: "bulk: an unrelated book is not caught", path: "/library/Unknown Author/Unknown Title/book.m4b",
			transcribed: "Marvel's Planet Hulk", candTitle: "Marvel's Planet Hulk", guard: ManualOnlyGuard{Bulk: true}, wantAllowed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := &database.Book{ID: "b1", Title: "", FilePath: tc.path, TranscribedTitle: strp(tc.transcribed), Duration: intp(36000)}
			cand := metafetch.MetadataCandidate{Title: tc.candTitle, Series: tc.candSeries, Author: "Big Finish Productions",
				Score: 0.95, DurationSec: 36000, Source: "Audible"}
			ts := TranscribedSearch{Query: tc.transcribed, Source: "transcribed_title", ExplainsStaleIdentity: true}
			v := EvaluateTranscribed(book, Authors{"Big Finish Productions"}, database.ComputeBookRuntime(book, nil), &cand,
				errors.Join(metafetch.ErrStaleMetadataCache), nil, ts, tc.guard)
			if v.Allowed != tc.wantAllowed {
				t.Fatalf("allowed = %v (reason %q: %s), want %v", v.Allowed, v.Reason, v.Detail, tc.wantAllowed)
			}
			if tc.wantReason == "" {
				return
			}
			if v.Reason != tc.wantReason {
				t.Errorf("reason = %q (%s), want %q", v.Reason, v.Detail, tc.wantReason)
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
