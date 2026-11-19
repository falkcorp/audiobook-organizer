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
// The llm.* family moves under the books module as books.<name>; the three
// non-chat families are renamed one by one. An ID this table does not know is
// kept verbatim, which is how HEAD itself treats an ID a newer build wrote
// ("kept on load and ignored by selection", internal/config/ai_endpoints.go).
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
		return "books.whisper_local", true
	}
	return "", false
}

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
