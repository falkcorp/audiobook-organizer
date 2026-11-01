// file: web/src/components/review/lanes/useAuthorsLane.test.ts
// version: 1.2.1
// guid: a5d29e61-7c04-4b8f-93e2-1f6a80c4b573
// last-edited: 2026-10-30

/**
 * Tests for the Authors lane's data layer.
 *
 * Fixtures are synthetic ("Author A", "Author B"); no real library rows.
 *
 * The reads module is mocked so each test controls WHEN a response lands. The
 * races that matter here (a response after the lane was left, two writes
 * finishing together, a poll outliving the page) are all about ordering, which a
 * mock that resolves immediately cannot express.
 */

import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import { asAuthorId, asSeriesId } from '../reviewActions';
import { ApiAuthRedirectError } from '../../../utils/apiFetch';
import { SESSION_EXPIRED_MESSAGE } from '../../../utils/apiResponse';
import {
  SERIES_RENAME_DEADLINE_MS,
  WRITE_DEADLINE_MS,
  parseLeftIds,
  useAuthorsLane,
} from './useAuthorsLane';
import * as reads from './authorsReads';

vi.mock('../../../services/api');
vi.mock('./authorsReads');

const toast = vi.fn();

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (v: T) => void;
  reject: (e: unknown) => void;
}
function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function wire(
  id: number,
  name: string,
  variants: string[] = [],
  extra: Partial<api.AuthorDedupGroup> = {}
): api.AuthorDedupGroup {
  return {
    canonical: { id, name, created_at: '2026-01-01T00:00:00Z' },
    variants: variants.map((v, i) => ({ id: id * 100 + i + 1, name: v, created_at: '2026-01-01T00:00:00Z' })),
    book_count: 3,
    ...extra,
  };
}

const GROUP_A = () => wire(1, 'Author A', ['Author A.', 'A. Author']);
const GROUP_B = () => wire(2, 'Author B', ['Author B.']);

function serve(groups: api.AuthorDedupGroup[], needsRefresh = false) {
  vi.mocked(reads.fetchAuthorGroups).mockResolvedValue({ groups, needsRefresh });
}

const op = (id: string) => ({ id, type: 'x', status: 'queued' }) as api.Operation;
const settled = (status = 'completed', error_message?: string) =>
  ({ id: 'op', status, error_message: error_message ?? null }) as unknown as api.OperationV2;

const merge = (n: number) => ({
  lane: 'authors' as const,
  entity: 'author' as const,
  type: 'mergeGroup' as const,
  canonicalId: asAuthorId(n),
});

function setup(opts: { active?: boolean; authorId?: string | null; view?: string | null } = {}) {
  return renderHook(
    ({ active }: { active: boolean }) =>
      useAuthorsLane(toast, active, { authorId: opts.authorId ?? null, view: opts.view ?? null }),
    { initialProps: { active: opts.active ?? true } }
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  toast.mockReset();
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.pollOperationV2).mockResolvedValue(settled());
  vi.mocked(api.mergeAuthors).mockImplementation(async (id: number) => op(`m${id}`));
  vi.mocked(api.reclassifyAuthorAsNarrator).mockImplementation(async (id: number) => op(`r${id}`));
  vi.mocked(reads.fetchAuthorName).mockResolvedValue('');
  vi.mocked(api.mergeSeriesGroup).mockImplementation(async (keep: number) => op(`s${keep}`));
  vi.mocked(reads.fetchSeriesGroups).mockResolvedValue({ groups: [], totalSeries: 0, needsRefresh: false });
});

afterEach(() => {
  vi.useRealTimers();
});

describe('authors lane fetches only while active', () => {
  it('makes no request while inactive, one set on activation, and aborts them on leaving', async () => {
    serve([GROUP_A()]);
    const { result, rerender } = setup({ active: false });
    // Let any stray effect run.
    await act(async () => {});
    expect(reads.fetchAuthorGroups).not.toHaveBeenCalled();
    expect(api.getDedupCandidates).not.toHaveBeenCalled();
    expect(result.current.status).toBe('loading');

    // A request that never answers, so its signal is observable.
    const pending = deferred<reads.AuthorGroupsResponse>();
    vi.mocked(reads.fetchAuthorGroups).mockReturnValue(pending.promise);
    rerender({ active: true });
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1));
    expect(api.getDedupCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ entity_type: 'author' }),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    );
    const groupsSignal = vi.mocked(reads.fetchAuthorGroups).mock.calls[0][0];
    const candSignal = vi.mocked(api.getDedupCandidates).mock.calls[0][1]?.signal;
    expect(groupsSignal.aborted).toBe(false);

    rerender({ active: false });
    expect(groupsSignal.aborted).toBe(true);
    expect(candSignal?.aborted).toBe(true);

    // The answer that lands after the lane was left is never painted.
    await act(async () => {
      pending.resolve({ groups: [wire(9, 'Late Author', ['Late A.'])], needsRefresh: false });
    });
    expect(result.current.groups).toEqual([]);
  });

  it('does not duplicate fetches on mount', async () => {
    serve([GROUP_A()]);
    setup();
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalled());
    await act(async () => {});
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);
    expect(api.getDedupCandidates).toHaveBeenCalledTimes(1);
  });
});

describe('loading, error, empty, needs-scan, populated', () => {
  it('walks loading -> populated', async () => {
    const d = deferred<reads.AuthorGroupsResponse>();
    vi.mocked(reads.fetchAuthorGroups).mockReturnValue(d.promise);
    const { result } = setup();
    expect(result.current.status).toBe('loading');
    await act(async () => d.resolve({ groups: [GROUP_A()], needsRefresh: false }));
    expect(result.current.status).toBe('populated');
    expect(result.current.groups.map((g) => g.canonical.name)).toEqual(['Author A']);
  });

  it('a failed request is an error with its message, not an empty list', async () => {
    vi.mocked(reads.fetchAuthorGroups).mockRejectedValue(new Error('boom: 500'));
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('error'));
    expect(result.current.error).toBe('boom: 500');
    expect(result.current.groups).toEqual([]);
  });

  it('a server that answered with no groups is empty', async () => {
    serve([]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('empty'));
  });

  it('a server with no cached scan is needs-scan, which is neither empty nor an error', async () => {
    serve([], true);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('needs-scan'));
    expect(result.current.error).toBeNull();
  });

  it('rows that arrive alongside needs_refresh are shown, never hidden behind the scan prompt', async () => {
    // The server never sends both today (a cold cache is always empty), but the
    // flag is the server's to set; a lane that put "not scanned yet" ahead of real
    // rows would hide data the reviewer is entitled to see.
    serve([GROUP_A()], true);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    expect(result.current.groups).toHaveLength(1);
  });

  it('a retry after an error recovers', async () => {
    vi.mocked(reads.fetchAuthorGroups).mockRejectedValueOnce(new Error('boom'));
    serve([GROUP_A()]);
    // mockResolvedValue after mockRejectedValueOnce: second call resolves.
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('error'));
    act(() => result.current.reload());
    await waitFor(() => expect(result.current.status).toBe('populated'));
  });
});

describe('stale responses', () => {
  it('a slow earlier reload never overwrites a newer one', async () => {
    const first = deferred<reads.AuthorGroupsResponse>();
    const second = deferred<reads.AuthorGroupsResponse>();
    vi.mocked(reads.fetchAuthorGroups).mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const { result } = setup();
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1));
    act(() => result.current.reload());
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2));

    await act(async () => second.resolve({ groups: [wire(2, 'Author B', ['Author B.'])], needsRefresh: false }));
    expect(result.current.groups.map((g) => g.canonical.name)).toEqual(['Author B']);
    // The older answer arrives last and must lose.
    await act(async () => first.resolve({ groups: [wire(1, 'Author A', ['Author A.'])], needsRefresh: false }));
    expect(result.current.groups.map((g) => g.canonical.name)).toEqual(['Author B']);
  });
});

describe('merging', () => {
  it('toggling a variant to narrator reclassifies before merge, and merges only the rest', async () => {
    serve([GROUP_A()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    const calls: string[] = [];
    vi.mocked(api.reclassifyAuthorAsNarrator).mockImplementation(async (id) => {
      calls.push(`reclassify:${id}`);
      return op(`r${id}`);
    });
    vi.mocked(api.mergeAuthors).mockImplementation(async (keep, ids) => {
      calls.push(`merge:${keep}:${ids.join(',')}`);
      return op('m1');
    });

    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'toggleRole',
        canonicalId: asAuthorId(1),
        target: { kind: 'variant', id: asAuthorId(101) },
      })
    );
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(result.current.groups).toEqual([]));

    expect(calls).toEqual(['reclassify:101', 'merge:1:102']);
    expect(toast).toHaveBeenCalledWith('Merged author(s) into "Author A"', 'success');
  });

  it('removing a variant excludes it from the merge, and removing it again puts it back', async () => {
    serve([GROUP_A()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    const exclude = (variantId: number) =>
      act(() =>
        result.current.dispatch({
          lane: 'authors',
          entity: 'author',
          type: 'excludeVariant',
          canonicalId: asAuthorId(1),
          variantId: asAuthorId(variantId),
        })
      );
    exclude(101);
    expect([...(result.current.choices.excluded['1'] ?? [])]).toEqual([101]);
    exclude(101);
    expect([...(result.current.choices.excluded['1'] ?? [])]).toEqual([]);
    exclude(102);

    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(result.current.groups).toEqual([]));
    expect(api.mergeAuthors).toHaveBeenCalledWith(1, [101]);
  });

  it('reads the choices when the merge RUNS: an exclusion made while it was queued is honoured', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    const gate = deferred<api.OperationV2>();
    vi.mocked(api.pollOperationV2).mockReturnValueOnce(gate.promise);

    act(() => result.current.dispatch(merge(2))); // runs first, blocked on its poll
    act(() => result.current.dispatch(merge(1))); // queued behind it
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));
    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'excludeVariant',
        canonicalId: asAuthorId(1),
        variantId: asAuthorId(101),
      })
    );
    await act(async () => gate.resolve(settled()));
    await waitFor(() => expect(result.current.groups).toEqual([]));
    expect(vi.mocked(api.mergeAuthors).mock.calls[1]).toEqual([1, [102]]);
  });

  it('a failed merge keeps the group and says why', async () => {
    serve([GROUP_A()]);
    vi.mocked(api.pollOperationV2).mockResolvedValue(settled('failed', 'store write failed'));
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(toast).toHaveBeenCalledWith('"Author A": store write failed', 'error'));
    expect(result.current.groups).toHaveLength(1);
    expect(result.current.pendingKeys.size).toBe(0);
  });

  it('a merge request that is refused keeps the group and reports it', async () => {
    serve([GROUP_A()]);
    vi.mocked(api.mergeAuthors).mockRejectedValue(new Error('Merge conflict'));
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(toast).toHaveBeenCalledWith('"Author A": Merge conflict', 'error'));
    expect(result.current.groups).toHaveLength(1);
  });
});

