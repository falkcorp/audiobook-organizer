// file: internal/transcribe/reconcile_test.go
// version: 1.0.1
// guid: d6a38f04-5b17-4e92-8c0d-93e1f7b25a68
// last-edited: 2026-11-17

package transcribe

import "testing"

func titleParse2(title, author, narr string) ClipParse {
	return ClipParse{Kind: ClipKindTitle, Title: title, Author: author, Narrator: narr}
}

// TestReconcileRule is the table for the no-downgrade rule; each row names the
// numbered rule in reconcile.go it pins.
func TestReconcileRule(t *testing.T) {
	cases := []struct {
		name         string
		stored       StoredIntro
		parse        ClipParse
		want         Identity
		wantStatus   string
		idWrite      bool
		statusWrite  bool
		filled, fix  int
		kindConflict bool
	}{
		{
			name:   "rule1 failure status untouched",
			stored: StoredIntro{Status: "whisper_error", Identity: Identity{Title: "Hush"}},
			parse:  titleParse2("The Long Halloween", "Alfred Pennyworth", ""),
			want:   Identity{Title: "Hush"}, wantStatus: "whisper_error",
		},
		{
			name:   "rule1 empty status untouched",
			stored: StoredIntro{Status: "empty"},
			parse:  titleParse2("The Long Halloween", "Alfred Pennyworth", ""),
			want:   Identity{}, wantStatus: "empty",
		},
		{
			name:   "rule1 unset status untouched",
			stored: StoredIntro{},
			parse:  titleParse2("Hush", "Alfred Pennyworth", ""),
			want:   Identity{}, wantStatus: "",
		},
		{
			name:   "rule2 kind conflict keeps identity",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Hush", Author: "Alfred Pennyworth"}},
			parse:  ClipParse{Kind: ClipKindChapter, ChapterNumber: 5, HasChapterNumber: true},
			want:   Identity{Title: "Hush", Author: "Alfred Pennyworth"}, wantStatus: "ok",
			kindConflict: true,
		},
		{
			name:   "rule2 non-title with no stored identity is not a conflict",
			stored: StoredIntro{Status: "unparsed"},
			parse:  ClipParse{Kind: ClipKindNarration},
			want:   Identity{}, wantStatus: "unparsed",
		},
		{
			name:   "rule2 narration never flips unparsed",
			stored: StoredIntro{Status: "unparsed"},
			parse:  ClipParse{Kind: ClipKindChapter, ChapterNumber: 2, HasChapterNumber: true},
			want:   Identity{}, wantStatus: "unparsed",
		},
		{
			name:       "rule3 fill empty fields and rule4 unparsed to ok",
			stored:     StoredIntro{Status: "unparsed"},
			parse:      titleParse2("The Long Halloween", "Alfred Pennyworth", "Selina Kyle"),
			want:       Identity{Title: "The Long Halloween", Author: "Alfred Pennyworth", Narrator: "Selina Kyle"},
			wantStatus: "ok", idWrite: true, statusWrite: true, filled: 3,
		},
		{
			name:   "rule3 empty new value never empties a stored one",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Hush", Author: "Alfred Pennyworth", Narrator: "Selina Kyle"}},
			parse:  titleParse2("Hush", "", ""),
			want:   Identity{Title: "Hush", Author: "Alfred Pennyworth", Narrator: "Selina Kyle"}, wantStatus: "ok",
		},
		{
			name:   "rule3 fills only the empty fields",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Hush", Author: "Alfred Pennyworth"}},
			parse:  titleParse2("Hush", "Alfred Pennyworth", "Selina Kyle"),
			want:   Identity{Title: "Hush", Author: "Alfred Pennyworth", Narrator: "Selina Kyle"}, wantStatus: "ok",
			idWrite: true, filled: 1,
		},
		{
			name:   "rule3 two clean differing values keep the stored one",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Hush"}},
			parse:  titleParse2("The Long Halloween", "", ""),
			want:   Identity{Title: "Hush"}, wantStatus: "ok",
		},
		{
			name:   "rule3 repair publisher lead in title",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Gotham Audio presents, Year One"}},
			parse:  titleParse2("Year One", "", ""),
			want:   Identity{Title: "Year One"}, wantStatus: "ok",
			idWrite: true, fix: 1,
		},
		{
			name:   "rule3 repair dangling verb in title",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Knightfall Book 3 Written"}},
			parse:  titleParse2("Knightfall Book 3", "", ""),
			want:   Identity{Title: "Knightfall Book 3"}, wantStatus: "ok",
			idWrite: true, fix: 1,
		},
		{
			name:   "rule3 repair translator welded into author",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Hush", Author: "Alfred Pennyworth Translated by Lucius Fox"}},
			parse:  ClipParse{Kind: ClipKindTitle, Title: "Hush", Author: "Alfred Pennyworth", Translator: "Lucius Fox"},
			want:   Identity{Title: "Hush", Author: "Alfred Pennyworth", Translator: "Lucius Fox"}, wantStatus: "ok",
			idWrite: true, filled: 1, fix: 1,
		},
		{
			name:   "rule3 defective new value never displaces a defective stored one",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Gotham Audio presents, Year One"}},
			parse:  titleParse2("Arkham Audio presents, Hush", "", ""),
			want:   Identity{Title: "Gotham Audio presents, Year One"}, wantStatus: "ok",
		},
		{
			name:   "rule3 defective new value never displaces a clean stored one",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Year One"}},
			parse:  titleParse2("Gotham Audio presents, Year One", "", ""),
			want:   Identity{Title: "Year One"}, wantStatus: "ok",
		},
		{
			name:   "rule3 a six word name is at the cap and clean",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Hush", Author: "Alfred Pennyworth Lucius Fox Barbara Gordon"}},
			parse:  titleParse2("Hush", "Selina Kyle", ""),
			want:   Identity{Title: "Hush", Author: "Alfred Pennyworth Lucius Fox Barbara Gordon"}, wantStatus: "ok",
		},
		{
			name:   "rule3 a seven word name is over the cap and defective",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Hush", Author: "Alfred Pennyworth Lucius Fox Barbara Gordon Kyle"}},
			parse:  titleParse2("Hush", "Selina Kyle", ""),
			want:   Identity{Title: "Hush", Author: "Selina Kyle"}, wantStatus: "ok",
			idWrite: true, fix: 1,
		},
		{
			name:   "rule3 over-long name is defective",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Hush", Author: "Alfred Pennyworth and Lucius Fox and Barbara Gordon and Selina Kyle"}},
			parse:  titleParse2("Hush", "Alfred Pennyworth", ""),
			want:   Identity{Title: "Hush", Author: "Alfred Pennyworth"}, wantStatus: "ok",
			idWrite: true, fix: 1,
		},
		{
			name:   "rule4 ok never changes",
			stored: StoredIntro{Status: "ok", Identity: Identity{Title: "Hush"}},
			parse:  titleParse2("Hush", "", ""),
			want:   Identity{Title: "Hush"}, wantStatus: "ok",
		},
		{
			name:   "rule4 unparsed stays when the title stays empty",
			stored: StoredIntro{Status: "unparsed"},
			parse:  ClipParse{Kind: ClipKindTitle, Author: "Alfred Pennyworth"},
			want:   Identity{Author: "Alfred Pennyworth"}, wantStatus: "unparsed",
			idWrite: true, filled: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Reconcile(tc.stored, tc.parse)
			if d.Identity != tc.want || d.Status != tc.wantStatus {
				t.Fatalf("identity %+v status %q; want %+v %q", d.Identity, d.Status, tc.want, tc.wantStatus)
			}
			if d.IdentityWrite != tc.idWrite || d.StatusWrite != tc.statusWrite {
				t.Fatalf("writes identity=%v status=%v; want %v %v", d.IdentityWrite, d.StatusWrite, tc.idWrite, tc.statusWrite)
			}
			if d.Filled != tc.filled || d.Repaired != tc.fix {
				t.Fatalf("filled %d repaired %d; want %d %d", d.Filled, d.Repaired, tc.filled, tc.fix)
			}
			if d.KindConflict != tc.kindConflict {
				t.Fatalf("kind conflict = %v, want %v", d.KindConflict, tc.kindConflict)
			}
			// Idempotent: applying the decision and reconciling again changes nothing.
			again := Reconcile(StoredIntro{Status: d.Status, Identity: d.Identity}, tc.parse)
			if again.IdentityWrite || again.StatusWrite {
				t.Fatalf("not idempotent: second pass %+v", again)
			}
		})
	}
}

// TestReconcileNeverEmptiesAField is the property form of the rule: whatever
// the parse says, no non-empty stored field ends up empty.
func TestReconcileNeverEmptiesAField(t *testing.T) {
	stored := Identity{Title: "Hush", Author: "Alfred Pennyworth", Narrator: "Selina Kyle", Translator: "Lucius Fox", CoverArtist: "Barbara Gordon"}
	parses := []ClipParse{
		{Kind: ClipKindTitle},
		titleParse2("", "", ""),
		titleParse2("Year One", "", ""),
		{Kind: ClipKindChapter}, {Kind: ClipKindNarration}, {Kind: ClipKindGarbage}, {Kind: ClipKindSilence},
		{Kind: ClipKindNone},
	}
	for _, status := range []string{"ok", "unparsed", "empty", "whisper_error", ""} {
		for _, p := range parses {
			d := Reconcile(StoredIntro{Status: status, Identity: stored}, p)
			if d.Identity.Title == "" || d.Identity.Author == "" || d.Identity.Narrator == "" ||
				d.Identity.Translator == "" || d.Identity.CoverArtist == "" {
				t.Fatalf("status %q parse %+v emptied a field: %+v", status, p, d.Identity)
			}
		}
	}
}
