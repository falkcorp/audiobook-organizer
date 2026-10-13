// file: web/src/components/review/lanes/seriesGroups.test.ts
// version: 1.0.0
// guid: 6e2d8b13-0a97-4c54-b3f1-47d9e5a0c628
// last-edited: 2026-10-13

import { describe, expect, it } from 'vitest';
import type { SeriesDupGroup } from '../../../services/api';
import { asAuthorId } from '../reviewActions';
import {
  NO_SERIES_CHOICES,
  dropSeriesRows,
  hasSeriesMergeWork,
  inMergeAll,
  isSeriesMergeable,
  keptRowOf,
  parseLeftSeriesIds,
  planSeriesMerge,
  renameSeriesRow,
  seriesGroupKey,
  sharedBookIds,
  toSeriesGroup,
  type SeriesChoices,
} from './seriesGroups';

function wire(rows: Array<[number, string, number?]>, match_type = 'exact', name = 'Series A'): SeriesDupGroup {
  return {
    name,
    count: rows.length,
    match_type,
    series: rows.map(([id, n, author]) => ({
      id,
      name: n,
      created_at: '',
      ...(author !== undefined ? { author_id: author, author_name: `Author ${author}` } : {}),
    })),
  };
}

const choices = (c: Partial<SeriesChoices>): SeriesChoices => ({ ...NO_SERIES_CHOICES, ...c });

describe('toSeriesGroup', () => {
  it('keys a group on its sorted ids and orders author-linked rows first, then by id', () => {
    const g = toSeriesGroup(wire([[30, 'c'], [12, 'b', 5], [11, 'a'], [20, 'd', 6]]));
    expect(g.key).toBe('s11-12-20-30');
    expect(g.series.map((r) => r.id)).toEqual([12, 20, 11, 30]);
  });

  it('never produces a key that could equal an author group key', () => {
    expect(seriesGroupKey([2, 1])).toBe('s1-2');
    expect(Number.isNaN(Number(seriesGroupKey([1, 2])))).toBe(true);
  });

  it('reads the match type: subseries is its own kind, anything else is a same-name group', () => {
    expect(toSeriesGroup(wire([[1, 'a'], [2, 'b']], 'subseries')).matchKind).toBe('subseries');
    expect(toSeriesGroup(wire([[1, 'a'], [2, 'b']], 'exact')).matchKind).toBe('exact');
    expect(toSeriesGroup(wire([[1, 'a'], [2, 'b']], 'fuzzy')).matchKind).toBe('exact');
  });

  it('brands the author id and leaves a series with no author as null', () => {
    const g = toSeriesGroup(wire([[1, 'a', 9], [2, 'b']]));
    expect(g.series[0].authorId).toBe(9);
    expect(g.series[1].authorId).toBeNull();
  });

  it('survives a group with no series array', () => {
    expect(toSeriesGroup({ name: 'x', count: 0 } as unknown as SeriesDupGroup).series).toEqual([]);
  });
});

describe('planSeriesMerge', () => {
  const g = toSeriesGroup(wire([[11, 'a', 5], [12, 'b', 5], [13, 'c', 6], [14, 'd']]));

  it('keeps the first included row and merges the rest, in order', () => {
    expect(planSeriesMerge(g, NO_SERIES_CHOICES)).toEqual({
      keepId: 11,
      mergeIds: [12, 13, 14],
      narratorIds: [],
    });
  });

  it('leaving out the first row moves the keep to the next one', () => {
    const plan = planSeriesMerge(g, choices({ excluded: { [g.key]: new Set([11]) } }));
    expect(plan?.keepId).toBe(12);
    expect(plan?.mergeIds).toEqual([13, 14]);
  });

  it('is null with fewer than two rows included', () => {
    expect(planSeriesMerge(g, choices({ excluded: { [g.key]: new Set([11, 12, 13]) } }))).toBeNull();
  });

  it('lists a flagged author once even when two included series share it, and skips left-out series', () => {
    const plan = planSeriesMerge(
      g,
      choices({ narrators: { [g.key]: new Set([5, 6]) }, excluded: { [g.key]: new Set([13]) } })
    );
    expect(plan?.narratorIds).toEqual([asAuthorId(5)]);
  });
});

