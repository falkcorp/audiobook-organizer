// file: web/src/components/review/ReviewWorkspace.ownerRejected.test.tsx
// version: 1.0.0
// guid: 4e2b2514-41c9-47c3-b42b-cec3205fd369
// last-edited: 2026-10-10
//
// A candidate the owner rejected is never applied: the server refuses it as
// owner_rejected whatever pin an apply button sends. The rail marks the row,
// so the owner sees why before an apply comes back blocked.

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
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
}

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
});

describe('owner-rejected candidates on /review', () => {
  it('marks only the rows whose candidate the owner rejected', async () => {
    seed([makeResult('a', { owner_rejected: true }), makeResult('b')]);
    render(
      <MemoryRouter initialEntries={['/review']}>
        <ToastProvider>
          <ReviewWorkspace />
        </ToastProvider>
      </MemoryRouter>
    );
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
    expect(await screen.findByTestId('owner-rejected-a')).toHaveTextContent('Rejected');
    expect(screen.queryByTestId('owner-rejected-b')).not.toBeInTheDocument();
  });
});
