// file: web/src/components/review/lanes/seriesGroups.ts
// version: 1.0.0
// guid: 3b7e91d4-a60c-4f58-8d12-c5e04f9a7b36
// last-edited: 2026-10-13

/**
 * The Series sub-view's model: what a same-name series group is, what a merge of
 * it would do, and how the lane's in-place edits change it. No React and no
 * requests, so every rule here is a plain function a test can call.
 *
 * WHY A SERIES GROUP HAS NO "CANONICAL" ROW
 *
 * An author group arrives with a canonical author and its variants. A series
 * group arrives as N peer rows; the old tab let the reviewer tick the ones to
 * merge and kept the first ticked. This model makes that explicit: every row is
 * either INCLUDED in the merge or left out, and the KEPT row is the first
 * included one in the group's order (author-linked series first, then lowest id),
 * the same pick the server's own dedup makes. The card marks it, so what a merge
 * keeps is on screen before the click rather than a side effect of tick order.
 *
 * WHY THE GROUP KEY NEVER CHANGES
 *
 * The key is the group's series ids as first read. After a partial merge the
 * group has fewer rows, and a key recomputed from them would slide the
 * selection, the left-out choices and the pending mark onto the wrong group (the
 * defect the old tab's index keys had). Rows are removed in place; the key is
 * not touched.
 */

import type { SeriesDupGroup, SeriesBookSummary } from '../../../services/api';
import { asAuthorId, asSeriesId, type AuthorId, type SeriesId } from '../reviewActions';

/** `exact`: the same name once normalised. `subseries`: one name is the other plus a volume suffix. */
export type SeriesMatchKind = 'exact' | 'subseries';

export type SeriesKindFilter = 'all' | SeriesMatchKind;

export interface SeriesRow {
  id: SeriesId;
  name: string;
  /** The author the series is credited to; null when it has none. */
  authorId: AuthorId | null;
  authorName: string;
  /** A preview of the series' books (the server sends at most five). */
  books: SeriesBookSummary[];
}

export interface SeriesGroup {
  /** The ids of the rows as first read, joined; stable across in-place edits. */
  key: string;
  /** The name the scan grouped them under. Does not follow a rename of one row. */
  name: string;
  matchKind: SeriesMatchKind;
  suggestedName?: string;
  /** In keep order: author-linked rows first, then ascending id. */
  series: SeriesRow[];
}

/** `s11-12`: the sorted ids. The `s` keeps it from ever equalling an author group's numeric key. */
export function seriesGroupKey(ids: readonly number[]): string {
  return `s${[...ids].sort((a, b) => a - b).join('-')}`;
}

/** Wire group -> lane group. The one place plain numbers become SeriesIds / AuthorIds. */
export function toSeriesGroup(g: SeriesDupGroup): SeriesGroup {
  const rows: SeriesRow[] = (g.series ?? []).map((s) => ({
    id: asSeriesId(s.id),
    name: s.name,
    authorId: s.author_id != null ? asAuthorId(s.author_id) : null,
    authorName: s.author_name ?? '',
    books: s.books ?? [],
  }));
  rows.sort((a, b) => Number(b.authorId !== null) - Number(a.authorId !== null) || a.id - b.id);
  return {
    key: seriesGroupKey(rows.map((r) => r.id)),
    name: g.name,
    matchKind: g.match_type === 'subseries' ? 'subseries' : 'exact',
    suggestedName: g.suggested_name || undefined,
    series: rows,
  };
}

/** What the reviewer has decided about a group, keyed by group key. */
export interface SeriesChoices {
  /** Series ids left out of their group's merge. */
  excluded: Readonly<Record<string, ReadonlySet<number>>>;
  /** Author ids flagged as narrators: reclassified before the merge runs. */
  narrators: Readonly<Record<string, ReadonlySet<number>>>;
}

export const NO_SERIES_CHOICES: SeriesChoices = { excluded: {}, narrators: {} };

const NONE: ReadonlySet<number> = new Set<number>();

/** The rows of `group` that a merge would take part in. */
export function includedRows(group: SeriesGroup, choices: SeriesChoices): SeriesRow[] {
  const out = choices.excluded[group.key] ?? NONE;
  return group.series.filter((r) => !out.has(r.id));
}

/**
 * The row a merge of this group keeps: the first one not left out. Takes the
 * group's own left-out set (not the whole choices), so a card can ask the
 * question from the two values it is handed.
 */
