// file: internal/plugins/maintenance/intro_migrate_single_file_test.go
// version: 1.1.0
// guid: 91e4c07b-5a63-4d28-8f10-2b74e9d6a35c
// last-edited: 2026-11-17

package maintenance

import (
	"log/slog"
	"reflect"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// TestApplyBookIntroFieldsTouchesNothingElse is THE guard on this migration.
//
// The op writes the whole BookFile row back via UpdateBookFile (full-column
// replacement), so the only thing standing between "copy 8 fields" and "clobber
// an unrelated column" is that applyBookIntroFieldsToFile mutates exactly the 8
// and no more. This repo's dominant data-loss shape is precisely a full-row
// write-back that carried a stale or zero value into a field nobody meant to
// touch (the AcoustIDFingerprint wipe, the Author/Series wipe).
//
// Rather than eyeballing the assignments, this fills EVERY field of a BookFile
// with a distinguishable non-zero value, applies the migration, and diffs every
// field reflectively. A new field added to BookFile is covered automatically.
func TestApplyBookIntroFieldsTouchesNothingElse(t *testing.T) {
	var dst database.BookFile
	fillAllFields(t, reflect.ValueOf(&dst).Elem(), 1)
	before := dst // struct copy: the pre-state of every field

	src := database.Book{
		IntroTranscription:    new("Dune by Frank Herbert read by Scott Brick"),
		TranscribedTitle:      new("Dune"),
		TranscribedAuthor:     new("Frank Herbert"),
		TranscribedNarrator:   new("Scott Brick"),
		IntroTranscribedAt:    new(time.Unix(1700000000, 0)),
		TranscribeStatus:      new("ok"),
		TranscribeError:       nil,
		TranscribeAttemptedAt: new(time.Unix(1700000001, 0)),
	}
	applyBookIntroFieldsToFile(&dst, src)

	allowed := map[string]bool{}
	for _, f := range introMigratedFields {
		allowed[f] = true
	}

	bt := reflect.TypeOf(before)
	bv, av := reflect.ValueOf(before), reflect.ValueOf(dst)
	var changed []string
	for i := 0; i < bt.NumField(); i++ {
		name := bt.Field(i).Name
		if !bt.Field(i).IsExported() {
			continue
		}
		if !reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface()) {
			changed = append(changed, name)
			if !allowed[name] {
				t.Errorf("field %q was modified but is NOT in introMigratedFields — "+
					"a full-row write-back would persist this unintended change", name)
			}
		}
	}

	// The converse: every field we claim to migrate must actually have changed,
	// or the list is lying and a future reader will trust it.
	changedSet := map[string]bool{}
	for _, c := range changed {
		changedSet[c] = true
	}
	for _, want := range introMigratedFields {
		if !changedSet[want] {
			t.Errorf("field %q is listed in introMigratedFields but was NOT modified", want)
		}
	}
}

// TestClassifyMigrateCandidate pins the eligibility rules — the correctness
// story of tier 0.
func TestClassifyMigrateCandidate(t *testing.T) {
	book := database.Book{ID: "b1", IntroTranscription: new("Dune by Frank Herbert")}
	mp3 := func(id, path string) database.BookFile {
		return database.BookFile{ID: id, BookID: "b1", FilePath: path}
	}

	cases := []struct {
		name       string
		files      []database.BookFile
		overwrite  bool
		wantReason string
		wantTarget string // file ID, "" when ineligible
	}{
		{
			name:       "single_audio_file_is_eligible",
			files:      []database.BookFile{mp3("f1", "/lib/a.mp3")},
			wantReason: "", wantTarget: "f1",
		},
		{
			// 🔴 The provenance constraint. retry2 may have transcribed file 2 and
			// nothing records which file won, so a copy would fabricate evidence.
			name:       "multi_file_is_REFUSED_on_provenance",
			files:      []database.BookFile{mp3("f1", "/lib/a.mp3"), mp3("f2", "/lib/b.mp3")},
			wantReason: migrateMultiFile,
		},
		{
			name:       "no_audio_extension_is_not_audio",
			files:      []database.BookFile{mp3("f1", "/lib/cover.jpg")},
			wantReason: migrateNoAudio,
		},
		{
			// Non-audio siblings must not push a genuinely single-file book into
			// the multi-file refusal — cover art is not a second recording.
			name:       "single_audio_plus_cover_art_still_eligible",
			files:      []database.BookFile{mp3("f1", "/lib/a.mp3"), mp3("f2", "/lib/cover.jpg")},
			wantReason: "", wantTarget: "f1",
		},
		{
			name: "already_migrated_is_skipped",
			files: []database.BookFile{{
				ID: "f1", BookID: "b1", FilePath: "/lib/a.mp3",
				IntroTranscription: new("already here"),
			}},
			wantReason: migrateAlreadyPresent,
		},
		{
			name: "overwrite_forces_recopy",
			files: []database.BookFile{{
				ID: "f1", BookID: "b1", FilePath: "/lib/a.mp3",
				IntroTranscription: new("already here"),
			}},
			overwrite:  true,
			wantReason: "", wantTarget: "f1",
		},
		{
			name:       "no_files_at_all",
			files:      nil,
			wantReason: migrateNoAudio,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, target := classifyMigrateCandidate(book, tc.files, tc.overwrite)
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
			switch {
			case tc.wantTarget == "" && target != nil:
				t.Errorf("expected no target, got %q", target.ID)
			case tc.wantTarget != "" && target == nil:
				t.Errorf("expected target %q, got nil", tc.wantTarget)
			case tc.wantTarget != "" && target.ID != tc.wantTarget:
				t.Errorf("target = %q, want %q", target.ID, tc.wantTarget)
			}
		})
	}
}

