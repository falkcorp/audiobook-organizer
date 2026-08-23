// file: internal/database/primary_version.go
// version: 1.0.0
// guid: a3f56aae-a5a8-4964-b75c-d6f01152041d
// last-edited: 2026-08-23

package database

// EffectiveIsPrimaryVersion resolves the nullable Book.IsPrimaryVersion flag to
// the single boolean the whole codebase filters on: **a nil flag counts as
// primary (true)**.
//
// The rule is not new — it is what ~28 existing read paths already implement
// inline, either as `flag == nil || *flag` or as its contrapositive skip,
// `if flag != nil && !*flag { continue }`. The memdb index declares the same
// default structurally (memdb_schema.go's `memIdxIsPrimaryVersion` uses
// effectiveBoolFieldIndex{Default: true}), and pebble_store.go's
// IsPrimaryVersion filter matches it so the degraded Pebble read path cannot
// answer a query differently from the warm memdb path. The C111 census
// (PR #2449) settled the semantics for the same reason: nil means primary,
// and the maintenance job that makes it explicit on disk exists precisely
// because the convention is otherwise invisible at the point of use.
//
// The hazard this function exists to remove is that the convention lives only
// in those inline expressions. Any reader who dereferences the raw *bool
// without knowing it — `flag != nil && *flag` — silently flips the answer for
// every nil-flagged row, and the two computations then disagree about the same
// book. That is exactly what happened on the GetAudiobooks post-filters (fixed
// in 8486c93f8) and on the listing DTO, which serialized the raw nullable field
// rather than the effective value the filter had just used.
//
// Callers that need the *stored* tri-state — genuinely distinguishing "no
// opinion recorded" from an explicit true — must read the field directly and
// say why; this helper deliberately erases that distinction.
func EffectiveIsPrimaryVersion(flag *bool) bool {
	return flag == nil || *flag
}
