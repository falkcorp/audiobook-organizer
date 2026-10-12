// file: internal/tagger/safe_write_verify_test.go
// version: 2.1.0
// guid: 7c1e5b94-2d38-4f6a-a0b7-93e4c8d51f26
// last-edited: 2026-10-11

package tagger

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	taglib "go.senan.xyz/taglib"
)

// These tests swap package variables and global config, so they do not run in
// parallel.

// copyFixture copies the checked-in testdata/fixtures/<name> into a fresh temp
// dir and returns the copy. The fixtures are plain files in git (Git LFS was
// retired 2026-09-22), so a missing or unreadable one is a failure, not a skip:
// a skip here would pass every gating job without testing anything.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := taglib.ReadTags(path); err != nil {
		t.Fatalf("fixture %s is not readable audio (an LFS pointer?): %v", name, err)
	}
	return path
}

// makeVerifyAudio is the m4b fixture copy most tests write to.
func makeVerifyAudio(t *testing.T) string {
	t.Helper()
	return copyFixture(t, "test_sample.m4b")
}

func setVerifyAfterWrite(t *testing.T, on bool) {
	t.Helper()
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.VerifyAfterWrite = on })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
}

// lossyWriter substitutes tagWriter with one that passes tags through f first,
// as a taglib that mangles what it is given would.
func lossyWriter(t *testing.T, f func(map[string][]string) map[string][]string) {
	t.Helper()
	prev := tagWriter
	tagWriter = func(path string, tags map[string][]string, opts taglib.WriteOption) error {
		return taglib.WriteTags(path, f(tags), opts)
	}
	t.Cleanup(func() { tagWriter = prev })
}

// dropKeyWriter writes every tag except drop, as a lossy taglib would.
func dropKeyWriter(t *testing.T, drop string) {
	t.Helper()
	lossyWriter(t, func(tags map[string][]string) map[string][]string {
		kept := map[string][]string{}
		for k, v := range tags {
			if k != drop {
				kept[k] = v
			}
		}
		return kept
	})
}

type captureRecorder struct {
	mu     sync.Mutex
	events []database.FileEvent
}

func (c *captureRecorder) AppendFileEvent(e database.FileEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}

var verifyTags = map[string][]string{"TITLE": {"Sample Title"}, "ALBUM": {"Sample Album"}}

