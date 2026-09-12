// file: internal/organizer/inplace_collision_test.go
// version: 1.0.1
// guid: 99027475-b084-4603-adf4-4061987f30b0
// last-edited: 2026-09-12

package organizer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// These run against a real PebbleStore: the behaviour under test is how the
// in-place move reads the occupant's row, its version group and its
// fingerprint, and a mock would only prove what the mock was told to return.

func setupInPlace(t *testing.T) (*Service, *database.PebbleStore, string) {
	t.Helper()
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	root := t.TempDir()
	config.AppConfig = config.Config{
		RootDir:             root,
		FolderNamingPattern: "{author}/{title}",
		FileNamingPattern:   "{title}",
	}
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewService(store), store, root
}

// addInPlaceBook writes content at path (when non-nil) and creates the book and
// its one book_file row. The returned book carries an in-memory Author so the
// target path resolves without an author row.
func addInPlaceBook(t *testing.T, store *database.PebbleStore, id, title, path string, content, fp []byte, fpDur float64) *database.Book {
	t.Helper()
	if content != nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := store.CreateBook(&database.Book{ID: id, Title: title, FilePath: path})
	if err != nil {
		t.Fatalf("create book %s: %v", id, err)
	}
	if err := store.CreateBookFile(&database.BookFile{
		ID: id + "-f", BookID: b.ID, FilePath: path, FileSize: int64(len(content)),
		AcoustIDFingerprint: fp, AcoustIDFingerprintDurationSec: fpDur,
	}); err != nil {
		t.Fatalf("create book file %s: %v", id, err)
	}
	b.Author = &database.Author{Name: "Some Author"}
	return b
}

func targetFor(t *testing.T, svc *Service, b *database.Book) string {
	t.Helper()
	target, err := svc.newOrganizer().GenerateTargetPath(b)
	if err != nil {
		t.Fatalf("target for %s: %v", b.ID, err)
	}
	return target
}

func filled(n int, v byte) []byte { return bytes.Repeat([]byte{v}, n) }

// fpStream builds a raw little-endian uint32 fingerprint of frames frames.
func fpStream(frames int, seed uint32) []byte {
	out := make([]byte, 4*frames)
	x := seed
	for i := range frames {
		x = x*1664525 + 1013904223
		binary.LittleEndian.PutUint32(out[4*i:], x)
	}
	return out
}

func invert(b []byte) []byte {
	out := make([]byte, len(b))
	for i := range b {
		out[i] = ^b[i]
	}
	return out
}

func mustContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s content changed", path)
	}
}

func getInPlaceBook(t *testing.T, store *database.PebbleStore, id string) *database.Book {
	t.Helper()
	b, err := store.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("get book %s: %v", id, err)
	}
	return b
}

// An in-place move onto a byte-identical occupant adopts: the book joins the
// occupant's version group as a non-primary version, the ledger records it,
// and neither file moves or disappears.
func TestInPlace_IdenticalOccupant_Adopts(t *testing.T) {
	svc, store, root := setupInPlace(t)
	audio := filled(4096, 0xA1)
	src := filepath.Join(root, "incoming", "x.mp3")
	b := addInPlaceBook(t, store, "book-b", "Title", src, audio, nil, 0)
	target := targetFor(t, svc, b)
	occ := addInPlaceBook(t, store, "book-o", "Title", target, audio, nil, 0)

	landing, err := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	if err != nil {
		t.Fatalf("OrganizeOneBook: %v", err)
	}
	if landing.Resolution == nil || landing.Resolution.Outcome != OutcomeAdopted {
		t.Fatalf("want outcome %q, got %+v", OutcomeAdopted, landing.Resolution)
	}
	if landing.Path != src {
		t.Fatalf("adopt must not move the source: landing %s, source %s", landing.Path, src)
	}
	outcome, _, err := svc.CommitLanding(b, landing, "op-adopt", &noopLogger{})
	if err != nil || outcome != LandingAdopted {
		t.Fatalf("CommitLanding = %v, %v; want LandingAdopted", outcome, err)
	}

	mustContent(t, src, audio)
	mustContent(t, target, audio)
	gotB, gotO := getInPlaceBook(t, store, b.ID), getInPlaceBook(t, store, occ.ID)
	if gotB.VersionGroupID == nil || gotO.VersionGroupID == nil || *gotB.VersionGroupID != *gotO.VersionGroupID {
		t.Fatalf("book and occupant must share a version group: %v vs %v", gotB.VersionGroupID, gotO.VersionGroupID)
	}
	if gotB.IsPrimaryVersion == nil || *gotB.IsPrimaryVersion {
		t.Fatalf("adopted book must be non-primary")
	}
	if gotO.IsPrimaryVersion == nil || !*gotO.IsPrimaryVersion {
		t.Fatalf("occupant must be the group's primary")
	}
	if gotB.FilePath != src {
		t.Fatalf("adopted book row must keep its path, got %s", gotB.FilePath)
	}

	changes, err := store.GetOperationChanges("op-adopt")
	if err != nil {
		t.Fatal(err)
	}
	var sawGroup bool
	for _, c := range changes {
		if c.BookID == b.ID && c.ChangeType == "metadata_update" && c.FieldName == "version_group_id" && c.NewValue == *gotB.VersionGroupID {
			sawGroup = true
		}
	}
	if !sawGroup {
		t.Fatalf("no version_group_id change row for the adopted book in %d rows", len(changes))
	}
}

