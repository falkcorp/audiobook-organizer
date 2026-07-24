// file: internal/itunes/relocate_sync_cycle.go
// version: 1.0.0
// guid: 6c2f9a81-7b54-4d60-9e18-3c7b5a0e2d73
// last-edited: 2026-07-24
//
// P2 of the 2-way-sync system design: the relocate-only sync cycle (the MVP write
// path). It composes the primitives verified/built in P0-P1:
//
//   1. PLAN     — ComputeRelocateOps: repoint each iTunes-linked book_file's track
//                 at the file's current canonical location. Location-only: 0 adds,
//                 0 removes, by construction (relocate.go).
//   2. GUARD    — SafeWriteITL's ContractConfig, armed for the AO library: F7 guard
//                 scope (AllowedWritebackRoot, #2045), K13 identity (the library
//                 fingerprint), K14 magnitude (PartitionedTrackCount, #2044), and a
//                 pre-rename SHA re-verify (FileSHA256 of exactly what we read).
//   3. VERIFY   — VerifyRelocateWrite (#2043): a per-track RAW-BYTE oracle proving
//                 ONLY the planned tracks changed, and only their location.
//   4. COMMIT   — gated behind cfg.Apply. Dry-run (default) computes the plan, applies
//                 it IN MEMORY, and runs the oracle WITHOUT writing — so the exact
//                 effect (incl. every new location) is inspectable before any write.
//                 Apply commits via ApplyITLOperations (backup + atomic rename), then
//                 RE-verifies and auto-rolls-back from the .bak on any oracle failure.
//
// SAFETY: single-flight only — never run concurrently with a manual relocate,
// pid-repair, or cleanup (all mutate the same .itl and SafeWriteITL's backup/rename
// is not concurrency-safe). The caller holds that lock.
//
// ⚠️ PRE-APPLY VERIFICATION (do NOT set Apply=true until this is checked): the
// relocate points each track at BookFile.FilePath's canonical WinPath. Confirm via a
// real DRY-RUN that those targets MATCH where the AO library's media physically lives
// — in particular whether they preserve the AO library's own `.itunes-writeback/iTunes
// Media/` root (F7) or canonicalize it away. A plan that strips the real media root
// would repoint tracks at non-existent files. The oracle proves "only location
// changed"; it does NOT prove the new location is correct. That is what the dry-run
// review is for.

package itunes

import (
	"fmt"
	"os"
	"strings"
)

// SyncCycleConfig configures one relocate sync cycle.
type SyncCycleConfig struct {
	// ITLPath is the AO writeback .itl to sync.
	ITLPath string
	// AllowedWritebackRoot scopes the location-form guard to the AO library's own
	// media root (F7, #2045). Required when the AO library legitimately lives under
	// a `.itunes-writeback/` directory; empty = strict guard.
	AllowedWritebackRoot string
	// Mappings map BookFile.FilePath (Linux) to the WinPath iTunes stores.
	Mappings []PathMapping
	// MaxDriftPct is the identity-refresh drift ceiling on Apply (0 = default 25).
	MaxDriftPct int
	// Apply commits the write. FALSE (default) = dry-run: plan + in-memory verify
	// only, nothing is written. Never set true before a dry-run has been reviewed.
	Apply bool
}

// SyncCycleResult reports one cycle.
type SyncCycleResult struct {
	Planned        int `json:"planned"`         // location updates in the plan (0 adds/0 removes)
	AlreadyCorrect int `json:"already_correct"` // matched tracks already at the wanted location
	Unmatched      int `json:"unmatched"`       // book_files whose PID is absent from the .itl
	Unmappable     int `json:"unmappable"`      // book_files whose FilePath can't canonicalize

	OracleOK          bool              `json:"oracle_ok"`          // the in-memory (pre-commit) oracle verdict
	RelocatedVerified int               `json:"relocated_verified"` // tracks the oracle confirmed changed location-only
	OracleViolations  []OracleViolation `json:"oracle_violations,omitempty"`

	Applied    bool   `json:"applied"` // true only if committed
	DryRun     bool   `json:"dry_run"` // true if no write was attempted
	BackupPath string `json:"backup_path,omitempty"`
	RolledBack bool   `json:"rolled_back"` // committed then reverted from .bak (post-commit oracle failure)
}

