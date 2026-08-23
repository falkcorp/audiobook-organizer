// file: internal/activity/api.go
// version: 1.10.0
// guid: 9a4f2e1b-3c7d-4b8e-a6f0-5d2c8e1b7a3f

package activity

import (
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// batchableTypes is the set of type strings that route through the ActivityBatcher.
// Only entries with both a non-empty OperationID and a registered type are batched.
var batchableTypes = map[string]bool{
	"embedded-tag-load":   true,
	"tag-scan":            true,
	"scan-file-processed": true,
	"metadata-apply":      true,
	"path-repair":         true,
	"isbn-enrich":         true,
	"temp-file-cleanup":   true,
	"missing-file-repair": true,
	"purge-deleted":       true,
}

// LogBatch submits a single BatchItem to the batcher inside w for the given
// operationID and batchType. If operationID is empty or batchType is not
// registered as batchable, the item is emitted as a plain debug ActivityEntry
// instead (non-blocking, best-effort). Safe to call from multiple goroutines.
// w may be nil — in that case the call is a no-op.
func LogBatch(w *Writer, operationID, batchType, source string, item BatchItem) {
	if w == nil {
		return
	}
	if operationID == "" || !batchableTypes[batchType] {
		// Fallback: emit as a plain debug entry; non-blocking.
		select {
		case w.ch <- database.ActivityEntry{
			Tier:        "debug",
			Type:        batchType,
			Level:       "info",
			Source:      source,
			OperationID: operationID,
			Summary:     item.Name,
		}:
		default:
			// channel full — drop silently, same policy as sendEntry for debug
		}
		return
	}
	w.batcher.Submit(BatchKey{
		Type:        batchType,
		Source:      source,
		OperationID: operationID,
	}, item)
}

// EmitInfo writes a single info-tier ActivityEntry directly to the activity log
// for the given operation. Use this for operation summary messages that should
// appear in the main activity feed regardless of whether any items were batched.
// Optional tags are stored as a comma-separated list; pass "no-op" to make the
// entry filterable from the default view. Safe to call from multiple goroutines.
// w may be nil — call is a no-op.
func EmitInfo(w *Writer, operationID, entryType, source, summary string, tags ...string) {
	if w == nil {
		return
	}
	select {
	case w.ch <- database.ActivityEntry{
		Tier:        "info",
		Type:        entryType,
		Level:       "info",
		Source:      source,
		OperationID: operationID,
		Summary:     summary,
		Tags:        tags,
	}:
	default:
	}
}

// NoOpTag marks entries where an operation did nothing; the frontend hides these by default.
const NoOpTag = "no-op"

// AlwaysShow marks entries from manually-triggered operations; the frontend always
// displays them regardless of active filter presets.
const AlwaysShow = "always-show"

// Scheduled marks entries emitted by the background scheduler or maintenance window;
// the frontend may suppress them unless the user opts into verbose view.
const Scheduled = "scheduled"

// MaintenanceWindow is the type string for per-window summary entries.
const MaintenanceWindow = "maintenance-window"

// TagsIf returns []string{tag} when cond is true, otherwise nil.
// Convenience helper for EmitInfo call sites that want to tag a no-op result:
//
//	activity.EmitInfo(..., msg, activity.TagsIf(count == 0, activity.NoOpTag)...)
func TagsIf(cond bool, tag string) []string {
	if cond {
		return []string{tag}
	}
	return nil
}

// WithTags merges base tags with extra tags and returns the combined slice.
// Useful when you need a fixed set of tags plus a conditional one.
func WithTags(base []string, extra ...string) []string {
	out := make([]string, len(base), len(base)+len(extra))
	copy(out, base)
	return append(out, extra...)
}

// typeToAction maps Type values to action verb tags.
func typeToAction(typeStr string) string {
	typeStr = strings.TrimSpace(typeStr)
	switch typeStr {
	case "metadata_apply", "metadata-apply":
		return "metadata-apply"
	case "tag_write", "tag-write":
		return "tag-write"
	case "rename":
		return "write-back"
	case "itunes_sync":
		return "import"
	case "scan", "tag-scan", "embedded-tag-load", "scan-file-processed":
		return "scan"
	case "purge-deleted":
		return "purge"
	case "isbn-enrich":
		return "metadata-apply"
	case "missing-file-repair", "path-repair":
		return "organizer"
	case "maintenance-window", "temp-file-cleanup":
		return "maintenance"
	case "cover-update":
		return "cover-update"
	case "fingerprint":
		return "fingerprint"
	case "dedup":
		return "dedup"
	case "acoustid.scan":
		return "fingerprint-scan"
	case "acoustid.backfill", "acoustid.fingerprint-rescan":
		return "fingerprint"
	case "acoustid.lookup-online":
		return "acoustid-lookup"
	case "acoustid.reset-all":
		return "fingerprint-reset"
	case "dedup.full-scan", "dedup.llm-review", "dedup.split-book-scan", "dedup.book-signature-scan":
		return "dedup"
	case "library.scan":
		return "scan"
	// metadata.candidate-fetch is the v2 def id whose v1 twin is
	// metadata_candidate_fetch. Every other entry here already pairs the two
	// spellings; this one was missed, and it went unnoticed while the handler
	// still minted a v1 row for each run. It no longer does, so without this the
	// activity feed labels every candidate fetch from the default branch.
	case "library.bulk-metadata-fetch", "metadata.candidate-fetch", "metadata_fetch", "metadata_candidate_fetch":
		return "metadata-apply"
	case "library.bulk-write-back", "bulk_write_back":
		return "tag-write"
	case "library.organize", "organize":
		return "organizer"
	case "itunes.import", "itunes.sync", "itunes.path-repair":
		return "import"
	case "reconcile":
		return "reconcile"
	case "merge":
		return "merge"
	default:
		if strings.HasPrefix(typeStr, "acoustid.") {
			return "fingerprint"
		}
		if strings.HasPrefix(typeStr, "dedup.") {
			return "dedup"
		}
		if strings.Contains(typeStr, "metadata") {
			return "metadata-apply"
		}
		if strings.Contains(typeStr, "fingerprint") {
			return "fingerprint"
		}
		if strings.Contains(typeStr, "reconcile") {
			return "reconcile"
		}
		return ""
	}
}

// systemLifecycle returns a "lifecycle:<phase>" tag for the given system log
// message, or "" if no phase keyword is recognised. Substring match is
// intentionally cheap and case-insensitive — this is decoration, not a
// contract. Phases:
//   - startup:    initialized / wired / listening / started / ready / recording
//   - shutdown:   shutting down / stopping / stopped / closed / canceling /
//     flushing / waiting for / forced shutdown
//   - connection: client connect / disconnect / register events
func systemLifecycle(msg string) string {
	if msg == "" {
		return ""
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "initialized"),
		strings.Contains(lower, " wired"),
		strings.Contains(lower, "listening"),
		strings.Contains(lower, " started"),
		strings.Contains(lower, " ready"),
		strings.Contains(lower, "recording"):
		return "lifecycle:startup"
	case strings.Contains(lower, "shutting down"),
		strings.Contains(lower, "shutdown"),
		strings.Contains(lower, "stopping"),
		strings.Contains(lower, " stopped"),
		strings.Contains(lower, " closed"),
		strings.Contains(lower, "canceling"),
		strings.Contains(lower, "flushing"),
		strings.Contains(lower, "waiting for"):
		return "lifecycle:shutdown"
	case strings.Contains(lower, "client connection"),
		strings.Contains(lower, "client unregistered"),
		strings.Contains(lower, "client registered"):
		return "lifecycle:connection"
	}
	return ""
}

