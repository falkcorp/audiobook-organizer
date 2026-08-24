// file: internal/scanner/op_params.go
// version: 1.1.0
// guid: b46c14ae-5979-4407-8149-e85b7a04dc2b
// last-edited: 2026-08-24

package scanner

// LibraryScanParams is the JSON wire shape of the "library.scan" v2 operation.
//
// It lives here, in the domain package, rather than next to the OperationDef in
// internal/server, because BOTH internal/server and internal/scheduler enqueue
// library.scan while internal/server imports internal/scheduler -- so the
// scheduler can never import the server back.
//
// Until 2026-08-24 the scheduler worked around that cycle with its own
// `type libraryScanParams struct{}`. That empty struct had no fields and so
// could not drift; it was correct precisely because {} was the wire shape the
// incremental scan wanted. The weekly full sweep needs real fields, and adding
// them to a local copy is what would have reintroduced the drift risk: the copy
// would be coupled to the real type only by JSON tags, with nothing to fail if
// they diverged. internal/scheduler's own comment on the dedup params records
// that shape going wrong -- seriesPruneOpParams simultaneously declared a
// legacy_op_id the op had stopped reading and omitted Detail, which the real
// type has. That one was caught before it changed any behaviour; a silent
// force_update that marshalled fine and did nothing would not have been.
//
// The fix there was to host the params in internal/dedup and share one type.
// This is the same fix for the same reason.
//
// Keep it a plain struct with no scanner-internal dependencies: it is a wire
// shape, and anything that makes it awkward to import is a reason for a caller
// to reach for a copy again.
type LibraryScanParams struct {
	FolderPath  *string `json:"folder_path,omitempty"`
	ForceUpdate *bool   `json:"force_update,omitempty"`

	// IncludeRootDir adds the organized library root to the scan while KEEPING
	// the incremental skip. force_update also reaches RootDir but disables the
	// skip at the same time, which turns "scan the whole library" into a full
	// re-hash of it; these are now separable.
	IncludeRootDir *bool `json:"include_root_dir,omitempty"`

	// ResumeFolderIdx / ResumeItemOffset are written by the scan's own
	// Checkpoint calls and merged back into params by resumeRestart() before
	// Run is re-invoked. They are not part of the public trigger payload -- a
	// caller may set them, but normally they arrive only from a checkpoint.
	ResumeFolderIdx  int `json:"resume_folder_idx,omitempty"`
	ResumeItemOffset int `json:"resume_item_offset,omitempty"`
}

// LibraryScanCheckpoint is the state blob persisted mid-scan. Its JSON field
// names must match LibraryScanParams exactly: resumeRestart() merges the blob
// into the params object, so a mismatch silently resumes from zero.
type LibraryScanCheckpoint struct {
	ResumeFolderIdx  int `json:"resume_folder_idx"`
	ResumeItemOffset int `json:"resume_item_offset"`
}
