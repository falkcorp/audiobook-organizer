// file: internal/config/blob_default_audit.go
// version: 1.0.0
// guid: 2b8d5f60-c179-4e34-a5d2-70f9e1c46b8a
// last-edited: 2026-08-12

package config

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
)

// maxAuditedKeysLogged bounds the boot log line. The count is always exact; only
// the enumeration is truncated, and the message says so when it truncates — a
// silently shortened list would be the same class of defect this audit exists to
// catch.
const maxAuditedKeysLogged = 40

// logDefaultsPreservedOverBlob reports every config key that the stored blob did
// NOT contain and which therefore kept its shipped default.
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
func logDefaultsPreservedOverBlob(blobStr string, loaded Config) {
	var blobMap map[string]any
	if err := json.Unmarshal([]byte(blobStr), &blobMap); err != nil {
		return // already reported by the caller's parse-failure branch
	}

	effective, err := json.Marshal(loaded)
	if err != nil {
		return
	}
	var effMap map[string]any
	if err := json.Unmarshal(effective, &effMap); err != nil {
		return
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

	if len(inherited) == 0 {
		return
	}
	sort.Strings(inherited)

	shown := inherited
	truncated := ""
	if len(shown) > maxAuditedKeysLogged {
		shown = shown[:maxAuditedKeysLogged]
		truncated = " (list truncated; count is exact)"
	}

	slog.Info("config: keys absent from the stored blob kept their shipped default"+truncated,
		"count", len(inherited),
		"keys", strings.Join(shown, ", "),
	)
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
