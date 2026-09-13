// file: web/src/components/activity/compactDays.ts
// version: 1.1.0
// guid: 4e8b2c61-9a3d-4f07-b5e1-7c2d8a6f3b90
// last-edited: 2026-09-13

/**
 * Largest day count the compaction accepts. Mirrors maintenance.MaxCompactDays
 * (internal/plugins/maintenance/compact_activity_log.go); keep them equal. Above
 * a bound like this time.Now().AddDate(0, 0, -days) overflows and wraps to a
 * cutoff near NOW, which compacts everything.
 */
export const MAX_COMPACT_DAYS = 36500;

/** Inline error shown when the custom compaction day count is not usable. */
export const COMPACT_DAYS_ERROR = `Enter a whole number of days from 1 to ${MAX_COMPACT_DAYS}`;

/**
 * Parses the Activity Log "Custom days" compaction box.
 *
 * Returns the day count only for a plain whole number from 1 to
 * MAX_COMPACT_DAYS, else null. Until 2026-09-13 the box ran parseInt, so
 * "1.75" compacted everything older than ONE day — and compaction is
 * irreversible (entries collapse into daily digests). Anything that is not
 * exactly digits — decimals, exponents ("1e3"), signs, surrounding
 * whitespace, empty — is refused rather than reinterpreted, so the number
 * that runs is always the number the user typed.
 */
export function parseCompactDays(raw: string): number | null {
  if (!/^\d+$/.test(raw)) return null;
  const n = Number(raw);
  return Number.isSafeInteger(n) && n >= 1 && n <= MAX_COMPACT_DAYS ? n : null;
}
