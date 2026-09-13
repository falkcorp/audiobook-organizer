// file: internal/aidispatch/capabilities_test.go
// version: 1.0.0
// guid: 00d9da8c-5dca-4cb5-827d-3ff1d368be5a
// last-edited: 2026-09-13

package aidispatch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// testOnlyNewCap stands in for "a capability someone registers tomorrow". It
// is registered once per test binary (package-level var, so -count=N does not
// re-register and panic). The "test." prefix keeps it out of the golden lists.
var testOnlyNewCap = newCapability(Spec{
	Kind:           KindChat,
	Description:    "test-only capability",
	DataSent:       "nothing",
	TypicalRequest: "none",
}, "test.brand_new_capability")

func productionRegistry() []Spec {
	var out []Spec
	for _, s := range Registry() {
		if !strings.HasPrefix(s.Capability.ID(), "test.") {
			out = append(out, s)
		}
	}
	return out
}

// The 13 capabilities of PLAN section 2, in registration order.
var wantCapabilityIDs = []string{
	"llm.filename_parse",
	"llm.audiobook_parse",
	"llm.cover_art_vision",
	"llm.author_review",
	"llm.author_discovery",
	"llm.metadata_rerank",
	"llm.dedup_review",
	"llm.author_dedup_batch",
	"llm.diagnostics",
	"embed.text",
	"embed.text_batch",
	"transcribe.batch",
	"transcribe.intro_clip",
}

func TestRegistry_HoldsExactlyThePlannedCapabilities(t *testing.T) {
	var got []string
	for _, s := range productionRegistry() {
		got = append(got, s.Capability.ID())
	}
	if !slices.Equal(got, wantCapabilityIDs) {
		t.Fatalf("registry drifted from PLAN section 2:\n got  %v\n want %v", got, wantCapabilityIDs)
	}
}

func TestRegistry_EveryEntryIsComplete(t *testing.T) {
	kindByPrefix := map[string]Kind{"llm.": KindChat, "embed.": KindEmbed, "transcribe.": KindWhisper}
	for _, s := range productionRegistry() {
		id := s.Capability.ID()
		if s.Description == "" || s.DataSent == "" || s.TypicalRequest == "" {
			t.Errorf("%s: Description, DataSent and TypicalRequest are all required", id)
		}
		var want Kind
		for p, k := range kindByPrefix {
			if strings.HasPrefix(id, p) {
				want = k
			}
		}
		if s.Kind != want {
			t.Errorf("%s: kind %q, want %q from its prefix", id, s.Kind, want)
		}
	}
}

// Section 2's "endpoint must also have" column. embed.text and
// transcribe.batch must have NONE: their extra rules (same embedding model,
// whisper_requires) are config-driven selection rules, and a feature string
// nothing sets would fail them closed forever.
func TestRegistry_RequiredFeatures(t *testing.T) {
	want := map[string][]string{
		"llm.cover_art_vision":   {FeatureVision},
		"llm.dedup_review":       {FeatureBatchAPI},
		"llm.author_dedup_batch": {FeatureBatchAPI},
		"llm.diagnostics":        {FeatureBatchAPI},
		"embed.text_batch":       {FeatureBatchAPI},
	}
	for _, s := range productionRegistry() {
		if got := s.RequiredFeatures; !slices.Equal(got, want[s.Capability.ID()]) {
			t.Errorf("%s: RequiredFeatures %v, want %v", s.Capability.ID(), got, want[s.Capability.ID()])
		}
		for _, f := range s.RequiredFeatures {
			if strings.HasPrefix(f, FeatureSharedFSPrefix) {
				t.Errorf("%s: shared_fs must never be required, only optional", s.Capability.ID())
			}
		}
	}
}

