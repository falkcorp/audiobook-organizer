// file: internal/books/cutover/m6/maps.go
// version: 1.0.1
// guid: ae55952e-4dad-4774-a3ae-22d7a560ba34
// last-edited: 2026-11-19

package m6

import (
	"strings"

	"github.com/falkcorp/mediacore/integrations"
)

// The normative tables of the spec, copied into Go with their citations. Each is
// pinned by a test, so a change to the spec's row is a failing test here.

// capabilityID is books' capability-ID map (05-ai section 10, "ticks rewritten
// through books' capability-ID map"): HEAD's aidispatch capability IDs
// (internal/aidispatch/capabilities.go) to the IDs books registers with core.
// The llm.* family moves under the books module as books.<name>; the
// non-chat families are renamed one by one. It is applied only to the IDs in
// headCapabilities: a tick or a capability_models key outside that set is
// dropped and reported (aiInstance), not passed through, because core refuses
// an imported endpoint that carries a tick its catalog does not know.
func capabilityID(id string) string {
	switch id {
	case "embed.text":
		return "books.embed_text"
	case "embed.text_batch":
		return "books.embed_text_batch"
	case "transcribe.batch":
		return "books.transcribe_batch"
	case "transcribe.intro_clip":
		return "books.transcribe_intro_clip"
	}
	if name, ok := strings.CutPrefix(id, "llm."); ok && name != "" {
		return "books." + name
	}
	return id
}

// headCapabilities is every capability HEAD's aidispatch registry registers
// (internal/aidispatch/capabilities.go), frozen here and pinned both ways by
// TestCapabilityID_CoversEveryHEADCapability. A tick outside it is one HEAD
// kept on load and ignored in selection (AIEndpoint.UnknownCapabilities); core's
// endpoint validator (ai.Router.ValidateEndpoint) refuses such a tick on an
// import (it has no stored row to grandfather it), which would quarantine the
// whole endpoint, so M6 drops the tick instead and reports it. A wildcard is
// never a HEAD capability. At this head
// ImportInstance runs only its structural check (core/integrations importer.go
// checkImport); the refusal arrives with #4804 (F7-09 T3), whose validateImport
// runs the adapter's validator on an import, together with F7-09's ai adapter
// wiring, which makes that ai.Router.ValidateEndpoint. The alternative T3 round 1
// raised, grandfathering stored ticks on an import as Put does for a stored
// row, was not taken: HEAD selected by none of them, so nothing is lost.
var headCapabilities = map[string]bool{
	"llm.filename_parse": true, "llm.audiobook_parse": true, "llm.cover_art_vision": true,
	"llm.author_review": true, "llm.author_discovery": true, "llm.metadata_rerank": true,
	"llm.dedup_review": true, "llm.author_dedup_batch": true, "llm.diagnostics": true,
	"embed.text": true, "embed.text_batch": true,
	"transcribe.batch": true, "transcribe.intro_clip": true,
}

// legacyKeyCapabilities are the capabilities HEAD's legacy OpenAI path served,
// which is what the ai/openai row M6 synthesises stands for (05-metadata M1.10).
// They are the capabilities of the call sites that read the scalar
// openai_api_key and were never routed through the endpoint pool, each gated on
// the key and on enable_ai_parsing:
//
//   - llm.author_review and llm.author_discovery: aiAuthorReviewCore
//     (internal/server/ai_ops.go) runs handlers.AIReviewGroupsMode
//     (parser.ReviewAuthorDuplicates) and AIReviewFullMode
//     (parser.DiscoverAuthorDuplicates) on newAIParser(key, EnableAIParsing);
//   - llm.author_dedup_batch: runAIDedupBatch
//     (internal/plugins/maintenance/dedup_ops.go) submits the author dedup batch
//     with the key, refusing when AI parsing is off;
//   - llm.diagnostics: diagnosticsAICore (internal/server/diagnostics_ai_ops.go)
//     submits the diagnostics batch, refusing without the key or EnableAIParsing.
//
// llm.filename_parse, embed.text and llm.cover_art_vision are left out on
// purpose: the pool routes them (internal/ai/pool_routing.go, cover_text.go), so
// a tick would let the router send paid scan, embedding and vision calls to
// OpenAI that HEAD, routing on, sent only where a row was ticked. The other
// key-reading sites are outside this set and fail closed after the flip, each to
// be decided on its own (TODO-F7-09-M6-OTHER-KEY-SITES). Pinned by
// TestSynthesisedOpenAI_TicksMatchHEADsLegacySites.
var legacyKeyCapabilities = []string{
	"llm.author_review", "llm.author_discovery", "llm.author_dedup_batch", "llm.diagnostics",
}