// A genuinely different occupant keeps its path and the book moves to the
// shared _copyN name.
func TestInPlace_DifferentOccupant_Suffixes(t *testing.T) {
	svc, store, root := setupInPlace(t)
	src := filepath.Join(root, "incoming", "x.mp3")
	srcAudio, occAudio := filled(2000, 0x01), filled(9000, 0x02)
	b := addInPlaceBook(t, store, "book-b", "Title", src, srcAudio, nil, 0)
	target := targetFor(t, svc, b)
	addInPlaceBook(t, store, "book-o", "Title", target, occAudio, nil, 0)

	landing, err := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	if err != nil {
		t.Fatalf("OrganizeOneBook: %v", err)
	}
	want := filepath.Join(filepath.Dir(target), "Title_copy1.mp3")
	if landing.Path != want || landing.Resolution == nil || landing.Resolution.Outcome != OutcomeSuffixed {
		t.Fatalf("want suffixed to %s, got path %s resolution %+v", want, landing.Path, landing.Resolution)
	}
	mustContent(t, want, srcAudio)
	mustContent(t, target, occAudio)
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source should have moved to the suffixed path")
	}
}

// Same size, different bytes, matching fingerprints and durations: the same
// recording with rewritten tags. Adopted like the byte-identical case.
func TestInPlace_SameRecordingConfirmed_Adopts(t *testing.T) {
	svc, store, root := setupInPlace(t)
	fp := fpStream(400, 7)
	src := filepath.Join(root, "incoming", "x.mp3")
	b := addInPlaceBook(t, store, "book-b", "Title", src, filled(5000, 0x10), fp, 3600)
	target := targetFor(t, svc, b)
	addInPlaceBook(t, store, "book-o", "Title", target, filled(5000, 0x20), fp, 3630)

	landing, err := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	if err != nil {
		t.Fatalf("OrganizeOneBook: %v", err)
	}
	if landing.Resolution == nil || landing.Resolution.Outcome != OutcomeAdoptedSameRecording || landing.Path != src {
		t.Fatalf("want %q in place, got path %s resolution %+v", OutcomeAdoptedSameRecording, landing.Path, landing.Resolution)
	}
	if got := getInPlaceBook(t, store, b.ID); got.IsPrimaryVersion == nil || *got.IsPrimaryVersion {
		t.Fatalf("adopted same-recording book must be non-primary")
	}
}

// Same size, different bytes, fingerprints that do NOT match: a different
// recording, so it is suffixed rather than linked.
func TestInPlace_SameSizeFingerprintMismatch_Suffixes(t *testing.T) {
	svc, store, root := setupInPlace(t)
	fp := fpStream(400, 7)
	src := filepath.Join(root, "incoming", "x.mp3")
	b := addInPlaceBook(t, store, "book-b", "Title", src, filled(5000, 0x10), fp, 3600)
	target := targetFor(t, svc, b)
	addInPlaceBook(t, store, "book-o", "Title", target, filled(5000, 0x20), invert(fp), 3600)

	landing, err := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
	if err != nil {
		t.Fatalf("OrganizeOneBook: %v", err)
	}
	if landing.Resolution == nil || landing.Resolution.Outcome != OutcomeSuffixed {
		t.Fatalf("want %q, got %+v", OutcomeSuffixed, landing.Resolution)
	}
}

