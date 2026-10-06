// file: web/src/components/dedup/__tests__/DedupBookTab.selectAll.test.tsx
// version: 1.0.0
// guid: 9a2d5e71-4c38-4f06-b1e9-6d7f0a3c8b52
// last-edited: 2026-10-06
//
// Version Groups tab: select page, select all groups across pages, shift
// range, confirmation for a selection wider than the page -- and the
// regression that index-keyed groups made possible: after a single-group
// merge removes a group, the selection and the "keep" choice must not slide
// onto the next group.

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { DedupBookTab } from '../DedupBookTab';
import * as api from '../../../services/api';
import type { Book, Operation } from '../../../services/api';

vi.mock('../../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../../services/api')>();
  return { ...actual, getBookDuplicates: vi.fn(), linkBooks: vi.fn(), pollOperation: vi.fn() };
});

type DuplicatesResponse = Awaited<ReturnType<typeof api.getBookDuplicates>>;

function book(id: string, title: string): Book {
  return { id, title, file_path: `/lib/${id}.m4b`, created_at: '', updated_at: '' } as Book;
}
const group = (n: number, title = `Title ${String(n).padStart(2, '0')}`) => [
  book(`g${n}a`, title),
  book(`g${n}b`, title),
];

function op(id: string, status: string): Operation {
  return {
    id,
    type: 'dedup.book-merge',
    status,
    progress: 0,
    total: 0,
    message: '',
    created_at: '',
  };
}

function seed(groups: Book[][]) {
  vi.mocked(api.getBookDuplicates).mockResolvedValue({
    groups,
    duplicate_count: groups.length,
  } as DuplicatesResponse);
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.linkBooks).mockImplementation(async (keepId: string) =>
    op(`op-${keepId}`, 'queued')
  );
  vi.mocked(api.pollOperation).mockImplementation(async (id: string) => op(id, 'completed'));
});

function renderTab() {
  return render(
    <MemoryRouter>
      <DedupBookTab />
    </MemoryRouter>
  );
}

describe('DedupBookTab selection', () => {
  it('selects the page, then all groups across pages, and confirms before merging them', async () => {
    const user = userEvent.setup();
    seed(Array.from({ length: 30 }, (_, i) => group(i + 1)));
    renderTab();
    await screen.findByRole('checkbox', { name: 'Select all 25 groups on this page' });

    await user.click(screen.getByRole('checkbox', { name: 'Select all 25 groups on this page' }));
    expect(screen.getByTestId('book-groups-select-all-banner')).toHaveTextContent(
      'All 25 groups on this page are selected.'
    );
    await user.click(screen.getByTestId('book-groups-select-all-matching'));
    expect(screen.getByTestId('book-groups-select-all-banner')).toHaveTextContent(
      'All 30 groups matching this filter are selected.'
    );

    await user.click(screen.getByRole('button', { name: 'Merge Selected (30)' }));
    const dialog = await screen.findByTestId('book-merge-selected-confirm');
    expect(dialog).toHaveTextContent('Merge 30 selected groups?');
    expect(api.linkBooks).not.toHaveBeenCalled();
    await user.click(within(dialog).getByTestId('book-merge-selected-confirm-btn'));
    await waitFor(() => expect(api.linkBooks).toHaveBeenCalledTimes(30));
  });

  it('shift-click selects a range of groups', async () => {
    const user = userEvent.setup();
    seed(Array.from({ length: 5 }, (_, i) => group(i + 1)));
    renderTab();
    const box = async (n: number) =>
      screen.findByRole('checkbox', { name: `Select group Title 0${n}` });
    await user.click(await box(1));
    await user.keyboard('{Shift>}');
    await user.click(await box(4));
    await user.keyboard('{/Shift}');
    expect(await screen.findByRole('button', { name: 'Merge Selected (4)' })).toBeInTheDocument();
    expect(await box(5)).not.toBeChecked();
  });

  it('after a single-group merge, the selection stays on the group that was ticked', async () => {
    const user = userEvent.setup();
    seed([group(1), group(2), group(3), group(4)]);
    renderTab();
    await user.click(await screen.findByRole('checkbox', { name: 'Select group Title 03' }));

    // Merge group 1 on its own: it leaves the list (the server is not refetched).
    const cards = screen.getAllByRole('button', { name: 'Merge' });
    await user.click(cards[0]);
    await waitFor(() => expect(api.linkBooks).toHaveBeenCalledWith('g1a', ['g1b']));
    await waitFor(() => expect(screen.queryByText('Title 01')).not.toBeInTheDocument());

    await user.click(screen.getByRole('button', { name: 'Merge Selected (1)' }));
    await waitFor(() => expect(api.linkBooks).toHaveBeenCalledTimes(2));
    // Group 3 keeps its own book. With index keys the selection slid to group
    // 4 and merged it keeping group 3's book.
    expect(api.linkBooks).toHaveBeenLastCalledWith('g3a', ['g3b']);
  });
});
