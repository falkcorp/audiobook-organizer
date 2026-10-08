// file: web/src/components/review/spine/viewMode.ts
// version: 1.0.0
// guid: e06ae98b-88e0-4b7d-8bd5-33b47bdeb9fc
// last-edited: 2026-10-07

/**
 * `compact` and `two-column` are the reviewer's explicit choice, carried over
 * from the dialog's ToggleButtonGroup unchanged. `candidates` (2026-10-07,
 * replacing `auto`) shows every ranked search candidate per book.
 */
export type SpineViewMode = 'compact' | 'two-column' | 'candidates';

/** Any other value -- notably the retired 'auto' -- reads as the default. */
export function normalizeViewMode(v: unknown): SpineViewMode {
  return v === 'two-column' || v === 'candidates' ? v : 'compact';
}
