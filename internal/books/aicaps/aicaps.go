// file: internal/books/aicaps/aicaps.go
// version: 0.1.0
// guid: e1b9e234-d07c-448f-ac15-07315b1db634
// last-edited: 2026-11-20

// Package aicaps is books' AI capability catalog for the integrations flip
// (F7-09): the 13 capabilities books declares through Registrar.AICapability,
// the map from HEAD's capability IDs to them, and the two batch job types that
// carry an apply kind (05-ai section 10).
//
// It is dark. Nothing registers these specs until the flip wires books' Register
// to the kernel's router; until then the package has no caller, and HEAD's
// internal/aidispatch registry stays the one in force. The specs are HEAD's
// registrations carried one to one, so the flip changes who holds the catalog and
// not what any capability asks of an endpoint.
//
// The package imports core only. Its tests import internal/aidispatch to prove the
// carry; the step that reads the map at the cutover (internal/books/cutover/m6)
// keeps its own copy until the flip pins the two together.
package aicaps

import (
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/integrations/ai"
)

// Module is books' module ID, the prefix core's catalog requires of every
// capability books registers.
const Module = "books"

// sharedFSRoot is HEAD's optional-feature placeholder for path delivery
// ("shared_fs:<root_id>", aidispatch FeatureSharedFSPrefix). core reads an
// optional feature for display only and never matches it, so the literal is
// carried as HEAD wrote it: an endpoint's measured shared_fs:<root> feature is
// what the transcription call checks, not this entry.
const sharedFSRoot = ai.FeatureSharedFS + "<root_id>"

// row is one catalog entry beside the HEAD ID it replaces.
type row struct {
	head string
	spec ai.CapabilitySpec
}

// rows is the catalog in HEAD's registration order (internal/aidispatch
// capabilities.go, "The 13 capabilities of PLAN section 2"). Kinds are core's
// names for HEAD's (chat is text, whisper is transcribe); every other field is
// HEAD's value.
//
// 05-ai's illustrative books.filename_parse example requires json_mode. HEAD's
// capability requires nothing, and json_mode is a feature an operator declares
// on an endpoint, so requiring it here would fail every existing endpoint closed
// at the flip. The carry keeps HEAD's empty requirement.
func rows() []row {
	return []row{
		{"llm.filename_parse", ai.CapabilitySpec{
			ID: "books.filename_parse", Kind: ai.KindText,
			Description:    "Parse author, title, series and narrator out of audiobook filenames.",
			DataSent:       "Filenames and folder names only.",
			TypicalRequest: "1 filename (handler) or 8 filenames per batch (scan).",
		}},
		{"llm.audiobook_parse", ai.CapabilitySpec{
			ID: "books.audiobook_parse", Kind: ai.KindText,
			Description:    "Parse one audiobook's metadata using its filename plus embedded tags as context.",
			DataSent:       "One filename and that file's embedded tag values.",
			TypicalRequest: "1 audiobook.",
		}},
		{"llm.cover_art_vision", ai.CapabilitySpec{
			ID: "books.cover_art_vision", Kind: ai.KindText, RequiredFeatures: []ai.Feature{ai.FeatureVision},
			Description:    "Read title and author from cover art with a vision model.",
			DataSent:       "The full cover image, base64-encoded.",
			TypicalRequest: "1 image, up to several megabytes.",
		}},
		{"llm.author_review", ai.CapabilitySpec{
			ID: "books.author_review", Kind: ai.KindText,
			Description:    "Review candidate groups of duplicate author names.",
			DataSent:       "Author names, grouped.",
			TypicalRequest: "A few groups of author names.",
		}},
		{"llm.author_discovery", ai.CapabilitySpec{
			ID: "books.author_discovery", Kind: ai.KindText,
			Description:    "Discover duplicate authors in a list of author names.",
			DataSent:       "Lists of author names.",
			TypicalRequest: "One page of author names.",
		}},
		{"llm.metadata_rerank", ai.CapabilitySpec{
			ID: "books.metadata_rerank", Kind: ai.KindText,
			Description:    "Rerank metadata-source candidates for a book.",
			DataSent:       "The book's current metadata and the candidate records.",
			TypicalRequest: "Up to 25 candidates.",
		}},
		{"llm.dedup_review", ai.CapabilitySpec{
			ID: "books.dedup_review", Kind: ai.KindText, RequiredFeatures: []ai.Feature{ai.FeatureBatch},
			Description:    "LLM review of duplicate-book candidate pairs, submitted through a Batch API.",
			DataSent:       "Metadata for pairs of books, as a JSONL file.",
			TypicalRequest: "One JSONL file of book pairs.",
		}},
		{"llm.author_dedup_batch", ai.CapabilitySpec{
			ID: "books.author_dedup_batch", Kind: ai.KindText, RequiredFeatures: []ai.Feature{ai.FeatureBatch},
			Description:    "Author dedup and author review submitted through a Batch API.",
			DataSent:       "Author names and groups, as a JSONL file.",
			TypicalRequest: "One JSONL file.",
		}},
		// 05-ai section 11 item 2 leaves books.diagnostics at AnyLocality: HEAD
		// lets it go to a cloud endpoint, and a floor is a product decision.
		{"llm.diagnostics", ai.CapabilitySpec{
			ID: "books.diagnostics", Kind: ai.KindText, RequiredFeatures: []ai.Feature{ai.FeatureBatch},
			Description:    "Diagnostics analysis submitted through a Batch API.",
			DataSent:       "Library slices, log excerpts and operation records: the most sensitive data this program sends.",
			TypicalRequest: "One JSONL file.",
		}},
		// Failover is restricted to endpoints serving the same embedding model:
		// vectors from different models are not comparable (the router pins the
		// model with WithPinnedModel, so no field of the spec carries this).
		{"embed.text", ai.CapabilitySpec{
			ID: "books.embed_text", Kind: ai.KindEmbed,
			Description:    "Embed text for similarity search and dedup. Failover only to endpoints with the same embedding model.",
			DataSent:       "Book titles, authors and descriptions as plain text.",
			TypicalRequest: "64 to 512 texts.",
		}},
		{"embed.text_batch", ai.CapabilitySpec{
			ID: "books.embed_text_batch", Kind: ai.KindEmbed, RequiredFeatures: []ai.Feature{ai.FeatureBatch},
			Description:    "Embed text through a Batch API.",
			DataSent:       "Book titles, authors and descriptions, as a JSONL file.",
			TypicalRequest: "One JSONL file.",
		}},
		// The whisper_requires label set is a config-driven selection rule, not a
		// static feature, so it is not a field here either.
		{"transcribe.batch", ai.CapabilitySpec{
			ID: "books.transcribe_batch", Kind: ai.KindTranscribe, OptionalFeatures: []ai.Feature{sharedFSRoot},
			Description:    "Batch speech-to-text for the opening of audio files. The endpoint must also carry every whisper_requires label.",
			DataSent:       "WAV audio cut from library files. With shared_fs, no audio is sent: the endpoint reads files directly from the library mount.",
			TypicalRequest: "About 46 MB of WAV per request.",
		}},
		{"transcribe.intro_clip", ai.CapabilitySpec{
			ID: "books.transcribe_intro_clip", Kind: ai.KindTranscribe, OptionalFeatures: []ai.Feature{sharedFSRoot},
			Description:    "Transcribe the first 30 seconds of a single file.",
			DataSent:       "One WAV clip of about 1 MB. With shared_fs, no audio is sent: the endpoint reads files directly from the library mount.",
			TypicalRequest: "1 WAV of about 1 MB.",
		}},
	}
}

