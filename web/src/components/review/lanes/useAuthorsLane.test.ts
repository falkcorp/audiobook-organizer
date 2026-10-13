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
import { useAuthorsLane } from './useAuthorsLane';
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

    act(() => result.current.dispatch({ lane: 'authors', entity: 'author', type: 'mergeAllGroups', expectedTotal: 2 }));
    await waitFor(() => expect(result.current.groups.map((g) => g.key)).toEqual(['3', '4']));
    expect(api.mergeAuthors).toHaveBeenCalledTimes(2);
    expect(toast).toHaveBeenCalledWith('Merged 2 of 2 all group(s)', 'success');
  });

  it('merge all refuses, and merges nothing, when the list moved since it was confirmed', async () => {
    serve([GROUP_A(), GROUP_B()]);
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() => result.current.dispatch({ lane: 'authors', entity: 'author', type: 'mergeAllGroups', expectedTotal: 5 }));
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
        'Merged 1 of 2 selected group(s); 1 failed: "Author B": store write failed',
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
  it('a split reclassifies the names flagged as narrators, then reloads', async () => {
    serve([wire(3, 'Author C / Author D', [], { split_names: ['Author C', 'Author D'] })]);
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
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2));
  });

  it('find real author starts the op, waits for it, and reloads on completion', async () => {
    serve([wire(4, 'Studio One', [], { is_production_company: true })]);
    vi.mocked(api.resolveProductionAuthor).mockResolvedValue(op('r1'));
    const { result } = setup();
    await waitFor(() => expect(result.current.status).toBe('populated'));
    act(() =>
      result.current.dispatch({ lane: 'authors', entity: 'author', type: 'resolveProduction', canonicalId: asAuthorId(4) })
    );
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Found the real author(s).', 'success'));
    expect(api.pollOperationV2).toHaveBeenCalledWith('r1', undefined, expect.any(Number), expect.anything());
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2));
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
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'm' }));
    });
    expect(api.mergeAuthors).not.toHaveBeenCalled();
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