// EnrichTags auto-derives structured tags from entry fields and appends them.
// Idempotent: existing tags prevent duplicates. Derived tags:
//   - op:<operation_id> — ties to operation
//   - book:<book_id> — ties to specific book
//   - outcome:ok|warn|error|skip — from Level
//   - source:<subsystem> — from Source
//   - action:<verb> — from Type via typeToAction map
//   - scope:book — if BookID non-empty (simple heuristic)
func EnrichTags(e *database.ActivityEntry) {
	if e == nil {
		return
	}
	seen := make(map[string]bool)
	for _, t := range e.Tags {
		seen[t] = true
	}

	var derived []string

	// op: tag
	if e.OperationID != "" && !seen["op:"+e.OperationID] {
		derived = append(derived, "op:"+e.OperationID)
	}

	// book: tag
	if e.BookID != "" && !seen["book:"+e.BookID] {
		derived = append(derived, "book:"+e.BookID)
	}

	// outcome: tag from Level
	outcome := "outcome:ok"
	switch e.Level {
	case "warn", "warning":
		outcome = "outcome:warn"
	case "error":
		outcome = "outcome:error"
	case "skip":
		outcome = "outcome:skip"
	}
	if !seen[outcome] {
		derived = append(derived, outcome)
	}

	// source: tag from Source
	if e.Source != "" && !(e.Source == "server" && e.Type == "system") {
		src := "source:" + e.Source
		if !seen[src] {
			derived = append(derived, src)
		}
	}

	// action: tag from Type
	if action := typeToAction(e.Type); action != "" {
		a := "action:" + action
		if !seen[a] {
			derived = append(derived, a)
		}
	}

	// scope: tag (simple heuristic — book if BookID present)
	if e.BookID != "" && !seen["scope:book"] {
		derived = append(derived, "scope:book")
	}

	for _, tag := range detailsTags(e) {
		if !seen[tag] {
			derived = append(derived, tag)
			seen[tag] = true
		}
	}

	for _, tag := range summaryTags(e.Summary) {
		if !seen[tag] {
			derived = append(derived, tag)
			seen[tag] = true
		}
	}

	// lifecycle: tag for system entries (startup/shutdown/connection).
	// Derived from Summary keywords so the operator can filter the firehose
	// to "show me everything that happened during boot" or "what stopped
	// during the last shutdown" without grepping raw text.
	if e.Type == "system" {
		if life := systemLifecycle(e.Summary); life != "" && !seen[life] {
			derived = append(derived, life)
		}
	}

	// component: tag — identifies the specific subsystem. Prefer an
	// explicit "component" value stored in Details (set by the slog parser
	// when it extracts a component= attr from a structured log line), then
	// fall back to a static source→component mapping for well-known sources.
	// We intentionally skip emitting component: when no mapping exists so
	// we don't produce misleading tags for generic "server" entries.
	if comp := componentFromEntry(e); comp != "" {
		tag := "component:" + comp
		if !seen[tag] {
			derived = append(derived, tag)
		}
	}

	e.Tags = append(e.Tags, derived...)
}

