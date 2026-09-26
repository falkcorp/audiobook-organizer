// file: internal/covertext/covertext_test.go
// version: 1.0.0
// guid: 3c9a6f18-7e2d-4b05-8d41-5f0b1e7a9c62
// last-edited: 2026-09-26

package covertext

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestRecordRoundTripPebble(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	h := hashOf("image-a")
	if r, err := Get(st, h); err != nil || r != nil {
		t.Fatalf("empty store: %+v %v", r, err)
	}
	when := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	in := &Record{
		Hash: h, Status: StatusOK, Model: "qwen2.5vl:7b", EndpointID: "node-a", PromptVersion: PromptVersion, ReadAt: when, Attempts: 1,
		Raw: "{\"title\":\"The Book\"}",
		Text: &Text{Title: "The Book", Subtitle: "A Novel", Authors: []string{"A. Writer"}, Narrators: []string{"N. Reader"},
			Series: "Saga", SeriesNumber: "2", Publisher: "Pub", OtherText: []string{"Unabridged"}},
	}
	if err := Put(st, in); err != nil {
		t.Fatal(err)
	}
	got, err := Get(st, h)
	if err != nil || got == nil {
		t.Fatalf("Get: %+v %v", got, err)
	}
	if got.Text.Title != "The Book" || got.Text.Authors[0] != "A. Writer" || got.Model != "qwen2.5vl:7b" ||
		got.PromptVersion != PromptVersion || !got.ReadAt.Equal(when) || !got.Current() {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	failed := hashOf("image-b")
	if err := Put(st, &Record{Hash: failed, Status: StatusError, Error: "timeout", PromptVersion: PromptVersion, ReadAt: when, Attempts: 2}); err != nil {
		t.Fatal(err)
	}
	if err := PutBook(st, &BookIndex{BookID: "book1", UpdatedAt: when, Images: []ImageRef{
		{Hash: h, Source: SourceFolder, Path: "/lib/a/cover.jpg"},
		{Hash: failed, Source: SourceEmbedded},
		{Hash: hashOf("image-c"), Source: SourceLocal},
	}}); err != nil {
		t.Fatal(err)
	}
	joined, err := ForBook(st, "book1")
	if err != nil || len(joined) != 3 {
		t.Fatalf("ForBook: %d %v", len(joined), err)
	}
	if !joined[0].Record.Current() || joined[1].Record.Status != StatusError || joined[1].Record.Current() || joined[2].Record != nil {
		t.Fatalf("joined states wrong: %+v", joined)
	}
	if none, err := ForBook(st, "nobody"); err != nil || none != nil {
		t.Fatalf("unindexed book: %v %v", none, err)
	}
}

func TestRejectsBadKeys(t *testing.T) {
	st, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := Get(st, "../etc"); err == nil {
		t.Fatal("invalid hash accepted")
	}
	if err := Put(st, &Record{Hash: "short"}); err == nil {
		t.Fatal("invalid record hash accepted")
	}
	if _, err := GetBook(st, "a:b"); err == nil {
		t.Fatal("invalid book id accepted")
	}
}