describe('concurrent actions do not undo each other', () => {
  it.each(['merge first', 'rename first'] as const)(
    'a rename of one group and a merge of another, finishing %s, both stick',
    async (order) => {
      serve([GROUP_A(), GROUP_B()]);
      const { result } = setup();
      await waitFor(() => expect(result.current.status).toBe('populated'));
      const renameGate = deferred<{ id: number; name: string }>();
      const mergeGate = deferred<api.OperationV2>();
      vi.mocked(api.renameAuthor).mockReturnValue(renameGate.promise);
      vi.mocked(api.pollOperationV2).mockReturnValue(mergeGate.promise);

      act(() =>
        result.current.dispatch({
          lane: 'authors',
          entity: 'author',
          type: 'renameCanonical',
          canonicalId: asAuthorId(1),
          name: 'Author A (renamed)',
        })
      );
      act(() => result.current.dispatch(merge(2)));
      await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));

      const finishMerge = () => act(async () => mergeGate.resolve(settled()));
      const finishRename = () => act(async () => renameGate.resolve({ id: 1, name: 'Author A (renamed)' }));
      if (order === 'merge first') {
        await finishMerge();
        await finishRename();
      } else {
        await finishRename();
        await finishMerge();
      }
      await waitFor(() => expect(result.current.pendingKeys.size).toBe(0));
      expect(result.current.groups.map((g) => g.canonical.name)).toEqual(['Author A (renamed)']);
    }
  );

  it('role toggles on two groups in one tick both stick', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => {
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'toggleRole',
        canonicalId: asAuthorId(1),
        target: { kind: 'variant', id: asAuthorId(101) },
      });
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'toggleRole',
        canonicalId: asAuthorId(2),
        target: { kind: 'variant', id: asAuthorId(201) },
      });
    });
    expect([...(result.current.choices.narrators['1'] ?? [])]).toEqual(['v:101']);
    expect([...(result.current.choices.narrators['2'] ?? [])]).toEqual(['v:201']);
  });

  it('two merges run one after another, never together', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    const gate = deferred<api.OperationV2>();
    vi.mocked(api.pollOperationV2).mockReturnValueOnce(gate.promise);
    act(() => result.current.dispatch(merge(1)));
    act(() => result.current.dispatch(merge(2)));
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));
    // Both rows show as pending; only the first has been sent.
    expect([...result.current.pendingKeys].sort()).toEqual(['1', '2']);
    await act(async () => gate.resolve(settled()));
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.groups).toEqual([]));
  });
});

describe('merge selected and merge all', () => {
  const composite = () => wire(3, 'Author C / Author D', [], { split_names: ['Author C', 'Author D'] });
  const production = () => wire(4, 'Studio One', [], { is_production_company: true });

  it('merge all counts and acts on the groups Merge applies to, not composites or production companies', async () => {
    serve([GROUP_A(), GROUP_B(), composite(), production()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    expect(result.current.kindCounts).toEqual({ all: 4, variants: 2, composite: 1, production: 1 });
    expect(result.current.mergeable.map((g) => g.key)).toEqual(['1', '2']);

    act(() => result.current.dispatch({ lane: 'authors', entity: 'author', type: 'mergeAllGroups', expectedIds: [asAuthorId(1), asAuthorId(2)] }));
    await waitFor(() => expect(result.current.groups.map((g) => g.key)).toEqual(['3', '4']));
    expect(api.mergeAuthors).toHaveBeenCalledTimes(2);
    expect(toast).toHaveBeenCalledWith('Merged 2 of 2 name-variant group(s).', 'success');
  });

  it('merge all refuses, and merges nothing, when the list moved since it was confirmed', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch({ lane: 'authors', entity: 'author', type: 'mergeAllGroups', expectedIds: [asAuthorId(1), asAuthorId(2), asAuthorId(9)] }));
    expect(api.mergeAuthors).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('Nothing was merged'), 'warning');
    expect(result.current.groups).toHaveLength(2);
  });

  it('merge selected reports the failures instead of "Merged N"', async () => {
    serve([GROUP_A(), GROUP_B()]);
    vi.mocked(api.pollOperationV2).mockImplementation(async (id: string) =>
      id === 'm2' ? settled('failed', 'store write failed') : settled()
    );
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'mergeSelected',
        ids: [asAuthorId(1), asAuthorId(2)],
      })
    );
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        'Merged 1 of 2 selected group(s); 1 failed: "Author B": store write failed.',
        'error'
      )
    );
    expect(result.current.groups.map((g) => g.key)).toEqual(['2']);
    expect(result.current.bulk).toBeNull();
  });

  it('the selected count is the rows that still exist', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => {
      result.current.selection.toggle('1');
      result.current.selection.toggle('2');
    });
    expect(result.current.selectedCount).toBe(2);
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(result.current.groups.map((g) => g.key)).toEqual(['2']));
    // The set still holds '1'; the count must not.
    expect(result.current.selection.selected.has('1')).toBe(true);
    expect(result.current.selectedCount).toBe(1);
    expect(result.current.selectedGroups.map((g) => g.key)).toEqual(['2']);
  });
});

describe('split, production, validate', () => {
  const compositeGroup = () => wire(3, 'Author C / Author D', [], { split_names: ['Author C', 'Author D'] });

  it('a split reclassifies the names flagged as narrators and drops the group without re-reading', async () => {
    serve([compositeGroup(), GROUP_A()]);
    vi.mocked(api.splitCompositeAuthor).mockResolvedValue({
      authors: [
        { id: 31, name: 'Author C' },
        { id: 32, name: 'Author D' },
      ],
      books_updated: 3,
    });
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'toggleRole',
        canonicalId: asAuthorId(3),
        target: { kind: 'split', name: 'Author D' },
      })
    );
    act(() =>
      result.current.dispatch({ lane: 'authors', entity: 'author', type: 'splitComposite', canonicalId: asAuthorId(3) })
    );
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Split "Author C / Author D" into 2 authors', 'success'));
    expect(api.reclassifyAuthorAsNarrator).toHaveBeenCalledTimes(1);
    expect(api.reclassifyAuthorAsNarrator).toHaveBeenCalledWith(32);
    expect(result.current.groups.map((g) => g.key)).toEqual(['1']);
    // Every author write clears the server's list; a re-read would return
    // "not scanned yet" and replace the groups still on screen.
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);
  });

  it('a split narrator name that matches no new author is reported', async () => {
    serve([compositeGroup()]);
    vi.mocked(api.splitCompositeAuthor).mockResolvedValue({ authors: [{ id: 31, name: 'Someone Else' }], books_updated: 1 });
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'toggleRole',
        canonicalId: asAuthorId(3),
        target: { kind: 'split', name: 'Author D' },
      })
    );
    act(() =>
      result.current.dispatch({ lane: 'authors', entity: 'author', type: 'splitComposite', canonicalId: asAuthorId(3) })
    );
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        expect.stringContaining('"Author D" matched no new author, so it was not reclassified as narrator'),
        'warning'
      )
    );
    expect(api.reclassifyAuthorAsNarrator).not.toHaveBeenCalled();
  });

  it('find real author starts the op, waits for it, and drops the group without re-reading', async () => {
    serve([wire(4, 'Studio One', [], { is_production_company: true }), GROUP_A()]);
    vi.mocked(api.resolveProductionAuthor).mockResolvedValue(op('r1'));
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() =>
      result.current.dispatch({ lane: 'authors', entity: 'author', type: 'resolveProduction', canonicalId: asAuthorId(4) })
    );
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Found the real author(s).', 'success'));
    expect(api.pollOperationV2).toHaveBeenCalledWith('r1', undefined, expect.any(Number), expect.anything());
    expect(result.current.groups.map((g) => g.key)).toEqual(['1']);
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);
  });

  it('a failed resolve is reported, not swallowed', async () => {
    serve([wire(4, 'Studio One', [], { is_production_company: true })]);
    vi.mocked(api.resolveProductionAuthor).mockRejectedValue(new Error('Resolution failed'));
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() =>
      result.current.dispatch({ lane: 'authors', entity: 'author', type: 'resolveProduction', canonicalId: asAuthorId(4) })
    );
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Resolution failed', 'error'));
    expect(result.current.pendingKeys.size).toBe(0);
  });

  it('validate keeps loading, error and results apart, and ignores a superseded answer', async () => {
    serve([GROUP_A()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    const first = deferred<{ results: api.ValidationResult[]; query: string }>();
    const second = deferred<{ results: api.ValidationResult[]; query: string }>();
    vi.mocked(reads.validateAuthorName).mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);

    act(() => result.current.validate('1'));
    expect(result.current.validation['1']).toEqual({ forName: 'Author A', state: 'loading' });
    act(() => result.current.validate('1'));
    // The first request was cancelled by the second.
    expect(vi.mocked(reads.validateAuthorName).mock.calls[0][1].aborted).toBe(true);
    await act(async () => second.resolve({ query: 'Author A', results: [{ source: 'Provider', title: 'T', author: 'Author A' }] }));
    await act(async () => first.resolve({ query: 'Author A', results: [] }));
    expect(result.current.validation['1']).toMatchObject({ state: 'ready', results: [{ source: 'Provider' }] });

    vi.mocked(reads.validateAuthorName).mockRejectedValueOnce(new Error('providers down'));
    act(() => result.current.validate('1'));
    await waitFor(() => expect(result.current.validation['1']).toMatchObject({ state: 'error', message: 'providers down' }));
  });
});