// Section 3b: the transcribe capabilities declare shared_fs as optional, and
// their DataSent says so in words an operator can judge.
func TestRegistry_TranscribeDeclaresSharedFS(t *testing.T) {
	for _, c := range []Capability{TranscribeBatch, TranscribeIntroClip} {
		s, _ := Lookup(c)
		if !slices.ContainsFunc(s.OptionalFeatures, func(f string) bool { return strings.HasPrefix(f, FeatureSharedFSPrefix) }) {
			t.Errorf("%s: missing optional shared_fs feature", c)
		}
		if !strings.Contains(s.DataSent, "reads files directly from the library mount") {
			t.Errorf("%s: DataSent must disclose direct mount reads, got %q", c, s.DataSent)
		}
	}
}

// Golden: the frozen list the PR 2 migration may tick. A failure here means
// someone added a capability to the baseline, which would tick it on existing
// servers without anyone deciding to.
func TestMigrationBaseline_Golden(t *testing.T) {
	var got []string
	for _, c := range MigrationBaseline {
		got = append(got, c.ID())
	}
	if !slices.Equal(got, wantCapabilityIDs) {
		t.Fatalf("MigrationBaseline is frozen; never add to it.\n got  %v\n want %v", got, wantCapabilityIDs)
	}
	for _, c := range MigrationBaseline {
		if _, ok := Lookup(c); !ok {
			t.Errorf("baseline entry %s is not registered", c)
		}
	}
	if InMigrationBaseline(testOnlyNewCap) {
		t.Fatal("a newly registered capability must not be in the migration baseline")
	}
}

// Section 7 item 4: a new capability on a config where every endpoint ticks
// every baseline capability, with every feature and label, still has zero
// capable endpoints.
func TestNewCapability_FreshConfigFailsClosed(t *testing.T) {
	var all []string
	for _, c := range MigrationBaseline {
		all = append(all, c.ID())
	}
	eps := []Endpoint{
		{ID: "local-llm", Protocol: ProtocolOpenAICompat, ChatModel: "m", EmbedModel: "e", Enabled: true,
			Capabilities: all, Features: []string{FeatureBatchAPI, FeatureVision}, Labels: []string{"gpu", "local"}},
		{ID: "openai", Protocol: ProtocolOpenAICompat, ChatModel: "m", EmbedModel: "e", Enabled: true,
			Capabilities: all, Features: []string{FeatureBatchAPI, FeatureVision}},
	}
	d := New(eps, WithSlots(NewSlots()), WithHealth(NewHealth()))
	called := false
	_, err := Call(context.Background(), d, testOnlyNewCap, func(context.Context, Target) (int, error) {
		called = true
		return 0, nil
	})
	if !errors.Is(err, ErrNoCapableEndpoint) {
		t.Fatalf("want ErrNoCapableEndpoint, got %v", err)
	}
	if called {
		t.Fatal("fn ran for a capability no endpoint ticked")
	}
	var nce *NoCapableEndpointError
	if !errors.As(err, &nce) || len(nce.Refusals) != len(eps) {
		t.Fatalf("want one refusal per endpoint (%d), got %+v", len(eps), nce)
	}
	if d.Capacity(testOnlyNewCap) != 0 {
		t.Fatal("Capacity of an unticked capability must be 0")
	}
}

func TestParseCapability(t *testing.T) {
	if c, ok := ParseCapability("llm.filename_parse"); !ok || c != LLMFilenameParse {
		t.Fatalf("known ID not resolved: %v %v", c, ok)
	}
	for _, id := range []string{"", "*", "llm.*", "llm.nope"} {
		if _, ok := ParseCapability(id); ok {
			t.Errorf("%q must not resolve", id)
		}
	}
}

func TestNewCapability_RejectsBadEntries(t *testing.T) {
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: expected panic", name)
			}
		}()
		f()
	}
	mustPanic("duplicate", func() {
		newCapability(Spec{Kind: KindChat, Description: "d", DataSent: "d", TypicalRequest: "d"}, "llm.filename_parse")
	})
	mustPanic("no description", func() {
		newCapability(Spec{Kind: KindChat, DataSent: "d", TypicalRequest: "d"}, "test.no_description")
	})
	mustPanic("bad kind", func() {
		newCapability(Spec{Kind: "vision", Description: "d", DataSent: "d", TypicalRequest: "d"}, "test.bad_kind")
	})
}
