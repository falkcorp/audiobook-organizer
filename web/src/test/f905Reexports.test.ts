// file: web/src/test/f905Reexports.test.ts
// version: 0.1.0
// guid: e0d3d1e2-8064-4802-abc3-a3941f286fe7
// last-edited: 2026-11-21

// F9-05: the old web paths of the moved hooks and operation grouping are
// re-exports of the packages' code, not copies. A copy that crept back would
// fork the behaviour the packages' tests pin.

import { describe, expect, it } from 'vitest';
import * as uiKit from '@mediacore/ui-kit';
import * as shell from '@mediacore/shell';
import * as asyncAction from '../hooks/useAsyncAction';
import * as debounced from '../hooks/useDebouncedSearch';
import * as rowSelection from '../hooks/useRowSelection';
import * as grouping from '../stores/operationGrouping';
import type { ActiveOperation } from '../stores/useOperationsStore';

describe('moved code is re-exported from its package', () => {
  it('ui-kit hooks', () => {
    expect(asyncAction.useAsyncAction).toBe(uiKit.useAsyncAction);
    expect(debounced.useDebouncedSearch).toBe(uiKit.useDebouncedSearch);
    expect(debounced.useSettledSearch).toBe(uiKit.useSettledSearch);
    expect(debounced.serverAnsweredTerm).toBe(uiKit.serverAnsweredTerm);
    expect(rowSelection.useRowSelection).toBe(uiKit.useRowSelection);
    expect(rowSelection.applyRowClick).toBe(uiKit.applyRowClick);
  });

  it('shell operation grouping', () => {
    expect(grouping.groupOperations).toBe(shell.groupOperations);
    expect(grouping.byNewestFirst).toBe(shell.byNewestFirst);
    expect(grouping.groupTimestamp).toBe(shell.groupTimestamp);
    expect(grouping.MIN_GROUP_SIZE).toBe(shell.MIN_GROUP_SIZE);
  });

  // ABO's richer row type still goes through the shell's structural one, and the
  // group parent it builds keeps the fields the Activity page reads.
  it('folds ABO operations through the shell grouping', () => {
    const t0 = Date.UTC(2026, 10, 20, 10, 0, 0);
    const ops = Array.from({ length: 3 }, (_, i): ActiveOperation => ({
      id: `op-${i}`,
      type: 'gadget-sync',
      displayName: 'Gadget sync',
      status: 'completed',
      progress: 1,
      total: 1,
      message: '',
      startedAt: t0 + i * 1000,
      finishedAt: t0 + i * 1000 + 500,
    }) as ActiveOperation);
    const out = grouping.groupOperations(ops);
    expect(out.filter((o) => o.group)).toHaveLength(1);
  });
});
