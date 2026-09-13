// file: internal/aidispatch/capabilities.go
// version: 1.0.0
// guid: 0db30d05-394d-4291-bc11-e466dd168c73
// last-edited: 2026-09-13

// Package aidispatch routes every kind of AI work to the endpoints that are
// explicitly allowed to do it.
//
// Each kind of work is a Capability. Each endpoint carries an explicit list of
// the capabilities it accepts, and the list is default-deny: an endpoint with
// no list accepts nothing, and a capability registered tomorrow starts with
// zero capable endpoints. That is the whole point of the package: a new
// feature cannot flood a server with work it was never ticked for.
package aidispatch

import (
	"fmt"
	"slices"
	"sync"
)

// Kind is the family of work a capability belongs to. It decides which
// endpoint protocols can serve it and which model field is consulted.
type Kind string

const (
	KindChat    Kind = "chat"
	KindEmbed   Kind = "embed"
	KindWhisper Kind = "whisper"
)

// Endpoint features a capability can require. They are properties of the
// endpoint, not kinds of work: an endpoint is ticked for a capability AND must
// also have every feature the capability requires.
const (
	FeatureBatchAPI = "batch_api"
	FeatureVision   = "vision"
	// FeatureSharedFSPrefix + root_id marks an endpoint that has proved it can
	// read that library root from its own mount (PLAN section 3b). It is a
	// MEASURED feature, set by probe verification in a later PR. It is never a
	// hard requirement of any capability -- a capability that can use it lists
	// it in OptionalFeatures, and delivery falls back to upload without it.
	FeatureSharedFSPrefix = "shared_fs:"
)

// SharedFSFeature returns the feature string for one library root.
func SharedFSFeature(rootID string) string { return FeatureSharedFSPrefix + rootID }

// Capability identifies one kind of AI work.
//
// It is a struct with an unexported field so that no code outside this package
// can conjure one from a string: a plain string does not compile where a
// Capability is expected, and the only values in existence are the ones this
// file registers. Code that receives an ID from outside (stored settings, a
// PUT body) goes through ParseCapability, which answers "is this registered?".
type Capability struct {
	id string
}

// ID returns the stable string form, e.g. "llm.filename_parse".
func (c Capability) ID() string { return c.id }

// String implements fmt.Stringer.
func (c Capability) String() string { return c.id }

// IsZero reports whether c is the zero value, which is never registered.
func (c Capability) IsZero() bool { return c.id == "" }

// Spec is the registry entry for one capability.
type Spec struct {
	Capability Capability
	Kind       Kind
	// RequiredFeatures must ALL be present on an endpoint (contains-all, never
	// contains-any). Only static endpoint features belong here. Selection rules
	// that depend on config -- the whisper_requires label set, embedding model
	// equality -- are enforced by the dispatcher and described in Description,
	// never encoded as feature strings, because a feature string nothing ever
	// sets would make the capability fail closed forever.
	RequiredFeatures []string
	// OptionalFeatures change HOW the work is delivered when present, never
	// WHETHER it can run (e.g. shared_fs: send a path instead of the bytes).
	OptionalFeatures []string
	Description      string
	// DataSent says, in words an operator can judge, what leaves this process.
	DataSent       string
	TypicalRequest string
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Spec{}
	// registryOrder keeps registration order so listings are stable.
	registryOrder []string
)

