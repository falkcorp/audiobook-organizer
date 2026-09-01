// file: internal/plugins/maintenance/extract_wav_clips_hash_test.go
// version: 1.2.0
// guid: c8a1d5f3-2e94-4b07-a6d1-5f83b20c9e7a
// last-edited: 2026-09-01

package maintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// fakeHashSetter records what maintenance.extract-wav-clips writes back to
// book_files.file_hash.
type fakeHashSetter struct {
	calls []struct{ id, hash string }
	err   error
}

func (f *fakeHashSetter) SetBookFileHash(id, hash string) error {
	f.calls = append(f.calls, struct{ id, hash string }{id, hash})
	return f.err
}

// writeSparseAudio creates a file of exactly size bytes whose middle is a hole.
// A hole costs no disk blocks on APFS or ext4, so a fixture above the 100 MB
// threshold is cheap to make.
//
// The size matters more than anything else in this file: at or below
// filehash.Threshold the canonical chunked digest and a plain whole-file
// SHA-256 are the SAME string. A test using a small fixture would pass
// identically against the buggy code and prove nothing.
func writeSparseAudio(t *testing.T, path string, size int64) {
	t.Helper()
	if size <= filehash.Threshold {
		t.Fatalf("fixture size %d is not above filehash.Threshold %d; the two hash strategies agree below it", size, int64(filehash.Threshold))
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString("HEAD-MARKER-clip"); err != nil {
		t.Fatalf("write head: %v", err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := f.Seek(size-int64(len("TAIL-MARKER-clip")), io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	if _, err := f.WriteString("TAIL-MARKER-clip"); err != nil {
		t.Fatalf("write tail: %v", err)
	}
}

// wholeFileSHA256 is the WRONG algorithm for this column, reproduced here on
// purpose so the tests can assert the stored value is not it.
func wholeFileSHA256(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type clipFixture struct {
	dir      string
	cacheDir string
	src      string
	clip     string
	chunked  string
	whole    string
}

func newClipFixture(t *testing.T) clipFixture {
	t.Helper()
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, ".wav-cache")
	if err := os.MkdirAll(cacheDir, 0o775); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	src := filepath.Join(dir, "Big Book.m4b")
	writeSparseAudio(t, src, filehash.Threshold+(1<<20))

	// Stand in for the WAV ffmpeg would have produced.
	clip := filepath.Join(cacheDir, "existing-key.wav")
	if err := os.WriteFile(clip, []byte("RIFF fake wav"), 0o644); err != nil {
		t.Fatalf("write clip: %v", err)
	}

	chunked, err := filehash.BookFileHash(src)
	if err != nil {
		t.Fatalf("BookFileHash: %v", err)
	}
	whole := wholeFileSHA256(t, src)
	if chunked == whole {
		t.Fatalf("fixture is degenerate: chunked and whole-file digests agree, so no assertion here can observe the bug")
	}
	return clipFixture{dir: dir, cacheDir: cacheDir, src: src, clip: clip, chunked: chunked, whole: whole}
}

// TestPersistCanonicalFileHash_WritesChunkedNotWholeFile is the regression test
// for the clip-extraction half of the split.
//
// maintenance.extract-wav-clips used to hash the source with a plain whole-file
// SHA-256 and store that in book_files.file_hash. Above 100 MB that value can
// never equal the digest the scanner and the backfill job write, so
// internal/dedup/collectors_exact.go — which reports Confidence 1.0 on a hash
// match — was comparing two different alphabets and silently found nothing.
func TestPersistCanonicalFileHash_WritesChunkedNotWholeFile(t *testing.T) {
	fx := newClipFixture(t)
	store := &fakeHashSetter{}

	ref := audioFileRef{
		Path:       fx.src,
		CacheKey:   "path:existing-key", // no stored hash -> path-derived cache key
		BookFileID: "bf-1",
		StoredHash: "",
	}

	got, err := persistCanonicalFileHash(store, fx.cacheDir, ref, fx.clip)
	if err != nil {
		t.Fatalf("persistCanonicalFileHash: %v", err)
	}
	if got != fx.chunked {
		t.Errorf("returned hash = %q, want canonical chunked digest %q", got, fx.chunked)
	}
	if len(store.calls) != 1 {
		t.Fatalf("SetBookFileHash calls = %d, want 1 (the row had no hash)", len(store.calls))
	}
	if store.calls[0].hash != fx.chunked {
		t.Errorf("persisted file_hash = %q, want canonical chunked digest %q", store.calls[0].hash, fx.chunked)
	}
	if store.calls[0].hash == fx.whole {
		t.Errorf("persisted a whole-file SHA-256 (%q); book_files.file_hash must hold filehash.BookFileHash", store.calls[0].hash)
	}
	if store.calls[0].id != "bf-1" {
		t.Errorf("persisted against book_file %q, want %q", store.calls[0].id, "bf-1")
	}
}

// TestPersistCanonicalFileHash_LeavesExistingHashAlone is the "(when missing)"
// half. The op's OperationDef.Description has always promised the write-back is
// conditional; there was no such guard in the body, and because a stored
// CHUNKED hash can never equal the full hash the op computed, the overwrite
// fired for every file above the threshold — exactly the population the chunked
// strategy exists for.
func TestPersistCanonicalFileHash_LeavesExistingHashAlone(t *testing.T) {
	fx := newClipFixture(t)
	store := &fakeHashSetter{}

	ref := audioFileRef{
		Path:       fx.src,
		CacheKey:   fx.chunked, // nthAudioFile uses the stored hash as the cache key
		BookFileID: "bf-1",
		StoredHash: fx.chunked,
	}

	if _, err := persistCanonicalFileHash(store, fx.cacheDir, ref, fx.clip); err != nil {
		t.Fatalf("persistCanonicalFileHash: %v", err)
	}
	if len(store.calls) != 0 {
		t.Errorf("SetBookFileHash called %d time(s) for a row that already has a hash: %+v", len(store.calls), store.calls)
	}
}

// TestPersistCanonicalFileHash_DoesNotOverwriteADisagreeingHash pins the
// decision that repair is NOT this op's job. A row whose stored hash disagrees
// with the canonical digest is corrupted; recomputing it as a side effect of
// caching a WAV is how it got corrupted in the first place.
func TestPersistCanonicalFileHash_DoesNotOverwriteADisagreeingHash(t *testing.T) {
	fx := newClipFixture(t)
	store := &fakeHashSetter{}

	ref := audioFileRef{
		Path:       fx.src,
		CacheKey:   fx.whole, // a row already corrupted by an earlier run
		BookFileID: "bf-1",
		StoredHash: fx.whole,
	}

	if _, err := persistCanonicalFileHash(store, fx.cacheDir, ref, fx.clip); err != nil {
		t.Fatalf("persistCanonicalFileHash: %v", err)
	}
	if len(store.calls) != 0 {
		t.Errorf("SetBookFileHash called %d time(s); a disagreeing hash needs a deliberate repair op, not a silent rewrite", len(store.calls))
	}
	// The hardlink still happens, so the cache is warm under the name a
	// repaired row will use.
	if _, err := os.Stat(filepath.Join(fx.cacheDir, fx.chunked+".wav")); err != nil {
		t.Errorf("content-addressed hardlink not created: %v", err)
	}
}

// TestPersistCanonicalFileHash_HardlinksUnderTheCanonicalName covers the cache
// key's own requirement: the clip must end up under the name a later run will
// look it up by, which is the canonical digest.
func TestPersistCanonicalFileHash_HardlinksUnderTheCanonicalName(t *testing.T) {
	fx := newClipFixture(t)
	store := &fakeHashSetter{}

	ref := audioFileRef{
		Path:       fx.src,
		CacheKey:   "path:existing-key",
		BookFileID: "bf-1",
	}
	if _, err := persistCanonicalFileHash(store, fx.cacheDir, ref, fx.clip); err != nil {
		t.Fatalf("persistCanonicalFileHash: %v", err)
	}

	linked := filepath.Join(fx.cacheDir, fx.chunked+".wav")
	body, err := os.ReadFile(linked)
	if err != nil {
		t.Fatalf("hardlink %s: %v", linked, err)
	}
	if string(body) != "RIFF fake wav" {
		t.Errorf("hardlink content = %q, want the extracted clip", string(body))
	}
	if _, err := os.Stat(filepath.Join(fx.cacheDir, fx.whole+".wav")); err == nil {
		t.Error("clip was linked under a whole-file SHA-256 name; nothing will ever look it up there")
	}
}

// TestPersistCanonicalFileHash_NoRowToWriteBackTo covers the Book.FilePath
// fallback, where there is no book_files row at all.
func TestPersistCanonicalFileHash_NoRowToWriteBackTo(t *testing.T) {
	fx := newClipFixture(t)
	store := &fakeHashSetter{}

	ref := audioFileRef{Path: fx.src, CacheKey: "path:existing-key"}
	got, err := persistCanonicalFileHash(store, fx.cacheDir, ref, fx.clip)
	if err != nil {
		t.Fatalf("persistCanonicalFileHash: %v", err)
	}
	if got != fx.chunked {
		t.Errorf("returned hash = %q, want %q", got, fx.chunked)
	}
	if len(store.calls) != 0 {
		t.Errorf("SetBookFileHash called with no book_file row present: %+v", store.calls)
	}
}

// TestNthAudioFile_CarriesStoredHash connects the two halves of the
// when-missing guard: persistCanonicalFileHash reads audioFileRef.StoredHash,
// and this is what proves nthAudioFile actually POPULATES it from the row.
//
// Without this, the guard and the field were introduced in the same change and
// nothing exercised the path between them — a guard reading a field nothing
// fills is decorative, and would silently readmit the overwrite it exists to
// prevent.
func TestNthAudioFile_CarriesStoredHash(t *testing.T) {
	t.Run("row with a hash", func(t *testing.T) {
		store := newSortStore([]database.BookFile{
			{ID: "f1", BookID: "b1", FilePath: "/lib/b/track01.mp3", TrackNumber: 1, FileHash: "deadbeef"},
		})
		ref, err := firstAudioFile(store, database.Book{ID: "b1"})
		if err != nil {
			t.Fatalf("firstAudioFile: %v", err)
		}
		if ref.StoredHash != "deadbeef" {
			t.Errorf("StoredHash = %q, want %q — the when-missing guard reads this field", ref.StoredHash, "deadbeef")
		}
		if ref.CacheKey != "deadbeef" {
			t.Errorf("CacheKey = %q, want the stored hash %q", ref.CacheKey, "deadbeef")
		}
	})

	t.Run("row without a hash", func(t *testing.T) {
		store := newSortStore([]database.BookFile{
			{ID: "f1", BookID: "b1", FilePath: "/lib/b/track01.mp3", TrackNumber: 1},
		})
		ref, err := firstAudioFile(store, database.Book{ID: "b1"})
		if err != nil {
			t.Fatalf("firstAudioFile: %v", err)
		}
		if ref.StoredHash != "" {
			t.Errorf("StoredHash = %q, want empty for a row with no hash", ref.StoredHash)
		}
		// The cache key falls back to a path-derived form, which is exactly why
		// StoredHash cannot be inferred from it.
		if !strings.HasPrefix(ref.CacheKey, "path:") {
			t.Errorf("CacheKey = %q, want a path: fallback", ref.CacheKey)
		}
	})
}

// TestPersistCanonicalFileHash_LinksWhenTheKeyMerelySTARTSWithTheHash pins the
// exact comparison the hardlink guard makes, because the guard was tightened
// and "equivalent" was asserted in prose rather than in a test.
//
// The guard used to read `!strings.HasPrefix(ref.CacheKey, srcHash)` and now
// reads `ref.CacheKey != srcHash`. Those two differ on exactly one input shape:
// a CacheKey that STARTS with the canonical hash but carries a suffix. HasPrefix
// treats such a key as already canonical and skips the link; `!=` treats it as a
// different name and links.
//
// `!=` is the behaviour we want, not merely a tidier spelling of the old one. A
// suffixed key is a DIFFERENT filename in the cache directory, so a later run
// keyed on the bare canonical hash finds nothing there — skipping the link would
// leave the canonical name permanently cold.
//
// No CacheKey produced by nthAudioFile has this shape today (it is a bare
// 64-hex hash, or an "fp:"/"path:" form that cannot begin with hex), so the two
// spellings agree on every value reachable right now and this test cannot fail
// by accident. It exists so that if the key scheme ever grows a suffix — a clip
// offset, a duration, an extension — the intended behaviour is already written
// down instead of being silently decided by whichever operator is in the source.
func TestPersistCanonicalFileHash_LinksWhenTheKeyMerelySTARTSWithTheHash(t *testing.T) {
	fx := newClipFixture(t)
	store := &fakeHashSetter{}

	ref := audioFileRef{
		Path:     fx.src,
		CacheKey: fx.chunked + "_30s",
		// No BookFileID: this test is about the link, not the write-back.
	}
	if _, err := persistCanonicalFileHash(store, fx.cacheDir, ref, fx.clip); err != nil {
		t.Fatalf("persistCanonicalFileHash: %v", err)
	}

	linked := filepath.Join(fx.cacheDir, fx.chunked+".wav")
	if _, err := os.Stat(linked); err != nil {
		t.Fatalf("a CacheKey that only starts with the canonical hash is a different "+
			"filename, so the clip must still be linked under the bare hash; stat %s: %v", linked, err)
	}
}
