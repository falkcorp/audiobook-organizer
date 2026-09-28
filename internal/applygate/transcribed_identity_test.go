// file: internal/applygate/transcribed_identity_test.go
// version: 1.0.0
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
			wantReason: ReasonIdentityStale, wantEvid: true, wantAgree: -1},
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
			v := EvaluateTranscribed(book, f.authors, database.ComputeBookRuntime(book, nil), &cand, idErr, nil, tc.ts)

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

func TestTranscribedTitleMatches(t *testing.T) {
	cases := []struct {
		query, title, pos string
		want              bool
	}{
		{"Marvel's Planet Hulk", "Marvel's Planet Hulk", "", true},
		{"marvel's planet hulk", "Marvel's Planet Hulk", "", true},
		{"Planet Hulk", "Planet Hulk: Gladiator", "", false},
		{"Planet Hulk", "World War Hulk", "", false},
		{"", "Planet Hulk", "", false},
		{"Planet Hulk", "", "", false},
	}
	for _, tc := range cases {
		c := &metafetch.MetadataCandidate{Title: tc.title, SeriesPosition: tc.pos}
		if got := TranscribedTitleMatches(tc.query, c); got != tc.want {
			t.Errorf("TranscribedTitleMatches(%q, %q) = %v, want %v", tc.query, tc.title, got, tc.want)
		}
	}
}
