// file: web/src/components/layout/operationsFormat.test.tsx
// version: 1.0.0
// guid: 5a8c31e0-9d47-4b62-8e15-2f0a7c94b3d6

import { describe, it, expect } from 'vitest';
import { formatProgressCounts } from './operationsFormat';
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
