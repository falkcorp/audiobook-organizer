// file: internal/plugins/dedup/emb_reencode.go
// version: 1.0.1
// guid: 9f4e2a1c-d7b3-4e8f-b5c0-2a1d9e4f7b3c
// last-edited: 2026-07-12

// Package dedup — op dedup.emb-reencode (T021, SPEC 3 §3).
//
// Why this op exists: before T021 all emb:v: blobs were raw float32
// (4 bytes per dimension, no version header — "v0" format).  T021 introduces
// float16+zstd encoding ("v1"), which halves the stored size per vector and
// applies a ~2–3× zstd compression ratio on top, yielding ~3.5–4× total
// reduction (~450 MB disk saved at 50K books × 3072 dims).
//
// This op iterates every emb:v: key, re-writes v0 blobs as v1, and leaves v1
// blobs untouched (idempotent).  A versioned flag `emb_f16_v1_done` is written
// on completion so re-triggering is safe.  The op defaults to dry-run (reports
// counts and the expected compression ratio without writing) — pass
// `{"apply":true}` to commit the re-encoding.
//
// Resumability: a batch-commit pattern writes up to batchSize rows at once, so
// a crash mid-run loses at most batchSize rewrites.  On restart the op re-scans
// from the beginning; v1 rows are skipped in O(1) (first-byte check), so the
// work is proportional to remaining v0 rows only.
//
// Rollback: dual-read is retained forever (decodeVector handles both v0 and v1).
// To revert writes, revert the encodeVector change so new writes are v0 again;
// existing v1 rows remain readable.

package dedup

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"log/slog"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// embReencodeDoneFlag is the versioned key stored in Settings to prevent
// re-running the re-encode after it has completed with apply=true.
// Bump to v2 if re-encoding criteria ever change (e.g. a v2 wire format).
const embReencodeDoneFlag = "emb_f16_v1_done"

// embReencodeBatchSize controls how many rows are committed per PebbleDB batch.
// Larger batches are faster but hold more memory; 512 is a safe default for
// ~3 KB v1 blobs (≈1.5 MB per batch).
const embReencodeBatchSize = 512

// embReencodeParams are the JSON parameters accepted by the op.
type embReencodeParams struct {
	// Apply, if true, writes re-encoded v1 blobs.  Default false (dry-run):
	// the op reports counts and the expected compression ratio only.
	Apply bool `json:"apply"`
}

// embReencodeDef returns the OperationDef for dedup.emb-reencode.
func (p *Plugin) embReencodeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "dedup.emb-reencode",
		Plugin:      "dedup",
		DisplayName: "Re-encode embeddings to float16+zstd (T021)",
		Description: "Rewrites all emb:v: blobs from legacy float32 (v0) to float16+zstd (v1), " +
			"saving ~3.5–4× disk space (~450 MB at 50K books × 3072 dims). " +
			"Dry-run by default (pass apply=true to execute). " +
			"Idempotent: v1 rows are skipped; a versioned flag prevents double-runs.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow, // background maintenance
		ConcurrencyKey:  "dedup.emb-reencode",
		Cancellable:     true,
		Isolate:         false,
		// Re-encoding 50K × 3KB blobs at ~10K rows/s is a ~5-second op; 30 min is generous.
		Timeout: 30 * time.Minute,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
		},
		Run: p.runEmbReencode,
	}
}

