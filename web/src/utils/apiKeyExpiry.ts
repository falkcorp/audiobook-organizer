// file: web/src/utils/apiKeyExpiry.ts
// version: 1.0.0
// guid: b55142f1-0389-400b-9e31-818583595581
// last-edited: 2026-10-07

// Every API key expires (server default 30 days, maximum 365). These are the
// lifetimes the create dialog offers; there is deliberately no "Never": the
// server treats 0 as its 30-day default, so a "Never" choice would lie.
export const API_KEY_EXPIRY_OPTIONS: ReadonlyArray<{ label: string; value: number }> = [
  { label: '30 days', value: 30 },
  { label: '60 days', value: 60 },
  { label: '90 days', value: 90 },
  { label: '180 days', value: 180 },
  { label: '365 days (maximum)', value: 365 },
];

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

/**
 * Short relative label for a key's expiry: "in 8h", "in 29d", "in 3mo",
 * or "Expired" once it has passed. Unlike a "N days ago" formatter it handles
 * the future, which is the normal case for an expiry.
 */
export function formatKeyExpiry(expiresAt: string, now: number = Date.now()): string {
  const ms = new Date(expiresAt).getTime() - now;
  if (Number.isNaN(ms)) return '—';
  if (ms <= 0) return 'Expired';
  if (ms < HOUR) return `in ${Math.max(1, Math.round(ms / 60_000))}m`;
  if (ms < DAY) return `in ${Math.floor(ms / HOUR)}h`;
  const days = Math.floor(ms / DAY);
  if (days < 60) return `in ${days}d`;
  return `in ${Math.floor(days / 30)}mo`;
}
