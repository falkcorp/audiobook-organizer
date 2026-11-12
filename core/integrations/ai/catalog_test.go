// file: core/integrations/ai/catalog_test.go
// version: 1.0.0
// guid: 91c4e7a2-6b38-4d15-a0f9-3e8d5c2b7a46
// last-edited: 2026-11-12

package ai_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations/ai"
)

// A module registers capabilities under its own ID only: a spec whose prefix is
// another module's, or that breaks any registration rule, is refused with the
// zero handle, and nothing is added after the catalog is frozen.
func TestCatalog_ForeignPrefixRejected(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	bad := []struct {
		name string
		mod  string
		mut  func(*ai.CapabilitySpec)
	}{
		{"another module's prefix", "gotham", func(s *ai.CapabilitySpec) { s.ID = "arkham.riddle" }},
		{"a prefix that merely starts the same", "gotham", func(s *ai.CapabilitySpec) { s.ID = "gothamcity.riddle" }},
		{"no name", "gotham", func(s *ai.CapabilitySpec) { s.ID = "gotham" }},
		{"an empty name", "gotham", func(s *ai.CapabilitySpec) { s.ID = "gotham." }},
		{"an upper-case name", "gotham", func(s *ai.CapabilitySpec) { s.ID = "gotham.Riddle" }},
		{"a wildcard name", "gotham", func(s *ai.CapabilitySpec) { s.ID = "gotham.*" }},
		{"a dotted name", "gotham", func(s *ai.CapabilitySpec) { s.ID = "gotham.riddle.v2" }},
		{"no registering module", "", func(s *ai.CapabilitySpec) {}},
		{"an unknown kind", "gotham", func(s *ai.CapabilitySpec) { s.Kind = "telepathy" }},
		{"an unknown required feature", "gotham", func(s *ai.CapabilitySpec) { s.RequiredFeatures = []ai.Feature{"x-ray"} }},
		{"an unknown optional feature", "gotham", func(s *ai.CapabilitySpec) { s.OptionalFeatures = []ai.Feature{"x-ray"} }},
		{"vision required of embedding work", "gotham", func(s *ai.CapabilitySpec) {
			s.Kind, s.RequiredFeatures = ai.KindEmbed, []ai.Feature{ai.FeatureVision}
		}},
		{"a missing description", "gotham", func(s *ai.CapabilitySpec) { s.Description = " " }},
		{"a missing data-sent disclosure", "gotham", func(s *ai.CapabilitySpec) { s.DataSent = "" }},
		{"a missing typical request", "gotham", func(s *ai.CapabilitySpec) { s.TypicalRequest = "" }},
		{"a negative timeout", "gotham", func(s *ai.CapabilitySpec) { s.AttemptTimeout = -time.Second }},
		{"an unknown locality floor", "gotham", func(s *ai.CapabilitySpec) { s.Locality = 7 }},
	}
	for _, tc := range bad {
		spec := capSpec("riddle")
		tc.mut(&spec)
		c, err := h.r.RegisterCapability(tc.mod, spec)
		if !errors.Is(err, ai.ErrInvalidCapability) {
			t.Errorf("%s: err = %v, want ErrInvalidCapability", tc.name, err)
		}
		if !c.IsZero() {
			t.Errorf("%s: got a non-zero handle on error", tc.name)
		}
	}
	if n := len(slices.Collect(h.r.Catalog())); n != 0 {
		t.Fatalf("%d capabilities registered by refused specs", n)
	}

	c := h.capability("riddle")
	if c.IsZero() || c.ID() != "gotham.riddle" {
		t.Fatalf("handle = %+v", c)
	}
	if _, err := h.r.RegisterCapability("gotham", capSpec("riddle")); !errors.Is(err, ai.ErrInvalidCapability) {
		t.Errorf("a duplicate = %v, want ErrInvalidCapability", err)
	}
	h.r.FreezeCatalog()
	h.r.FreezeCatalog() // idempotent
	if _, err := h.r.RegisterCapability("gotham", capSpec("cipher")); !errors.Is(err, ai.ErrCatalogFrozen) {
		t.Errorf("after the freeze = %v, want ErrCatalogFrozen", err)
	}
	// Service is unaffected by the freeze.
	f := fake()
	f.ScriptText(coretest.FakeAIStep{Text: "ok"})
	h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle"), f)
	if _, err := h.r.Generate(context.Background(), c, text("who?")); err != nil {
		t.Errorf("a frozen catalog refused a registered capability: %v", err)
	}
}