describe('mergeability', () => {
  const exact = toSeriesGroup(wire([[1, 'a'], [2, 'b']]));
  const sub = toSeriesGroup(wire([[3, 'a'], [4, 'b']], 'subseries'));

  it('a group needs two included rows', () => {
    expect(isSeriesMergeable(exact, NO_SERIES_CHOICES)).toBe(true);
    expect(isSeriesMergeable(exact, choices({ excluded: { [exact.key]: new Set([1]) } }))).toBe(false);
    expect(hasSeriesMergeWork(exact, new Set([2]))).toBe(false);
    expect(hasSeriesMergeWork(exact, new Set())).toBe(true);
  });

  it('Merge All takes same-name groups only', () => {
    expect(inMergeAll(exact, NO_SERIES_CHOICES)).toBe(true);
    expect(isSeriesMergeable(sub, NO_SERIES_CHOICES)).toBe(true);
    expect(inMergeAll(sub, NO_SERIES_CHOICES)).toBe(false);
  });

  it('keptRowOf is the first row not left out', () => {
    expect(keptRowOf(exact, new Set())?.id).toBe(1);
    expect(keptRowOf(exact, new Set([1]))?.id).toBe(2);
    expect(keptRowOf(exact, new Set([1, 2]))).toBeUndefined();
  });
});

describe('in-place edits', () => {
  const a = toSeriesGroup(wire([[11, 'a'], [12, 'b'], [13, 'c']]));
  const b = toSeriesGroup(wire([[21, 'a'], [22, 'b']]));

  it('dropSeriesRows removes rows and keeps the key', () => {
    const next = dropSeriesRows([a, b], a.key, new Set([12]));
    expect(next[0].key).toBe(a.key);
    expect(next[0].series.map((r) => r.id)).toEqual([11, 13]);
    expect(next[1]).toBe(b);
  });

  it('a group left with fewer than two rows is not a duplicate any more and goes', () => {
    expect(dropSeriesRows([a, b], a.key, new Set([12, 13])).map((g) => g.key)).toEqual([b.key]);
  });

  it('is a no-op (same array) when nothing goes', () => {
    const gs = [a, b];
    expect(dropSeriesRows(gs, a.key, new Set())).toBe(gs);
  });

  it('renameSeriesRow renames one row, and returns the same array when the name already matches or the row is gone', () => {
    const next = renameSeriesRow([a, b], 12, 'Renamed');
    expect(next[0].series.map((r) => r.name)).toEqual(['a', 'Renamed', 'c']);
    expect(next[1]).toBe(b);
    const gs = [a, b];
    expect(renameSeriesRow(gs, 12, 'b')).toBe(gs);
    expect(renameSeriesRow(gs, 999, 'x')).toBe(gs);
  });
});

describe('parseLeftSeriesIds', () => {
  it('reads the ids the server left behind', () => {
    expect(parseLeftSeriesIds('series merge left series ids [13 14]: series merge merged 1 of 3')).toEqual(
      new Set([13, 14])
    );
  });

  it('an empty list is an empty set, which means everything merged', () => {
    expect(parseLeftSeriesIds('series merge left series ids []: series merge merged 2 of 2')).toEqual(new Set());
  });

  it('is null for any failure that does not carry a list', () => {
    expect(parseLeftSeriesIds('cancelled')).toBeNull();
    expect(parseLeftSeriesIds('author merge left author ids [3]')).toBeNull();
    expect(parseLeftSeriesIds('')).toBeNull();
  });
});

describe('sharedBookIds', () => {
  it('names the books listed under more than one row', () => {
    const g = toSeriesGroup({
      ...wire([[1, 'a'], [2, 'b']]),
      series: [
        { id: 1, name: 'a', created_at: '', books: [{ id: 'x', title: 'Book One' }, { id: 'y', title: 'Book Two' }] },
        { id: 2, name: 'b', created_at: '', books: [{ id: 'x', title: 'Book One' }] },
      ],
    });
    expect([...sharedBookIds(g)]).toEqual(['x']);
  });
});