export function keptRowOf(group: SeriesGroup, excluded: ReadonlySet<number>): SeriesRow | undefined {
  return group.series.find((r) => !excluded.has(r.id));
}

export interface SeriesMergePlan {
  /** The row kept: the first included one. */
  keepId: SeriesId;
  /** The rows merged into it, in group order. */
  mergeIds: SeriesId[];
  /** Distinct authors flagged as narrators among the included rows. */
  narratorIds: AuthorId[];
}

/**
 * What a merge of this group would do, or null when there is nothing to merge
 * (fewer than two rows included). Read when the merge RUNS, not when it was
 * clicked, so a choice made while it waited in the queue is honoured.
 */
export function planSeriesMerge(group: SeriesGroup, choices: SeriesChoices): SeriesMergePlan | null {
  const rows = includedRows(group, choices);
  if (rows.length < 2) return null;
  const flagged = choices.narrators[group.key] ?? NONE;
  const narratorIds: AuthorId[] = [];
  for (const r of rows) {
    if (r.authorId !== null && flagged.has(r.authorId) && !narratorIds.includes(r.authorId)) {
      narratorIds.push(r.authorId);
    }
  }
  return { keepId: rows[0].id, mergeIds: rows.slice(1).map((r) => r.id), narratorIds };
}

/** Whether at least two of the group's rows are still in its merge (given its own left-out set). */
export function hasSeriesMergeWork(group: SeriesGroup, excluded: ReadonlySet<number>): boolean {
  let n = 0;
  for (const r of group.series) if (!excluded.has(r.id) && ++n >= 2) return true;
  return false;
}

/** A group a Merge button applies to, given the reviewer's current choices. */
export function isSeriesMergeable(group: SeriesGroup, choices: SeriesChoices): boolean {
  return hasSeriesMergeWork(group, choices.excluded[group.key] ?? NONE);
}

/**
 * Whether Merge All takes this group in: a same-name group with something to
 * merge. Sub-series groups ("Series" next to "Series, Book 2") are judgement
 * calls about which name wins, so they are merged one at a time, never in bulk
 * (the old tab's Merge All did not take them either).
 */
export function inMergeAll(group: SeriesGroup, choices: SeriesChoices): boolean {
  return group.matchKind === 'exact' && isSeriesMergeable(group, choices);
}

/**
 * Removes `gone` rows from one group, and the group itself once fewer than two
 * rows are left (one series is not a duplicate of anything). Other groups, and
 * the group's key, are untouched.
 */
export function dropSeriesRows(gs: SeriesGroup[], key: string, gone: ReadonlySet<number>): SeriesGroup[] {
  if (gone.size === 0) return gs;
  return gs.flatMap((g) => {
    if (g.key !== key) return [g];
    const rest = g.series.filter((r) => !gone.has(r.id));
    return rest.length < 2 ? [] : [{ ...g, series: rest }];
  });
}

/** Renames one row in place. */
export function renameSeriesRow(gs: SeriesGroup[], seriesId: number, name: string): SeriesGroup[] {
  let changed = false;
  const next = gs.map((g) => {
    if (!g.series.some((r) => r.id === seriesId && r.name !== name)) return g;
    changed = true;
    return { ...g, series: g.series.map((r) => (r.id === seriesId ? { ...r, name } : r)) };
  });
  return changed ? next : gs;
}

/**
 * The ids a failed dedup.series-merge run says it left behind. The server writes
 * them in this exact form (SeriesMergeFailedIDsPrefix in
 * internal/server/duplicates_ops.go); change both together. The list may be empty
 * (everything merged and a later step failed). Null when the message does not
 * carry a list at all (any other failure, including a cancelled run).
 */
export function parseLeftSeriesIds(message: string): Set<number> | null {
  const m = /series merge left series ids \[([0-9 ]*)\]/.exec(message);
  if (!m) return null;
  return new Set(
    m[1]
      .split(' ')
      .filter(Boolean)
      .map(Number)
  );
}

/** Books that appear under more than one row of the group: the clearest sign two rows are one series. */
export function sharedBookIds(group: SeriesGroup): ReadonlySet<string> {
  const seen = new Map<string, number>();
  for (const r of group.series) {
    for (const b of r.books) seen.set(b.id, (seen.get(b.id) ?? 0) + 1);
  }
  return new Set([...seen].filter(([, n]) => n > 1).map(([id]) => id));
}
