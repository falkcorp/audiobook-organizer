// file: web/src/components/layout/operationProgress.test.ts
// version: 0.1.0
// guid: 0d4f7b93-8c26-4e51-b9a0-e3c1d6a5f748
// last-edited: 2026-11-21

import { describe, expect, it } from 'vitest';
import { formatProgressCounts } from './operationProgress';

describe('formatProgressCounts', () => {
  it('shows counts and a rounded percentage when there is a total', () => {
    expect(formatProgressCounts({ progress: 1, total: 3 })).toBe('1 / 3 (33%)');
    expect(formatProgressCounts({ progress: 1500, total: 3000 })).toBe('1,500 / 3,000 (50%)');
  });

  it('shows "processed" for progress without a total (a run that cannot count)', () => {
    expect(formatProgressCounts({ progress: 2_000_000, total: 0 })).toBe('2,000,000 processed');
  });

  it('says "Starting..." with neither', () => {
    expect(formatProgressCounts({ progress: 0, total: 0 })).toBe('Starting...');
  });
});
