// file: core/web/packages/api-client/src/runtime/apiKeyExpiry.ts
// version: 0.1.0
// guid: 4d3ca9be-2b31-4660-811c-bc51bb804ff6
// last-edited: 2026-11-22

// Every API key expires (server default 30 days, maximum 1096 days: three
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
 * The server's maximum key lifetime in days (handlers.MaxAPIKeyTTLDays and
 * core's max_ttl), counted from the request: three years as 1096 days, so
 * the three-year anniversary date is pickable even across a leap day.
 */
export const API_KEY_MAX_TTL_DAYS = 1096;

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
 * between tomorrow and API_KEY_MAX_TTL_DAYS out (counted on the calendar).
 *
 * The dialog sends days, not an instant: the server counts them from its own
 * clock, so the latest date is never refused because the browser's clock runs
 * ahead of the server's, and the day count maps straight onto core's
 * duration-only expires_in after the identity flip. The count is chosen so
 * that now + days × 24 h falls on the picked date in the viewer's zone: across
 * a daylight-saving change the plain calendar count can land an hour past
 * midnight on the next day (late evening before spring-forward) or an hour
 * before midnight on the day before (just after midnight before fall-back),
 * so one day less or more is taken when that one lands on the picked date.
 *
 * No whole-day count can hit the spring-forward day itself (23 hours long)
 * from a time in the hour next to midnight; then the count is the one that
 * keeps the key alive through the whole picked date, expiring within an hour
 * after it ends. At the last date before a fall-back, one more day would pass
 * the maximum, so the maximum stands and the key expires within the hour
 * before that date begins.
 */
export function customExpiryDays(value: string, now: Date = new Date()): number | null {
  const calendar = daysUntilDate(value, now);
  if (calendar === null || calendar < 1 || calendar > API_KEY_MAX_TTL_DAYS) return null;
  const candidates = [calendar - 1, calendar, calendar + 1].filter(
    (days) => days >= 1 && days <= API_KEY_MAX_TTL_DAYS
  );
  const landing = (days: number) => toDateInputValue(expiryAfterDays(days, now.getTime()));
  const exact = candidates.find((days) => landing(days) === value);
  if (exact !== undefined) return exact;
  // YYYY-MM-DD strings compare in date order.
  return candidates.find((days) => landing(days) > value) ?? calendar;
}

/** The instant a key created now with an expiry of `days` days expires. */
export function expiryAfterDays(days: number, now: number = Date.now()): Date {
  return new Date(now + days * DAY);
}