// coreFeatures are the features core's endpoint validator accepts
// (core/integrations/ai declaredFeatures), plus the shared_fs:<root> form. HEAD
// stores features as free strings, and core refuses an endpoint carrying any
// other, so M6 drops one outside this set and reports it.
// TestAIInstances_PassCoresEndpointValidator pins it against the real validator.
var coreFeatures = map[string]bool{
	"json_schema": true, "json_mode": true, "vision": true, "batch_api": true, "tools": true, "timestamps": true,
}

// sharedFSPrefix is the feature prefix of a measured shared-mount feature, valid
// in both HEAD and core when a root ID follows it.
const sharedFSPrefix = "shared_fs:"

func knownFeature(f string) bool {
	return coreFeatures[f] || (strings.HasPrefix(f, sharedFSPrefix) && len(f) > len(sharedFSPrefix))
}

// protocolAdapter is the adapter an ai_endpoints row's protocol becomes
// (05-ai section 10; 05-metadata M1.10): openai_compat to core's adapter, the
// two whisper protocols to books' own.
func protocolAdapter(protocol string) (integrations.AdapterID, bool) {
	switch protocol {
	case "openai_compat":
		return "core.openai_compat", true
	case "whisper_server":
		return "books.whisper_server", true
	case "local_process":
		return whisperLocalAdapter, true
	}
	return "", false
}

// whisperLocalAdapter is books' local_process adapter (whisper.LocalAdapterID),
// frozen here and pinned by TestWhisperLocal_AdapterAndMarkerArePinned.
const whisperLocalAdapter integrations.AdapterID = "books.whisper_local"

// localUVEndpointURL is HEAD's config.LocalUVEndpointURL: the marker a local
// process row carries in place of an address. Frozen, and pinned.
const localUVEndpointURL = "local:uv-openai-whisper"

// webhookTopics is the event-name map of 05-notify section 10 ("plugin event
// types"): HEAD's plugin event types (internal/plugin/events.go) to the books
// topics declared notifiable in the events catalog.
var webhookTopics = map[string]string{
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

// webhookDefaultEvents is what "all" or an empty events setting means: HEAD's
// default list (internal/plugins/webhook/plugin.go allEventTypes), the ten
// events without book.quarantined and book.unquarantined.
var webhookDefaultEvents = []string{
	"book.imported", "book.deleted", "metadata.applied", "tags.written", "file.organized",
	"dedup.detected", "dedup.merged", "cover.changed", "read_status.changed", "scan.completed",
}

// jobTypeRow is what a HEAD aijob type becomes (05-ai section 10): the catalog
// capability, and the apply kind "<module>.ai.batch-apply.<capability name with
// _ as ->" of 05-ai's JobStore comment.
type jobTypeRow struct{ Capability, ApplyKind string }

// jobTypes are the two job types HEAD registers an aijobs callback for:
// dedup_review (internal/ai/dedup_review.go) and author_dedup_full
// (internal/ai/author_dedup_job.go, AuthorDedupJobType), whose capabilities are
// llm.dedup_review and llm.author_dedup_batch.
var jobTypes = map[string]jobTypeRow{
	"dedup_review":      {Capability: "books.dedup_review", ApplyKind: "books.ai.batch-apply.dedup-review"},
	"author_dedup_full": {Capability: "books.author_dedup_batch", ApplyKind: "books.ai.batch-apply.author-dedup-batch"},
}

// unknownJobType is the capability and apply kind a job of a type no callback
// exists for is given. The importer finds neither in the catalog and writes the
// job JobFailed with the reason in its note row (05-ai section 10, "a type with
// no catalog capability"); the step also forces JobFailed itself, so the job
// is never left open when the importer was built without a catalog.
func unknownJobType(t string) jobTypeRow {
	name := strings.ToLower(strings.NewReplacer("_", "-", ".", "-", ":", "-").Replace(t))
	return jobTypeRow{Capability: "books." + t, ApplyKind: "books.ai.batch-apply." + name}
}
