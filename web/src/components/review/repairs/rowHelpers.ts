// file: web/src/components/review/repairs/rowHelpers.ts
// version: 1.3.1
// guid: 21e5fc7d-cd22-4c0a-9773-b83015403a68
// last-edited: 2026-10-23

/**
 * Pure helpers shared by the three repairs row views (Compact, Details,
 * Grouped). No React here, so the views and their tests can use them freely.
 */

import type { RepairRow, RepairRowMember } from '../../../services/api';

/** True for the skipped tab and for any one-kind view of it (owner rows are skipped rows). */
export function isSkippedFilter(filter: string): boolean {
  return filter === 'skipped' || filter === 'owner_applicable' || filter.startsWith('skipped:');
}

/**
 * Whether a row may be ticked: never a skipped row, and never one an apply
 * already settled (the stored trial still lists it, the library no longer
 * matches it). The skipped tab offers no checkboxes at all.
 */
export function isSelectableRow(row: RepairRow, settledRowIds: ReadonlySet<string>): boolean {
  return !row.skipped && !settledRowIds.has(row.row_id);
}

/** The words a row is explained by on its tab: the skip reason on the skipped tab. */
export function rowReason(row: RepairRow, skippedTab: boolean): string {
  if (skippedTab) return row.skip_reason || row.skipped || row.reason;
  return row.reason;
}

const NONE = '(none)';

function shown(v: string | undefined): string {
  return v === undefined || v === '' ? NONE : v;
}

/** Every field the row names, current or proposed, sorted. */
export function rowFieldKeys(row: RepairRow): string[] {
  return [...new Set([...Object.keys(row.current ?? {}), ...Object.keys(row.proposed ?? {})])].sort();
}

/**
 * One line naming what the row changes: `series: A → (none); position: 3 → 4`.
 * Only changed fields are listed; a row naming fields but changing none says
 * "no change", and a row naming no fields at all reads "—".
 */
export function changeSummary(row: RepairRow): string {
  const cur = row.current ?? {};
  const prop = row.proposed ?? {};
  const keys = rowFieldKeys(row);
  if (keys.length === 0) return '—';
  const parts = keys
    .filter((k) => shown(cur[k]) !== shown(prop[k]))
    .map((k) => `${k}: ${shown(cur[k])} → ${shown(prop[k])}`);
  return parts.length > 0 ? parts.join('; ') : 'no change';
}

/** The value the Details "After" column shows for a field. */
export function afterValue(row: RepairRow, key: string): string {
  return shown(row.proposed?.[key]);
}

/** The value the Details "Now" column shows for a field. */
export function nowValue(row: RepairRow, key: string): string {
  return shown(row.current?.[key]);
}

/** Every book of the row; rows without member detail fall back to their ids. */
export function rowMembers(row: RepairRow): RepairRowMember[] {
  return row.members && row.members.length > 0
    ? row.members
    : row.book_ids.map((id) => ({ book_id: id, files: 0 }));
}

/** Seconds as h:mm ("" when unknown). */
export function formatHMM(seconds: number | undefined | null): string {
  if (seconds == null || !Number.isFinite(seconds) || seconds <= 0) return '';
  const total = Math.round(seconds / 60);
  const h = Math.floor(total / 60);
  const m = total % 60;
  return `${h}:${String(m).padStart(2, '0')}`;
}

/** A group of rows on the page that share a class and a reason (the same fix). */
export interface RepairRowGroup {
  key: string;
  rowClass: string | undefined;
  reason: string;
  rows: RepairRow[];
}

/**
 * Groups the page's rows by class and reason, biggest group first; groups of
 * equal size keep the order their first row had on the page.
 */