describe('leaving the lane or unmounting', () => {
  it('stops waiting on an operation and sets no state or toast afterwards', async () => {
    serve([GROUP_A()]);
    const { result, unmount } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    // A poll that only ends when it is aborted, as the real one does.
    let pollSignal: AbortSignal | undefined;
    vi.mocked(api.pollOperationV2).mockImplementation(
      (_id, _cb, _ms, opts) =>
        new Promise((_resolve, reject) => {
          pollSignal = opts?.signal;
          opts?.signal?.addEventListener('abort', () =>
            reject(Object.assign(new Error('aborted'), { name: 'AbortError' }))
          );
        })
    );
    const errors = vi.spyOn(console, 'error').mockImplementation(() => {});
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(pollSignal).toBeDefined());
    expect(pollSignal?.aborted).toBe(false);

    unmount();
    expect(pollSignal?.aborted).toBe(true);
    await act(async () => {});
    expect(toast).not.toHaveBeenCalled();
    expect(errors).not.toHaveBeenCalled();
    errors.mockRestore();
  });

  /** A poll that ends only when it is aborted, as the real one does. */
  function hangingPoll() {
    vi.mocked(api.pollOperationV2).mockImplementation(
      (_id, _cb, _ms, opts) =>
        new Promise((_resolve, reject) => {
          opts?.signal?.addEventListener('abort', () =>
            reject(Object.assign(new Error('aborted'), { name: 'AbortError' }))
          );
        })
    );
  }

  it('a write still queued when the lane is left is dropped, not sent', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result, rerender } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    hangingPoll();
    act(() => result.current.dispatch(merge(1)));
    act(() => result.current.dispatch(merge(2))); // queued behind the first
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));
    expect([...result.current.pendingKeys].sort()).toEqual(['1', '2']);

    rerender({ active: false });
    await waitFor(() => expect(result.current.pendingKeys.size).toBe(0));
    // The reviewer is told the second merge was never sent.
    expect(toast).toHaveBeenCalledWith('1 queued merge was not sent because you left the lane.', 'warning');
    // Coming back does not revive it either.
    rerender({ active: true });
    await act(async () => {});
    expect(api.mergeAuthors).toHaveBeenCalledTimes(1);
  });

  it('a merge that was sent before the lane was left stays locked on return and is followed to its end', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result, rerender } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    hangingPoll();
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));

    rerender({ active: false });
    await waitFor(() => expect(result.current.pendingKeys.size).toBe(0));

    // On return its operation is followed again, and its row is locked: a second
    // Merge for a group whose first one may have gone through must not be offered.
    const gate = deferred<api.OperationV2>();
    vi.mocked(api.pollOperationV2).mockReset();
    vi.mocked(api.pollOperationV2).mockReturnValue(gate.promise);
    rerender({ active: true });
    await waitFor(() => expect(api.pollOperationV2).toHaveBeenCalledWith('m1', undefined, expect.any(Number), expect.anything()));
    expect([...result.current.pendingKeys]).toEqual(['1']);
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch(merge(1)));
    expect(api.mergeAuthors).toHaveBeenCalledTimes(1);

    await act(async () => gate.resolve(settled()));
    await waitFor(() => expect(result.current.groups.map((g) => g.key)).toEqual(['2']));
    expect(result.current.pendingKeys.size).toBe(0);
    expect(toast).toHaveBeenCalledWith('Merged author(s) into "Author A" (it finished after you left).', 'success');
  });

  it('a bulk merge stops sending requests the moment the lane is left, and clears its progress', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result, rerender } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    hangingPoll();
    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'mergeSelected',
        ids: [asAuthorId(1), asAuthorId(2)],
      })
    );
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));
    expect(result.current.bulk).toEqual({ done: 0, total: 2, entity: 'author' });

    rerender({ active: false });
    await waitFor(() => expect(result.current.bulk).toBeNull());
    expect(result.current.pendingKeys.size).toBe(0);
    expect(api.mergeAuthors).toHaveBeenCalledTimes(1);
    // No "Merged 0 of 0" announcement for a run the reviewer walked away from;
    // the one thing worth saying is what was never sent. The first group's merge
    // WAS sent (it is unconfirmed), the second was not.
    expect(toast).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith('1 queued merge was not sent because you left the lane.', 'warning');
  });

  it('a scan queued behind a write is dropped when the lane is left, and can be started again', async () => {
    serve([GROUP_A()]);
    const { result, rerender } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    hangingPoll();
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));
    act(() => result.current.refresh());
    expect(result.current.scanning).toBe(true);

    rerender({ active: false });
    await waitFor(() => expect(result.current.scanning).toBe(false));
    expect(api.refreshAuthorDuplicates).not.toHaveBeenCalled();
  });

  it('keyboard handlers exist only while the lane is active', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result, rerender } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    expect(result.current.focusedKey).toBe('1');
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'j' }));
    });
    expect(result.current.focusedKey).toBe('2');
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k' }));
    });
    expect(result.current.focusedKey).toBe('1');

    rerender({ active: false });
    // Navigation and help are state the lane keeps; none of it may move while
    // another lane is showing. (A merge key could not reach the server anyway --
    // a write queued from an inactive lane is dropped -- so the observable proof
    // that the listener is gone is the state it would have changed.)
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'j' }));
      window.dispatchEvent(new KeyboardEvent('keydown', { key: '?' }));
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'm' }));
    });
    await act(async () => {});
    expect(result.current.focusedKey).toBe('1');
    expect(result.current.helpOpen).toBe(false);
    expect(api.mergeAuthors).not.toHaveBeenCalled();
    expect(result.current.pendingKeys.size).toBe(0);
  });

  it('a merge whose narrator step was in flight when the lane was left never sends the merge', async () => {
    serve([GROUP_A()]);
    const { result, rerender } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    const reclassify = deferred<api.Operation>();
    vi.mocked(api.reclassifyAuthorAsNarrator).mockReturnValue(reclassify.promise);
    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'toggleRole',
        canonicalId: asAuthorId(1),
        target: { kind: 'variant', id: asAuthorId(101) },
      })
    );
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(api.reclassifyAuthorAsNarrator).toHaveBeenCalledTimes(1));

    rerender({ active: false });
    await act(async () => reclassify.resolve(op('r101')));
    await waitFor(() => expect(result.current.pendingKeys.size).toBe(0));
    expect(api.mergeAuthors).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it('m merges the focused group and ? toggles the help', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'j' }));
    });
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'm' }));
    });
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledWith(2, [201]));
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: '?' }));
    });
    expect(result.current.helpOpen).toBe(true);
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(result.current.helpOpen).toBe(false);
  });
});

describe('seeding from the URL', () => {
  it('?author= focuses the group the author belongs to, once', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup({ authorId: '201' }); // a variant of group B
    await waitFor(() => expect(result.current.status).toBe('populated'));
    expect(result.current.focusedKey).toBe('2');
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k' }));
    });
    expect(result.current.focusedKey).toBe('1');
    act(() => result.current.reload());
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.status).toBe('populated'));
    // The seed was spent: a reload does not pull focus back.
    expect(result.current.focusedKey).toBe('1');
  });

  it('an unusable ?author= is ignored', async () => {
    serve([GROUP_A()]);
    const { result } = setup({ authorId: 'not-a-number' });
    await waitFor(() => expect(result.current.status).toBe('populated'));
    expect(result.current.focusedKey).toBe('1');
  });
});

describe('author candidates', () => {
  it('lists entity_type author pairs, naming them from the groups first', async () => {
    serve([GROUP_A()]);
    vi.mocked(api.getDedupCandidates).mockResolvedValue({
      total: 1,
      candidates: [
        {
          id: 7,
          entity_type: 'author',
          entity_a_id: '1',
          entity_b_id: '999',
          layer: 'embedding',
          similarity: 0.93,
          status: 'pending',
          created_at: '',
          updated_at: '',
        },
      ],
    });
    const lookup = deferred<string>();
    vi.mocked(reads.fetchAuthorName).mockReturnValue(lookup.promise);
    const { result } = setup();
    await waitFor(() => expect(result.current.candidates.status).toBe('populated'));
    expect(api.getDedupCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ entity_type: 'author', status: 'pending' }),
      expect.anything()
    );
    expect(result.current.candidates.total).toBe(1);
    expect(result.current.candidates.nameOf('1')).toBe('Author A');
    // Until the lookup answers the row falls back to the id; it is not an error.
    expect(result.current.candidates.nameOf('999')).toBe('Author #999');
    await act(async () => lookup.resolve('Author Z'));
    await waitFor(() => expect(result.current.candidates.nameOf('999')).toBe('Author Z'));
    // Only the id the groups did not know was looked up.
    expect(vi.mocked(reads.fetchAuthorName).mock.calls.map((c) => c[0])).toEqual([999]);
  });

  it('a failed candidates read is an error of its own and does not touch the groups', async () => {
    serve([GROUP_A()]);
    vi.mocked(api.getDedupCandidates).mockRejectedValue(new Error('candidates down'));
    const { result } = setup();
    await waitFor(() => expect(result.current.candidates.status).toBe('error'));
    expect(result.current.candidates.error).toBe('candidates down');
    expect(result.current.status).toBe('populated');
  });
});

describe('refresh (the author scan)', () => {
  it('starts one scan however many times it is clicked, then reloads', async () => {
    serve([GROUP_A()]);
    const gate = deferred<api.OperationV2>();
    vi.mocked(api.refreshAuthorDuplicates).mockResolvedValue(op('scan1'));
    vi.mocked(api.pollOperationV2).mockReturnValueOnce(gate.promise);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => {
      result.current.refresh();
      result.current.refresh();
    });
    expect(result.current.scanning).toBe(true);
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Author scan started — watch the bell for progress.', 'info'));
    await act(async () => gate.resolve(settled()));
    await waitFor(() => expect(result.current.scanning).toBe(false));
    expect(api.refreshAuthorDuplicates).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2));
  });
});

// ---------------------------------------------------------------------------
// Review fixes (PR #4004)
// ---------------------------------------------------------------------------

const exclude = (n: number, variantId: number) => ({
  lane: 'authors' as const,
  entity: 'author' as const,
  type: 'excludeVariant' as const,
  canonicalId: asAuthorId(n),
  variantId: asAuthorId(variantId),
});
const narrator = (n: number, variantId: number) => ({
  lane: 'authors' as const,
  entity: 'author' as const,
  type: 'toggleRole' as const,
  canonicalId: asAuthorId(n),
  target: { kind: 'variant' as const, id: asAuthorId(variantId) },
});

