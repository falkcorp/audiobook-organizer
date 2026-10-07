// file: web/src/components/audiobooks/stagedMetadataApply.test.ts
// version: 1.0.0
// guid: 7e2d5a90-3c41-4b8f-9a16-d0c4f2b8e731
// last-edited: 2026-10-06

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { MetadataCandidate, OperationV2 } from '../../services/api';

vi.mock('../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/api')>();
  return {
    ApiError: actual.ApiError,
    asinConflictOf: actual.asinConflictOf,
    applyMetadataCandidate: vi.fn(),
    pollOperationV2: vi.fn(),
    getBook: vi.fn(),
    undoLastApply: vi.fn(),
  };
});

import { applyMetadataCandidate, pollOperationV2, getBook } from '../../services/api';
import {
  STAGED_APPLY_CONCURRENCY,
  runBounded,
  sameCandidate,
  submitStagedApplies,
} from './stagedMetadataApply';

const mockApply = vi.mocked(applyMetadataCandidate);
const mockPoll = vi.mocked(pollOperationV2);
const mockGetBook = vi.mocked(getBook);

const candidate: MetadataCandidate = {
  title: 'Synthetic Title',
  author: 'Synthetic Author',
  source: 'openlibrary',
  score: 0.9,
};

beforeEach(() => {
  vi.clearAllMocks();
  mockGetBook.mockImplementation(async (id) => ({ id }) as never);
});

describe('runBounded', () => {
  it('never runs more than the limit at once and runs every item once', async () => {
    let inFlight = 0;
    let peak = 0;
    const seen: number[] = [];
    await runBounded([...Array(10).keys()], 4, async (i) => {
      inFlight += 1;
      peak = Math.max(peak, inFlight);
      await new Promise((r) => setTimeout(r, 1));
      seen.push(i);
      inFlight -= 1;
    });
    expect(peak).toBe(4);
    expect(seen.sort((a, b) => a - b)).toEqual([...Array(10).keys()]);
  });
});

describe('sameCandidate', () => {
  it('matches a re-fetched copy of the same result and not another result', () => {
    expect(sameCandidate(candidate, { ...candidate })).toBe(true);
    expect(sameCandidate(candidate, { ...candidate, source: 'audible' })).toBe(false);
  });
});

describe('submitStagedApplies', () => {
  it('keeps at most STAGED_APPLY_CONCURRENCY applies in flight and toasts once', async () => {
    let inFlight = 0;
    let peak = 0;
    mockApply.mockImplementation(async (bookId) => {
      inFlight += 1;
      peak = Math.max(peak, inFlight);
      await new Promise((r) => setTimeout(r, 1));
      inFlight -= 1;
      return {
        message: 'queued',
        book: { id: bookId } as never,
        source: 'openlibrary',
        operation_id: `op-${bookId}`,
      };
    });
    mockPoll.mockResolvedValue({ status: 'completed' } as OperationV2);
    const toast = vi.fn();
    const onDone = vi.fn();
    const entries = [...Array(10).keys()].map((i) => ({
      book: { id: `b${i}`, title: `Book ${i}` },
      pick: { candidate },
    }));

    await submitStagedApplies({ entries, writeToFiles: false, toast, onDone });

    expect(peak).toBe(STAGED_APPLY_CONCURRENCY);
    expect(mockApply).toHaveBeenCalledTimes(10);
    expect(mockPoll).toHaveBeenCalledTimes(10);
    expect(toast.mock.calls.map((c) => c[0])).toEqual([
      'Applying metadata to 10 books in the background',
      'Metadata applied to 10 of 10 books',
    ]);
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it('reports a failed operation and does not call onDone when nothing changed', async () => {
    mockApply.mockResolvedValue({
      message: 'queued',
      book: { id: 'b1' } as never,
      source: 'openlibrary',
      operation_id: 'op-1',
    });
    mockPoll.mockResolvedValue({ status: 'failed', error_message: 'boom' } as OperationV2);
    const toast = vi.fn();
    const onDone = vi.fn();

    await submitStagedApplies({
      entries: [{ book: { id: 'b1', title: 'Book 1' }, pick: { candidate } }],
      writeToFiles: true,
      toast,
      onDone,
    });

    expect(toast).toHaveBeenLastCalledWith(
      'Metadata apply failed for 1 book: Metadata apply for "Book 1" failed: boom',
      'error'
    );
    expect(onDone).not.toHaveBeenCalled();
  });
});
