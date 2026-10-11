// file: internal/tagger/safe_write_verify_test.go
// version: 1.1.0
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
	taglib "go.senan.xyz/taglib"
)

// These tests swap package variables and global config, so they do not run in
// parallel.

func makeVerifyAudio(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	path := filepath.Join(t.TempDir(), "book.m4a")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1", "-c:a", "aac", path).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot encode here: %v: %s", err, out)
	}
	return path
}

func setVerifyAfterWrite(t *testing.T, on bool) {
	t.Helper()
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.VerifyAfterWrite = on })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
}

// dropKeyWriter writes every tag except drop, as a lossy taglib would.
func dropKeyWriter(t *testing.T, drop string) {
	t.Helper()
	prev := tagWriter
	tagWriter = func(path string, tags map[string][]string, opts taglib.WriteOption) error {
		kept := map[string][]string{}
		for k, v := range tags {
			if k != drop {
				kept[k] = v
			}
		}
		return taglib.WriteTags(path, kept, opts)
	}
	t.Cleanup(func() { tagWriter = prev })
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
	if !strings.Contains(err.Error(), "ALBUM") {
		t.Errorf("message %q does not name the key", err.Error())
	}
	// The write stands: the surviving tag is on disk.
	got, rerr := taglib.ReadTags(path)
	if rerr != nil || len(got["TITLE"]) != 1 || got["TITLE"][0] != "Sample Title" {
		t.Errorf("TITLE after failed verification = %v (err %v); the file must stay as written", got["TITLE"], rerr)
	}
	// With no book_file row there is nothing to attach an event to.
	if len(rec.events) != 0 {
		t.Errorf("recorded %d events with no book_file row; want 0", len(rec.events))
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
	if ev.Kind != database.FileEventObserved || ev.BookFileID != "bf-1" || !strings.Contains(ev.Detail, "ALBUM") {
		t.Errorf("event = %+v; want an observed event on bf-1 naming ALBUM", ev)
	}
}

func TestWriteTagsSafe_VerifyAfterWrite_OffIgnoresDroppedTag(t *testing.T) {
	setVerifyAfterWrite(t, false)
	dropKeyWriter(t, "ALBUM")
	path := makeVerifyAudio(t)
	if err := WriteTagsSafe(context.Background(), path, verifyTags, 0, SafeWriteDeps{}); err != nil {
		t.Fatalf("flag off: err = %v; want nil", err)
	}
}

func TestWriteTagsSafe_VerifyAfterWrite_CleanWriteAndCaseAndDelete(t *testing.T) {
	setVerifyAfterWrite(t, true)
	path := makeVerifyAudio(t)
	// A lower-case key matches taglib's upper-cased read-back.
	if err := WriteTagsSafe(context.Background(), path, map[string][]string{"title": {"Sample Title"}, "NARRATOR": {"A", "B"}}, 0, SafeWriteDeps{}); err != nil {
		t.Fatalf("clean write: err = %v; want nil", err)
	}
	// A delete (no values) must read back absent.
	if err := WriteTagsSafe(context.Background(), path, map[string][]string{"NARRATOR": {}}, 0, SafeWriteDeps{}); err != nil {
		t.Fatalf("delete write: err = %v; want nil", err)
	}
}

func TestWriteTagsSafe_VerifyAfterWrite_UnreadableFileFails(t *testing.T) {
	setVerifyAfterWrite(t, true)
	prev := tagReader
	tagReader = func(string) (map[string][]string, error) { return nil, os.ErrInvalid }
	t.Cleanup(func() { tagReader = prev })
	path := makeVerifyAudio(t)
	if err := WriteTagsSafe(context.Background(), path, verifyTags, 0, SafeWriteDeps{}); !errors.Is(err, ErrVerifyAfterWrite) {
		t.Fatalf("err = %v; want ErrVerifyAfterWrite when the read-back fails", err)
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