func mustRead(t *testing.T, path string) map[string][]string {
	t.Helper()
	got, err := taglib.ReadTags(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return got
}

func TestWriteTagsSafe_VerifyAfterWrite_DroppedTagFails(t *testing.T) {
	setVerifyAfterWrite(t, true)
	dropKeyWriter(t, "ALBUM")
	path := makeVerifyAudio(t)
	rec := &captureRecorder{}

	err := WriteTagsSafe(context.Background(), path, verifyTags, 0, SafeWriteDeps{Provenance: rec})
	if !errors.Is(err, ErrVerifyAfterWrite) {
		t.Fatalf("err = %v; want ErrVerifyAfterWrite", err)
	}
	var vae *VerifyAfterWriteError
	if !errors.As(err, &vae) || len(vae.Keys) != 1 || vae.Keys[0] != "ALBUM" {
		t.Fatalf("err = %v; want a VerifyAfterWriteError naming only ALBUM", err)
	}
	if !strings.Contains(err.Error(), "ALBUM") || !strings.Contains(err.Error(), `"Sample Album"`) {
		t.Errorf("message %q does not name the key and the value it wanted", err.Error())
	}
	// The write stands: the surviving tag is on disk.
	if got := mustRead(t, path); len(got["TITLE"]) != 1 || got["TITLE"][0] != "Sample Title" {
		t.Errorf("TITLE after failed verification = %v; the file must stay as written", got["TITLE"])
	}
}

// With no book_file row the event is keyed on the file's digest (an orphan
// observation, found through FindFileEventsByHash), not dropped.
func TestWriteTagsSafe_VerifyAfterWrite_NoRowRecordsHashKeyedEvent(t *testing.T) {
	setVerifyAfterWrite(t, true)
	dropKeyWriter(t, "ALBUM")
	path := makeVerifyAudio(t)
	rec := &captureRecorder{}

	if err := WriteTagsSafe(context.Background(), path, verifyTags, 0, SafeWriteDeps{Provenance: rec}); !errors.Is(err, ErrVerifyAfterWrite) {
		t.Fatalf("err = %v; want ErrVerifyAfterWrite", err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("recorded %d events; want 1 hash-keyed event", len(rec.events))
	}
	ev := rec.events[0]
	wantSHA, wantSize, herr := fileops.ComputeFileHashAndSize(path)
	if herr != nil {
		t.Fatal(herr)
	}
	if ev.BookFileID != "" || ev.Digest.SHA256Full != wantSHA || ev.Digest.SizeBytes != wantSize {
		t.Errorf("event = %+v; want no book_file ID and the post-write digest %s / %d", ev, wantSHA, wantSize)
	}
}

func TestWriteTagsSafe_VerifyAfterWrite_RecordsObservedEvent(t *testing.T) {
	setVerifyAfterWrite(t, true)
	dropKeyWriter(t, "ALBUM")
	path := makeVerifyAudio(t)
	rec := &captureRecorder{}
	deps := SafeWriteDeps{Provenance: rec, BookFileID: "bf-1", HashStore: &pathRecorder{rows: map[string]string{}}}

	if err := WriteTagsSafe(context.Background(), path, verifyTags, 0, deps); !errors.Is(err, ErrVerifyAfterWrite) {
		t.Fatalf("err = %v; want ErrVerifyAfterWrite", err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("recorded %d events; want 1", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Kind != database.FileEventObserved || ev.BookFileID != "bf-1" {
		t.Fatalf("event = %+v; want an observed event on bf-1", ev)
	}
	// The diff carries wanted and read values, not just the key.
	for _, want := range []string{"ALBUM", `want ["Sample Album"]`, "got <absent>"} {
		if !strings.Contains(ev.Detail, want) {
			t.Errorf("Detail %q lacks %q", ev.Detail, want)
		}
	}
	// The digest is the post-write file's, with its size.
	sha, size, herr := fileops.ComputeFileHashAndSize(path)
	if herr != nil {
		t.Fatal(herr)
	}
	if ev.Digest.SHA256Full != sha || ev.Digest.SizeBytes != size {
		t.Errorf("event digest = %s/%d; want the post-write %s/%d", ev.Digest.SHA256Full, ev.Digest.SizeBytes, sha, size)
	}
}

func TestWriteTagsSafe_VerifyAfterWrite_OffIgnoresDroppedTag(t *testing.T) {
	setVerifyAfterWrite(t, false)
	dropKeyWriter(t, "ALBUM")
	path := makeVerifyAudio(t)
	if err := WriteTagsSafe(context.Background(), path, verifyTags, 0, SafeWriteDeps{}); err != nil {
		t.Fatalf("flag off: err = %v; want nil", err)
	}
	// ForceVerify checks regardless of the setting (WriteTagProperties).
	err := WriteTagsSafe(context.Background(), path, verifyTags, 0, SafeWriteDeps{ForceVerify: true})
	if !errors.Is(err, ErrVerifyAfterWrite) {
		t.Fatalf("flag off + ForceVerify: err = %v; want ErrVerifyAfterWrite", err)
	}
}

// One write is one read-back.
func TestWriteTagsSafe_VerifyAfterWrite_ReadsBackOnce(t *testing.T) {
	setVerifyAfterWrite(t, true)
	reads := 0
	prev := tagReader
	tagReader = func(p string) (map[string][]string, error) { reads++; return prev(p) }
	t.Cleanup(func() { tagReader = prev })
	path := makeVerifyAudio(t)
	if err := WriteTagsSafe(context.Background(), path, verifyTags, 0, SafeWriteDeps{ForceVerify: true}); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("read-backs = %d; want 1", reads)
	}
}

// Every fixture container round-trips a plain write, a case-insensitive key, a
// multi-value key and a delete without a false mismatch.
func TestWriteTagsSafe_VerifyAfterWrite_CleanWriteAndCaseAndDelete(t *testing.T) {
	setVerifyAfterWrite(t, true)
	for _, name := range []string{"test_sample.m4b", "test_sample.mp3", "test_sample.flac"} {
		t.Run(name, func(t *testing.T) {
			path := copyFixture(t, name)
			// A lower-case key matches taglib's upper-cased read-back.
			if err := WriteTagsSafe(context.Background(), path, map[string][]string{"title": {"Sample Title"}, "NARRATOR": {"A", "B"}}, 0, SafeWriteDeps{}); err != nil {
				t.Fatalf("clean write: err = %v; want nil", err)
			}
			// A delete (no values) must read back absent.
			if err := WriteTagsSafe(context.Background(), path, map[string][]string{"NARRATOR": {}}, 0, SafeWriteDeps{}); err != nil {
				t.Fatalf("delete write: err = %v; want nil", err)
			}
			if got := mustRead(t, path); len(got["NARRATOR"]) != 0 {
				t.Fatalf("NARRATOR after delete = %v; want absent", got["NARRATOR"])
			}
		})
	}
}

// go-taglib erases a key whose joined value is empty, so {""} is a delete, not
// a write of an empty string. buildWriteTagMap emits ALBUM:[""] (a series with
// no album) and WriteSingleTag(path, tag, "") clears this way; both must verify.
func TestWriteTagsSafe_VerifyAfterWrite_EmptyStringValueIsADelete(t *testing.T) {
	setVerifyAfterWrite(t, true)
	for _, name := range []string{"test_sample.m4b", "test_sample.mp3", "test_sample.flac"} {
		t.Run(name, func(t *testing.T) {
			path := copyFixture(t, name)
			ctx := context.Background()
			// Delete of a key the file does not have.
			if err := WriteTagsSafe(ctx, path, map[string][]string{"ALBUM": {""}}, 0, SafeWriteDeps{}); err != nil {
				t.Fatalf(`ALBUM:[""] on a file without ALBUM: err = %v; want nil`, err)
			}
			// Delete of a key the file has.
			if err := WriteTagsSafe(ctx, path, map[string][]string{"ALBUM": {"Set"}, "TITLE": {"T"}}, 0, SafeWriteDeps{}); err != nil {
				t.Fatal(err)
			}
			if err := WriteTagsSafe(ctx, path, map[string][]string{"ALBUM": {""}}, 0, SafeWriteDeps{}); err != nil {
				t.Fatalf(`ALBUM:[""] on a file with ALBUM: err = %v; want nil`, err)
			}
			if got := mustRead(t, path); len(got["ALBUM"]) != 0 || len(got["TITLE"]) != 1 {
				t.Fatalf("after the delete ALBUM=%v TITLE=%v; want ALBUM absent, TITLE kept", got["ALBUM"], got["TITLE"])
			}
		})
	}
}

// A delete the writer did not make must fail, both spellings ({} and {""}).
func TestWriteTagsSafe_VerifyAfterWrite_DeleteNotMadeFails(t *testing.T) {
	setVerifyAfterWrite(t, true)
	for _, del := range [][]string{{}, {""}} {
		path := makeVerifyAudio(t)
		if err := WriteTagsSafe(context.Background(), path, map[string][]string{"ALBUM": {"Keep Me"}}, 0, SafeWriteDeps{}); err != nil {
			t.Fatal(err)
		}
		// A writer that ignores deletes, as a lossy taglib would.
		lossyWriter(t, func(tags map[string][]string) map[string][]string {
			kept := map[string][]string{}
			for k, v := range tags {
				if strings.Join(v, "\v") != "" {
					kept[k] = v
				}
			}
			return kept
		})
		err := WriteTagsSafe(context.Background(), path, map[string][]string{"ALBUM": del}, 0, SafeWriteDeps{})
		var vae *VerifyAfterWriteError
		if !errors.As(err, &vae) || len(vae.Keys) != 1 || vae.Keys[0] != "ALBUM" {
			t.Fatalf("delete %#v not made: err = %v; want a VerifyAfterWriteError naming ALBUM", del, err)
		}
		if got := vae.Diffs[0].Got; len(got) != 1 || got[0] != "Keep Me" {
			t.Errorf("Diffs[0].Got = %v; want the surviving value", got)
		}
	}
}

// Order matters: a writer that reorders the values of a multi-value key
// differs from what was asked for even though the set is the same.
func TestWriteTagsSafe_VerifyAfterWrite_ValueOrderMatters(t *testing.T) {
	setVerifyAfterWrite(t, true)
	lossyWriter(t, func(tags map[string][]string) map[string][]string {
		out := map[string][]string{}
		for k, v := range tags {
			rev := make([]string, len(v))
			for i := range v {
				rev[len(v)-1-i] = v[i]
			}
			out[k] = rev
		}
		return out
	})
	path := makeVerifyAudio(t)
	err := WriteTagsSafe(context.Background(), path, map[string][]string{"NARRATOR": {"Alpha", "Beta"}}, 0, SafeWriteDeps{})
	if !errors.Is(err, ErrVerifyAfterWrite) {
		t.Fatalf("reordered values: err = %v; want ErrVerifyAfterWrite", err)
	}
}

// Every value is compared, not just the first.
func TestWriteTagsSafe_VerifyAfterWrite_EveryValueIsCompared(t *testing.T) {
	setVerifyAfterWrite(t, true)
	lossyWriter(t, func(tags map[string][]string) map[string][]string {
		out := map[string][]string{}
		for k, v := range tags {
			c := append([]string(nil), v...)
			if len(c) > 1 {
				c[1] = "Changed"
			}
			out[k] = c
		}
		return out
	})
	path := makeVerifyAudio(t)
	err := WriteTagsSafe(context.Background(), path, map[string][]string{"NARRATOR": {"Alpha", "Beta"}}, 0, SafeWriteDeps{})
	if !errors.Is(err, ErrVerifyAfterWrite) {
		t.Fatalf("second value changed: err = %v; want ErrVerifyAfterWrite", err)
	}
}

func TestWriteTagsSafe_VerifyAfterWrite_UnreadableFileFails(t *testing.T) {
	setVerifyAfterWrite(t, true)
	prev := tagReader
	tagReader = func(string) (map[string][]string, error) { return nil, os.ErrInvalid }
	t.Cleanup(func() { tagReader = prev })
	path := makeVerifyAudio(t)
	err := WriteTagsSafe(context.Background(), path, verifyTags, 0, SafeWriteDeps{})
	if !errors.Is(err, ErrVerifyAfterWrite) || !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("err = %v; want ErrVerifyAfterWrite wrapping the read error", err)
	}
	var vae *VerifyAfterWriteError
	if !errors.As(err, &vae) || len(vae.Keys) != 2 {
		t.Fatalf("err = %v; want every written key reported", err)
	}
}

// MP4 stores track, disc, tempo and compilation as integer atoms, so "03" reads
// back as "3" and "01/02" as "1/2". That is the value written, not a loss.
func TestWriteTagsSafe_VerifyAfterWrite_MP4NumericAtomsNormalise(t *testing.T) {
	setVerifyAfterWrite(t, true)
	path := makeVerifyAudio(t)
	tags := map[string][]string{
		"TRACKNUMBER": {"03"},
		"DISCNUMBER":  {"01/02"},
		"BPM":         {"0120"},
		"COMPILATION": {"1"},
	}
	if err := WriteTagsSafe(context.Background(), path, tags, 0, SafeWriteDeps{}); err != nil {
		t.Fatalf("zero-padded numeric atoms: err = %v; want nil", err)
	}
	got := mustRead(t, path)
	if got["TRACKNUMBER"][0] != "3" || got["DISCNUMBER"][0] != "1/2" {
		t.Fatalf("m4b read back %v; this test expects the atoms to normalise (taglib behaviour changed?)", got)
	}
	// "n/total" forms.
	if err := WriteTagsSafe(context.Background(), path, map[string][]string{"TRACKNUMBER": {"007/010"}}, 0, SafeWriteDeps{}); err != nil {
		t.Fatalf("n/total: err = %v; want nil", err)
	}
	// A non-numeric value an integer atom cannot hold is a real loss (it reads
	// back 0) and must still fail.
	err := WriteTagsSafe(context.Background(), path, map[string][]string{"DISCNUMBER": {"side A"}}, 0, SafeWriteDeps{})
	if !errors.Is(err, ErrVerifyAfterWrite) {
		t.Fatalf("non-numeric disc number: err = %v; want ErrVerifyAfterWrite", err)
	}
}

// Two spellings of one key in one write are folded before the write, so the
// outcome no longer depends on map iteration order.
func TestWriteTagsSafe_VerifyAfterWrite_CaseDuplicateKeysAreDeterministic(t *testing.T) {
	setVerifyAfterWrite(t, true)
	for i := 0; i < 20; i++ {
		path := makeVerifyAudio(t)
		tags := map[string][]string{"title": {"lower"}, "TITLE": {"UPPER"}, "Title": {"Mixed"}}
		if err := WriteTagsSafe(context.Background(), path, tags, 0, SafeWriteDeps{}); err != nil {
			t.Fatalf("iteration %d: err = %v; want nil (verification must follow the folded winner)", i, err)
		}
		if got := mustRead(t, path)["TITLE"]; len(got) != 1 || got[0] != "UPPER" {
			t.Fatalf("iteration %d: TITLE = %v; want the upper-case key's value", i, got)
		}
	}
}

// pathCopyImporter stands in for the Deluge importer: it copies src into libDir
// and returns the copy.
type pathCopyImporter struct{ libDir string }

func (c pathCopyImporter) ImportPath(_ context.Context, src, _ string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(c.libDir, filepath.Base(src))
	return dst, os.WriteFile(dst, data, 0o644)
}

// After a protected-path redirect the tags are on the library copy and the
// original is untouched; verification must read the copy. A clean write must
// not be blamed for the untouched original lacking its tags, and a lossy one
// must be reported against the copy's path.
func TestWriteTagsSafe_VerifyAfterWrite_ReadsTheRedirectedCopy(t *testing.T) {
	setVerifyAfterWrite(t, true)
	src := makeVerifyAudio(t)
	libDir := t.TempDir()
	deps := SafeWriteDeps{
		ProtectedCache: &staticPathChecker{protected: map[string]bool{src: true}},
		Importer:       pathCopyImporter{libDir: libDir},
	}
	dst := filepath.Join(libDir, filepath.Base(src))

	if err := WriteTagsSafe(context.Background(), src, verifyTags, 0, deps); err != nil {
		t.Fatalf("clean redirected write: err = %v; want nil", err)
	}
	if got := mustRead(t, dst); got["TITLE"][0] != "Sample Title" {
		t.Fatalf("library copy TITLE = %v; the write did not land there", got["TITLE"])
	}
	if got := mustRead(t, src); len(got["TITLE"]) != 0 {
		t.Fatalf("protected original TITLE = %v; it must be left alone", got["TITLE"])
	}

	dropKeyWriter(t, "ALBUM")
	err := WriteTagsSafe(context.Background(), src, verifyTags, 0, deps)
	var vae *VerifyAfterWriteError
	if !errors.As(err, &vae) || vae.Path != dst {
		t.Fatalf("lossy redirected write: err = %v; want a VerifyAfterWriteError on %s", err, dst)
	}
}

// The event names the row of the file written, which hashOptions looks up; the
// caller's deps.BookFileID is not consulted when it is empty.
func TestWriteTagsSafe_VerifyAfterWrite_EventUsesTheRowOfTheFileWritten(t *testing.T) {
	setVerifyAfterWrite(t, true)
	dropKeyWriter(t, "ALBUM")
	path := makeVerifyAudio(t)
	rec := &captureRecorder{}
	deps := SafeWriteDeps{Provenance: rec, HashStore: &pathRecorder{rows: map[string]string{path: "bf-looked-up"}}}

	if err := WriteTagsSafe(context.Background(), path, verifyTags, 0, deps); !errors.Is(err, ErrVerifyAfterWrite) {
		t.Fatalf("err = %v; want ErrVerifyAfterWrite", err)
	}
	if len(rec.events) != 1 || rec.events[0].BookFileID != "bf-looked-up" {
		t.Fatalf("events = %+v; want one on the looked-up row bf-looked-up", rec.events)
	}
}

// innerStore is a store that is both a hash recorder and a provenance
// recorder, as the production Pebble store is.
type innerStore struct {
	database.Store // nil: only the overridden methods may be called
	captureRecorder
}

func (*innerStore) GetBookFileByPath(string) (*database.BookFile, error)      { return nil, nil }
func (*innerStore) UpdateBookFileHashes(string, string, string, string) error { return nil }

// decoratedStore mirrors server.indexedStore: it embeds only database.Store,
// so AppendFileEvent is not in its method set, and it opts in to capability
// lookup through Unwrap.
type decoratedStore struct {
	database.Store
	inner *innerStore
}

func (d *decoratedStore) Unwrap() database.Store { return d.inner }

// The production HashStore is a decorator that hides AppendFileEvent; the
// event must still reach the recorder underneath.
func TestWriteTagsSafe_VerifyAfterWrite_RecordsThroughStoreDecorator(t *testing.T) {
	setVerifyAfterWrite(t, true)
	dropKeyWriter(t, "ALBUM")
	path := makeVerifyAudio(t)
	inner := &innerStore{}
	deps := SafeWriteDeps{BookFileID: "bf-9", HashStore: &decoratedStore{Store: inner, inner: inner}}

	if err := WriteTagsSafe(context.Background(), path, verifyTags, 0, deps); !errors.Is(err, ErrVerifyAfterWrite) {
		t.Fatalf("err = %v; want ErrVerifyAfterWrite", err)
	}
	if len(inner.events) != 1 || inner.events[0].BookFileID != "bf-9" {
		t.Fatalf("events through the decorator = %+v; want one on bf-9", inner.events)
	}
}

// Extra coverage for containers the checked-in fixtures do not include. It
// needs ffmpeg and skips without it; the fixture tests above are the gate.
func TestWriteTagsSafe_VerifyAfterWrite_FFmpegContainers(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	setVerifyAfterWrite(t, true)
	cases := map[string][]string{
		"book.m4a":  {"-c:a", "aac"},
		"book.ogg":  {"-c:a", "vorbis", "-strict", "-2", "-ac", "2"},
		"book.opus": {"-c:a", "libopus", "-ar", "48000"},
	}
	for name, codec := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			args := append([]string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1"}, codec...)
			if out, err := exec.Command("ffmpeg", append(args, path)...).CombinedOutput(); err != nil {
				t.Skipf("ffmpeg cannot encode %s here: %v: %s", name, err, out)
			}
			ctx := context.Background()
			if err := WriteTagsSafe(ctx, path, map[string][]string{"TITLE": {"T"}, "NARRATOR": {"A", "B"}, "ALBUM": {""}}, 0, SafeWriteDeps{}); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := WriteTagsSafe(ctx, path, map[string][]string{"NARRATOR": {}}, 0, SafeWriteDeps{}); err != nil {
				t.Fatalf("delete: %v", err)
			}
		})
	}
}
