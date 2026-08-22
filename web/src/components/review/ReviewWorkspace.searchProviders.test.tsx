// file: web/src/components/review/ReviewWorkspace.searchProviders.test.tsx
// version: 1.0.0
// guid: 3f8c2a1d-9e5f-4a2b-8c1e-6d7f9a2b5c3e
// last-edited: 2026-08-22
//
// The Search providers… command in the Metadata menu must send an explicit
// selection for unmatched books, never an empty request body. This test guards
// against regressions where the command sends {} which the server rejects with
// "book_ids or selection is required".

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import userEvent from '@testing-library/user-event';
import { vi, describe, it, expect, beforeEach } from 'vitest';
import * as api from '../../services/api';
import { ReviewWorkspace } from './ReviewWorkspace';
import { ToastProvider } from '../toast/ToastProvider';

vi.mock('../../services/api');

function makeResult(id: string, overrides: Partial<api.CandidateResult> = {}) {
  return {
    book: { id, title: `Book ${id}`, language: 'en' },
    status: 'matched',
    candidate: {
      source: 'audible',
      title: `Cand ${id}`,
      author: 'A',
      narrator: 'N',
      score: 2.0,
      language: 'en',
    },
    ...overrides,
  } as unknown as api.CandidateResult;
}

function renderWorkspace() {
  return render(
    <MemoryRouter initialEntries={['/review']}>
      <ToastProvider>
        <ReviewWorkspace />
      </ToastProvider>
    </MemoryRouter>
  );
}

/** Seeds the review set and the collaborators the workspace touches on mount. */
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
  vi.mocked(api.getReviewCount).mockResolvedValue({ count: 0, byKind: {} });
  vi.mocked(api.batchFetchCandidates).mockResolvedValue({ operation_id: 'op-1' });
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
}

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
});

async function openWorkspace() {
  renderWorkspace();
  await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
}

describe('Search providers command from /review', () => {
  it('sends an explicit unmatched selection, never an empty body', async () => {
    const user = userEvent.setup();
    seed([makeResult('a'), makeResult('b')] as api.CandidateResult[]);
    await openWorkspace();

    await user.click(screen.getByTestId('command-menu-metadata'));
    await user.click(await screen.findByTestId('command-search-providers'));

    await waitFor(() => expect(api.batchFetchCandidates).toHaveBeenCalledTimes(1));
    expect(api.batchFetchCandidates).toHaveBeenCalledWith({
      selection: { filter: { only_unmatched: true } },
    });
    expect(api.batchFetchCandidates).not.toHaveBeenCalledWith({});
  });

  it('the working sibling command is unaffected', async () => {
    const user = userEvent.setup();
    seed([makeResult('a')] as api.CandidateResult[]);
    await openWorkspace();

    await user.click(screen.getByTestId('command-menu-metadata'));

    expect(screen.getByTestId('command-search-providers')).toBeInTheDocument();
    expect(screen.getByTestId('command-bulk-search-selected')).toBeInTheDocument();
  });
});
