// file: web/src/pages/activityUrl.test.ts
// version: 1.0.0
// guid: 2b7e90c4-5f13-4d68-b8a2-71e4c0d93a5f
// last-edited: 2026-11-05

import { describe, expect, it } from 'vitest';
import { activityFiltersDiffer, activityUrlOwned, readActivityUrl } from './activityUrl';

const DEFAULT_SINCE = '2026-11-04T09:00';
const read = (qs: string) => readActivityUrl(new URLSearchParams(qs), DEFAULT_SINCE);

describe('readActivityUrl', () => {
  it('an empty query is the page defaults', () => {
    const s = read('');
    expect(s).toMatchObject({
      op: null,
      search: '',
      typeFilter: '',
      levelFilter: '',
      operationId: '',
      sinceFilter: DEFAULT_SINCE,
      untilFilter: '',
      hideNoOp: true,
      tagFilter: [],
      page: 1,
      pageSize: 25,
    });
    expect([...s.tiers].sort()).toEqual(['audit', 'change', 'digest', 'info']);
  });

  it('since=all is all history; a value is kept', () => {
    expect(read('since=all').sinceFilter).toBe('');
    expect(read('since=2026-10-01T08%3A00').sinceFilter).toBe('2026-10-01T08:00');
  });

  it('a bad page or size falls back instead of reaching the server', () => {
    expect(read('page=0&size=-5').page).toBe(1);
    expect(read('page=abc&size=1.5')).toMatchObject({ page: 1, pageSize: 25 });
  });

  it('tiers and tags are comma lists', () => {
    expect([...read('tiers=audit,debug').tiers].sort()).toEqual(['audit', 'debug']);
    expect(read('tag=a,b').tagFilter).toEqual(['a', 'b']);
  });
});

describe('activityUrlOwned', () => {
  it('a state read from the URL writes back to the same keys', () => {
    const qs =
      'op=op-1&q=Joker&tiers=audit%2Cdebug&type=t&level=l&operation=o&since=all&until=u&hide_noop=0&tag=a%2Cb&page=2&size=50';
    const owned = activityUrlOwned(read(qs), DEFAULT_SINCE);
    expect(owned).toEqual({
      op: 'op-1',
      q: 'Joker',
      tiers: 'audit,debug',
      type: 't',
      level: 'l',
      operation: 'o',
      since: 'all',
      until: 'u',
      hide_noop: '0',
      tag: 'a,b',
      page: '2',
      size: '50',
    });
  });

  it('every default is absent', () => {
    const owned = activityUrlOwned(read(''), DEFAULT_SINCE);
    expect(Object.values(owned).every((v) => v === null || v === '')).toBe(true);
  });
});

describe('activityFiltersDiffer', () => {
  it('ignores the page and the open op, sees every filter', () => {
    const base = read('');
    expect(activityFiltersDiffer(base, read('page=4&op=x'))).toBe(false);
    for (const qs of ['q=a', 'type=t', 'level=l', 'operation=o', 'since=all', 'until=u', 'hide_noop=0', 'tag=a', 'tiers=audit', 'size=50']) {
      expect(activityFiltersDiffer(base, read(qs)), qs).toBe(true);
    }
  });
});