describe('the server clears its list on every author write', () => {
  const composite = () => wire(3, 'Author C / Author D', [], { split_names: ['Author C', 'Author D'] });

  it('keeps the groups it holds, flagged stale, when a re-read returns needs_refresh after a split', async () => {
    serve([composite(), GROUP_A()]);
    vi.mocked(api.splitCompositeAuthor).mockResolvedValue({
      authors: [
        { id: 31, name: 'Author C' },
        { id: 32, name: 'Author D' },
      ],
      books_updated: 1,
    });
    const { result, rerender } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    expect(result.current.stale).toBe(false);
    act(() =>
      result.current.dispatch({ lane: 'authors', entity: 'author', type: 'splitComposite', canonicalId: asAuthorId(3) })
    );
    await waitFor(() => expect(result.current.groups.map((g) => g.key)).toEqual(['1']));

    // The split cleared the server's cache: every read from now on is cold.
    vi.mocked(reads.fetchAuthorGroups).mockResolvedValue({ groups: [], needsRefresh: true });
    rerender({ active: false });
    rerender({ active: true });
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.stale).toBe(true));
    expect(result.current.status).toBe('populated');
    expect(result.current.groups.map((g) => g.key)).toEqual(['1']);

    // Retry reads cold again and the groups are still there.
    act(() => result.current.reload());
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(result.current.stale).toBe(true));
    expect(result.current.groups.map((g) => g.key)).toEqual(['1']);
  });

  it('a lane that held nothing still shows the cold cache as needs-scan, not stale', async () => {
    serve([], true);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('needs-scan'));
    expect(result.current.stale).toBe(false);
  });

  it('a narrator-only merge takes the reclassified variants out of the group and does not re-read', async () => {
    serve([GROUP_A()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch(exclude(1, 102))); // 102 stays, left out
    act(() => result.current.dispatch(narrator(1, 101)));
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Reclassified 1 variant(s) as narrator', 'success'));
    expect(api.mergeAuthors).not.toHaveBeenCalled();
    expect(result.current.groups.map((g) => g.variants.map((v) => v.id))).toEqual([[102]]);
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);
  });

  it('a narrator-only merge removes the group when no variant is left', async () => {
    serve([wire(1, 'Author A', ['Author A.'])]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch(narrator(1, 101)));
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(result.current.groups).toEqual([]));
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);
  });
});

describe('a partly failed merge', () => {
  it('parses the ids the server says it left behind', () => {
    expect(parseLeftIds('author merge left author ids [102 7] of 3 not merged, 1 merged: x')).toEqual(new Set([102, 7]));
    expect(parseLeftIds('store write failed')).toBeNull();
  });

  it('keeps the group with only the variants that remain, and says which ids failed', async () => {
    serve([GROUP_A()]);
    vi.mocked(api.pollOperationV2).mockResolvedValue(
      settled('failed', 'author merge left author ids [102] of 2 not merged, 1 merged: failed to update book b9')
    );
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(expect.stringContaining('1 author record(s) were not merged (ids 102)'), 'error')
    );
    expect(result.current.groups).toHaveLength(1);
    expect(result.current.groups[0].variants.map((v) => v.id)).toEqual([102]);
    expect(result.current.pendingKeys.size).toBe(0);
  });
});

describe('a write that gets no answer', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('releases the queue, says the merge may have run, and lets the next merge go', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    vi.mocked(api.mergeAuthors).mockImplementationOnce(() => new Promise<api.Operation>(() => {}));
    // Only the timer is faked: the queue and the mocks are promise-driven.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    act(() => result.current.dispatch(merge(1)));
    act(() => result.current.dispatch(merge(2)));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(WRITE_DEADLINE_MS + 1);
    });
    vi.useRealTimers();

    expect(toast).toHaveBeenCalledWith(
      `No answer after ${WRITE_DEADLINE_MS / 1000}s — the merge may have run; re-reading.`,
      'warning'
    );
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.pendingKeys.size).toBe(0));
    expect(result.current.busy).toBe(false);
    // It re-read rather than assuming either outcome.
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2);
  });

  it('a hung rename unlocks its row', async () => {
    serve([GROUP_A()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    vi.mocked(api.renameAuthor).mockImplementation(() => new Promise(() => {}));
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'renameCanonical',
        canonicalId: asAuthorId(1),
        name: 'Author A Edited',
      })
    );
    expect(result.current.pendingKeys.has('1')).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(WRITE_DEADLINE_MS + 1);
    });
    vi.useRealTimers();
    expect(result.current.pendingKeys.size).toBe(0);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('the rename may have run'), 'warning');
  });

  it('a bulk run stops at the hung merge and does not send the rest', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    vi.mocked(api.mergeAuthors).mockImplementation(() => new Promise<api.Operation>(() => {}));
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'mergeSelected',
        ids: [asAuthorId(1), asAuthorId(2)],
      })
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(WRITE_DEADLINE_MS + 1);
    });
    vi.useRealTimers();
    expect(api.mergeAuthors).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('the other 1 were not sent'), 'warning');
    expect(result.current.bulk).toBeNull();
    expect(result.current.pendingKeys.size).toBe(0);
  });
});

describe('narrator reclassification is an operation', () => {
  it('waits for the reclassify operation to settle before the merge is sent, polling the id the route returned', async () => {
    serve([GROUP_A()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    const gate = deferred<api.OperationV2>();
    vi.mocked(api.pollOperationV2).mockImplementation(async (id: string) =>
      id === 'r101' ? gate.promise : settled()
    );
    act(() => result.current.dispatch(narrator(1, 101)));
    act(() => result.current.dispatch(merge(1)));

    await waitFor(() =>
      expect(api.pollOperationV2).toHaveBeenCalledWith(
        'r101',
        undefined,
        expect.any(Number),
        expect.anything()
      )
    );
    expect(
      api.mergeAuthors,
      'a 202 alone is not "reclassified": the merge waits for the op'
    ).not.toHaveBeenCalled();

    await act(async () => gate.resolve(settled()));
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledWith(1, [102]));
  });

  it('counts a reclassify operation that FAILED as a failure, and the merge still goes', async () => {
    serve([GROUP_A()]);
    vi.mocked(api.pollOperationV2).mockImplementation(async (id: string) =>
      id === 'r101' ? settled('failed', 'author-reclassify: author 101 was kept') : settled()
    );
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch(narrator(1, 101)));
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        'Merged author(s) into "Author A" (1 narrator reclassification failed)',
        'warning'
      )
    );
    expect(api.mergeAuthors).toHaveBeenCalledWith(1, [102]);
  });
});

describe('narrator reclassification failures are reported', () => {
  it('on a single merge', async () => {
    serve([GROUP_A()]);
    vi.mocked(api.reclassifyAuthorAsNarrator).mockRejectedValue(new Error('nope'));
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch(narrator(1, 101)));
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        'Merged author(s) into "Author A" (1 narrator reclassification failed)',
        'warning'
      )
    );
  });

  it('in a bulk run, summed across the groups, even when every merge succeeds', async () => {
    serve([GROUP_A(), GROUP_B()]);
    vi.mocked(api.reclassifyAuthorAsNarrator).mockRejectedValue(new Error('nope'));
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch(narrator(1, 101)));
    act(() => result.current.dispatch(narrator(2, 201)));
    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'mergeSelected',
        ids: [asAuthorId(1), asAuthorId(2)],
      })
    );
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        expect.stringContaining('Merged 2 of 2 selected group(s). 2 narrator reclassifications failed.'),
        'warning'
      )
    );
  });
});

describe('a group whose every variant is left out', () => {
  it('is not mergeable, not counted, and a merge of it says so instead of reporting success', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch(exclude(2, 201)));
    expect(result.current.mergeable.map((g) => g.key)).toEqual(['1']);
    expect(result.current.selectableTotal).toBe(1);

    act(() => result.current.dispatch(merge(2)));
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith('"Author B": every variant is left out of this merge', 'warning')
    );
    expect(api.mergeAuthors).not.toHaveBeenCalled();
    expect(result.current.groups).toHaveLength(2);
  });
});

describe('an expired session during a bulk run', () => {
  it('stops, says once how many merged, and does not send the rest', async () => {
    const g3 = () => wire(3, 'Author C', ['Author C.']);
    serve([GROUP_A(), GROUP_B(), g3()]);
    vi.mocked(api.mergeAuthors)
      .mockImplementationOnce(async (id: number) => op(`m${id}`))
      .mockRejectedValueOnce(new ApiAuthRedirectError('/api/v1/authors/merge', 'https://login.example/'));
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'mergeSelected',
        ids: [asAuthorId(1), asAuthorId(2), asAuthorId(3)],
      })
    );
    await waitFor(() => expect(result.current.bulk).toBeNull());
    expect(api.mergeAuthors).toHaveBeenCalledTimes(2);
    expect(toast).toHaveBeenCalledTimes(1);
    const [message, severity] = toast.mock.calls[0];
    expect(message).toContain(SESSION_EXPIRED_MESSAGE);
    expect(message).toContain('1 group(s) were merged before it expired');
    expect(message).toContain('the other 1 were not sent');
    expect(severity).toBe('error');
  });
});

describe('keyboard while something is open in front of the list', () => {
  const press = (key: string) =>
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key }));
    });

  it('m and j/k do nothing behind the books popover; Escape closes it and they work again', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.openBooks('1', document.body));
    press('j');
    press('m');
    await act(async () => {});
    expect(result.current.focusedKey).toBe('1');
    expect(api.mergeAuthors).not.toHaveBeenCalled();

    press('Escape');
    expect(result.current.booksFor).toBeNull();
    press('m');
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));
  });

  it('and behind the shortcut help', async () => {
    serve([GROUP_A()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    press('?');
    expect(result.current.helpOpen).toBe(true);
    press('m');
    await act(async () => {});
    expect(api.mergeAuthors).not.toHaveBeenCalled();
  });

  it('Enter on a focused button leaves it to the button instead of opening the books', async () => {
    serve([GROUP_A()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    const button = document.createElement('button');
    document.body.appendChild(button);
    button.focus();
    press('Enter');
    expect(result.current.booksFor).toBeNull();
    button.blur();
    document.body.removeChild(button);
    press('Enter');
    expect(result.current.booksFor?.key).toBe('1');
  });
});

describe('what is open is cleared when it can no longer be seen', () => {
  it('leaving the lane closes the books popover, the name editor and the help', async () => {
    serve([GROUP_A()]);
    const { result, rerender } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => {
      result.current.openBooks('1', document.body);
      result.current.startEdit('1', 'Author A');
      result.current.setHelpOpen(true);
    });
    rerender({ active: false });
    expect(result.current.booksFor).toBeNull();
    expect(result.current.editing).toBeNull();
    expect(result.current.helpOpen).toBe(false);
  });

  it('a popover on a group that merged away is closed, not left pointing at a gone card', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.openBooks('1', document.body));
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(result.current.groups.map((g) => g.key)).toEqual(['2']));
    expect(result.current.booksFor).toBeNull();
  });
});

describe('merge all compares the set of groups, not just how many', () => {
  it('refuses when one group left and another became mergeable, so the count is unchanged', async () => {
    const composite = wire(3, 'Author C / Author D', [], { split_names: ['Author C', 'Author D'] });
    serve([GROUP_A(), GROUP_B(), composite]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    // Confirmed {1, 3}; what would run is {1, 2}: same size, different groups.
    act(() =>
      result.current.dispatch({
        lane: 'authors',
        entity: 'author',
        type: 'mergeAllGroups',
        expectedIds: [asAuthorId(1), asAuthorId(3)],
      })
    );
    expect(api.mergeAuthors).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('Nothing was merged'), 'warning');
  });
});