// Same size, different bytes, no fingerprints: duration alone is not enough.
// Both files and rows stay put, a durable skip is recorded, the retry is
// suppressed, and a change to the occupant's mtime lets it be tried again.
func TestInPlace_SameSizeUnverified_SkipsDurablyUntilOccupantChanges(t *testing.T) {
	svc, store, root := setupInPlace(t)
	src := filepath.Join(root, "incoming", "x.mp3")
	srcAudio, occAudio := filled(5000, 0x10), filled(5000, 0x20)
	b := addInPlaceBook(t, store, "book-b", "Title", src, srcAudio, nil, 0)
	target := targetFor(t, svc, b)
	occ := addInPlaceBook(t, store, "book-o", "Title", target, occAudio, nil, 0)

	category := func() string {
		_, err := svc.OrganizeOneBook(svc.newOrganizer(), b, &noopLogger{})
		var conflict *DestinationConflictError
		if !errors.As(err, &conflict) || !errors.Is(err, ErrDestinationConflictUnresolved) {
			t.Fatalf("want a DestinationConflictError, got %v", err)
		}
		return conflict.Category
	}

	if got := category(); got != OutcomeSameAudioUnverified {
		t.Fatalf("first attempt: want %q, got %q", OutcomeSameAudioUnverified, got)
	}
	mustContent(t, src, srcAudio)
	mustContent(t, target, occAudio)
	if got := getInPlaceBook(t, store, b.ID); got.VersionGroupID != nil || got.FilePath != src {
		t.Fatalf("unverified pair must leave the book row alone: %+v", got)
	}
	if got := getInPlaceBook(t, store, occ.ID); got.VersionGroupID != nil {
		t.Fatalf("unverified pair must leave the occupant row alone")
	}
	if rec, ok := loadDurableSkip(store, OrganizeCollisionSkipPrefix, b.ID); !ok || rec.Category != OutcomeSameAudioUnverified || rec.OccupantSize != int64(len(occAudio)) {
		t.Fatalf("durable skip not recorded as expected: %+v %v", rec, ok)
	}

	if got := category(); got != OutcomeSkippedDurable {
		t.Fatalf("retry with nothing changed: want %q, got %q", OutcomeSkippedDurable, got)
	}

	later := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(target, later, later); err != nil {
		t.Fatal(err)
	}
	if got := category(); got != OutcomeSameAudioUnverified {
		t.Fatalf("after the occupant's mtime changed: want a fresh attempt (%q), got %q", OutcomeSameAudioUnverified, got)
	}
}

// Several books from one source directory computing one destination are not
// moved at all: fragment_collapse, recorded, nothing touched.
func TestInPlace_FragmentCollapse_NotMoved(t *testing.T) {
	svc, store, root := setupInPlace(t)
	dir := filepath.Join(root, "incoming", "Frag")
	b1 := addInPlaceBook(t, store, "frag-1", "Frag Title", filepath.Join(dir, "Frag - 01.mp3"), filled(3000, 0x31), nil, 0)
	b2 := addInPlaceBook(t, store, "frag-2", "Frag Title", filepath.Join(dir, "Frag - 02.mp3"), filled(3300, 0x32), nil, 0)

	stats := svc.organizeBooks(context.Background(), []database.Book{*b1, *b2}, nil, &noopLogger{}, "")
	if got := stats.Collisions[OutcomeFragmentCollapse]; got != 2 {
		t.Fatalf("want 2 fragment_collapse, got %d (stats %+v)", got, stats)
	}
	if stats.Failed != 0 {
		t.Fatalf("fragment collapse is not a failure: %+v", stats)
	}
	mustContent(t, b1.FilePath, filled(3000, 0x31))
	mustContent(t, b2.FilePath, filled(3300, 0x32))
	if rec, ok := loadDurableSkip(store, OrganizeCollisionSkipPrefix, b1.ID); !ok || rec.Category != OutcomeFragmentCollapse {
		t.Fatalf("fragment_collapse skip not recorded: %+v %v", rec, ok)
	}
}

func TestIsPlaceholderTitle(t *testing.T) {
	for title, want := range map[string]bool{
		"":                 true,
		"  ":               true,
		"Unknown Title":    true,
		"unknown author":   true,
		"Read by Narrator": true,
		"narrator":         true,
		"The Hobbit":       false,
		"Unknown Soldier":  false,
	} {
		if got := IsPlaceholderTitle(title); got != want {
			t.Errorf("IsPlaceholderTitle(%q) = %v, want %v", title, got, want)
		}
	}
}

// A placeholder title is held back by the organize filter and counted.
func TestFilter_PlaceholderTitleSkipped(t *testing.T) {
	svc, store, root := setupInPlace(t)
	ph := addInPlaceBook(t, store, "ph", "Unknown Title", filepath.Join(root, "in", "a.mp3"), filled(100, 1), nil, 0)
	real := addInPlaceBook(t, store, "real", "Real Title", filepath.Join(root, "in", "b.mp3"), filled(100, 2), nil, 0)

	toOrganize, _, skipped := svc.filterBooksNeedingOrganization([]database.Book{*ph, *real}, &noopLogger{})
	if skipped != 1 {
		t.Fatalf("want 1 placeholder skip, got %d", skipped)
	}
	if len(toOrganize) != 1 || toOrganize[0].ID != real.ID {
		t.Fatalf("want only the real-titled book organized, got %+v", toOrganize)
	}
}

