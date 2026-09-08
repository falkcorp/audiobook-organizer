// file: web/src/components/layout/operationsFormat.test.ts
// version: 1.1.0
// guid: 5a8c31e0-9d47-4b62-8e15-2f0a7c94b3d6

import { describe, it, expect } from 'vitest';
import { formatProgressCounts, operationDisplayName } from './operationsFormat';
import type { ActiveOperation } from '../../stores/useOperationsStore';

function op(progress: number, total: number): ActiveOperation {
  return { progress, total } as ActiveOperation;
}

describe('formatProgressCounts', () => {
  it('shows a percentage when the operation has a denominator', () => {
    expect(formatProgressCounts(op(50, 200))).toBe('50 / 200 (25%)');
  });

  it('shows the count for a denominator-less operation that is under way', () => {
    // The Pebble→SQLite activity migration reports no total on purpose. Before
    // this it rendered 'Starting...' for hours while millions of rows went by,
    // which reads as a stuck job rather than a running one.
    expect(formatProgressCounts(op(4_739_375, 0))).toBe('4,739,375 processed');
  });

  it("still says 'Starting...' before there is any progress to show", () => {
    expect(formatProgressCounts(op(0, 0))).toBe('Starting...');
  });
});

// operationDisplayName exists because the bell and the Activity page disagreed:
// one had a private label switch, the other rendered `displayName || def_id`, so
// the activity migration read "Activity Log Migration" in one and
// "activity.sql-migration" in the other on the same screen.
describe('operationDisplayName', () => {
  it('labels the activity migration, whose def_id the server echoes back', () => {
    // No OperationDef is registered for it on purpose (registering one would add
    // a Run button capable of launching a second concurrent backfill), so
    // displayNameFor falls back to the raw def id and sends it as display_name.
    expect(
      operationDisplayName({
        displayName: 'activity.sql-migration',
        def_id: 'activity.sql-migration',
        type: 'sql-migration',
      })
    ).toBe('Activity Log Migration');
  });

  it('prefers a genuine server-supplied name over the local map', () => {
    expect(
      operationDisplayName({ displayName: 'Purge soft-deleted books', def_id: 'scheduler.purge' })
    ).toBe('Purge soft-deleted books');
  });

  it('de-slugs an unknown def id rather than printing it raw', () => {
    expect(operationDisplayName({ def_id: 'maintenance.title-backfill' })).toBe('Title Backfill');
  });
});
