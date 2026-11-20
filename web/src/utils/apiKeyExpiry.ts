// file: web/src/utils/apiKeyExpiry.ts
// version: 1.0.0
// guid: b55142f1-0389-400b-9e31-818583595581
// last-edited: 2026-10-07

// Every API key expires (server default 30 days, maximum 1095 days: three
// years). These are the lifetimes the create dialog offers beside a custom
// date; there is deliberately no "Never": the server treats 0 as its 30-day
// default, so a "Never" choice would lie.
export const API_KEY_EXPIRY_OPTIONS: ReadonlyArray<{ label: string; value: number }> = [
  { label: '30 days', value: 30 },
  { label: '60 days', value: 60 },
  { label: '90 days', value: 90 },
  { label: '180 days', value: 180 },
  { label: '365 days', value: 365 },
];

/**
 * The server's maximum key lifetime in days (handlers.MaxAPIKeyTTLDays):
 * three years of 365 days, counted from the request. Not three calendar
 * years, which can be 1096 days and would offer a date the server refuses.
 */
export const API_KEY_MAX_TTL_DAYS = 1095;

/** The create dialog's Select value for "pick a date". */
export const API_KEY_CUSTOM_EXPIRY = 'custom';

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

const pad2 = (n: number) => String(n).padStart(2, '0');

/** A local calendar date as the YYYY-MM-DD a date input uses. */
function toDateInputValue(d: Date): string {
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

/**
 * The earliest and latest dates the custom-expiry picker offers, as
 * YYYY-MM-DD in the viewer's time zone: tomorrow, and today plus
 * API_KEY_MAX_TTL_DAYS.
 */
export function customExpiryBounds(now: Date = new Date()): { min: string; max: string } {
  const at = (days: number) =>
    toDateInputValue(new Date(now.getFullYear(), now.getMonth(), now.getDate() + days));
  return { min: at(1), max: at(API_KEY_MAX_TTL_DAYS) };
}

/**
 * Whole calendar days from today (in the viewer's time zone) to a YYYY-MM-DD
 * date, or null when the value is not a real date. Counted on the calendar,
 * so a daylight-saving change in between does not shift it by an hour.
 */
export function daysUntilDate(value: string, now: Date = new Date()): number | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!m) return null;
  const [y, mo, d] = [Number(m[1]), Number(m[2]) - 1, Number(m[3])];
  const picked = Date.UTC(y, mo, d);
  const check = new Date(picked);
  // Reject a date that rolled over, such as 2027-02-30.
  if (check.getUTCFullYear() !== y || check.getUTCMonth() !== mo || check.getUTCDate() !== d) {
    return null;
  }
  const today = Date.UTC(now.getFullYear(), now.getMonth(), now.getDate());
  return Math.round((picked - today) / DAY);
}

/**
 * The expires_in_days a custom date asks for, or null when the date is not
 * between tomorrow and API_KEY_MAX_TTL_DAYS out. The dialog sends days, not an
 * instant: the server counts them from its own clock, so the latest date is
 * never refused because the browser's clock runs ahead of the server's.
 */
export function customExpiryDays(value: string, now: Date = new Date()): number | null {
  const days = daysUntilDate(value, now);
  if (days === null || days < 1 || days > API_KEY_MAX_TTL_DAYS) return null;
  return days;
}

/** The instant a key created now with an expiry of `days` days expires. */
export function expiryAfterDays(days: number, now: number = Date.now()): Date {
  return new Date(now + days * DAY);
}