// runEmbReencode implements the dedup.emb-reencode op.
func (p *Plugin) runEmbReencode(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	if p.embeddingStore == nil {
		return fmt.Errorf("embedding store not available")
	}
	if p.store == nil {
		return fmt.Errorf("main store not available")
	}

	// --- Parse params ---
	var params embReencodeParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("parse params: %w", err)
		}
	}

	reporter.Logger().Info("emb-reencode start", "apply", params.Apply, "flag", embReencodeDoneFlag)

	// --- Guard: skip if already completed with apply=true ---
	if params.Apply {
		if done, err := p.isFlagSet(embReencodeDoneFlag); err != nil {
			reporter.Logger().Warn("emb-reencode: flag check error (proceeding)", "error", err)
		} else if done {
			reporter.Logger().Info("emb-reencode: already completed; skipping (flag set)",
				"flag", embReencodeDoneFlag)
			_ = reporter.UpdateProgress(1, 1, "Already completed (flag set); nothing to do.")
			return nil
		}
	}

	db := p.embeddingStore.PebbleDB()
	if db == nil {
		return fmt.Errorf("embedding store PebbleDB handle is nil")
	}

	_ = reporter.UpdateProgress(0, 3, "Scanning emb:v: keys…")

	// --- Scan and re-encode ---
	const embVecPfx = "emb:v:" // mirrors the const in embedding_store.go

	prefix := []byte(embVecPfx)
	upper := pebbleUpperBound(prefix)

	iter, err := db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return fmt.Errorf("emb-reencode: open iterator: %w", err)
	}

	var (
		totalRows    int
		v0Rows       int
		v1Rows       int
		totalV0Bytes int64
		totalV1Bytes int64
		batch        []reencodeTarget
	)

	// We collect all targets in memory first (one embRec JSON decode per row).
	// At 50K books, the JSON overhead per row is small (~200 bytes) and the
	// alternative (interleaved read+write) risks iterator invalidation on some
	// PebbleDB iterator implementations.
	type rowInfo struct {
		key      []byte
		oldVBlob []byte
	}
	var rows []rowInfo

	for iter.First(); iter.Valid(); iter.Next() {
		select {
		case <-ctx.Done():
			iter.Close()
			return ctx.Err()
		default:
		}
		if reporter.IsCanceled() {
			iter.Close()
			return context.Canceled
		}

		// embRec is JSON; unmarshal to get the vector blob.
		type embRecPartial struct {
			V []byte `json:"v"`
		}
		var rec embRecPartial
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			logging.Warn(ctx, "emb-reencode: skip malformed row", "key", string(iter.Key()), "error", err)
			continue
		}

		keyCopy := make([]byte, len(iter.Key()))
		copy(keyCopy, iter.Key())
		blobCopy := make([]byte, len(rec.V))
		copy(blobCopy, rec.V)

		rows = append(rows, rowInfo{key: keyCopy, oldVBlob: blobCopy})
	}
	if err := iter.Error(); err != nil {
		iter.Close()
		return fmt.Errorf("emb-reencode: scan error: %w", err)
	}
	iter.Close()

	totalRows = len(rows)
	reporter.Logger().Info("emb-reencode: scan complete", "total_rows", totalRows)
	_ = reporter.UpdateProgress(1, 3, fmt.Sprintf("Scanned %d emb:v: rows; preparing re-encode…", totalRows))

	// --- Build re-encode targets ---
	for _, row := range rows {
		if database.IsVectorV1Exported(row.oldVBlob) {
			v1Rows++
			totalV1Bytes += int64(len(row.oldVBlob))
			continue
		}
		// v0 row: decode float32 then re-encode to v1.
		vec := database.DecodeVectorExported(row.oldVBlob)
		if vec == nil {
			logging.Warn(ctx, "emb-reencode: skip empty vector blob", "key", string(row.key))
			continue
		}
		newBlob := database.EncodeVectorExported(vec)

		totalV0Bytes += int64(len(row.oldVBlob))
		totalV1Bytes += int64(len(newBlob))
		v0Rows++

		batch = append(batch, reencodeTarget{
			key:  row.key,
			newV: newBlob,
		})
	}

	// Calculate ratio from the v0→v1 pairs we've collected.
	ratio := float64(0)
	if totalV0Bytes > 0 && v0Rows > 0 {
		// Recompute purely over the v0 rows: v0 size vs their v1 size.
		v1forV0 := int64(0)
		for _, t := range batch {
			v1forV0 += int64(len(t.newV))
		}
		if v1forV0 > 0 {
			ratio = float64(totalV0Bytes) / float64(v1forV0)
		}
	}

	reporter.Logger().Info("emb-reencode: analysis",
		"total_rows", totalRows,
		"v0_rows", v0Rows,
		"already_v1_rows", v1Rows,
		"compression_ratio", fmt.Sprintf("%.2f×", ratio),
		"v0_bytes_total", totalV0Bytes,
		"apply", params.Apply,
	)

	// v1BytesForV0 is the total compressed size of the newly-encoded v1 blobs
	// (excludes already-v1 rows, which were measured in their existing v1 size).
	v1BytesForV0 := int64(0)
	for _, t := range batch {
		v1BytesForV0 += int64(len(t.newV))
	}
	dryRunSummary := fmt.Sprintf(
		"Scan: %d total rows, %d v0 (to re-encode), %d already v1; "+
			"compression ratio %.2f× (v0 avg %d B → v1 avg %d B)",
		totalRows, v0Rows, v1Rows, ratio,
		safeDiv(totalV0Bytes, int64(max1(v0Rows))),
		safeDiv(v1BytesForV0, int64(max1(v0Rows))),
	)

	if !params.Apply {
		_ = reporter.UpdateProgress(3, 3,
			fmt.Sprintf("Dry-run complete — %d v0 rows would be re-encoded (%.2f× ratio). "+
				"Pass apply=true to execute.", v0Rows, ratio))
		reporter.Logger().Info("emb-reencode: dry-run only; no changes written", "would_reencode", v0Rows)
		return nil
	}

	// --- Apply: write re-encoded rows in batches ---
	_ = reporter.UpdateProgress(2, 3, fmt.Sprintf("Re-encoding %d v0 rows…", v0Rows))

	written := 0
	for batchStart := 0; batchStart < len(batch); batchStart += embReencodeBatchSize {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if reporter.IsCanceled() {
			return context.Canceled
		}

		end := batchStart + embReencodeBatchSize
		if end > len(batch) {
			end = len(batch)
		}
		chunk := batch[batchStart:end]

		if err := p.reencodeChunk(db, chunk); err != nil {
			reporter.Logger().Error("emb-reencode: batch write error", "batch_start", batchStart, "error", err)
			return fmt.Errorf("emb-reencode: batch write: %w", err)
		}
		written += len(chunk)
		reporter.Logger().Debug("emb-reencode: batch committed",
			"written_so_far", written, "total_v0", v0Rows)
	}

	// --- Set versioned completion flag ---
	if err := p.store.SetSetting(embReencodeDoneFlag, "true", "bool", false); err != nil {
		reporter.Logger().Warn("emb-reencode: could not set done flag", "flag", embReencodeDoneFlag, "error", err)
	} else {
		reporter.Logger().Info("emb-reencode: set done flag", "flag", embReencodeDoneFlag)
	}

	_ = reporter.UpdateProgress(3, 3,
		fmt.Sprintf("Complete — %d/%d v0 rows re-encoded to v1 (ratio %.2f×). %s",
			written, v0Rows, ratio, dryRunSummary))
	reporter.Logger().Info("emb-reencode: complete",
		"reencoded", written, "intended", v0Rows,
		"compression_ratio", fmt.Sprintf("%.2f×", ratio))
	return nil
}