describe('candidate name lookups', () => {
  it('are not restarted when the list is edited', async () => {
    serve([GROUP_A(), GROUP_B()]);
    vi.mocked(api.getDedupCandidates).mockResolvedValue({
      total: 1,
      candidates: [
        {
          id: 7,
          entity_type: 'author',
          entity_a_id: '999',
          entity_b_id: '998',
          layer: 'embedding',
          status: 'pending',
          created_at: '',
          updated_at: '',
        },
      ],
    });
    vi.mocked(reads.fetchAuthorName).mockImplementation(async (id) => `Looked ${id}`);
    const { result } = setup();
    await waitFor(() => expect(result.current.candidates.nameOf('999')).toBe('Looked 999'));
    await waitFor(() => expect(result.current.candidates.nameOf('998')).toBe('Looked 998'));
    expect(reads.fetchAuthorName).toHaveBeenCalledTimes(2);

    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(result.current.groups.map((g) => g.key)).toEqual(['2']));
    await act(async () => {});
    expect(reads.fetchAuthorName).toHaveBeenCalledTimes(2);
  });
});

// ---------------------------------------------------------------------------
// The Series sub-view
// ---------------------------------------------------------------------------

/** A wire series row. `author` credits it to author id `author`. */
function srow(id: number, name: string, author?: number): api.SeriesWithBooks {
  return {
    id,
    name,
    created_at: '2026-01-01T00:00:00Z',
    ...(author !== undefined ? { author_id: author, author_name: `Author ${author}` } : {}),
  };
}
function swire(name: string, rows: api.SeriesWithBooks[], match_type = 'exact'): api.SeriesDupGroup {
  return { name, count: rows.length, series: rows, match_type };
}

/** Group "s11-12": 12 is credited to author 7, so it is the one a merge keeps. */
const SG_A = () => swire('Series A', [srow(11, 'Series A'), srow(12, 'series a', 7)]);
/** Group "s21-22". */
const SG_B = () => swire('Series B', [srow(21, 'Series B'), srow(22, 'series b')]);
/** A sub-series group "s31-32": never part of Merge All. */
const SG_SUB = () => swire('Series C, Book 2', [srow(31, 'Series C'), srow(32, 'Series C, Book 2')], 'subseries');

function serveSeries(groups: api.SeriesDupGroup[], opts: { needsRefresh?: boolean; total?: number } = {}) {
  vi.mocked(reads.fetchSeriesGroups).mockResolvedValue({
    groups,
    totalSeries: opts.total ?? groups.length * 2,
    needsRefresh: opts.needsRefresh ?? false,
  });
}

const sact = {
  merge: (groupKey: string) => ({
    lane: 'authors' as const,
    entity: 'series' as const,
    type: 'mergeSeriesGroup' as const,
    groupKey,
  }),
  selected: (groupKeys: string[]) => ({
    lane: 'authors' as const,
    entity: 'series' as const,
    type: 'mergeSelectedSeries' as const,
    groupKeys,
  }),
  all: (expectedKeys: string[]) => ({
    lane: 'authors' as const,
    entity: 'series' as const,
    type: 'mergeAllSeriesGroups' as const,
    expectedKeys,
  }),
  rename: (groupKey: string, seriesId: number, name: string) => ({
    lane: 'authors' as const,
    entity: 'series' as const,
    type: 'renameSeries' as const,
    groupKey,
    seriesId: asSeriesId(seriesId),
    name,
  }),
  narrator: (groupKey: string, authorId: number) => ({
    lane: 'authors' as const,
    entity: 'series' as const,
    type: 'toggleSeriesNarrator' as const,
    groupKey,
    authorId: asAuthorId(authorId),
  }),
  include: (groupKey: string, seriesId: number) => ({
    lane: 'authors' as const,
    entity: 'series' as const,
    type: 'toggleSeriesIncluded' as const,
    groupKey,
    seriesId: asSeriesId(seriesId),
  }),
};

const press = (key: string) =>
  act(() => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key }));
  });

describe('the series sub-view fetches only when it is selected', () => {
  it('makes no series request while Authors shows, one on selecting Series, and one authors read per return', async () => {
    serve([GROUP_A()]);
    serveSeries([SG_A()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    await act(async () => {});
    expect(reads.fetchSeriesGroups).not.toHaveBeenCalled();

    act(() => result.current.setView('series'));
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(1);
    // Leaving Authors did not re-read it; coming back reads it once, and never reads Series again.
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);

    act(() => result.current.setView('authors'));
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2));
    await act(async () => {});
    expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(1);
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2);
    expect(api.getDedupCandidates).toHaveBeenCalledTimes(2);
  });

  it('?view=series opens on Series and makes no author request at all', async () => {
    serve([GROUP_A()]);
    serveSeries([SG_A()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    await act(async () => {});
    expect(result.current.view).toBe('series');
    expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(1);
    expect(reads.fetchAuthorGroups).not.toHaveBeenCalled();
    expect(api.getDedupCandidates).not.toHaveBeenCalled();
  });

  it('an unknown ?view= opens Authors', async () => {
    serve([GROUP_A()]);
    const { result } = setup({ view: 'labels' });
    await waitFor(() => expect(result.current.status).toBe('populated'));
    expect(result.current.view).toBe('authors');
    expect(reads.fetchSeriesGroups).not.toHaveBeenCalled();
  });

  it('an inactive lane reads nothing, and leaving aborts the series read; a late answer is never painted', async () => {
    const pending = deferred<reads.SeriesGroupsResponse>();
    vi.mocked(reads.fetchSeriesGroups).mockReturnValue(pending.promise);
    const { result, rerender } = setup({ active: false, view: 'series' });
    await act(async () => {});
    expect(reads.fetchSeriesGroups).not.toHaveBeenCalled();

    rerender({ active: true });
    await waitFor(() => expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(1));
    const signal = vi.mocked(reads.fetchSeriesGroups).mock.calls[0][0];
    expect(signal.aborted).toBe(false);
    rerender({ active: false });
    expect(signal.aborted).toBe(true);
    await act(async () => {
      pending.resolve({ groups: [SG_A()], totalSeries: 4, needsRefresh: false });
    });
    expect(result.current.series.groups).toEqual([]);
  });

  it('does not duplicate the read on mount', async () => {
    serveSeries([SG_A()]);
    setup({ view: 'series' });
    await waitFor(() => expect(reads.fetchSeriesGroups).toHaveBeenCalled());
    await act(async () => {});
    expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(1);
  });
});

