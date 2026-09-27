// file: web/src/components/review/ReviewWorkspace.replaceConfirm.test.tsx
// version: 1.0.0
// guid: 510b281d-e333-4300-8b36-49e93cd8a7aa
// last-edited: 2026-09-27
//
// Owner decision on #3580: with the bulk toggle on "Replace existing", EVERY
// bulk entry point -- the action bar's buttons, a group's Apply All, and the
// workspace's "Apply selected fields" / "Apply all fields" -- confirms exactly
// once (the lane's applySelected dispatch asks, nothing else does), naming the
// book count. Cancel sends no request. On "Fill empty fields" nothing asks.

import { render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import userEvent from '@testing-library/user-event';
import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import * as api from '../../services/api';
import { ReviewWorkspace } from './ReviewWorkspace';
import { ToastProvider } from '../toast/ToastProvider';
import { STORAGE_KEYS } from '../../lib/storageKeys';

vi.mock('../../services/api');

function makeResult(id: string, candTitle = `Cand ${id}`) {
  return {
    book: { id, title: `Book ${id}`, language: 'en' },
    status: 'matched',
    candidate_hash: `h-${id}`,
    candidate: {
      source: 'audible',
      title: candTitle,
      author: 'A',
      narrator: 'N',
      score: 2.0,
      language: 'en',
    },
  } as unknown as api.CandidateResult;
}

function seed(results: api.CandidateResult[]) {
  vi.mocked(api.getCachedReviewResults).mockResolvedValue({
    results,
    total_count: results.length,
    matched: results.length,
    no_match: 0,
    errors: 0,
    stale: 0,
  } as unknown as Awaited<ReturnType<typeof api.getCachedReviewResults>>);
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  vi.mocked(api.getReviewItems).mockResolvedValue({
    items: [],
    count: 0,
    limit: 500,
    offset: 0,
    total: 0,
  });
  vi.mocked(api.getReviewCount).mockResolvedValue({ count: 0, by_kind: {} });
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
  vi.mocked(api.batchApplyFromCache).mockResolvedValue({
    op_id: 'op-1',
  } as Awaited<ReturnType<typeof api.batchApplyFromCache>>);
  vi.mocked(api.pollOperationV2).mockReturnValue(
    new Promise(() => {}) as ReturnType<typeof api.pollOperationV2>
  );
}

async function openWorkspace(mode: api.BulkApplyMode, results: api.CandidateResult[]) {
  window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_BULK_APPLY_MODE, mode);
  seed(results);
  render(
    <MemoryRouter initialEntries={['/review']}>
      <ToastProvider>
        <ReviewWorkspace />
      </ToastProvider>
    </MemoryRouter>
  );
  await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
}

type User = ReturnType<typeof userEvent.setup>;

/** Each entry point: the rows it needs, and how to click it. Every one sends 2 books. */
const entryPoints: Array<{
  name: string;
  results: () => api.CandidateResult[];
  click: (user: User) => Promise<void>;
}> = [
  {
    name: 'action bar Apply page',
    results: () => [makeResult('a'), makeResult('b')],
    click: async (user) => user.click(await screen.findByTestId('apply-page')),
  },
  {
    name: 'group Apply All',
    // Same candidate on both books: one multi-book group card.
    results: () => [makeResult('a', 'Shared'), makeResult('b', 'Shared')],
    click: async (user) => user.click(await screen.findByTestId('group-apply-all')),
  },
  {
    name: 'workspace Apply selected fields',
    results: () => [makeResult('a'), makeResult('b')],
    click: async (user) => {
      const boxes = within(screen.getByTestId('compare-spine')).getAllByRole('checkbox');
      await user.click(boxes[0]);
      await user.click(boxes[1]);
      await user.click(screen.getByTestId('command-menu-metadata'));
      await user.click(await screen.findByTestId('command-apply-selected-fields'));
    },
  },
  {
    name: 'workspace Apply all fields',
    results: () => [makeResult('a'), makeResult('b')],
    click: async (user) => {
      await user.click(screen.getByTestId('command-menu-metadata'));
      await user.click(await screen.findByTestId('command-apply-all-fields'));
    },
  },
];

let confirmSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
  confirmSpy = vi.spyOn(window, 'confirm');
});

afterEach(() => {
  confirmSpy.mockRestore();
});

describe.each(entryPoints)('$name', ({ results, click }) => {
  it('Replace: confirms once naming the count, then applies with mode replace', async () => {
    confirmSpy.mockReturnValue(true);
    const user = userEvent.setup();
    await openWorkspace('replace', results());
    await click(user);

    await waitFor(() => expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1));
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(confirmSpy).toHaveBeenCalledWith(expect.stringContaining('2 book(s)'));
    expect(confirmSpy).toHaveBeenCalledWith(expect.stringContaining('REPLACE existing values'));
    expect(vi.mocked(api.batchApplyFromCache).mock.calls[0][3]).toBe('replace');
  });

  it('Replace: cancel sends no request', async () => {
    confirmSpy.mockReturnValue(false);
    const user = userEvent.setup();
    await openWorkspace('replace', results());
    await click(user);

    await waitFor(() => expect(confirmSpy).toHaveBeenCalledTimes(1));
    expect(api.batchApplyFromCache).not.toHaveBeenCalled();
  });

  it('Fill: no confirm, applies with mode fill', async () => {
    const user = userEvent.setup();
    await openWorkspace('fill', results());
    await click(user);

    await waitFor(() => expect(api.batchApplyFromCache).toHaveBeenCalledTimes(1));
    expect(confirmSpy).not.toHaveBeenCalled();
    expect(vi.mocked(api.batchApplyFromCache).mock.calls[0][3]).toBe('fill');
  });
});
