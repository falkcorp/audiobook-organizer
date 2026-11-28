// file: internal/books/cutover/registered_relocation_outbox_test.go
// version: 0.1.0
// guid: 3c9e7a52-1d84-4b60-a2f5-7e0b6d4c9a18
// last-edited: 2026-11-22

package cutover_test

import (
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// TestRegisteredHarness_RelocationFollowUpWaitsForTheTasksFlip (F6-03): the
// durable relocation follow-up is one unit, the consumer that starts
// books.deluge.path-update and the staging that feeds it, and it is wired only
// with the tasks flip (books' relocationFollowUpOn), because the consumer starts
// a task run and core refuses a boot where the consumer's kind is not a
// registered definition its owner's system actor may start (a message would
// dead-letter). The registered build is the pre-flip one: books lists no
// consumer, the store has no relocation outbox, and an organized copy
// (CreateBookWithRelocation, the single-book organize request's write) commits
// and stages nothing.
//
// What it leaves to others, by design: the staging half is the store's own
// tests (book_relocated_outbox_test.go, which install an outbox themselves) and,
// booted, the cell this one replaces, which comes back with F5-18's
// registration of books.deluge.path-update (it needs the flip on and the kind
// registered, or the boot is refused); the delivery half is the deluge
// package's TestRelocationFollowUpDurable, against the real definition.
func TestRegisteredHarness_RelocationFollowUpWaitsForTheTasksFlip(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	admin := h.Principal("key:admin:every")
	if admin == nil {
		t.Fatal("the harness has no admin key")
	}
	res := h.MustDo(t, admin, "GET", "/api/core/v1/outbox/consumers", nil)
	var body struct {
		Items []struct {
			Name  string `json:"name"`
			Topic string `json:"topic"`
		} `json:"items"`
	}
	if err := json.Unmarshal(res.Body, &body); err != nil {
		t.Fatalf("outbox consumers: %v\n%s", err, res.Body)
	}
	for _, c := range body.Items {
		if c.Topic == store.BookRelocatedTopicName {
			t.Fatalf("consumer %s of %s is registered before the tasks flip: its kind is not registered, so every message would dead-letter", c.Name, c.Topic)
		}
	}

	s := store.GetGlobalStore()
	probe, ok := s.(interface{ RelocationOutboxInstalled() bool })
	if !ok {
		t.Fatalf("the booted store %T cannot report its relocation outbox", s)
	}
	if probe.RelocationOutboxInstalled() {
		t.Fatal("the relocation outbox is installed before the tasks flip: a copy would stage a message no consumer delivers")
	}
	b, err := s.CreateBook(&store.Book{Title: "Year One", FilePath: "/library/year-one/year-one.m4b", Format: "m4b"})
	if err != nil {
		t.Fatal(err)
	}
	copier, ok := s.(organizer.RelocatingCopyCreator)
	if !ok {
		t.Fatalf("the booted store %T cannot create a relocating copy", s)
	}
	if _, err := copier.CreateBookWithRelocation(&store.Book{Title: b.Title, Format: "m4b", FilePath: "/library/batman/year-one-copy/year-one.m4b"}, b.ID); err != nil {
		t.Fatalf("an organized copy before the tasks flip must commit: %v", err)
	}
}
