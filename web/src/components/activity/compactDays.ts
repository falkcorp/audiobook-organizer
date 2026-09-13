// file: web/src/components/activity/compactDays.ts
// version: 1.0.0
// guid: 4e8b2c61-9a3d-4f07-b5e1-7c2d8a6f3b90
// last-edited: 2026-09-13

/** Inline error shown when the custom compaction day count is not usable. */
export const COMPACT_DAYS_ERROR = 'Enter a whole number of days (1 or more)';

/**
 * Parses the Activity Log "Custom days" compaction box.
 *
 * Returns the day count only for a plain whole number of at least 1, else
 * null. Until 2026-09-13 the box ran parseInt, so "1.75" compacted everything
 * older than ONE day — and compaction is irreversible (entries collapse into
 * daily digests). Anything that is not exactly digits — decimals, exponents
 * ("1e3"), signs, surrounding whitespace, empty — is refused rather than
 * reinterpreted, so the number that runs is always the number the user typed.
 */
export function parseCompactDays(raw: string): number | null {
  if (!/^\d+$/.test(raw)) return null;
  const n = Number(raw);
  return Number.isSafeInteger(n) && n >= 1 ? n : null;
}