// reencodeTarget is a single row to re-encode.
type reencodeTarget struct {
	key  []byte
	newV []byte
}

// reencodeChunk writes a batch of re-encoded rows atomically.
// Each row's JSON is read fresh, the vector blob replaced, and the updated
// JSON written back.  Rows that can't be read are skipped with a warning.
func (p *Plugin) reencodeChunk(db *pebble.DB, chunk []reencodeTarget) error {
	b := db.NewBatch()
	defer b.Close()

	for _, t := range chunk {
		val, closer, err := db.Get(t.key)
		if err == pebble.ErrNotFound {
			continue // deleted between scan and apply — skip
		}
		if err != nil {
			return fmt.Errorf("read row %s: %w", t.key, err)
		}

		// Splice the new vector blob into the raw JSON without a full unmarshal/remarshal
		// of the entire embRec — just replace the "v" field value.  We use a generic
		// map so we don't need to import the embRec type from another package.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(val, &raw); err != nil {
			closer.Close()
			slog.Warn("emb-reencode: skip malformed JSON row", "key", string(t.key), "error", err)
			continue
		}
		closer.Close()

		newVJSON, err := json.Marshal(t.newV)
		if err != nil {
			return fmt.Errorf("marshal new vector blob: %w", err)
		}
		raw["v"] = json.RawMessage(newVJSON)

		newJSON, err := json.Marshal(raw)
		if err != nil {
			return fmt.Errorf("marshal updated row: %w", err)
		}
		if err := b.Set(t.key, newJSON, nil); err != nil {
			return fmt.Errorf("set row %s: %w", t.key, err)
		}
	}

	return b.Commit(pebble.Sync)
}

// pebbleUpperBound returns the smallest key strictly greater than all keys
// with the given prefix.  Mirrors the helper in embedding_store.go but is
// unexported within the dedup plugin package.
func pebbleUpperBound(prefix []byte) []byte {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper[:i+1]
		}
	}
	return nil
}

// safeDiv returns a/b or 0 when b==0.
func safeDiv(a, b int64) int64 {
	if b == 0 {
		return 0
	}
	return a / b
}

// max1 returns v if v>0, else 1 (prevents divide-by-zero in format strings).
func max1(v int) int {
	if v > 0 {
		return v
	}
	return 1
}
