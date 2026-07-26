// file: web/src/utils/mediaFormat.ts
// version: 1.0.0
// guid: 5b1e9c30-7a42-4d68-9f0c-2e6b3d8a1c74
// last-edited: 2026-07-26

// Shared human-readable formatters for media file metadata (byte sizes, durations).
// Extracted from the dedup compare view so the review-queue cards render identical
// units without a third copy of the same logic. Keep these pure and dependency-free.

/** formatBytes renders a byte count as B / KB / MB. Returns '' for null/undefined. */
export function formatBytes(bytes: number | undefined | null): string {
  if (bytes == null) return '';
  if (bytes < 1024) return `${bytes}B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)}KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)}MB`;
}

/** formatDuration renders a second count as "Hh Mm" (or "Mm" under an hour).
 *  Returns '' for null/undefined. */
export function formatDuration(seconds: number | undefined | null): string {
  if (seconds == null) return '';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}
