// file: internal/config/blob_default_audit.go
// version: 1.2.0
// guid: 2b8d5f60-c179-4e34-a5d2-70f9e1c46b8a
// last-edited: 2026-08-30

package config

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
)

// maxAuditedKeysLogged bounds the boot log line. The enumeration is data-scaled
// — Config carries operator-controlled maps (plugins, per-source credentials,
// per-kind dedup confidence, per-model embedding thresholds), so the flattened
// key set grows with the install, not just with the source. An unbounded line is
// therefore not an option.
//
// The count is always exact, and when the list is cut the line now reports
// `omitted` so the operator can see how many names are missing rather than
// inferring it from `count` minus a list they have to measure by hand. That is
// still less than the whole truth: on the production install that motivated this
// audit, 122 keys inherited and the one that caused the incident
// (scheduled.library_scan.interval) sorts past the cut. Callers that need the
// complete set must use defaultsPreservedOverBlob, which never truncates.
const maxAuditedKeysLogged = 40

// defaultsPreservedOverBlob returns every config key that the stored blob did
// NOT contain and which therefore kept its shipped default, sorted and COMPLETE.
// It never truncates: the truncation belongs to the log renderer, so a test or a
// future caller can assert on the real inherited set rather than on how many of
// its names happened to fit in one line.
//
// This exists because the inheritance is a real behaviour change and used to be
// invisible. LoadConfigFromDatabase previously unmarshalled the blob into an
// all-zero struct and assigned it wholesale, so a key absent from the blob was
// silently zeroed rather than defaulted — that is how the periodic library scan
// (default enabled=true / interval=360) came to be off on production with no
// trace. Seeding from Snapshot() fixes the behaviour; this function makes the
// consequence auditable at boot instead of leaving the operator to discover it.
//
// Only keys whose surviving default is NON-ZERO are reported: a key absent from
// the blob whose default is false/0/"" produces the same value either way, so
// listing it would bury the meaningful entries in noise.
//
// Keys only — never values. The blob is documented as secret-free, but this runs
// during config load and there is no reason to put config values into a log line
// to answer the question "which keys inherited a default".
func defaultsPreservedOverBlob(blobStr string, loaded Config) []string {
	var blobMap map[string]any
	if err := json.Unmarshal([]byte(blobStr), &blobMap); err != nil {
		return nil // already reported by the caller's parse-failure branch
	}

	effective, err := json.Marshal(loaded)
	if err != nil {
		return nil
	}
	var effMap map[string]any
	if err := json.Unmarshal(effective, &effMap); err != nil {
		return nil
	}

	stored := make(map[string]struct{})
	flattenKeys(blobMap, "", stored)

	effLeaves := make(map[string]any)
	flattenLeaves(effMap, "", effLeaves)

	var inherited []string
	for key, val := range effLeaves {
		if _, present := stored[key]; present {
			continue
		}
		if isZeroJSON(val) {
			continue
		}
		inherited = append(inherited, key)
	}

	sort.Strings(inherited)
	return inherited
}

// logDefaultsPreservedOverBlob renders the audit set to the boot log. It is a
// presentation layer over defaultsPreservedOverBlob and holds no logic of its
// own beyond the line bound described on maxAuditedKeysLogged.
func logDefaultsPreservedOverBlob(blobStr string, loaded Config) {
	inherited := defaultsPreservedOverBlob(blobStr, loaded)
	if len(inherited) == 0 {
		return
	}

	shown := inherited
	omitted := 0
	if len(shown) > maxAuditedKeysLogged {
		omitted = len(shown) - maxAuditedKeysLogged
		shown = shown[:maxAuditedKeysLogged]
	}

	attrs := []any{
		"count", len(inherited),
		"keys", strings.Join(shown, ", "),
	}
	msg := "config: keys absent from the stored blob kept their shipped default"
	if omitted > 0 {
		// Name the shortfall as a number. "(list truncated)" told the operator
		// something was missing without telling them how much, which is the
		// same shape of invisibility this audit exists to remove.
		msg += " (list truncated; count is exact)"
		attrs = append(attrs, "omitted", omitted)
	}

	slog.Info(msg, attrs...)
}

// explicitChapterConsolidationDisable reports only an operator-persisted
// disable. A missing key must remain distinguishable because it inherits the
// shipped default; treating it as zero would recreate the silent-default loss
// this audit was written to prevent.
func explicitChapterConsolidationDisable(blobStr string, loaded Config) bool {
	if loaded.ChapterConsolidationThresholdMin > 0 {
		return false
	}
	var blob map[string]json.RawMessage
	if err := json.Unmarshal([]byte(blobStr), &blob); err != nil {
		return false
	}
	raw, present := blob["chapter_consolidation_threshold_min"]
	if !present {
		return false
	}
	var stored int
	return json.Unmarshal(raw, &stored) == nil && stored <= 0
}

func logExplicitChapterConsolidationDisable(blobStr string, loaded Config) {
	if !explicitChapterConsolidationDisable(blobStr, loaded) {
		return
	}
	slog.Warn("config: chapter consolidation is explicitly disabled; album-less multi-file books will not be grouped",
		"chapter_consolidation_threshold_min", loaded.ChapterConsolidationThresholdMin)
}

// flattenKeys records every leaf path present in a decoded JSON object. Nested
// objects contribute their leaves, not themselves, so a blob that stores
// scheduled.library_scan.interval but not .enabled marks only the former.
func flattenKeys(m map[string]any, prefix string, out map[string]struct{}) {
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if child, ok := v.(map[string]any); ok {
			flattenKeys(child, path, out)
			continue
		}
		out[path] = struct{}{}
	}
}

// flattenLeaves is flattenKeys but keeps the values, for the zero check.
func flattenLeaves(m map[string]any, prefix string, out map[string]any) {
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if child, ok := v.(map[string]any); ok {
			flattenLeaves(child, path, out)
			continue
		}
		out[path] = v
	}
}

// isZeroJSON reports whether a decoded JSON value is the zero value for its
// kind. encoding/json decodes every number as float64, so 0 covers both int and
// float defaults.
func isZeroJSON(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return !t
	case float64:
		return t == 0
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}
