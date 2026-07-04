// file: internal/itunes/itl_combined_mutate.go
// version: 1.4.0
// guid: f7a8b9c0-d1e2-3f4a-5b6c-7d8e9f0a1b2c
// last-edited: 2026-07-03
//
// Combined ITL mutation: applies removes, adds, and location patches in a
// single read-modify-write pass. This avoids redundant decrypt/compress
// cycles on the 29MB production ITL.

package itunes

import (
	"fmt"
	"os"
	"strings"
)

// ITLOperationSet holds all pending mutations to apply in one pass.
type ITLOperationSet struct {
	Removes         map[string]bool     // PID hex strings to remove
	Adds            []ITLNewTrack       // New tracks to insert
	LocationUpdates []ITLLocationUpdate // Location changes for existing tracks
	MetadataUpdates []ITLMetadataUpdate // Metadata changes for existing tracks
}

// IsEmpty returns true if there are no operations to apply.
func (ops *ITLOperationSet) IsEmpty() bool {
	return len(ops.Removes) == 0 && len(ops.Adds) == 0 &&
		len(ops.LocationUpdates) == 0 && len(ops.MetadataUpdates) == 0
}

// applyOpsMutate returns the SafeWriteITL mutate closure that applies an
// ITLOperationSet (removes → adds → location → metadata) to a decompressed LE
// payload. *updated is set to the total number of items touched. The closure
// performs no I/O and no header work — header regeneration and the full
// ITLSafetyContract (including the dangling-ref check, now the
// `no-new-dangling-refs` guard) run inside SafeWriteITL / safeEncodeITL
// (TASK-004), which is why the old in-line VerifyITLNoNewDanglingRefsLE gate is
// gone from here.
func applyOpsMutate(ops ITLOperationSet, updated *int) func([]byte) ([]byte, error) {
	return func(decompressed []byte) ([]byte, error) {
		// BE refusal is enforced by SafeWriteITL/safeEncodeITL BEFORE mutate is
		// invoked (the contract chokepoint), so production here is always LE.
		total := 0

		// Phase 1: Removes
		if len(ops.Removes) > 0 {
			var removed int
			decompressed, removed = RemoveTracksByPIDLE(decompressed, ops.Removes)
			total += removed
		}
		// Phase 2: Adds
		if len(ops.Adds) > 0 {
			decompressed = AddTracksLE(decompressed, ops.Adds)
			total += len(ops.Adds)
		}
		// Phase 3: Location patches
		if len(ops.LocationUpdates) > 0 {
			updateMap := make(map[string]string, len(ops.LocationUpdates))
			for _, u := range ops.LocationUpdates {
				updateMap[strings.ToLower(u.PersistentID)] = u.NewLocation
			}
			var patched int
			decompressed, patched = rewriteChunksLE(decompressed, updateMap)
			total += patched
		}
		// Phase 4: Metadata updates (title, artist, album, genre, etc.)
		if len(ops.MetadataUpdates) > 0 {
			var metaUpdated int
			decompressed, metaUpdated = UpdateMetadataLE(decompressed, ops.MetadataUpdates)
			total += metaUpdated
		}
		if updated != nil {
			*updated = total
		}
		return decompressed, nil
	}
}

// ApplyITLOperations reads the ITL file, applies all mutations to the
// decompressed payload in one pass, and writes the result through the
// SafeWriteITL atomic protocol (TASK-004): header counts are regenerated from
// the mutated payload (CRIT-3) and the full ITLSafetyContract runs on both the
// in-memory and re-read bytes before anything reaches disk.
// Order: removes, then adds, then location patches, then metadata.
//
// The optional cfg overrides the contract guardrails. The default (no cfg) is
// the SPEC bounded-delta cap (max 5000 removed tracks / 20% mhoh rewrite); the
// nuclear rebuild and full-export paths pass ForceContractConfig() because they
// INTENTIONALLY remove every track — bounded-delta is explicitly
// Force-overridable for these (SPEC §2). Structural guards never relax.
func ApplyITLOperations(inputPath, outputPath string, ops ITLOperationSet, cfg ...ContractConfig) (*ITLWriteBackResult, error) {
	if ops.IsEmpty() {
		return &ITLWriteBackResult{OutputPath: outputPath}, nil
	}

	contractCfg := contractCfgOrDefault(cfg)
	totalUpdated := 0
	mutate := applyOpsMutate(ops, &totalUpdated)

	// In-place write → full atomic SafeWriteITL protocol (backup + rotation +
	// fsync + re-read contract). A distinct output path (e.g. a caller-managed
	// .tmp that the batcher renames itself) → safe in-memory encode (header
	// regeneration + contract) written to outputPath.
	if inputPath == outputPath {
		if _, err := SafeWriteITL(inputPath, mutate, WithContractConfig(contractCfg)); err != nil {
			return nil, err
		}
		return &ITLWriteBackResult{UpdatedCount: totalUpdated, OutputPath: outputPath}, nil
	}

	raw, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, fmt.Errorf("reading ITL: %w", err)
	}
	// K13: the distinct-output path bypasses SafeWriteITL's sidecar load, so
	// arm the library-identity guard from the INPUT library's sidecar here —
	// otherwise rebuild/export-to-tmp writes escape identity checking.
	if contractCfg.ExpectedIdentity == nil && !contractCfg.AdoptLibrary {
		sidecarID, idErr := LoadLibraryIdentity(inputPath)
		if idErr != nil {
			return nil, fmt.Errorf("ApplyITLOperations: identity sidecar for %s: %w", inputPath, idErr)
		}
		contractCfg.ExpectedIdentity = sidecarID
	}
	outBytes, err := safeEncodeITL(raw, mutate, contractCfg)
	if err != nil {
		return nil, err
	}
	if err := writeFileSync(outputPath, outBytes); err != nil {
		return nil, fmt.Errorf("writing ITL: %w", err)
	}
	fixITLPermissions(outputPath)
	return &ITLWriteBackResult{UpdatedCount: totalUpdated, OutputPath: outputPath}, nil
}

// ApplyITLOperationsInMemory applies the same mutations as ApplyITLOperations
// but returns the resulting ITL bytes instead of writing to disk. It routes
// through safeEncodeITL so the export path also gets header regeneration
// (CRIT-3) and the full ITLSafetyContract (TASK-004). The optional cfg has the
// same Force semantics as ApplyITLOperations (the full-export path passes
// ForceContractConfig() because it strips every template track).
// Used by the partial-export path (Task 033 / ARCH-6-4).
func ApplyITLOperationsInMemory(inputPath string, ops ITLOperationSet, cfg ...ContractConfig) ([]byte, error) {
	raw, err := os.ReadFile(inputPath)
	if err != nil {
		return nil, fmt.Errorf("reading ITL: %w", err)
	}
	return safeEncodeITL(raw, applyOpsMutate(ops, nil), contractCfgOrDefault(cfg))
}