// Specs is the 13 capabilities books registers, in HEAD's registration order. A
// fresh slice per call, so a caller cannot change what the next one sees.
func Specs() []ai.CapabilitySpec {
	rs := rows()
	out := make([]ai.CapabilitySpec, len(rs))
	for i, r := range rs {
		s := r.spec
		s.RequiredFeatures = slices.Clone(s.RequiredFeatures)
		s.OptionalFeatures = slices.Clone(s.OptionalFeatures)
		out[i] = s
	}
	return out
}

// HEADToBooks is books' capability-ID map (05-ai section 10): HEAD's
// aidispatch capability ID to the catalog ID. A fresh map per call.
func HEADToBooks() map[string]string {
	out := map[string]string{}
	for _, r := range rows() {
		out[r.head] = r.spec.ID
	}
	return out
}

// BooksID is the catalog ID of a HEAD capability ID. An ID the map does not know
// is returned unchanged, which is how HEAD treats an ID a newer build wrote: kept
// on load and ignored by selection (internal/config/ai_endpoints.go).
func BooksID(head string) string {
	for _, r := range rows() {
		if r.head == head {
			return r.spec.ID
		}
	}
	return head
}

// HEADID is the HEAD capability ID a catalog ID replaced; false for an ID the
// catalog does not hold.
func HEADID(id string) (string, bool) {
	for _, r := range rows() {
		if r.spec.ID == id {
			return r.head, true
		}
	}
	return "", false
}

// JobType is what a HEAD AI batch job type becomes: the catalog capability and the
// apply kind core mints for its BatchApplyTask, "<module>.ai.batch-apply.<name>"
// with the capability name's underscores written as hyphens.
type JobType struct{ Capability, ApplyKind string }

// JobTypes are the two job types HEAD registers an aijobs callback for:
// dedup_review (internal/ai/dedup_review.go) and author_dedup_full
// (internal/ai/author_dedup_job.go), keyed by the type string stored on a job. A
// fresh map per call. The apply kinds are the router's own derivation, checked
// against it in the tests.
func JobTypes() map[string]JobType {
	return map[string]JobType{
		"dedup_review":      jobType("llm.dedup_review"),
		"author_dedup_full": jobType("llm.author_dedup_batch"),
	}
}

// jobType is the row of the job type whose HEAD capability is head.
func jobType(head string) JobType {
	id := BooksID(head)
	name := strings.NewReplacer("_", "-").Replace(strings.TrimPrefix(id, Module+"."))
	return JobType{Capability: id, ApplyKind: Module + ".ai.batch-apply." + name}
}