export function groupRows(rows: RepairRow[], bySkipReason = false): RepairRowGroup[] {
  const byKey = new Map<string, RepairRowGroup>();
  for (const r of rows) {
    // On the skipped tab the rows show why they were skipped, so the groups
    // follow that, not the reason a fix was proposed.
    const reason = bySkipReason ? r.skip_reason || r.skipped || r.reason : r.reason;
    const key = `${r.class ?? ''}\u0000${reason}`;
    let g = byKey.get(key);
    if (!g) {
      g = { key, rowClass: r.class, reason, rows: [] };
      byKey.set(key, g);
    }
    g.rows.push(r);
  }
  const groups = [...byKey.values()];
  // Array.prototype.sort is stable, so ties keep page order.
  return groups.sort((a, b) => b.rows.length - a.rows.length);
}

/** "A · B · C · +N more" over a group's titles. */
export function titlesPreview(rows: RepairRow[], max = 5): string {
  const titles = rows.slice(0, max).map((r) => r.title || r.book_ids[0] || r.row_id);
  const more = rows.length - titles.length;
  return more > 0 ? `${titles.join(' · ')} · +${more} more` : titles.join(' · ');
}

/** Words for a fixer's row class, falling back to the class id. */
export function classLabel(c: string): string {
  return CLASS_LABELS[c] ?? c;
}

/** Display names of fixer row flags (Row.flags); an unknown flag shows as itself. */
const FLAG_LABELS: Record<string, string> = {
  combine_undo_aware: 'From a combine that can still be undone',
};

export function flagLabel(f: string): string {
  return FLAG_LABELS[f] ?? f;
}

/** The rows-endpoint filter that pages the rows carrying flag `f`. */
export function flagFilter(f: string): `flag:${string}` {
  return `flag:${f}`;
}

/** True for a one-flag view (any tab: the rows are applicable and skipped alike). */
export function isFlagFilter(filter: string): boolean {
  return filter.startsWith('flag:');
}

const CLASS_LABELS: Record<string, string> = {
  moved: 'Moved',
  copy: 'Copy',
  'hash-copy': 'Single-file copy of a track (same file hash)',
  'no-parent': 'No parent',
  'manual-only': 'Manual only',
  ambiguous: 'Ambiguous',
  held: 'Held',
  carry: 'Finish an interrupted repair (move its files off a merged survivor)',
  'existing-book': 'Existing book (never a second copy)',
  unplaced: 'Unplaced fragment (no chapter group)',
  relink: 'Relink (history agrees or none)',
  'held-cleared-by-history': 'Cleared (history)',
  'held-name-mismatch': 'Name mismatch',
  'held-series-id-mismatch': 'Series id ≠ stored object',
  'name-match': 'Name match',
  orphan: 'Orphan',
  duplicate_link: 'Combined credit beside its authors',
  combined_only: 'Only the combined credit',
  partial_link: 'Some of its authors credited',
  split_new_authors: 'Creates a missing author',
  by_prefix: 'Byline ("By: ...") removed',
  single_word_name: 'Single-word pen name',
  // maintenance.relink-pre-fix-imports: a link row is listed under the first
  // evidence check it fails (missing, location, format, dates), so each chip's
  // count opens the rows that check put there. A row failing several checks is
  // counted once; its evidence names the rest.
  would_link: 'Link: every check agrees (low risk)',
  would_link_review_missing: 'Link, review: file marked missing',
  would_link_review_location: 'Link, review: file not in an import folder',
  would_link_review_format: 'Link, review: format disagrees',
  would_link_review_dates: 'Link, review: import and file record dated far apart',
  same_book_version: 'Same-book link (another file of this torrent is already linked): checks agree',
  same_book_version_review_missing: 'Same-book link, review: file marked missing',
  same_book_version_review_location: 'Same-book link, review: file not in an import folder',
  same_book_version_review_format: 'Same-book link, review: format disagrees',
  same_book_version_review_dates: 'Same-book link, review: import and file record dated far apart',
  error: 'Error',
};

/** "parent · 3 files · 1 missing" for a member. */
export function memberCounts(m: { role?: string; files: number; missing_files?: number }): string {
  return [m.role, m.files ? `${m.files} file${m.files === 1 ? '' : 's'}` : '', m.missing_files ? `${m.missing_files} missing` : '']
    .filter(Boolean)
    .join(' · ');
}
