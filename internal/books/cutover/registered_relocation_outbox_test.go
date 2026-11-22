// file: internal/books/cutover/registered_relocation_outbox_test.go
// version: 0.1.0
// guid: 3c9e7a52-1d84-4b60-a2f5-7e0b6d4c9a18
// last-edited: 2026-11-22

package cutover_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// TestRegisteredHarness_RelocationIsStagedByTheBootedModule (F6-03): on the
// registered build books' Register installs the relocation outbox before the
// store takes writes (store.SetRelocationOutbox, internal/books/module.go), so
// an organized copy (CreateBookWithRelocation, the single-book organize
// request's write) leaves a message for books' relocation consumer, which the
// operator route sees as backlog on that consumer (intake_backlog: staged, not
// yet sequenced). The store's own tests prove the staging with an outbox they
// install themselves; only a booted module can show that Register did.
//
// Nothing else stages: a create, and a path change of a stored book (the
// organize-in-place, repair, scanner and version-swap writers), leave nothing,
// even with the outbox installed, as those writers moved no torrent at base. The
// harness boots no executor, so the relay never runs and the message stays in
// the intake whatever F5-18 registers: the cell reads staging, not delivery.
// Delivery and the crash-restart half are TestRelocationFollowUpDurable's, once
// F5-18 registers books' task kinds.
func TestRegisteredHarness_RelocationIsStagedByTheBootedModule(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	admin := h.Principal("key:admin:every")
	if admin == nil {
		t.Fatal("the harness has no admin key")
	}
	backlog := func() int {
		t.Helper()
		res := h.MustDo(t, admin, "GET", "/api/core/v1/outbox/consumers", nil)
		var body struct {
			Items []struct {
				Name     string `json:"name"`
				Topic    string `json:"topic"`
				Pending  int    `json:"pending"`
				Retrying int    `json:"retrying"`
				Dead     int    `json:"dead"`
				Intake   int    `json:"intake_backlog"`
			} `json:"items"`
		}
		if err := json.Unmarshal(res.Body, &body); err != nil {
			t.Fatalf("outbox consumers: %v\n%s", err, res.Body)
		}
		for _, c := range body.Items {
			if c.Topic == store.BookRelocatedTopicName {
				return c.Pending + c.Retrying + c.Dead + c.Intake
			}
		}
		t.Fatalf("no consumer of %s in %s", store.BookRelocatedTopicName, res.Body)
		return 0
	}

	s := store.GetGlobalStore()
	b, err := s.CreateBook(&store.Book{Title: "Year One", FilePath: "/library/year-one/year-one.m4b", Format: "m4b"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // a create would have been relayed by now
	if n := backlog(); n != 0 {
		t.Fatalf("a create left %d relocation message(s), want 0", n)
	}
	if _, err := s.UpdateBook(b.ID, &store.Book{Title: b.Title, Format: "m4b", FilePath: "/library/batman/year-one/year-one.m4b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ModifyBook(b.ID, func(bk *store.Book) error { bk.FilePath = "/library/batman/year-one-2/year-one.m4b"; return nil }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if n := backlog(); n != 0 {
		t.Fatalf("path changes of a stored book left %d relocation message(s), want 0: only a copy-organize stages", n)
	}
	if _, err := s.CreateBookWithRelocation(&store.Book{Title: b.Title, Format: "m4b", FilePath: "/library/batman/year-one-copy/year-one.m4b"}, b.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for backlog() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("an organized copy left no relocation message: Register did not install the relocation outbox")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
