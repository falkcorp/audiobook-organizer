// file: web/src/components/review/spine/rowState.ts
// version: 1.4.0
// guid: 4d17c69b-0e83-4a25-91f6-7b2c8a034e51
// last-edited: 2026-10-07
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
 * Per-row outcome, matching `rowStates` in MetadataReviewDialog (:221).
 *
 * `'pending'` is an EXPLICIT undecided state, not a synonym for absent. It is
 * seeded for every fetched row (:310) and is what both undo paths write back:
 * un-rejecting and un-skipping set `'pending'` rather than deleting the key.
 * Modelling undo as a deletion would work until something iterates the map to
 * count decided rows.
 *
 * `'error'` is deliberately NOT here. The dialog uses that string for toast
 * severities and for `CandidateResult.status`, never for a row state -- an easy
 * one to fold in by pattern-matching on the surrounding code, and it would
 * produce a state that no branch ever sets and every comparison silently misses.
 */
export type RowState = 'pending' | 'applied' | 'rejected' | 'skipped';

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

/**
 * Whether a row has been decided. `'pending'` and `undefined` are both "no".
 *
 * Exists so callers stop writing `state !== undefined`, which counts every
 * seeded row as decided -- the dialog seeds `'pending'` for the whole page on
 * fetch, so that test would report every row decided the moment it loaded.
 */
export function isDecided(state: RowState | undefined): boolean {
  return state === 'applied' || state === 'rejected' || state === 'skipped';
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

/**
 * The apply gate's runtime tolerance: `internal/applygate/evidence.go`
 * RuntimeBlockRatio. A candidate more than 10% off the book's runtime is a
 * runtime_mismatch there, and the gate refuses it. The hide switch uses this
 * together with the warning chip's rule (runtimeHiddenBySwitch below).
 *
 * Mirrors the gate's rule, not RUNTIME_WARN_THRESHOLD_SEC above: that flat
 * ten minutes is the spine's warning chip, and on a 40-hour book it flags a
 * 2% difference the gate calls agreement.
 */
export const RUNTIME_BLOCK_RATIO = 0.1;

/** The runtime inputs `runtimeDiffersFromBook` reads off a review row. */
export interface RuntimeRow {
  book: { duration_seconds?: number; runtime_lower_bound_seconds?: number };
  candidate?: { duration_sec?: number; duration_delta_sec?: number } | null;
}

/**
 * Whether a review row's candidate runtime is KNOWN to differ from the book's
 * beyond the apply gate's tolerance (applygate checkRuntime).
 *
 *  - Complete book runtime (`duration_seconds`) and a candidate runtime:
 *    |book - candidate| / book > 10%, strictly greater, as in the gate.
 *  - Partial runtime (`runtime_lower_bound_seconds`): the true length is at
 *    least the lower bound, so it proves a mismatch in one direction only --
 *    the bound exceeds the candidate by more than 10% of the bound
 *    (applygate lowerBoundContradicts).
 *  - Anything else -- no candidate, no candidate runtime, no book runtime --
 *    is unknown, and unknown is never hidden: it is not evidence of a
 *    mismatch.
 */
export function runtimeDiffersFromBook(r: RuntimeRow): boolean {
  const cand = r.candidate?.duration_sec ?? 0;
  if (cand <= 0) return false;
  const book = r.book.duration_seconds ?? 0;
  if (book > 0) {
    return Math.abs(book - cand) / book > RUNTIME_BLOCK_RATIO;
  }
  const lb = r.book.runtime_lower_bound_seconds ?? 0;
  if (lb > cand) {
    return (lb - cand) / lb > RUNTIME_BLOCK_RATIO;
  }
  return false;
}

/**
 * Whether the "Hide runtime differences" switch hides this row.
 *
 * The union of the two runtime rules: the gate's 10% rule
 * (runtimeDiffersFromBook) and the spine's flat ten-minute warning chip
 * (runtimeDiffers). The owner's rule (2026-09-30): a row showing the
 * "runtime differs" chip must never appear while the switch is on, even when
 * the gate would accept it -- a 53-minute gap on a nine-hour book is under
 * 10% but still warned.
 */
export function runtimeHiddenBySwitch(r: RuntimeRow): boolean {
  return runtimeDiffers(r.candidate?.duration_delta_sec) || runtimeDiffersFromBook(r);
}

/** What a candidate-less row's chip says, and how loud it is. */
export interface NoCandidateLabel {
  label: string;
  color: 'error' | 'warning' | 'default';
  /** Longer explanation for a tooltip. */
  detail: string;
}

/**
 * The chip for a row with no candidate, by the row's status.
 *
 * Every renderer uses this one mapping. The two-column card used to label
 * every candidate-less row that was not `no_match` as
 * `Error: ${error_message || 'Unknown'}` -- but the unreviewable bucket's
 * `no_candidates` and `resolved_no_candidates` rows are not errors and the
 * server never sends an `error_message` for them (it sets one only for
 * `decode_error`), so every one of them read "Error: Unknown".
 */
export function noCandidateLabel(r: {
  status: string;
  error_message?: string;
  fetched_at?: string;
  fallback_deferred?: boolean;
}): NoCandidateLabel {
  const when = r.fetched_at ? ` Last searched ${new Date(r.fetched_at).toLocaleDateString()}.` : '';
  const deferred = r.fallback_deferred
    ? ' A fallback provider lookup was deferred and will be retried.'
    : '';
  switch (r.status) {
    case 'no_match':
      return { label: 'No match found', color: 'default', detail: `No provider returned a match.${when}` };
    case 'no_candidates':
      return {
        label: 'No candidates cached',
        color: 'default',
        detail: `The metadata cache holds no candidate for this book; a refetch or a manual search may find one.${when}${deferred}`,
      };
    case 'resolved_no_candidates':
      return {
        label: 'Reviewed — no candidate left',
        color: 'default',
        detail: `The book was already ruled on and has no stored candidate.${when}`,
      };
    case 'decode_error':
      return {
        label: `Candidate will not decode${r.error_message ? `: ${r.error_message}` : ''}`,
        color: 'error',
        detail: r.error_message || 'The stored candidate could not be decoded.',
      };
    case 'skipped':
      return {
        label: 'Not searched',
        color: 'default',
        detail: 'The candidate fetch did not search this book (marked no match, or no usable title).',
      };
    case 'deferred':
      return {
        label: 'Lookup deferred',
        color: 'warning',
        detail: `The fallback lookup was put off (budget spent or provider held); a later run asks again.${when}`,
      };
    case 'error':
      return {
        label: r.error_message ? `Error: ${r.error_message}` : 'Error (no reason recorded)',
        color: 'error',
        detail: r.error_message || 'The server reported an error for this row without a reason.',
      };
    default:
      return {
        label: r.error_message ? r.error_message : `No candidate (${r.status})`,
        color: r.error_message ? 'error' : 'default',
        detail: r.error_message || `Status: ${r.status}`,
      };
  }
}

/**
 * Width at which the candidates card shows its two columns side by side
 * (CandidatesCard); below it the card stacks them.
 *
 * 700px is the spine's OWN width, not the viewport's -- which is the entire
 * point. The dialog's two-column card is a `Stack direction="row"` with
 * `flex: 1 / flex: 1` and no responsive collapse at any width: put it beside a
 * queue rail on a laptop and both columns squish rather than stacking. A media
 * query cannot fix that, because the window can be wide while the spine is not.
 */
export const SPINE_TWO_COLUMN_MIN = 700;