// newCapability registers a capability and returns its handle. Unexported on
// purpose: adding a capability is an edit to this file, in review, never a
// runtime act by a caller. It panics on a duplicate or malformed entry because
// both are programming errors caught the moment the package initialises.
func newCapability(spec Spec, id string) Capability {
	c := Capability{id: id}
	spec.Capability = c
	if err := validateSpec(spec); err != nil {
		panic(fmt.Sprintf("aidispatch: invalid capability %q: %v", id, err))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[id]; dup {
		panic(fmt.Sprintf("aidispatch: capability %q registered twice", id))
	}
	registry[id] = spec
	registryOrder = append(registryOrder, id)
	return c
}

func validateSpec(s Spec) error {
	switch {
	case s.Capability.id == "":
		return fmt.Errorf("empty id")
	case s.Kind != KindChat && s.Kind != KindEmbed && s.Kind != KindWhisper:
		return fmt.Errorf("unknown kind %q", s.Kind)
	case s.Description == "":
		return fmt.Errorf("missing Description")
	case s.DataSent == "":
		return fmt.Errorf("missing DataSent")
	case s.TypicalRequest == "":
		return fmt.Errorf("missing TypicalRequest")
	}
	return nil
}

// The 13 capabilities of PLAN section 2. Call-site numbers refer to the
// section 1 table.
var (
	// Call sites 1 and 3.
	LLMFilenameParse = newCapability(Spec{
		Kind:           KindChat,
		Description:    "Parse author, title, series and narrator out of audiobook filenames.",
		DataSent:       "Filenames and folder names only.",
		TypicalRequest: "1 filename (handler) or 8 filenames per batch (scan).",
	}, "llm.filename_parse")

	// Call site 2.
	LLMAudiobookParse = newCapability(Spec{
		Kind:           KindChat,
		Description:    "Parse one audiobook's metadata using its filename plus embedded tags as context.",
		DataSent:       "One filename and that file's embedded tag values.",
		TypicalRequest: "1 audiobook.",
	}, "llm.audiobook_parse")

	// Call site 4.
	LLMCoverArtVision = newCapability(Spec{
		Kind:             KindChat,
		RequiredFeatures: []string{FeatureVision},
		Description:      "Read title and author from cover art with a vision model.",
		DataSent:         "The full cover image, base64-encoded.",
		TypicalRequest:   "1 image, up to several megabytes.",
	}, "llm.cover_art_vision")

	// Call site 5.
	LLMAuthorReview = newCapability(Spec{
		Kind:           KindChat,
		Description:    "Review candidate groups of duplicate author names.",
		DataSent:       "Author names, grouped.",
		TypicalRequest: "A few groups of author names.",
	}, "llm.author_review")

	// Call site 6.
	LLMAuthorDiscovery = newCapability(Spec{
		Kind:           KindChat,
		Description:    "Discover duplicate authors in a list of author names.",
		DataSent:       "Lists of author names.",
		TypicalRequest: "One page of author names.",
	}, "llm.author_discovery")

	// Call site 7.
	LLMMetadataRerank = newCapability(Spec{
		Kind:           KindChat,
		Description:    "Rerank metadata-source candidates for a book.",
		DataSent:       "The book's current metadata and the candidate records.",
		TypicalRequest: "Up to 25 candidates.",
	}, "llm.metadata_rerank")

	// Call site 8.
	LLMDedupReview = newCapability(Spec{
		Kind:             KindChat,
		RequiredFeatures: []string{FeatureBatchAPI},
		Description:      "LLM review of duplicate-book candidate pairs, submitted through a Batch API.",
		DataSent:         "Metadata for pairs of books, as a JSONL file.",
		TypicalRequest:   "One JSONL file of book pairs.",
	}, "llm.dedup_review")

	// Call sites 9 and 10.
	LLMAuthorDedupBatch = newCapability(Spec{
		Kind:             KindChat,
		RequiredFeatures: []string{FeatureBatchAPI},
		Description:      "Author dedup and author review submitted through a Batch API.",
		DataSent:         "Author names and groups, as a JSONL file.",
		TypicalRequest:   "One JSONL file.",
	}, "llm.author_dedup_batch")

	// Call site 11.
	LLMDiagnostics = newCapability(Spec{
		Kind:             KindChat,
		RequiredFeatures: []string{FeatureBatchAPI},
		Description:      "Diagnostics analysis submitted through a Batch API.",
		DataSent:         "Library slices, log excerpts and operation records: the most sensitive data this program sends.",
		TypicalRequest:   "One JSONL file.",
	}, "llm.diagnostics")

	// Call site 13. Failover is restricted to endpoints serving the SAME
	// embedding model: vectors from different models are not comparable, and
	// mixing them corrupts every similarity score computed afterwards.
	EmbedText = newCapability(Spec{
		Kind:           KindEmbed,
		Description:    "Embed text for similarity search and dedup. Failover only to endpoints with the same embedding model.",
		DataSent:       "Book titles, authors and descriptions as plain text.",
		TypicalRequest: "64 to 512 texts.",
	}, "embed.text")

	// Call site 14.
	EmbedTextBatch = newCapability(Spec{
		Kind:             KindEmbed,
		RequiredFeatures: []string{FeatureBatchAPI},
		Description:      "Embed text through a Batch API.",
		DataSent:         "Book titles, authors and descriptions, as a JSONL file.",
		TypicalRequest:   "One JSONL file.",
	}, "embed.text_batch")

	// Call site 15. The whisper_requires label set is a config-driven
	// selection rule (WithRequiredLabels), not a static feature.
	TranscribeBatch = newCapability(Spec{
		Kind:             KindWhisper,
		OptionalFeatures: []string{FeatureSharedFSPrefix + "<root_id>"},
		Description:      "Batch speech-to-text for the opening of audio files. The endpoint must also carry every whisper_requires label.",
		DataSent:         "WAV audio cut from library files. With shared_fs, no audio is sent: the endpoint reads files directly from the library mount.",
		TypicalRequest:   "About 46 MB of WAV per request.",
	}, "transcribe.batch")

	// Call site 16.
	TranscribeIntroClip = newCapability(Spec{
		Kind:             KindWhisper,
		OptionalFeatures: []string{FeatureSharedFSPrefix + "<root_id>"},
		Description:      "Transcribe the first 30 seconds of a single file.",
		DataSent:         "One WAV clip of about 1 MB. With shared_fs, no audio is sent: the endpoint reads files directly from the library mount.",
		TypicalRequest:   "1 WAV of about 1 MB.",
	}, "transcribe.intro_clip")
)

// MigrationBaseline is the FROZEN set of capabilities the first-boot migration
// (PLAN section 5, PR 2) may tick on the rows it creates. It is exactly the set
// that exists on the day routing moved to the dispatcher, because those are the
// ones with an existing route to preserve.
//
// 🔴 Never add to this list. A capability registered after the migration has no
// historical route; putting it here would tick it on existing servers without
// anyone deciding to, which is the precise failure default-deny exists to stop.
// capabilities_test.go pins this list as a golden value.
var MigrationBaseline = []Capability{
	LLMFilenameParse,
	LLMAudiobookParse,
	LLMCoverArtVision,
	LLMAuthorReview,
	LLMAuthorDiscovery,
	LLMMetadataRerank,
	LLMDedupReview,
	LLMAuthorDedupBatch,
	LLMDiagnostics,
	EmbedText,
	EmbedTextBatch,
	TranscribeBatch,
	TranscribeIntroClip,
}

// InMigrationBaseline reports whether the migration may tick c.
func InMigrationBaseline(c Capability) bool {
	return slices.Contains(MigrationBaseline, c)
}

// Lookup returns the registry entry for c.
func Lookup(c Capability) (Spec, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	s, ok := registry[c.id]
	return s, ok
}

// ParseCapability resolves a stored or submitted ID. ok is false for an ID
// that is not registered: stored settings keep such IDs and ignore them (so a
// rollback is safe), and a PUT that contains one is rejected.
func ParseCapability(id string) (Capability, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	s, ok := registry[id]
	return s.Capability, ok
}

// Registry returns every registered capability in registration order.
func Registry() []Spec {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Spec, 0, len(registryOrder))
	for _, id := range registryOrder {
		out = append(out, registry[id])
	}
	return out
}