// sourceToComponent maps well-known Source values to component names.
// This is the last-resort fallback when no explicit "component" attr was
// emitted and the source path derivation in ParseLogLineFull didn't match.
var sourceToComponent = map[string]string{
	"scanner":     "scanner",
	"itunes":      "itunes_sync",
	"acoustid":    "acoustid",
	"dedup":       "dedup",
	"isbn":        "isbn_enrich",
	"scheduler":   "scheduler",
	"maintenance": "maintenance",
}

func detailsTags(e *database.ActivityEntry) []string {
	if e.Details == nil {
		return nil
	}
	var tags []string
	for _, kp := range []struct{ key, prefix string }{
		{"def_id", "def"},
		{"plugin", "plugin"},
		{"phase", "phase"},
		{"status", "status"},
		{"outcome", "status"},
	} {
		if v, ok := e.Details[kp.key]; ok {
			if s, ok := v.(string); ok && s != "" {
				tags = append(tags, kp.prefix+":"+s)
			}
		}
	}
	if method, ok := e.Details["method"].(string); ok && method != "" {
		tags = append(tags, "http:"+strings.ToLower(method))
	}
	return tags
}

func summaryTags(summary string) []string {
	lower := strings.ToLower(summary)
	var tags []string
	switch {
	case strings.Contains(lower, "http request"):
		tags = append(tags, "http:request")
	case strings.Contains(lower, "tls handshake"):
		tags = append(tags, "network:tls")
	case strings.Contains(lower, "cache"):
		tags = append(tags, "cache")
	case strings.Contains(lower, "dedup"):
		tags = append(tags, "domain:dedup")
	case strings.Contains(lower, "metadata"):
		tags = append(tags, "domain:metadata")
	case strings.Contains(lower, "fingerprint"), strings.Contains(lower, "acoustid"):
		tags = append(tags, "domain:fingerprint")
	}
	if strings.Contains(lower, "not found") {
		tags = append(tags, "error:not-found")
	}
	if strings.Contains(lower, "rate limit") || strings.Contains(lower, "429") {
		tags = append(tags, "error:rate-limit")
	}
	return tags
}

// componentFromEntry derives the component name for an ActivityEntry.
// Priority: Details["component"] > Details["subsystem"] > sourceToComponent[Source].
// Returns "" when no component can be reliably inferred.
func componentFromEntry(e *database.ActivityEntry) string {
	if e.Details != nil {
		for _, key := range []string{"component", "subsystem"} {
			if v, ok := e.Details[key]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
	}
	if comp, ok := sourceToComponent[e.Source]; ok {
		return comp
	}
	return ""
}

// FlushOperation immediately flushes all pending batches whose OperationID
// matches operationID. Call this just before recording an operation's
// completion event, so the batch rows land before the completion row.
// Safe to call from any goroutine. w may be nil.
func FlushOperation(w *Writer, operationID string) {
	if w == nil || operationID == "" {
		return
	}
	w.batcher.mu.Lock()
	keys := make([]BatchKey, 0)
	for k := range w.batcher.pending {
		if k.OperationID == operationID {
			keys = append(keys, k)
		}
	}
	w.batcher.mu.Unlock()
	for _, k := range keys {
		w.batcher.flushKey(k)
	}
}
