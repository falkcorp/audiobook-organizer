// file: internal/books/cutover/head_effective.go
// version: 1.0.0
// guid: 269f11a2-59b2-4c5e-8f24-679a255ac834
// last-edited: 2026-11-11

// FROZEN COPY of HEAD (748fd787a) post-decode config normalisation (normalizeLogRetentionDays, normalizeChapterConsolidationThreshold, restoreNilSliceDefaults, MigrateMaintenanceWindow); read-only; deleted in R3 with the other HEAD-format readers (05-config §1.12.2, 03 §3.8).

package cutover

import (
	"encoding/json"
	"strconv"
)

// headMaintenanceMarkerKey is the settings row whose value "true" means
// MigrateMaintenanceWindow already ran (persistence.go:1315).
const headMaintenanceMarkerKey = "maintenance_window_migrated"

// headEffectiveTree applies, to the decoded blob tree, the four steps HEAD
// runs after decoding the blob and that headDecodeBlob does not replay (it
// writes nothing and stops at the decode), so the tree holds HEAD's
// effective stored values:
//
//   - log_retention_days stored as 0 means the default (normalizeLogRetentionDays):
//     the member is dropped so the default applies;
//   - chapter_consolidation_threshold_min at or below 0 is not a usable
//     threshold (normalizeChapterConsolidationThreshold): dropped likewise;
//   - a JSON null where a slice or map is stored is a nil slice, which
//     restoreNilSliceDefaults replaces with the default: dropped likewise (a
//     present empty array stays: it is an operator's choice);
//   - the maintenance window one-shot (MigrateMaintenanceWindow), when its
//     marker row is not "true": a zero window takes the auto-update window's
//     value where that is above 0, and a zero pair becomes 1 and 4.
//
// The window one-shot reads values by the effective view (a member absent
// from the blob is HEAD's default), so it changes the tree only where HEAD
// would have. It returns the names of members it dropped or set, for the
// report; never their values.
func headEffectiveTree(tree map[string]any, maintenanceMarkerSet bool) (changed []string) {
	if n, ok := headNumber(tree["log_retention_days"]); ok && n == 0 {
		delete(tree, "log_retention_days")
		changed = append(changed, "log_retention_days")
	}
	if n, ok := headNumber(tree["chapter_consolidation_threshold_min"]); ok && n <= 0 {
		delete(tree, "chapter_consolidation_threshold_min")
		changed = append(changed, "chapter_consolidation_threshold_min")
	}
	for name, v := range tree {
		if v == nil {
			delete(tree, name)
			changed = append(changed, name)
		}
	}
	if maintenanceMarkerSet {
		return changed
	}
	maint, _ := tree["maintenance"].(map[string]any)
	if maint == nil {
		maint = map[string]any{}
	}
	au, _ := tree["auto_update"].(map[string]any)
	get := func(obj map[string]any, parent, field string) float64 {
		if v, ok := obj[field]; ok {
			if n, ok := headNumber(v); ok {
				return n
			}
			return 0
		}
		def, _ := headDefault(parent + "." + field)
		n, _ := headNumber(def)
		return n
	}
	start, end := get(maint, "maintenance", "window_start"), get(maint, "maintenance", "window_end")
	auStart, auEnd := get(au, "auto_update", "window_start"), get(au, "auto_update", "window_end")
	if start == 0 && auStart > 0 {
		start = auStart
	}
	if end == 0 && auEnd > 0 {
		end = auEnd
	}
	if start == 0 && end == 0 {
		start, end = 1, 4
	}
	oldStart, oldEnd := get(maint, "maintenance", "window_start"), get(maint, "maintenance", "window_end")
	if start != oldStart || end != oldEnd {
		maint["window_start"], maint["window_end"] = json.Number(strconv.Itoa(int(start))), json.Number(strconv.Itoa(int(end)))
		tree["maintenance"] = maint
		changed = append(changed, "maintenance.window_start", "maintenance.window_end")
	}
	return changed
}