// RunRelocateSyncCycle runs one relocate sync cycle against the AO library. Dry-run
// unless cfg.Apply. Returns an error only on I/O / contract failure; a clean dry-run
// with oracle violations returns a result (OracleOK=false), not an error.
func RunRelocateSyncCycle(store RebuildStore, cfg SyncCycleConfig) (*SyncCycleResult, error) {
	if cfg.ITLPath == "" {
		return nil, fmt.Errorf("sync cycle: ITLPath required")
	}

	// 1. PLAN (read-only).
	ops, preview, err := ComputeRelocateOps(store, cfg.ITLPath, cfg.Mappings)
	if err != nil {
		return nil, fmt.Errorf("sync cycle: compute relocate ops: %w", err)
	}
	res := &SyncCycleResult{
		Planned: len(ops.LocationUpdates),
		DryRun:  !cfg.Apply,
	}
	if preview != nil {
		res.AlreadyCorrect = preview.AlreadyCorrect
		res.Unmatched = preview.UnmatchedFiles
		res.Unmappable = preview.Unmappable
	}
	if len(ops.LocationUpdates) == 0 {
		res.OracleOK = true // nothing to do — trivially clean
		return res, nil
	}

	// 2. Arm the contract for the AO library. Identity + magnitude + F7 scope + a
	//    pre-rename SHA re-verify pinned to EXACTLY the bytes we just read.
	rawBefore, err := os.ReadFile(cfg.ITLPath)
	if err != nil {
		return nil, fmt.Errorf("sync cycle: read %s: %w", cfg.ITLPath, err)
	}
	hdr, before, err := decodeITLForContractFile(cfg.ITLPath)
	if err != nil {
		return nil, fmt.Errorf("sync cycle: decode %s: %w", cfg.ITLPath, err)
	}
	identity, err := ComputeLibraryIdentity(before, hdr)
	if err != nil {
		return nil, fmt.Errorf("sync cycle: compute identity: %w", err)
	}
	identity.FileSHA256 = FileSHA256Hex(rawBefore) // K17: reject if the file changes before our rename
	ab, nonAB, err := PartitionedTrackCount(cfg.ITLPath)
	if err != nil {
		return nil, fmt.Errorf("sync cycle: partitioned count: %w", err)
	}
	contractCfg := ContractConfig{
		AllowedWritebackRoot: cfg.AllowedWritebackRoot,
		ExpectedIdentity:     identity,
		ExpectedTrackCount:   ab + nonAB,
	}

	// 3. VERIFY in memory (no write): apply the plan through the real contract path,
	//    decrypt the result, and run the oracle. This never touches disk.
	relocatedPIDs := relocatedPIDSet(*ops)
	encodedAfter, err := ApplyITLOperationsInMemory(cfg.ITLPath, *ops, contractCfg)
	if err != nil {
		// Contract rejected the in-memory result — surface it; nothing was written.
		return res, fmt.Errorf("sync cycle: in-memory apply rejected by contract: %w", err)
	}
	after, err := DecryptAndInflateITL(encodedAfter)
	if err != nil {
		return nil, fmt.Errorf("sync cycle: decrypt in-memory result: %w", err)
	}
	verdict, err := VerifyRelocateWrite(before, after, relocatedPIDs)
	if err != nil {
		return nil, fmt.Errorf("sync cycle: oracle: %w", err)
	}
	res.OracleOK = verdict.OK
	res.RelocatedVerified = verdict.RelocatedVerified
	res.OracleViolations = verdict.Violations

	// 4. COMMIT — only on Apply AND a clean pre-commit oracle. Dry-run stops here.
	if !cfg.Apply || !verdict.OK {
		return res, nil
	}

	writeRes, err := ApplyITLOperations(cfg.ITLPath, cfg.ITLPath, *ops, contractCfg)
	if err != nil {
		return res, fmt.Errorf("sync cycle: commit: %w", err)
	}
	res.Applied = true
	res.DryRun = false
	if writeRes != nil {
		res.BackupPath = writeRes.BackupPath
	}

	// 5. POST-COMMIT re-verify (defense in depth): re-read the committed file and
	//    re-run the oracle. Any violation → auto-rollback from the .bak.
	rawAfterCommit, rerr := os.ReadFile(cfg.ITLPath)
	if rerr == nil {
		if committed, derr := DecryptAndInflateITL(rawAfterCommit); derr == nil {
			if v2, verr := VerifyRelocateWrite(before, committed, relocatedPIDs); verr == nil && !v2.OK {
				res.OracleViolations = v2.Violations
				res.OracleOK = false
				if res.BackupPath != "" {
					if rbErr := restoreITLBackup(res.BackupPath, cfg.ITLPath); rbErr == nil {
						res.RolledBack = true
						res.Applied = false
					} else {
						return res, fmt.Errorf("sync cycle: post-commit oracle FAILED and rollback FAILED (%v) — .itl may be bad; restore %s manually", rbErr, res.BackupPath)
					}
				}
				return res, fmt.Errorf("sync cycle: post-commit oracle failed; rolled back from %s", res.BackupPath)
			}
		}
	}

	// 6. Success — refresh the identity sidecar so the next cycle's K13/K14 track the
	//    committed state (pin PID, drift ceiling). Best-effort: a refresh failure does
	//    not invalidate the committed, oracle-verified write.
	if _, _, refErr := RefreshLibraryIdentity(cfg.ITLPath, RefreshOptions{MaxDriftPct: cfg.MaxDriftPct}); refErr != nil {
		res.OracleViolations = append(res.OracleViolations, OracleViolation{
			Kind:   "sidecar-refresh-warning",
			Detail: refErr.Error(),
		})
	}
	return res, nil
}

// relocatedPIDSet returns the lower-hex PID set targeted by the plan's location
// updates — the set the oracle is told to expect changes on.
func relocatedPIDSet(ops ITLOperationSet) map[string]bool {
	out := make(map[string]bool, len(ops.LocationUpdates))
	for _, u := range ops.LocationUpdates {
		out[strings.ToLower(u.PersistentID)] = true
	}
	return out
}

// restoreITLBackup copies a SafeWriteITL .bak back over the live path (auto-rollback).
func restoreITLBackup(backupPath, livePath string) error {
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("read backup %s: %w", backupPath, err)
	}
	if err := os.WriteFile(livePath, data, 0o664); err != nil {
		return fmt.Errorf("restore %s: %w", livePath, err)
	}
	return nil
}
