// file: web/src/components/review/lanes/useAuthorsLane.test.ts
// version: 1.0.0
// guid: a5d29e61-7c04-4b8f-93e2-1f6a80c4b573
// last-edited: 2026-10-13

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
import { asAuthorId } from '../reviewActions';
import { ApiAuthRedirectError } from '../../../utils/apiFetch';
import { SESSION_EXPIRED_MESSAGE } from '../../../utils/apiResponse';
import { WRITE_DEADLINE_MS, parseLeftIds, useAuthorsLane } from './useAuthorsLane';
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

function setup(opts: { active?: boolean; authorId?: string | null } = {}) {
  return renderHook(
    ({ active }: { active: boolean }) =>
      useAuthorsLane(toast, active, { authorId: opts.authorId ?? null }),
    { initialProps: { active: opts.active ?? true } }
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  toast.mockReset();
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.pollOperationV2).mockResolvedValue(settled());
  vi.mocked(api.mergeAuthors).mockImplementation(async (id: number) => op(`m${id}`));
  vi.mocked(api.reclassifyAuthorAsNarrator).mockResolvedValue({ narrator_id: 1, books_updated: 1 });
  vi.mocked(reads.fetchAuthorName).mockResolvedValue('');
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
      return { narrator_id: 1, books_updated: 1 };
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
    expect(result.current.bulk).toEqual({ done: 0, total: 2 });

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
    const reclassify = deferred<{ narrator_id: number; books_updated: number }>();
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
    await act(async () => reclassify.resolve({ narrator_id: 1, books_updated: 1 }));
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
