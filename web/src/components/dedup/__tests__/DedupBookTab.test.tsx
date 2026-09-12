// file: web/src/components/dedup/__tests__/DedupBookTab.test.tsx
// version: 1.0.0
// guid: 57376ab7-3c03-4bea-92fc-da54bfa8e9ac
// last-edited: 2026-09-12

import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { DedupBookTab } from '../DedupBookTab';
import * as api from '../../../services/api';
import type { Book, Operation } from '../../../services/api';

// Only the calls DedupBookTab makes are stubbed; pollOperation must be stubbed
// because the real one loops on a 1000ms timer until a terminal status.
vi.mock('../../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../../services/api')>();
  return {
    ...actual,
    getBookDuplicates: vi.fn(),
    mergeBooks: vi.fn(),
    pollOperation: vi.fn(),
  };
});

type DuplicatesResponse = Awaited<ReturnType<typeof api.getBookDuplicates>>;

function makeBook(id: string, title: string): Book {
  return {
    id,
    title,
    author_name: 'Some Author',
    file_path: `/library/${id}.m4b`,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
  };
}

// Three groups, keep book is always the first entry (the component's default).
const GROUPS: Book[][] = [
  [makeBook('a1', 'Alpha Book'), makeBook('a2', 'Alpha Book')],
  [makeBook('b1', 'Bravo Book'), makeBook('b2', 'Bravo Book')],
  [makeBook('c1', 'Charlie Book'), makeBook('c2', 'Charlie Book')],
];

function op(id: string, status: string, error_message?: string): Operation {
  return {
    id,
    type: 'dedup.book-merge',
    status,
    progress: 0,
    total: 0,
    message: '',
    created_at: '2026-09-12T00:00:00Z',
    error_message,
  };
}

// outcomes maps keep-book id -> how that group's merge ends.
type Outcome = 'ok' | 'reject' | 'failed' | 'canceled';

function wireMerges(outcomes: Record<string, Outcome>) {
  vi.mocked(api.mergeBooks).mockImplementation(async (keepId: string) => {
    if (outcomes[keepId] === 'reject') {
      throw new Error(`409: refused for ${keepId}`);
    }
    return op(`op-${keepId}`, 'queued');
  });
  vi.mocked(api.pollOperation).mockImplementation(async (id: string) => {
    const keepId = id.replace(/^op-/, '');
    switch (outcomes[keepId]) {
      case 'failed':
        return op(id, 'failed', `store write failed for ${keepId}`);
      case 'canceled':
        return op(id, 'canceled');
      default:
        return op(id, 'completed');
    }
  });
}

function renderTab() {
  return render(
    <MemoryRouter>
      <DedupBookTab />
    </MemoryRouter>
  );
}

async function clickMergeAll() {
  fireEvent.click(await screen.findByRole('button', { name: /merge all/i }));
  fireEvent.click(await screen.findByRole('button', { name: /confirm/i }));
}

// The post-merge refetch is what used to erase the failures (fetchDuplicates
// clears `error` first). Wait for it to be called AND to settle before
// asserting, so a fix that parks the report in `error` fails these tests.
async function waitForRefetchSettled() {
  await waitFor(() => expect(api.getBookDuplicates).toHaveBeenCalledTimes(2));
  await screen.findAllByText('Alpha Book');
}

describe('DedupBookTab bulk merge outcome reporting', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.getBookDuplicates).mockResolvedValue({
      groups: GROUPS,
      duplicate_count: GROUPS.length,
    } as DuplicatesResponse);
  });

  it('shows a success banner only when every group merged', async () => {
    wireMerges({ a1: 'ok', b1: 'ok', c1: 'ok' });
    renderTab();
    await clickMergeAll();
    await waitForRefetchSettled();

    expect(api.mergeBooks).toHaveBeenCalledTimes(3);
    expect(screen.getByText('Merged 3 of 3 group(s)')).toBeInTheDocument();
    expect(screen.queryByTestId('bulk-merge-report')).not.toBeInTheDocument();
  });

  it('reports "Merged N of M; K failed" when a merge request is rejected, with no success banner', async () => {
    wireMerges({ a1: 'ok', b1: 'reject', c1: 'ok' });
    renderTab();
    await clickMergeAll();
    await waitForRefetchSettled();

    const report = screen.getByTestId('bulk-merge-report');
    expect(report).toHaveTextContent('Merged 2 of 3 group(s); 1 failed');
    expect(report).toHaveTextContent('Bravo Book');
    expect(report).toHaveTextContent('409: refused for b1');
    expect(report).not.toHaveTextContent(/\ball\b/i);
    expect(screen.queryByText(/merged all/i)).not.toBeInTheDocument();
    expect(screen.queryByText('Merged 3 of 3 group(s)')).not.toBeInTheDocument();
  });

  it('counts an operation that ends failed or canceled as a failure, not a success', async () => {
    // pollOperation RESOLVES on these terminal statuses; it does not throw.
    wireMerges({ a1: 'failed', b1: 'ok', c1: 'canceled' });
    renderTab();
    await clickMergeAll();
    await waitForRefetchSettled();

    const report = screen.getByTestId('bulk-merge-report');
    expect(report).toHaveTextContent('Merged 1 of 3 group(s); 2 failed');
    expect(report).toHaveTextContent('Alpha Book');
    expect(report).toHaveTextContent('store write failed for a1');
    expect(report).toHaveTextContent('Charlie Book');
    expect(report).toHaveTextContent('Merge ended with status "canceled"');
    expect(report).not.toHaveTextContent('Bravo Book');
    expect(report).toHaveClass('MuiAlert-colorWarning');
  });

  it('reports every group failed as an error, with no success banner', async () => {
    wireMerges({ a1: 'reject', b1: 'failed', c1: 'reject' });
    renderTab();
    await clickMergeAll();
    await waitForRefetchSettled();

    const report = screen.getByTestId('bulk-merge-report');
    expect(report).toHaveTextContent('Merged 0 of 3 group(s); 3 failed');
    expect(report).toHaveClass('MuiAlert-colorError');
    expect(screen.queryByText(/^Merged \d+ of \d+ group\(s\)$/)).not.toBeInTheDocument();
  });

  it('Merge Selected counts only the groups it attempted', async () => {
    wireMerges({ a1: 'ok', c1: 'reject' });
    renderTab();
    const checkboxes = await screen.findAllByRole('checkbox');
    fireEvent.click(checkboxes[0]);
    fireEvent.click(checkboxes[2]);
    fireEvent.click(screen.getByRole('button', { name: /merge selected \(2\)/i }));
    await waitForRefetchSettled();

    expect(api.mergeBooks).toHaveBeenCalledTimes(2);
    const report = screen.getByTestId('bulk-merge-report');
    expect(report).toHaveTextContent('Merged 1 of 2 group(s); 1 failed');
    expect(report).toHaveTextContent('Charlie Book');
  });

  it('single-group Merge shows an error, not success, when the operation is canceled', async () => {
    wireMerges({ a1: 'canceled' });
    renderTab();
    const mergeButtons = await screen.findAllByRole('button', { name: /^merge$/i });
    fireEvent.click(mergeButtons[0]);

    expect(await screen.findByText('Merge ended with status "canceled"')).toBeInTheDocument();
    expect(screen.queryByText(/merged duplicates of/i)).not.toBeInTheDocument();
  });
});
