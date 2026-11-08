// file: core/internal/restoreswap/doc.go
// version: 1.0.0
// guid: eee335d6-866d-40e3-a22b-ad99e4920fa0
// last-edited: 2026-11-07

// Package restoreswap is the one journaled swap (05-backup §5B.3.4,
// 05-storage §5S.3.15, §5S.3.16 machine 1): restores of every kind and the
// factory reset share one intent slot (<state_dir>/restore/pending.json,
// created by restorejournal.CreateIntentFS), one rejection rule, one journal
// (restore/<id>/journal.json) and one in-flight file
// (restore/<id>/inflight.json), which is created by renaming pending.json
// before the first live item moves in every kind (R9-7).
//
// The kernel holds the Swapper: it stages through it and runs it at boot
// phase P0a, before storage opens, then reports the restore boot's steps
// after P2 and P4b (opened, marker_written, applied), a reset boot's after
// P4a (applied, hook_done), or the failure exits (step x, or failed for a
// KindCheckpoint restore). storage.Admin has no staging or swap method
// (COR8-5).
//
// It is L1: it imports storage and its L0 leaves, never the reverse (03 R4).
// Every operation goes through one vfs.FS and, inside a crash-matrix boot,
// carries its writer-table label (06 §6.5.1, core/internal/bootseam).
//
// A KindRewrite intent (M8, §5S.3.13) is F3-04's: this build halts on one
// rather than skip it.
package restoreswap