// Concurrent workers moving different books onto one occupied destination must
// each get their own _copyN name.
func TestInPlace_ConcurrentSuffix_DistinctNames(t *testing.T) {
	svc, store, root := setupInPlace(t)
	const n = 4
	books := make([]*database.Book, n)
	for i := range n {
		books[i] = addInPlaceBook(t, store, "b"+string(rune('a'+i)), "Title",
			filepath.Join(root, "in"+string(rune('a'+i)), "x.mp3"), filled(1000+200*i, byte(i+1)), nil, 0)
	}
	target := targetFor(t, svc, books[0])
	occAudio := filled(50000, 0xEE)
	addInPlaceBook(t, store, "occ", "Title", target, occAudio, nil, 0)

	paths := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			paths[i], _, errs[i] = svc.reOrganizeInPlace(books[i], &noopLogger{})
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if seen[paths[i]] {
			t.Fatalf("two workers landed on %s", paths[i])
		}
		seen[paths[i]] = true
		mustContent(t, paths[i], filled(1000+200*i, byte(i+1)))
	}
	mustContent(t, target, occAudio)
}

// Concurrent workers adopting against one occupant leave exactly one primary.
func TestInPlace_ConcurrentAdopt_OnePrimary(t *testing.T) {
	svc, store, root := setupInPlace(t)
	audio := filled(4096, 0x5A)
	const n = 3
	books := make([]*database.Book, n)
	for i := range n {
		books[i] = addInPlaceBook(t, store, "a"+string(rune('a'+i)), "Title",
			filepath.Join(root, "in"+string(rune('a'+i)), "x.mp3"), audio, nil, 0)
	}
	target := targetFor(t, svc, books[0])
	occ := addInPlaceBook(t, store, "occ", "Title", target, audio, nil, 0)

	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if _, res, err := svc.reOrganizeInPlace(books[i], &noopLogger{}); err != nil || !res.adopted() {
				t.Errorf("worker %d: res %+v err %v", i, res, err)
			}
		})
	}
	wg.Wait()

	group := getInPlaceBook(t, store, occ.ID).VersionGroupID
	if group == nil {
		t.Fatal("occupant has no version group")
	}
	members, err := store.GetBooksByVersionGroup(*group)
	if err != nil {
		t.Fatal(err)
	}
	primaries := 0
	for _, m := range members {
		if m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			primaries++
		}
	}
	if len(members) != n+1 || primaries != 1 {
		t.Fatalf("want %d members with 1 primary, got %d members, %d primaries", n+1, len(members), primaries)
	}
}

// The negative: two books from DIFFERENT source directories computing one
// destination are not fragments. They reach the worker, one takes the target
// and the other is suffixed.
func TestInPlace_SameTargetDifferentDirs_NotFragmentCollapse(t *testing.T) {
	svc, store, root := setupInPlace(t)
	b1 := addInPlaceBook(t, store, "dir-1", "Same Title", filepath.Join(root, "incoming", "one", "x.mp3"), filled(3000, 0x41), nil, 0)
	b2 := addInPlaceBook(t, store, "dir-2", "Same Title", filepath.Join(root, "incoming", "two", "x.mp3"), filled(3300, 0x42), nil, 0)

	stats := svc.organizeBooks(context.Background(), []database.Book{*b1, *b2}, nil, &noopLogger{}, "")
	if got := stats.Collisions[OutcomeFragmentCollapse]; got != 0 {
		t.Fatalf("books from different directories must not be fragment_collapse, got %d", got)
	}
	// Asserted on disk and in the tally, not on ReOrganized: ReOrganizeInPlace
	// rewrites book.FilePath before CommitLanding compares paths, so an in-place
	// move is counted as AlreadyCorrect (the same on origin/main).
	if stats.Collisions[OutcomeSuffixed] != 1 || stats.Failed != 0 || stats.Skipped != 0 {
		t.Fatalf("want one plain move and one suffixed move, got %+v", stats)
	}
	for _, src := range []string{b1.FilePath, b2.FilePath} {
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Fatalf("source %s should have been moved (stat err=%v)", src, err)
		}
	}
	var copies int
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(filepath.Base(path), "_copy1") {
			copies++
		}
		return nil
	})
	if copies != 1 {
		t.Fatalf("want exactly one _copy1 file under the root, got %d", copies)
	}
}

func TestDirIsEmpty(t *testing.T) {
	empty := t.TempDir()
	full := t.TempDir()
	if err := os.WriteFile(filepath.Join(full, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !dirIsEmpty(empty) || dirIsEmpty(full) || dirIsEmpty(filepath.Join(empty, "missing")) {
		t.Fatalf("dirIsEmpty: empty=%v full=%v missing=%v", dirIsEmpty(empty), dirIsEmpty(full), dirIsEmpty(filepath.Join(empty, "missing")))
	}
}