// TestMigrateOneBookReportsRowShapeBeforeTranscript pins the check ORDER in
// migrateOneBook. Measured 2026-08-07: the ~1,122 zero-book_file-row books have
// neither file rows nor a book-level transcript. With the transcript checked
// first they all landed in skip_book_has_no_transcript, reporting
// skip_no_book_file_rows as 0 and hiding the unlinked population. Row shape is
// the more actionable reason (relink, then transcribe), so it must win when
// both apply.
func TestMigrateOneBookReportsRowShapeBeforeTranscript(t *testing.T) {
	log := slog.Default()

	bookNoTranscript := func(id string) (*database.Book, error) {
		return &database.Book{ID: id}, nil // no IntroTranscription either
	}

	t.Run("zero_rows_and_no_transcript_reports_no_rows", func(t *testing.T) {
		store := &database.MockStore{
			GetBookByIDFunc:  bookNoTranscript,
			GetBookFilesFunc: func(string) ([]database.BookFile, error) { return nil, nil },
		}
		if got := t6g4MigrateOneBook(store, store, log, "b-zero", true, false); got != migrateNoRows {
			t.Errorf("reason = %q, want %q — zero-row books must be reported by row shape "+
				"even when the transcript is also missing", got, migrateNoRows)
		}
	})

	t.Run("rows_present_but_no_transcript_reports_no_transcript", func(t *testing.T) {
		store := &database.MockStore{
			GetBookByIDFunc: bookNoTranscript,
			GetBookFilesFunc: func(id string) ([]database.BookFile, error) {
				return []database.BookFile{{ID: "f1", BookID: id, FilePath: "/lib/a.mp3"}}, nil
			},
		}
		if got := t6g4MigrateOneBook(store, store, log, "b-linked", true, false); got != migrateNoTranscript {
			t.Errorf("reason = %q, want %q", got, migrateNoTranscript)
		}
	})
}

// TestMigratedFieldsMatchBookFileSchema fails when someone adds a per-file
// transcription field to BookFile without deciding whether tier 0 should carry
// it (introMigratedFields) or not (introNotMigratedFields, with the reason).
// Without this, a new field silently stays empty on 33,780 migrated rows.
func TestMigratedFieldsMatchBookFileSchema(t *testing.T) {
	bf := reflect.TypeFor[database.BookFile]()
	for _, name := range introMigratedFields {
		if _, ok := bf.FieldByName(name); !ok {
			t.Errorf("introMigratedFields names %q, which BookFile does not have", name)
		}
	}
	// Every BookFile field whose name looks like part of the transcription group
	// must be accounted for.
	listed := map[string]bool{}
	for _, n := range introMigratedFields {
		listed[n] = true
	}
	for _, n := range introNotMigratedFields {
		if _, ok := bf.FieldByName(n); !ok {
			t.Errorf("introNotMigratedFields names %q, which BookFile does not have", n)
		}
		if listed[n] {
			t.Errorf("%q is in both introMigratedFields and introNotMigratedFields", n)
		}
		listed[n] = true
	}
	for field := range bf.Fields() {
		n := field.Name
		looksTranscription := len(n) >= 5 &&
			(n[:5] == "Intro" || (len(n) >= 10 && n[:10] == "Transcribe") || (len(n) >= 11 && n[:11] == "Transcribed"))
		if looksTranscription && !listed[n] {
			t.Errorf("BookFile.%s looks like a transcription field but is not in "+
				"introMigratedFields or introNotMigratedFields — decide explicitly whether tier 0 carries it", n)
		}
	}
}

