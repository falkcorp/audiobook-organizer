// file: internal/books/topics/topics_test.go
// version: 0.1.0
// guid: 836c3b4f-9ea8-4d82-94da-16a8c532d3ee
// last-edited: 2026-11-20

package topics_test

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/topics"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/integrations/notify"
	"github.com/falkcorp/mediacore/rbac"
)

// wantTopics is 05-notify section 10's map, written out: a change to the spec's
// row is a failing test here and not a drifted constant.
var wantTopics = map[string]string{
	"book.imported":       "books.book.imported.v1",
	"book.deleted":        "books.book.deleted.v1",
	"metadata.applied":    "books.metadata.applied.v1",
	"tags.written":        "books.tags.written.v1",
	"file.organized":      "books.file.organized.v1",
	"dedup.detected":      "books.dedup.detected.v1",
	"dedup.merged":        "books.dedup.merged.v1",
	"cover.changed":       "books.cover.changed.v1",
	"read_status.changed": "books.read-status.changed.v1",
	"scan.completed":      "books.scan.completed.v1",
	"book.quarantined":    "books.book.quarantined.v1",
	"book.unquarantined":  "books.book.unquarantined.v1",
}

func TestHEADEvents_IsTheSpecMap(t *testing.T) {
	if got := topics.HEADEvents(); !maps.Equal(got, wantTopics) {
		t.Fatalf("map\n got %v\nwant %v", got, wantTopics)
	}
}

// The catalog holds exactly the twelve topics of the map, in HEAD's event order,
// each once.
func TestSpecs_TwelveTopicsOfTheMap(t *testing.T) {
	order := []string{
		"book.imported", "book.deleted", "metadata.applied", "tags.written", "file.organized", "dedup.detected",
		"dedup.merged", "cover.changed", "read_status.changed", "scan.completed", "book.quarantined", "book.unquarantined",
	}
	var want, got []string
	for _, e := range order {
		want = append(want, wantTopics[e])
	}
	for _, s := range topics.Specs() {
		got = append(got, s.Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("catalog\n got %v\nwant %v", got, want)
	}
}

// Declared through the real events catalog as module books, every entry is
// accepted, and each answers the questions notify and rbac ask of it: a schema,
// notify metadata whose severity core's notify parses, and a permission in
// books' own namespace.
func TestSpecs_DeclareThroughTheRealCatalog(t *testing.T) {
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare(topics.Module, topics.Specs()...); err != nil {
		t.Fatalf("Declare: %v", err)
	}
	for _, s := range topics.Specs() {
		if len(s.Schema) == 0 || !json.Valid(s.Schema) {
			t.Errorf("%s: no usable payload schema", s.Name)
		}
		if s.Notify == nil || s.Notify.Title == "" {
			t.Errorf("%s: no notify title", s.Name)
			continue
		}
		if _, ok := notify.ParseSeverity(s.Notify.DefaultSeverity); !ok {
			t.Errorf("%s: default severity %q is not one core's notify knows", s.Name, s.Notify.DefaultSeverity)
		}
		if s.Durable != nil || s.Realtime != nil {
			t.Errorf("%s: a notifiable bus topic carries no Durable or Realtime", s.Name)
		}
		if !reflect.DeepEqual(s.Permission, rbac.Need("books:view")) {
			t.Errorf("%s: permission is not books:view", s.Name)
		}
		if s.PayloadType() == nil {
			t.Errorf("%s: no payload type, so a typed publisher cannot be checked against it", s.Name)
		}
	}
}

// A topic declared twice, or under another module, is refused: the entries carry
// their own namespace, so this proves Declare reads the names and not a module
// argument the test chose.
func TestSpecs_RefusedUnderAnotherModule(t *testing.T) {
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("gotham", topics.Specs()...); err == nil {
		t.Fatal("books' topics were accepted under module gotham")
	}
}

// Only a quarantine warns by default.
func TestSpecs_OnlyQuarantineWarns(t *testing.T) {
	for _, s := range topics.Specs() {
		want := "info"
		if s.Name == topics.BookQuarantinedName {
			want = "warning"
		}
		if s.Notify.DefaultSeverity != want {
			t.Errorf("%s: default severity %q, want %q", s.Name, s.Notify.DefaultSeverity, want)
		}
	}
}

// The payloads carry the keys HEAD's publishers put in an event's Data map, under
// the same names, so a receiver of the old webhook finds each field it read.
func TestPayloads_CarryHEADKeys(t *testing.T) {
	keys := func(v any) []string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return slices.Sorted(maps.Keys(m))
	}
	cases := []struct {
		name string
		v    any
		want []string
	}{
		// internal/quarantine/service.go:262 and :402.
		{"quarantined", topics.BookQuarantinedV1{}, []string{"author", "book_id", "file_path", "original_path", "quarantined_at", "reason", "title"}},
		{"unquarantined", topics.BookUnquarantinedV1{}, []string{"book_id", "file_path", "quarantine_path"}},
		// handlers/audiobooks/handler_crud.go:367.
		{"deleted", topics.BookDeletedV1{}, []string{"block_hash", "book_id", "soft_delete"}},
		// handlers/metadata/book_scan_lock.go:354.
		{"metadata applied", topics.MetadataAppliedV1{Message: "m"}, []string{"book_id", "message", "source"}},
		// handlers/organize.go:394 and :431 (the second adds original_book_id).
		{"organized", topics.FileOrganizedV1{OperationID: "o", OriginalBookID: "b"}, []string{"book_id", "new_path", "old_path", "operation_id", "original_book_id"}},
		// handlers/filesystem.go:361 and itunes/service/importer.go:233.
		{"imported", topics.BookImportedV1{FilePath: "p"}, []string{"book_id", "file_path", "source"}},
		// handlers/dedup/handler.go:2435 and :2491.
		{"merged", topics.DedupMergedV1{AlreadyMerged: true}, []string{"already_merged", "candidate_id", "entity_a_id", "entity_b_id", "entity_type"}},
	}
	for _, c := range cases {
		if got := keys(c.v); !slices.Equal(got, c.want) {
			t.Errorf("%s: keys %v, want %v", c.name, got, c.want)
		}
	}
}

// The optional fields are omitted when empty, as HEAD omitted the keys it did not
// set (an iTunes import sends no file_path; a plain organize no original_book_id).
func TestPayloads_OptionalFieldsOmitted(t *testing.T) {
	b, _ := json.Marshal(topics.BookImportedV1{BookID: "b", Source: "itunes"})
	if string(b) != `{"book_id":"b","source":"itunes"}` {
		t.Errorf("imported: %s", b)
	}
	b, _ = json.Marshal(topics.FileOrganizedV1{BookID: "b", OldPath: "o", NewPath: "n"})
	if string(b) != `{"book_id":"b","old_path":"o","new_path":"n"}` {
		t.Errorf("organized: %s", b)
	}
}
