// file: web/src/components/review/spine/rowState.ts
// version: 1.0.0
// guid: 4d17c69b-0e83-4a25-91f6-7b2c8a034e51
// last-edited: 2026-08-20
//
// Row-state derivations and formatting, lifted verbatim from
// MetadataReviewDialog so CompareSpine can reuse them rather than reimplement
// them.
//
// Lifting rather than rewriting is the whole point. `getRowSx` and
// `isRowActionable` are behaviour, not state -- they are the two functions in
// the dialog most likely to be reproduced "obviously" during a port and end up
// subtly different, because both contain an asymmetry that looks like an
// oversight and is not (see the notes on each). Everything here is a pure
// function of its arguments, which is what makes it liftable at all.

import type { SxProps, Theme } from '@mui/material/styles';

/**
 * Per-row outcome. `undefined` means "not yet decided", which is a distinct
 * fourth state and not a default worth collapsing into one of these.
 */
export type RowState = 'applied' | 'rejected' | 'skipped' | 'error';

/** Provider chip colours, keyed by candidate `source`. */
export const SOURCE_COLORS: Record<
  string,
  'primary' | 'secondary' | 'success' | 'warning' | 'info'
> = {
  openlibrary: 'primary',
  google_books: 'secondary',
  audible: 'success',
  goodreads: 'warning',
  manual: 'info',
};

/**
 * Row background/opacity for a decided row.
 *
 * ASYMMETRY, PRESERVED DELIBERATELY: `applied` and `skipped` get a background,
 * `rejected` does not -- it falls through to the default. This reads as a
 * missing branch and is not one: a rejected row renders a "Rejected -- click to
 * undo" chip, which is what communicates its state, and dimming the row as well
 * would make the undo affordance the least visible thing on it.
 *
 * If this is ever changed, change it because the rejected-row treatment was
 * reconsidered, not because the branch looked absent.
 */
export function getRowSx(state: RowState | undefined): SxProps<Theme> {
  if (state === 'applied') {
    return { bgcolor: 'success.main', opacity: 0.6, borderRadius: 1, transition: 'all 0.3s' };
  }
  if (state === 'skipped') {
    return {
      bgcolor: 'action.disabledBackground',
      opacity: 0.5,
      borderRadius: 1,
      transition: 'all 0.3s',
    };
  }
  return { borderRadius: 1, transition: 'all 0.3s' };
}

/**
 * Whether a row still accepts actions.
 *
 * ASYMMETRY, PRESERVED DELIBERATELY: `skipped` is still actionable. Skipping
 * means "not now", so the reviewer must be able to come back and apply it in the
 * same session; applying and rejecting are decisions that close the row. This is
 * why the check enumerates the two closed states rather than testing
 * `state !== undefined`, which would compile, read more simply, and silently
 * make every skip permanent.
 */
export function isRowActionable(state: RowState | undefined): boolean {
  return state !== 'applied' && state !== 'rejected';
}

/** `3h 20m`, or `20m` under an hour. */
export function formatDuration(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

/** Binary units, matching what the dialog has always shown. */
export function formatFileSize(bytes: number): string {
  if (bytes >= 1073741824) return `${(bytes / 1073741824).toFixed(1)} GB`;
  if (bytes >= 1048576) return `${(bytes / 1048576).toFixed(0)} MB`;
  return `${(bytes / 1024).toFixed(0)} KB`;
}

/**
 * Score chip colour. Thresholds are the dialog's: >=0.85 success, >=0.6 warning.
 *
 * These are display bands and are NOT the same as the dedup lane's calibrated
 * CERTAIN/HIGH/MEDIUM bands, which come from the backend. Two different things
 * that both colour a chip by score; do not unify them.
 */
export function scoreColor(score: number): 'success' | 'warning' | 'default' {
  if (score >= 0.85) return 'success';
  if (score >= 0.6) return 'warning';
  return 'default';
}

/**
 * Whether a runtime gap is worth warning about.
 *
 * 600 seconds. A ten-minute difference between a local file and a candidate is
 * routinely an abridgement, a different narrator's recording, or the wrong book
 * entirely -- all things the reviewer should look at before applying.
 */
export const RUNTIME_WARN_THRESHOLD_SEC = 600;

export function runtimeDiffers(deltaSec: number | undefined | null): boolean {
  return Math.abs(deltaSec ?? 0) > RUNTIME_WARN_THRESHOLD_SEC;
}