// fillAllFields sets every settable exported field to a distinguishable
// non-zero value so the diff above can detect an unintended write to any of them.
func fillAllFields(t *testing.T, v reflect.Value, seed int) {
	t.Helper()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Kind() {
		case reflect.String:
			f.SetString("v" + string(rune('a'+i%26)))
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			f.SetInt(int64(i + seed))
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			f.SetUint(uint64(i + seed))
		case reflect.Float32, reflect.Float64:
			f.SetFloat(float64(i) + 0.5)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		case reflect.Map:
			f.Set(reflect.MakeMap(f.Type()))
		case reflect.Pointer:
			p := reflect.New(f.Type().Elem())
			switch p.Elem().Kind() {
			case reflect.String:
				p.Elem().SetString("p" + string(rune('a'+i%26)))
			case reflect.Int, reflect.Int64:
				p.Elem().SetInt(int64(i + seed))
			case reflect.Bool:
				p.Elem().SetBool(true)
			case reflect.Struct:
				if tv, ok := p.Interface().(*time.Time); ok {
					*tv = time.Unix(int64(1600000000+i), 0)
				}
			}
			f.Set(p)
		case reflect.Struct:
			if tv, ok := f.Addr().Interface().(*time.Time); ok {
				*tv = time.Unix(int64(1500000000+i), 0)
			}
		}
	}
}

// R3-m4: the migration replaces the file's transcript, so the classification it
// carries must describe the NEW text. Before, IntroParse stayed what it was, and with
// overwrite=true a file that held a chapter marker and received a title announcement
// kept the chapter-marker parse: a stored parse describing text the row no longer
// holds. The parse is derived from the copied text by the same derivation the re-parse
// op uses.
func TestApplyBookIntroFieldsDerivesTheParseFromTheCopiedText(t *testing.T) {
	text := "The Killing Joke, written by Alfred Pennyworth, read by Selina Kyle"
	stale := &database.IntroParse{Version: transcribe.ClipParserVersion, Kind: "chapter_marker", ChapterNumber: 12, HasChapterNumber: true}
	dst := database.BookFile{ID: "f1", IntroTranscription: new("Chapter 12. The Long Halloween."), IntroParse: stale}

	applyBookIntroFieldsToFile(&dst, database.Book{IntroTranscription: &text})

	want := introParseFromClip(transcribe.ParseClip(text))
	if dst.IntroParse == nil || *dst.IntroParse != want {
		t.Fatalf("IntroParse = %+v, want the parse of the copied text %+v", dst.IntroParse, want)
	}
	if dst.IntroParse.Kind != "title_announcement" {
		t.Errorf("kind = %q, want title_announcement", dst.IntroParse.Kind)
	}
	if *stale != (database.IntroParse{Version: transcribe.ClipParserVersion, Kind: "chapter_marker", ChapterNumber: 12, HasChapterNumber: true}) {
		t.Errorf("the previous parse was mutated in place: %+v", stale)
	}

	// No text, no parse: nothing to describe.
	applyBookIntroFieldsToFile(&dst, database.Book{})
	if dst.IntroParse != nil {
		t.Errorf("IntroParse = %+v with no transcript copied, want nil", dst.IntroParse)
	}
}

// R3-m4 through the real store and the merge classes: with overwrite=true the file's
// stored text and parse are both replaced, and the stored parse is the new text's.
func TestMigrateOneBookOverwriteReplacesTheParseWithTheTranscript(t *testing.T) {
	s := newRepairPebble(t)
	text := "The Killing Joke, written by Alfred Pennyworth, read by Selina Kyle"
	book, err := s.CreateBook(&database.Book{Title: "Batman: The Killing Joke", FilePath: "/lib/Gotham/Killing-Joke", IntroTranscription: &text})
	if err != nil {
		t.Fatal(err)
	}
	f := &database.BookFile{BookID: book.ID, FilePath: "/lib/Gotham/Killing-Joke/01.m4b", FileHash: "gotham-hash-01", Duration: database.Secs(600), FileSize: 4096}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatal(err)
	}
	own := "Chapter 12. The Long Halloween."
	if _, err := s.SetBookFileIntro(book.ID, f.ID, database.BookFileIntroResult{
		SourceHash: f.FileHash, RunOpID: "op-batman", Status: "unparsed", Done: true, Transcript: &own,
		Parse: &database.IntroParse{Version: transcribe.ClipParserVersion, Kind: "chapter_marker", ChapterNumber: 12, HasChapterNumber: true},
	}); err != nil {
		t.Fatal(err)
	}

	if got := t6g4MigrateOneBook(s, s, slog.Default(), book.ID, false, true); got != migrateWrote {
		t.Fatalf("migrate = %q, want %q", got, migrateWrote)
	}
	row, err := s.GetBookFileByID(book.ID, f.ID)
	if err != nil || row == nil {
		t.Fatalf("read: %v %v", row, err)
	}
	if row.IntroTranscription == nil || *row.IntroTranscription != text {
		t.Fatalf("transcript = %v, want the book's", row.IntroTranscription)
	}
	want := introParseFromClip(transcribe.ParseClip(text))
	if row.IntroParse == nil || *row.IntroParse != want {
		t.Errorf("stored parse = %+v, want the parse of the stored text %+v", row.IntroParse, want)
	}
}
