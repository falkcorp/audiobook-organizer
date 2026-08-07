// file: internal/plugins/maintenance/intro_migrate_single_file_test.go
// version: 1.0.0
// guid: 91e4c07b-5a63-4d28-8f10-2b74e9d6a35c
// last-edited: 2026-08-07

package maintenance

import (
	"reflect"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
		IntroTranscription:    strp("Dune by Frank Herbert read by Scott Brick"),
		TranscribedTitle:      strp("Dune"),
		TranscribedAuthor:     strp("Frank Herbert"),
		TranscribedNarrator:   strp("Scott Brick"),
		IntroTranscribedAt:    timep(time.Unix(1700000000, 0)),
		TranscribeStatus:      strp("ok"),
		TranscribeError:       nil,
		TranscribeAttemptedAt: timep(time.Unix(1700000001, 0)),
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
	book := database.Book{ID: "b1", IntroTranscription: strp("Dune by Frank Herbert")}
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
				IntroTranscription: strp("already here"),
			}},
			wantReason: migrateAlreadyPresent,
		},
		{
			name: "overwrite_forces_recopy",
			files: []database.BookFile{{
				ID: "f1", BookID: "b1", FilePath: "/lib/a.mp3",
				IntroTranscription: strp("already here"),
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

// TestMigratedFieldsMatchBookFileSchema fails when someone adds a per-file
// transcription field to BookFile without deciding whether tier 0 should carry
// it. Without this, a new field silently stays empty on 33,780 migrated rows.
func TestMigratedFieldsMatchBookFileSchema(t *testing.T) {
	bf := reflect.TypeOf(database.BookFile{})
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
	for i := 0; i < bf.NumField(); i++ {
		n := bf.Field(i).Name
		looksTranscription := len(n) >= 5 &&
			(n[:5] == "Intro" || (len(n) >= 10 && n[:10] == "Transcribe") || (len(n) >= 11 && n[:11] == "Transcribed"))
		if looksTranscription && !listed[n] {
			t.Errorf("BookFile.%s looks like a transcription field but is not in "+
				"introMigratedFields — decide explicitly whether tier 0 carries it", n)
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
		case reflect.Ptr:
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

func strp(s string) *string        { return &s }
func timep(t time.Time) *time.Time { return &t }