// Lookup and the catalog speak catalog IDs only: a display text, a bare name, a
// wildcard or a differently cased ID is not a capability.
func TestCatalog_LookupCatalogIDsOnly(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.capability("riddle")
	h.capability("cipher", func(s *ai.CapabilitySpec) { s.Kind = ai.KindEmbed })
	h.capability("sonata", func(s *ai.CapabilitySpec) { s.Kind = ai.KindTranscribe })

	c, spec, ok := h.r.Lookup("gotham.riddle")
	if !ok || c.ID() != "gotham.riddle" || spec.Description == "" {
		t.Fatalf("Lookup(gotham.riddle) = %+v, %+v, %v", c, spec, ok)
	}
	for _, id := range []string{"riddle", "gotham.*", "GOTHAM.RIDDLE", "gotham.Riddle", "Names the villain a riddle points at.", "", "gotham.", ".riddle", "gotham.riddle "} {
		if _, _, ok := h.r.Lookup(id); ok {
			t.Errorf("Lookup(%q) resolved; only catalog IDs do", id)
		}
		if h.r.HasCapability(id) {
			t.Errorf("HasCapability(%q) = true", id)
		}
	}
	var ids []string
	timeouts := map[string]time.Duration{}
	for s := range h.r.Catalog() {
		ids = append(ids, s.ID)
		timeouts[s.ID] = s.AttemptTimeout
	}
	if !slices.Equal(ids, []string{"gotham.cipher", "gotham.riddle", "gotham.sonata"}) {
		t.Errorf("Catalog order = %v, want ID order", ids)
	}
	want := map[string]time.Duration{"gotham.riddle": 120 * time.Second, "gotham.cipher": 60 * time.Second, "gotham.sonata": 30 * time.Minute}
	for id, d := range want {
		if timeouts[id] != d {
			t.Errorf("%s default attempt timeout = %v, want %v", id, timeouts[id], d)
		}
	}
	// The yielded spec is a copy: editing it does not edit the catalog.
	for s := range h.r.Catalog() {
		s.RequiredFeatures = append(s.RequiredFeatures, ai.FeatureVision)
		s.Description = "changed"
	}
	if _, spec, _ := h.r.Lookup("gotham.riddle"); spec.Description == "changed" || len(spec.RequiredFeatures) != 0 {
		t.Error("a caller edited the catalog through the spec Catalog yielded")
	}
}

// A stored tick for a capability the catalog does not know (a disabled module,
// a binary without it) is kept and ignored, not stripped and not an error; a
// PUT cannot add one.
func TestCatalog_UnknownTickKept(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	c := h.capability("riddle")
	f := fake()
	f.ScriptText(coretest.FakeAIStep{Text: "ok"})
	ref := h.addEndpoint("arkham", 1, textEndpoint("joker-7b", "gotham.riddle", "retired.capability"), f)

	// Ignored: the row still serves what it ticks that the catalog knows.
	if _, err := h.r.Generate(context.Background(), c, text("who?")); err != nil {
		t.Fatalf("an unknown tick broke the known one: %v", err)
	}
	// Kept: the stored row still carries it.
	cfg, ok := h.reg.Config(ref)
	if !ok {
		t.Fatal("no stored instance")
	}
	caps, _ := cfg.Settings["capabilities"].([]any)
	if len(caps) != 2 || caps[1] != "retired.capability" {
		t.Errorf("stored ticks = %v; the unknown one must be kept", caps)
	}
	// A PUT that keeps the stored unknown tick is fine; one that adds another is not.
	stored := cfg.Settings
	keep := settingsMap(t, textEndpoint("joker-7b", "gotham.riddle", "retired.capability"))
	if err := h.r.ValidateEndpoint(keep, stored); err != nil {
		t.Errorf("re-saving a row that carried an unknown tick: %v", err)
	}
	add := settingsMap(t, textEndpoint("joker-7b", "gotham.riddle", "retired.capability", "other.unknown"))
	err := h.r.ValidateEndpoint(add, stored)
	if !errors.Is(err, ai.ErrInvalidEndpoint) || !errors.Is(err, ai.ErrUnknownCapability) {
		t.Errorf("adding an unknown tick = %v, want ErrInvalidEndpoint wrapping ErrUnknownCapability", err)
	}
	if err := h.r.ValidateEndpoint(keep, nil); !errors.Is(err, ai.ErrUnknownCapability) {
		t.Errorf("a create with an unknown tick = %v, want ErrUnknownCapability", err)
	}
	// Once the module is back the kept tick is live again, with no edit to the row.
	spec := capSpec("capability")
	spec.ID = "retired.capability"
	back, err := h.r.RegisterCapability("retired", spec)
	if err != nil {
		t.Fatal(err)
	}
	cands, _, err := h.r.Candidates(back)
	if err != nil || len(cands) != 1 || cands[0].Ref != ref {
		t.Errorf("after the module returned: candidates = %+v, %v; want the row that kept the tick", cands, err)
	}
}
