// file: internal/books/cutover/m6/catalog_pins_test.go
// version: 0.1.0
// guid: 5a83d0f6-12c9-4e47-8b3d-70e2f9a1c685
// last-edited: 2026-11-22

package m6

import (
	"maps"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/aicaps"
	"github.com/falkcorp/audiobook-organizer/internal/books/topics"
)

// M6's maps.go tables are frozen copies of what HEAD's data becomes (a migration
// does not follow a catalog that later changes), and F7-09 T4 owns the catalogs
// books registers with core (internal/books/aicaps, internal/books/topics). These
// pins hold the two to each other: a disagreement is a failing test here, in
// whichever file changed last, never a silent drift. They are tests and not
// imports so that M6 keeps importing no module package (TestLayering), which is
// what keeps the migration frozen. TODO-F7-09-T1-M6-CATALOG-SWITCH.

// TestCatalog_CapabilityTablesMatchBooks pins headCapabilities and capabilityID
// to aicaps: the same HEAD IDs, each mapped to the ID books registers, one for
// one, with no catalog capability left over for a tick to be dropped from.
func TestCatalog_CapabilityTablesMatchBooks(t *testing.T) {
	want := aicaps.HEADToBooks()
	if got := slices.Sorted(maps.Keys(headCapabilities)); !slices.Equal(got, slices.Sorted(maps.Keys(want))) {
		t.Errorf("headCapabilities %v, aicaps maps %v", got, slices.Sorted(maps.Keys(want)))
	}
	for head, books := range want {
		if got := capabilityID(head); got != books {
			t.Errorf("capabilityID(%q) = %q, aicaps maps it to %q", head, got, books)
		}
	}
	ids := map[string]bool{}
	for _, s := range aicaps.Specs() {
		ids[s.ID] = true
	}
	mapped := map[string]bool{}
	for head := range headCapabilities {
		mapped[capabilityID(head)] = true
	}
	if !maps.Equal(ids, mapped) {
		t.Errorf("M6 maps onto %v, books registers %v", slices.Sorted(maps.Keys(mapped)), slices.Sorted(maps.Keys(ids)))
	}
	for _, c := range legacyKeyCapabilities {
		if !headCapabilities[c] {
			t.Errorf("legacyKeyCapabilities holds %q, which is no HEAD capability", c)
		}
	}
}

// TestCatalog_JobTypesMatchBooks pins jobTypes to aicaps' job types: the same two
// HEAD types, each to the capability and apply kind books declares.
func TestCatalog_JobTypesMatchBooks(t *testing.T) {
	want := aicaps.JobTypes()
	if got := slices.Sorted(maps.Keys(jobTypes)); !slices.Equal(got, slices.Sorted(maps.Keys(want))) {
		t.Fatalf("jobTypes %v, aicaps has %v", got, slices.Sorted(maps.Keys(want)))
	}
	for typ, w := range want {
		if got := jobTypes[typ]; got.Capability != w.Capability || got.ApplyKind != w.ApplyKind {
			t.Errorf("jobTypes[%q] = %+v, aicaps declares {%s %s}", typ, got, w.Capability, w.ApplyKind)
		}
	}
}

// TestCatalog_WebhookTopicsMatchBooks pins webhookTopics to topics.HEADEvents, and
// holds every topic it names to be one the events catalog declares notifiable
// (a webhook rule is a notification rule).
func TestCatalog_WebhookTopicsMatchBooks(t *testing.T) {
	want := topics.HEADEvents()
	if !maps.Equal(webhookTopics, want) {
		t.Errorf("webhookTopics %v, topics maps %v", webhookTopics, want)
	}
	notifiable := map[string]bool{}
	for _, s := range topics.Specs() {
		if s.Notify != nil {
			notifiable[s.Name] = true
		}
	}
	for ev, topic := range webhookTopics {
		if !notifiable[topic] {
			t.Errorf("%s maps to %q, which the events catalog does not declare notifiable", ev, topic)
		}
	}
}