describe('series: loading, error, empty, needs-scan, populated', () => {
  it('walks loading -> populated and keys groups on their series ids', async () => {
    const d = deferred<reads.SeriesGroupsResponse>();
    vi.mocked(reads.fetchSeriesGroups).mockReturnValue(d.promise);
    const { result } = setup({ view: 'series' });
    expect(result.current.series.status).toBe('loading');
    await act(async () => d.resolve({ groups: [SG_A()], totalSeries: 40, needsRefresh: false }));
    expect(result.current.series.status).toBe('populated');
    expect(result.current.series.totalSeries).toBe(40);
    expect(result.current.series.groups.map((g) => g.key)).toEqual(['s11-12']);
    // The author-linked series sorts first: it is the one a merge keeps.
    expect(result.current.series.groups[0].series.map((r) => r.id)).toEqual([12, 11]);
  });

  it('a failed request is an error with its message, not an empty list; Retry recovers', async () => {
    vi.mocked(reads.fetchSeriesGroups).mockRejectedValueOnce(new Error('boom: 500'));
    serveSeries([SG_A()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('error'));
    expect(result.current.series.error).toBe('boom: 500');
    expect(result.current.series.groups).toEqual([]);
    act(() => result.current.series.reload());
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
  });

  it('answered-with-nothing is empty; a cold cache is needs-scan, which is neither empty nor an error', async () => {
    serveSeries([]);
    const a = setup({ view: 'series' });
    await waitFor(() => expect(a.result.current.series.status).toBe('empty'));
    serveSeries([], { needsRefresh: true });
    const b = setup({ view: 'series' });
    await waitFor(() => expect(b.result.current.series.status).toBe('needs-scan'));
    expect(b.result.current.series.error).toBeNull();
  });

  it('kind counts add up to the total and match what each filter shows', async () => {
    serveSeries([SG_A(), SG_B(), SG_SUB()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    expect(result.current.series.kindCounts).toEqual({ all: 3, exact: 2, subseries: 1 });
    act(() => result.current.series.setFilter('subseries'));
    expect(result.current.series.filtered.map((g) => g.key)).toEqual(['s31-32']);
    act(() => result.current.series.setFilter('exact'));
    expect(result.current.series.filtered).toHaveLength(result.current.series.kindCounts.exact);
  });
});

describe('the server clears its series list on every write', () => {
  it('a merge edits the list in place; a later re-read that finds the cache gone keeps it, flagged stale', async () => {
    serveSeries([SG_A(), SG_B()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(result.current.series.groups.map((g) => g.key)).toEqual(['s21-22']));
    // No re-read after the write.
    expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(1);
    expect(result.current.series.stale).toBe(false);

    vi.mocked(reads.fetchSeriesGroups).mockResolvedValue({ groups: [], totalSeries: 0, needsRefresh: true });
    act(() => result.current.series.reload());
    await waitFor(() => expect(result.current.series.stale).toBe(true));
    expect(result.current.series.status).toBe('populated');
    expect(result.current.series.groups.map((g) => g.key)).toEqual(['s21-22']);
    expect(result.current.series.totalSeries).toBe(4);
  });

  it('a lane that held nothing still shows a cold cache as needs-scan, not stale', async () => {
    serveSeries([], { needsRefresh: true });
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('needs-scan'));
    expect(result.current.series.stale).toBe(false);
  });
});

describe('series rename', () => {
  it('uses the undoable endpoint, edits the row in place, and Undo reverts through the journal', async () => {
    serveSeries([SG_A()]);
    vi.mocked(api.renameSeries).mockResolvedValue({ id: 'rename-op-1' } as api.Operation);
    vi.mocked(api.undoSeriesRename).mockResolvedValue({ restored: 1 } as api.RevertOperationResult);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));

    act(() => result.current.dispatch(sact.rename('s11-12', 11, '  Series A Renamed ')));
    await waitFor(() => expect(result.current.series.renames[11]).toBeDefined());
    expect(api.renameSeries).toHaveBeenCalledWith(11, 'Series A Renamed');
    expect(api.updateSeriesName).not.toHaveBeenCalled();
    const row = (id: number) => result.current.series.groups[0].series.find((r) => r.id === id);
    expect(row(11)?.name).toBe('Series A Renamed');
    // Edited in place: no re-read of the list.
    expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(1);
    const undoAction = toast.mock.calls.find((c) => c[2]?.label === 'Undo')?.[2];
    expect(undoAction).toBeDefined();

    // The toast's Undo button.
    await act(async () => undoAction.onClick());
    await waitFor(() => expect(api.undoSeriesRename).toHaveBeenCalledWith('rename-op-1'));
    await waitFor(() => expect(row(11)?.name).toBe('Series A'));
    expect(result.current.series.renames[11]).toBeUndefined();
    expect(api.renameSeries).toHaveBeenCalledTimes(1); // undo is a revert, not a second rename
  });

  it('Undo while the group has a change in flight says so instead of doing nothing', async () => {
    serveSeries([SG_A()]);
    vi.mocked(api.renameSeries).mockResolvedValue({ id: 'rename-op-1' } as api.Operation);
    const gate = deferred<api.OperationV2>();
    vi.mocked(api.pollOperationV2).mockReturnValue(gate.promise);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.rename('s11-12', 11, 'Series A Renamed')));
    await waitFor(() => expect(result.current.series.renames[11]).toBeDefined());
    act(() => result.current.dispatch(sact.merge('s11-12'))); // holds the group
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1));

    act(() =>
      result.current.dispatch({ lane: 'authors', entity: 'series', type: 'undoRenameSeries', seriesId: asSeriesId(11) })
    );
    expect(toast).toHaveBeenCalledWith('That series group has a change in flight. Try Undo again in a moment.', 'info');
    expect(api.undoSeriesRename).not.toHaveBeenCalled();
    await act(async () => gate.resolve(settled()));
  });

  it('Undo via the lane action does the same, and a refused revert is shown and not offered again', async () => {
    serveSeries([SG_A()]);
    vi.mocked(api.renameSeries).mockResolvedValue({ id: 'rename-op-1' } as api.Operation);
    vi.mocked(api.undoSeriesRename).mockRejectedValue(new Error('renamed again since'));
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.rename('s11-12', 11, 'Series A Renamed')));
    await waitFor(() => expect(result.current.series.renames[11]).toBeDefined());
    act(() =>
      result.current.dispatch({ lane: 'authors', entity: 'series', type: 'undoRenameSeries', seriesId: asSeriesId(11) })
    );
    await waitFor(() => expect(toast).toHaveBeenCalledWith('renamed again since', 'error'));
    expect(result.current.series.renames[11]).toBeUndefined();
    expect(result.current.series.groups[0].series.find((r) => r.id === 11)?.name).toBe('Series A Renamed');
  });

  it('a rename that fails is reported and leaves the name alone; the row unlocks', async () => {
    serveSeries([SG_A()]);
    vi.mocked(api.renameSeries).mockRejectedValue(new Error('rename still queued (op x)'));
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.rename('s11-12', 11, 'New Name')));
    await waitFor(() => expect(toast).toHaveBeenCalledWith('rename still queued (op x)', 'error'));
    expect(result.current.pendingKeys.size).toBe(0);
    expect(result.current.series.groups[0].series.find((r) => r.id === 11)?.name).toBe('Series A');
    expect(result.current.series.renames[11]).toBeUndefined();
  });

  it('renaming to the name it already has sends nothing and closes the editor', async () => {
    serveSeries([SG_A()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.series.startEdit('s11-12', asSeriesId(11), 'Series A'));
    expect(result.current.series.editing).not.toBeNull();
    act(() => result.current.dispatch(sact.rename('s11-12', 11, 'Series A')));
    expect(api.renameSeries).not.toHaveBeenCalled();
    expect(result.current.series.editing).toBeNull();
  });

  it('its own deadline comes after the api wrapper\'s wait, so the wrapper\'s message wins', () => {
    expect(SERIES_RENAME_DEADLINE_MS).toBeGreaterThan(api.SERIES_RENAME_WAIT_MS);
  });

  it('a rename that gets no answer unlocks the row and says it may have run', async () => {
    serveSeries([SG_A()]);
    vi.mocked(api.renameSeries).mockImplementation(() => new Promise(() => {}));
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    act(() => result.current.dispatch(sact.rename('s11-12', 11, 'New Name')));
    expect(result.current.pendingKeys.has('s11-12')).toBe(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(SERIES_RENAME_DEADLINE_MS + 1);
    });
    vi.useRealTimers();
    expect(result.current.pendingKeys.size).toBe(0);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('the rename may have run'), 'warning');
  });
});

describe('series merge', () => {
  it('keeps the author-linked series and merges the rest into it', async () => {
    serveSeries([SG_A()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(result.current.series.groups).toEqual([]));
    expect(api.mergeSeriesGroup).toHaveBeenCalledWith(12, [11]);
    expect(toast).toHaveBeenCalledWith('Merged 1 series into "series a"', 'success');
  });

  it('the narrator flag is sent AFTER the merge completes: merge first, then reclassify', async () => {
    serveSeries([SG_A()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    const calls: string[] = [];
    vi.mocked(api.reclassifyAuthorAsNarrator).mockImplementation(async (id) => {
      calls.push(`reclassify:${id}`);
      return op(`r${id}`);
    });
    vi.mocked(api.mergeSeriesGroup).mockImplementation(async (keep, ids) => {
      calls.push(`merge:${keep}:${ids.join(',')}`);
      return op('s1');
    });
    act(() => result.current.dispatch(sact.narrator('s11-12', 7)));
    expect(result.current.series.choices.narrators['s11-12']?.has(7)).toBe(true);
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(calls).toEqual(['merge:12:11', 'reclassify:7']));
    expect(result.current.series.groups).toEqual([]);
    expect(toast).toHaveBeenCalledWith('Merged 1 series into "series a"', 'success');
  });

  it.each([
    ['the merge operation fails without naming ids', () => settled('failed', 'cancelled')],
    ['the merge fails part-way', () => settled('failed', 'series merge left series ids [11]: x')],
  ])('no author is reclassified when %s, and the flag stays for a retry', async (_name, outcome) => {
    serveSeries([SG_A()]);
    vi.mocked(api.pollOperationV2).mockResolvedValue(outcome());
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.narrator('s11-12', 7)));
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(toast).toHaveBeenCalledWith(expect.stringContaining('narrator flags not applied'), 'error'));
    expect(api.reclassifyAuthorAsNarrator).not.toHaveBeenCalled();
    expect(result.current.series.choices.narrators['s11-12']?.has(7)).toBe(true);
  });

  it('no author is reclassified when the merge request is refused', async () => {
    serveSeries([SG_A()]);
    vi.mocked(api.mergeSeriesGroup).mockRejectedValue(new Error('refused'));
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.narrator('s11-12', 7)));
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(toast).toHaveBeenCalledWith('"Series A": refused (narrator flags not applied)', 'error'));
    expect(api.reclassifyAuthorAsNarrator).not.toHaveBeenCalled();
  });

  it('a reclassification that fails after the merge names the author and does not undo the merge', async () => {
    serveSeries([SG_A()]);
    vi.mocked(api.reclassifyAuthorAsNarrator).mockRejectedValue(new Error('author gone'));
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.narrator('s11-12', 7)));
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        'Merged 1 series into "series a" (narrator reclassification failed or was not sent for author id 7)',
        'warning'
      )
    );
    expect(result.current.series.groups).toEqual([]);
  });

  it('a narrator flag on a series that was left out is not sent', async () => {
    serveSeries([swire('Series A', [srow(11, 'Series A', 5), srow(12, 'series a', 7), srow(13, 'SERIES A')])]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.narrator('s11-12-13', 5)));
    act(() => result.current.dispatch(sact.include('s11-12-13', 11)));
    act(() => result.current.dispatch(sact.merge('s11-12-13')));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalled());
    expect(api.reclassifyAuthorAsNarrator).not.toHaveBeenCalled();
    // 11 left out: 12 (author-linked, lowest id) is kept and 13 merged into it. 11 stays.
    expect(api.mergeSeriesGroup).toHaveBeenCalledWith(12, [13]);
    await waitFor(() => expect(result.current.series.groups[0]?.series.map((r) => r.id)).toEqual([11, 12]));
  });

  it('reads the choices when the merge RUNS: a series left out while it was queued is honoured', async () => {
    serveSeries([swire('Series A', [srow(11, 'Series A'), srow(12, 'series a'), srow(13, 'SERIES A')]), SG_B()]);
    const gate = deferred<api.OperationV2>();
    vi.mocked(api.pollOperationV2).mockReturnValueOnce(gate.promise);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.merge('s21-22'))); // holds the queue
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1));
    act(() => result.current.dispatch(sact.merge('s11-12-13'))); // waits
    act(() => result.current.dispatch(sact.include('s11-12-13', 13)));
    await act(async () => gate.resolve(settled()));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(2));
    expect(api.mergeSeriesGroup).toHaveBeenLastCalledWith(11, [12]);
  });

  it('a group with fewer than two series included is not mergeable, and a merge of it says so', async () => {
    serveSeries([SG_A()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.include('s11-12', 11)));
    expect(result.current.series.mergeable).toEqual([]);
    expect(result.current.series.mergeAll).toEqual([]);
    expect(result.current.series.selectableTotal).toBe(0);
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(toast).toHaveBeenCalledWith('"Series A": fewer than two series are included in this merge', 'warning'));
    expect(api.mergeSeriesGroup).not.toHaveBeenCalled();
  });

  it('a series action never reaches an author endpoint, and an author action never a series one', async () => {
    serve([GROUP_A()]);
    serveSeries([SG_A()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    // A series group whose first id equals an author id: the shapes collide on purpose.
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(result.current.series.groups).toEqual([]));
    expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1);
    expect(api.mergeAuthors).not.toHaveBeenCalled();

    act(() => result.current.setView('authors'));
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch(merge(1)));
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));
    expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1);
  });
});

describe('a series merge that leaves series behind', () => {
  it('keeps the group with the series that remain, and says which ids failed', async () => {
    serveSeries([swire('Series A', [srow(11, 'Series A'), srow(12, 'series a'), srow(13, 'SERIES A')])]);
    vi.mocked(api.pollOperationV2).mockResolvedValue(
      settled('failed', 'series merge left series ids [13]: series merge merged 1 of 2 series with 1 error(s): refusing to delete series 13')
    );
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.merge('s11-12-13')));
    await waitFor(() => expect(toast).toHaveBeenCalledWith(expect.stringContaining('1 series was not merged (ids 13); the other 1 was'), 'error'));
    // 12 went; the kept series (11) and 13 remain: the group is still a duplicate.
    expect(result.current.series.groups[0].series.map((r) => r.id)).toEqual([11, 13]);
    expect(result.current.series.groups[0].key).toBe('s11-12-13'); // the key does not move
    expect(result.current.pendingKeys.size).toBe(0);
  });

  it('a failure with an empty list means everything merged and something later failed: the group goes, the failure is shown', async () => {
    serveSeries([SG_A()]);
    vi.mocked(api.pollOperationV2).mockResolvedValue(
      settled('failed', 'series merge left series ids []: series merge merged 1 of 1 series with 1 error(s): failed to set authors')
    );
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(result.current.series.groups).toEqual([]));
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('failed to set authors'), 'error');
  });

  it('a failure that names no ids (a cancelled run) keeps the group and re-reads', async () => {
    serveSeries([SG_A()]);
    vi.mocked(api.pollOperationV2).mockResolvedValue(settled('failed', 'cancelled'));
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(toast).toHaveBeenCalledWith('"Series A": cancelled', 'error'));
    expect(result.current.series.groups.map((g) => g.key)).toEqual(['s11-12']);
    await waitFor(() => expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(2));
  });
});

describe('series merge selected and merge all', () => {
  it('merge all takes the same-name groups, never the sub-series ones, and merges each through the queue', async () => {
    serveSeries([SG_A(), SG_SUB(), SG_B()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    expect(result.current.series.mergeAll.map((g) => g.key)).toEqual(['s11-12', 's21-22']);
    expect(result.current.series.mergeable).toHaveLength(3);

    act(() => result.current.dispatch(sact.all(['s11-12', 's21-22'])));
    await waitFor(() => expect(result.current.series.groups.map((g) => g.key)).toEqual(['s31-32']));
    expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(2);
    expect(api.deduplicateSeries).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith('Merged 2 of 2 same-name group(s).', 'success');
  });

  it('merge all refuses, and merges nothing, when one group left and another became mergeable (same count)', async () => {
    serveSeries([SG_A(), SG_B(), swire('Series D', [srow(41, 'Series D'), srow(42, 'series d')])]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    // The reviewer confirmed A and B; then B was reduced to one series (it left Merge All)
    // while D, which was already there, is still in: the count is 2 either way.
    act(() => result.current.dispatch(sact.include('s21-22', 21)));
    act(() => result.current.dispatch(sact.all(['s11-12', 's21-22'])));
    expect(api.mergeSeriesGroup).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('Nothing was merged'), 'warning');
    expect(result.current.series.groups).toHaveLength(3);
  });

  it('merge selected reports the failures, with counts, instead of "Merged N"', async () => {
    serveSeries([SG_A(), SG_B()]);
    vi.mocked(api.pollOperationV2).mockImplementation(async (id: string) =>
      id === 's21' ? settled('failed', 'store write failed') : settled()
    );
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    // A failure that names no ids makes the lane re-read; the server now has only B.
    vi.mocked(reads.fetchSeriesGroups).mockResolvedValue({ groups: [SG_B()], totalSeries: 2, needsRefresh: false });
    act(() => result.current.dispatch(sact.selected(['s11-12', 's21-22'])));
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        'Merged 1 of 2 selected group(s); 1 failed: "Series B": store write failed.',
        'error'
      )
    );
    expect(result.current.series.groups.map((g) => g.key)).toEqual(['s21-22']);
    expect(result.current.bulk).toBeNull();
  });

  it('a partly merged group in a bulk run counts as a failure and keeps its remaining series', async () => {
    serveSeries([swire('Series A', [srow(11, 'Series A'), srow(12, 'series a'), srow(13, 'SERIES A')]), SG_B()]);
    vi.mocked(api.pollOperationV2).mockImplementation(async (id: string) =>
      id === 's11' ? settled('failed', 'series merge left series ids [13]: series merge merged 1 of 2') : settled()
    );
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.selected(['s11-12-13', 's21-22'])));
    await waitFor(() => expect(result.current.bulk).toBeNull());
    expect(toast).toHaveBeenCalledWith(expect.stringMatching(/^Merged 1 of 2 selected group\(s\); 1 failed: "Series A": 1 series was not merged/), 'error');
    expect(result.current.series.groups.map((g) => g.series.map((r) => r.id))).toEqual([[11, 13]]);
  });

  it('an expired session stops the run and says how many merged and how many were never sent', async () => {
    serveSeries([SG_A(), SG_B(), swire('Series D', [srow(41, 'Series D'), srow(42, 'series d')])]);
    vi.mocked(api.mergeSeriesGroup)
      .mockImplementationOnce(async (keep: number) => op(`s${keep}`))
      .mockRejectedValueOnce(new ApiAuthRedirectError('/api/v1/series/merge', 'https://login.example/'));
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.selected(['s11-12', 's21-22', 's41-42'])));
    await waitFor(() => expect(result.current.bulk).toBeNull());
    expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(2);
    expect(toast).toHaveBeenCalledTimes(1);
    const [message, severity] = toast.mock.calls[0];
    expect(message).toContain(SESSION_EXPIRED_MESSAGE);
    expect(message).toContain('1 group(s) were merged before it expired');
    expect(message).toContain('the other 1 were not sent');
    expect(severity).toBe('error');
    expect(result.current.series.groups.map((g) => g.key)).toEqual(['s21-22', 's41-42']);
  });

  it('a bulk run stops at the merge that gets no answer and does not send the rest', async () => {
    serveSeries([SG_A(), SG_B()]);
    vi.mocked(api.mergeSeriesGroup).mockImplementation(() => new Promise<api.Operation>(() => {}));
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    act(() => result.current.dispatch(sact.selected(['s11-12', 's21-22'])));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(WRITE_DEADLINE_MS + 1);
    });
    vi.useRealTimers();
    expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('the other 1 were not sent'), 'warning');
    expect(result.current.bulk).toBeNull();
    expect(result.current.pendingKeys.size).toBe(0);
    // It re-read the SERIES list, not the authors one.
    await waitFor(() => expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(2));
  });

  it('a merge that gets no answer unlocks the queue so the next merge goes', async () => {
    serveSeries([SG_A(), SG_B()]);
    vi.mocked(api.mergeSeriesGroup).mockImplementationOnce(() => new Promise<api.Operation>(() => {}));
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    act(() => result.current.dispatch(sact.merge('s11-12')));
    act(() => result.current.dispatch(sact.merge('s21-22')));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(WRITE_DEADLINE_MS + 1);
    });
    vi.useRealTimers();
    expect(toast).toHaveBeenCalledWith(
      `No answer after ${WRITE_DEADLINE_MS / 1000}s — the merge may have run; re-reading.`,
      'warning'
    );
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.pendingKeys.size).toBe(0));
    expect(result.current.busy).toBe(false);
  });
});

describe('leaving the lane during a series merge', () => {
  function hangingPoll() {
    vi.mocked(api.pollOperationV2).mockImplementation(
      (_id, _cb, _ms, opts) =>
        new Promise((_resolve, reject) => {
          opts?.signal?.addEventListener('abort', () =>
            reject(Object.assign(new Error('aborted'), { name: 'AbortError' }))
          );
        })
    );
  }

  it('says WHICH queued merges were not sent, and follows the one already sent when the lane is re-entered', async () => {
    serveSeries([SG_A(), SG_B(), swire('Series D', [srow(41, 'Series D'), srow(42, 'series d')])]);
    const { result, rerender } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    hangingPoll();
    act(() => result.current.dispatch(sact.selected(['s11-12', 's21-22', 's41-42'])));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1));
    expect(result.current.bulk).toEqual({ done: 0, total: 3, entity: 'series' });

    rerender({ active: false });
    await waitFor(() => expect(result.current.bulk).toBeNull());
    expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1);
    // The first was sent; the other two were not, and the message names them.
    expect(toast).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith(
      '2 queued merges were not sent because you left the lane: "Series B", "Series D".',
      'warning'
    );

    const gate = deferred<api.OperationV2>();
    vi.mocked(api.pollOperationV2).mockReset();
    vi.mocked(api.pollOperationV2).mockReturnValue(gate.promise);
    rerender({ active: true });
    await waitFor(() =>
      expect(api.pollOperationV2).toHaveBeenCalledWith('s12', undefined, expect.any(Number), expect.anything())
    );
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    // Locked while it is followed: a second Merge for a group whose first may have gone through is not sent.
    expect([...result.current.pendingKeys]).toEqual(['s11-12']);
    act(() => result.current.dispatch(sact.merge('s11-12')));
    expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1);

    await act(async () => gate.resolve(settled()));
    await waitFor(() => expect(result.current.series.groups.map((g) => g.key)).toEqual(['s21-22', 's41-42']));
    expect(result.current.pendingKeys.size).toBe(0);
    expect(toast).toHaveBeenCalledWith('Merged series into "Series A" (it finished after you left).', 'success');
  });

  it('a followed merge that finished partly drops only the series that went', async () => {
    serveSeries([swire('Series A', [srow(11, 'Series A'), srow(12, 'series a'), srow(13, 'SERIES A')])]);
    const { result, rerender } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    hangingPoll();
    act(() => result.current.dispatch(sact.merge('s11-12-13')));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1));
    rerender({ active: false });
    await waitFor(() => expect(result.current.pendingKeys.size).toBe(0));

    vi.mocked(api.pollOperationV2).mockReset();
    vi.mocked(api.pollOperationV2).mockResolvedValue(settled('failed', 'series merge left series ids [13]: x'));
    rerender({ active: true });
    await waitFor(() => expect(result.current.series.groups[0]?.series.map((r) => r.id)).toEqual([11, 13]));
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('series merge left series ids [13]'), 'error');
  });

  it('a whole bulk run still queued behind another write is dropped, and the message names every group in it', async () => {
    serveSeries([SG_A(), SG_B(), swire('Series D', [srow(41, 'Series D'), srow(42, 'series d')])]);
    const { result, rerender } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    hangingPoll();
    act(() => result.current.dispatch(sact.merge('s11-12'))); // holds the queue
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1));
    act(() => result.current.dispatch(sact.selected(['s21-22', 's41-42']))); // waits behind it
    expect(result.current.bulk).toEqual({ done: 0, total: 2, entity: 'series' });
    rerender({ active: false });
    await waitFor(() => expect(result.current.bulk).toBeNull());
    expect(result.current.pendingKeys.size).toBe(0);
    expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith(
      '2 queued merges were not sent because you left the lane: "Series B", "Series D".',
      'warning'
    );
  });

  it('a queued series merge is dropped, not sent, when the lane is left', async () => {
    serveSeries([SG_A(), SG_B()]);
    const { result, rerender } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    hangingPoll();
    act(() => result.current.dispatch(sact.merge('s11-12')));
    act(() => result.current.dispatch(sact.merge('s21-22')));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1));
    rerender({ active: false });
    await waitFor(() => expect(result.current.pendingKeys.size).toBe(0));
    expect(toast).toHaveBeenCalledWith('1 queued merge was not sent because you left the lane: "Series B".', 'warning');
    rerender({ active: true });
    await act(async () => {});
    expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1);
  });
});

describe('switching sub-views while a write is in flight', () => {
  it('does not abort the write (the lane is still visible), and the other sub-view waits for it', async () => {
    serve([GROUP_A()]);
    serveSeries([SG_A()]);
    const gate = deferred<api.OperationV2>();
    vi.mocked(api.pollOperationV2).mockReturnValue(gate.promise);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1));
    act(() => result.current.setView('authors'));
    await waitFor(() => expect(result.current.status).toBe('populated'));
    expect(result.current.busy).toBe(true);
    const signal = vi.mocked(api.pollOperationV2).mock.calls[0][3]?.signal;
    expect(signal?.aborted).toBe(false);

    await act(async () => gate.resolve(settled()));
    await waitFor(() => expect(result.current.busy).toBe(false));
    expect(toast).toHaveBeenCalledWith('Merged 1 series into "series a"', 'success');
  });
});

describe('series keyboard', () => {
  it('m merges the focused SERIES group and never an author; j/k move the focus', async () => {
    serve([GROUP_A()]);
    serveSeries([SG_A(), SG_B()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    press('j');
    expect(result.current.series.focusedKey).toBe('s21-22');
    press('m');
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledWith(21, [22]));
    expect(api.mergeAuthors).not.toHaveBeenCalled();
  });

  it('with the Series sub-view showing, the author keys do nothing; with Authors showing, the series keys do nothing', async () => {
    serve([GROUP_A()]);
    serveSeries([SG_A()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    // Authors never loaded, so a stray `m` has nothing to merge in either handler's reach.
    press('?');
    expect(result.current.series.helpOpen).toBe(true);
    expect(result.current.helpOpen).toBe(false);
    press('?');

    act(() => result.current.setView('authors'));
    await waitFor(() => expect(result.current.status).toBe('populated'));
    press('?');
    expect(result.current.helpOpen).toBe(true);
    expect(result.current.series.helpOpen).toBe(false);
    press('?');
    press('m');
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));
    expect(api.mergeSeriesGroup).not.toHaveBeenCalled();
  });

  it('m, j and k do nothing behind the help; Escape closes it and they work again', async () => {
    serveSeries([SG_A(), SG_B()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    press('?');
    expect(result.current.series.helpOpen).toBe(true);
    press('j');
    press('m');
    await act(async () => {});
    expect(result.current.series.focusedKey).toBe('s11-12');
    expect(api.mergeSeriesGroup).not.toHaveBeenCalled();

    press('Escape');
    expect(result.current.series.helpOpen).toBe(false);
    press('m');
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1));
  });

  it('m does nothing behind an open name editor, and Escape closes the editor', async () => {
    serveSeries([SG_A()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.series.startEdit('s11-12', asSeriesId(11), 'Series A'));
    press('m');
    await act(async () => {});
    expect(api.mergeSeriesGroup).not.toHaveBeenCalled();
    press('Escape');
    expect(result.current.series.editing).toBeNull();
  });

  it("? is taken before the app's own ? handler sees it, so the lane's help opens and the app's dialog does not", async () => {
    // The app registers its handler on window first (App.tsx), in the bubble phase.
    const appHandler = vi.fn();
    window.addEventListener('keydown', appHandler);
    try {
      serveSeries([SG_A()]);
      const { result } = setup({ view: 'series' });
      await waitFor(() => expect(result.current.series.status).toBe('populated'));
      const pressOnPage = (key: string) =>
        act(() => {
          document.body.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
        });
      pressOnPage('?');
      expect(result.current.series.helpOpen).toBe(true);
      pressOnPage('?'); // closing it is also the lane's
      expect(result.current.series.helpOpen).toBe(false);
      expect(appHandler).not.toHaveBeenCalled();
      // Keys the lane does not take still reach the app.
      pressOnPage('x');
      expect(appHandler).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener('keydown', appHandler);
    }
  });

  it("the Authors sub-view takes ? before the app's own handler too", async () => {
    const appHandler = vi.fn();
    window.addEventListener('keydown', appHandler);
    try {
      serve([GROUP_A()]);
      const { result } = setup();
      await waitFor(() => expect(result.current.status).toBe('populated'));
      const pressOnPage = (key: string) =>
        act(() => {
          document.body.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
        });
      pressOnPage('?');
      expect(result.current.helpOpen).toBe(true);
      pressOnPage('?');
      expect(result.current.helpOpen).toBe(false);
      expect(appHandler).not.toHaveBeenCalled();
      pressOnPage('x');
      expect(appHandler).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener('keydown', appHandler);
    }
  });

  it('a group that cannot be merged is not merged by m', async () => {
    serveSeries([SG_A()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.include('s11-12', 11)));
    press('m');
    await act(async () => {});
    expect(api.mergeSeriesGroup).not.toHaveBeenCalled();
  });

  it('handlers are unbound when the lane is left or the sub-view changes', async () => {
    serveSeries([SG_A()]);
    const { result, rerender } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    rerender({ active: false });
    press('?');
    expect(result.current.series.helpOpen).toBe(false);
  });
});

describe('what is open is cleared when it can no longer be seen (series)', () => {
  it('leaving the Series sub-view closes its help and name editor', async () => {
    serve([]);
    serveSeries([SG_A()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => {
      result.current.series.startEdit('s11-12', asSeriesId(11), 'Series A');
      result.current.series.setHelpOpen(true);
    });
    act(() => result.current.setView('authors'));
    expect(result.current.series.editing).toBeNull();
    expect(result.current.series.helpOpen).toBe(false);
  });

  it('an editor on a group that merged away is closed', async () => {
    serveSeries([SG_A(), SG_B()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.series.startEdit('s21-22', asSeriesId(21), 'Series B'));
    act(() => result.current.dispatch(sact.merge('s21-22')));
    await waitFor(() => expect(result.current.series.groups.map((g) => g.key)).toEqual(['s11-12']));
    expect(result.current.series.editing).toBeNull();
  });
});

describe('series selection', () => {
  it('the selected count is the rows that still exist', async () => {
    serveSeries([SG_A(), SG_B()]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => {
      result.current.series.selection.toggle('s11-12');
      result.current.series.selection.toggle('s21-22');
    });
    expect(result.current.series.selectedCount).toBe(2);
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(result.current.series.groups.map((g) => g.key)).toEqual(['s21-22']));
    expect(result.current.series.selection.selected.has('s11-12')).toBe(true);
    expect(result.current.series.selectedCount).toBe(1);
  });

  it('the per-group choices do not slide onto the next group when one is merged away', async () => {
    // Ported from the old tab's index-key test: merging Series 1 used to shift the
    // keep choice and the selection onto Series 2.
    serveSeries([SG_A(), SG_B(), swire('Series D', [srow(41, 'Series D'), srow(42, 'series d')])]);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.dispatch(sact.include('s41-42', 41)));
    act(() => result.current.series.selection.toggle('s21-22'));
    act(() => result.current.dispatch(sact.merge('s11-12')));
    await waitFor(() => expect(result.current.series.groups.map((g) => g.key)).toEqual(['s21-22', 's41-42']));
    expect(result.current.series.choices.excluded['s41-42']?.has(41)).toBe(true);
    expect(result.current.series.choices.excluded['s21-22']).toBeUndefined();
    expect(result.current.series.selectedGroups.map((g) => g.key)).toEqual(['s21-22']);
    act(() => result.current.dispatch(sact.selected(['s21-22'])));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenLastCalledWith(21, [22]));
  });
});

describe('series scan and validate', () => {
  it('starts one scan across both sub-views however many times it is clicked, then re-reads the series list', async () => {
    serveSeries([SG_A()]);
    const gate = deferred<api.OperationV2>();
    vi.mocked(api.refreshSeriesDuplicates).mockResolvedValue(op('scan1'));
    vi.mocked(api.pollOperationV2).mockReturnValueOnce(gate.promise);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => {
      result.current.series.refresh();
      result.current.series.refresh();
      result.current.refresh(); // the Authors scan cannot start alongside it
    });
    expect(result.current.scanning).toBe(true);
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Series scan started — watch the bell for progress.', 'info'));
    await act(async () => gate.resolve(settled()));
    await waitFor(() => expect(result.current.scanning).toBe(false));
    expect(api.refreshSeriesDuplicates).toHaveBeenCalledTimes(1);
    expect(api.refreshAuthorDuplicates).not.toHaveBeenCalled();
    await waitFor(() => expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(2));
    expect(toast).toHaveBeenCalledWith('Series scan finished.', 'success');
  });

  it('a scan that fails to start says so', async () => {
    serveSeries([SG_A()]);
    vi.mocked(api.refreshSeriesDuplicates).mockRejectedValue(new Error('scan queue full'));
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.series.refresh());
    await waitFor(() => expect(toast).toHaveBeenCalledWith('scan queue full', 'error'));
    expect(result.current.scanning).toBe(false);
  });

  it('validate keeps loading, error and results apart, on the group name', async () => {
    serveSeries([SG_A()]);
    const first = deferred<{ results: api.ValidationResult[]; query: string }>();
    vi.mocked(reads.validateSeriesName).mockReturnValueOnce(first.promise);
    const { result } = setup({ view: 'series' });
    await waitFor(() => expect(result.current.series.status).toBe('populated'));
    act(() => result.current.series.validate('s11-12'));
    expect(result.current.series.validation['s11-12']).toEqual({ forName: 'Series A', state: 'loading' });
    expect(reads.validateSeriesName).toHaveBeenCalledWith('Series A', expect.any(AbortSignal));
    await act(async () =>
      first.resolve({ results: [{ source: 'P', title: 'T', author: 'A' } as api.ValidationResult], query: 'Series A' })
    );
    expect(result.current.series.validation['s11-12']).toMatchObject({ state: 'ready' });

    vi.mocked(reads.validateSeriesName).mockRejectedValueOnce(new Error('providers down'));
    act(() => result.current.series.validate('s11-12'));
    await waitFor(() =>
      expect(result.current.series.validation['s11-12']).toMatchObject({ state: 'error', message: 'providers down' })
    );
  });
});
