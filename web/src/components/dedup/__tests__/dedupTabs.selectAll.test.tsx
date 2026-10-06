// file: web/src/components/dedup/__tests__/dedupTabs.selectAll.test.tsx
// version: 1.0.0
// guid: 6e1a9c40-2b7d-4f58-8c13-9d0e5f2a7b64
// last-edited: 2026-10-06
//
// Select page / select all / shift range on the Authors, Series, AI Review
// and Reconcile tabs. Authors and Series page client-side, so "select all
// matching" covers every group and a wider-than-page merge confirms first.
// AI Review and Reconcile show one list: the header selects it all, skipping
// rows that cannot be selected.

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import type { ReactElement } from 'react';
import { AuthorDedupTab } from '../DedupAuthorTab';
import { SeriesDedupTab } from '../DedupSeriesTab';
import { AIReviewTab } from '../DedupAIReviewTab';
import { ReconcileTab } from '../DedupReconcileTab';

vi.mock('../../../services/api');

const op = (id: string, status = 'completed') =>
  ({ id, type: 'x', status, progress: 0, total: 0, message: '', created_at: '' }) as api.Operation;

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.pollOperation).mockImplementation(async (id: string) => op(id));
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
});

function renderIn(el: ReactElement) {
  return render(<MemoryRouter>{el}</MemoryRouter>);
}

describe('AuthorDedupTab', () => {
  it('select page -> all 30 groups -> confirmed merge of every group', async () => {
    const user = userEvent.setup();
    const groups = Array.from({ length: 30 }, (_, i) => ({
      canonical: { id: i + 1, name: `Author ${i + 1}` },
      variants: [{ id: 100 + i, name: `Author ${i + 1}.` }],
      book_count: 1,
    })) as unknown as api.AuthorDedupGroup[];
    vi.mocked(api.getAuthorDuplicates).mockResolvedValue({ groups, needs_refresh: false } as never);
    vi.mocked(api.mergeAuthors).mockImplementation(async (id: number) => op(`m${id}`));
    renderIn(<AuthorDedupTab />);

    await user.click(
      await screen.findByRole('checkbox', { name: 'Select all 25 groups on this page' })
    );
    await user.click(screen.getByTestId('author-groups-select-all-matching'));
    expect(screen.getByTestId('author-groups-select-all-banner')).toHaveTextContent(
      'All 30 groups matching this filter are selected.'
    );
    await user.click(screen.getByRole('button', { name: 'Merge Selected (30)' }));
    const dialog = await screen.findByTestId('author-merge-selected-confirm');
    expect(api.mergeAuthors).not.toHaveBeenCalled();
    await user.click(within(dialog).getByTestId('author-merge-selected-confirm-btn'));
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(30));
  });
});

describe('SeriesDedupTab', () => {
  function seriesGroup(n: number): api.SeriesDupGroup {
    return {
      name: `Series ${n}`,
      count: 2,
      series: [
        { id: n * 10 + 1, name: `Series ${n}` },
        { id: n * 10 + 2, name: `Series ${n}` },
      ],
    } as unknown as api.SeriesDupGroup;
  }

  it('shift range selects groups, and a single merge does not slide the selection', async () => {
    const user = userEvent.setup();
    vi.mocked(api.getSeriesDuplicates).mockResolvedValue({
      groups: [1, 2, 3, 4].map(seriesGroup),
      total_series: 8,
    } as never);
    vi.mocked(api.mergeSeriesGroup).mockImplementation(async (keep: number) => op(`s${keep}`));
    renderIn(<SeriesDedupTab />);
    const box = async (n: number) =>
      screen.findByRole('checkbox', { name: `Select group Series ${n}` });

    await user.click(await box(2));
    await user.keyboard('{Shift>}');
    await user.click(await box(3));
    await user.keyboard('{/Shift}');
    expect(await screen.findByRole('button', { name: 'Merge Selected (2)' })).toBeInTheDocument();
    await user.click(await box(2)); // leave only group 3 selected

    // Merge group 1 alone; it leaves the list.
    await user.click(screen.getAllByRole('button', { name: /^Merge$/ })[0]);
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledWith(11, [12]));
    await waitFor(() =>
      expect(screen.queryByRole('checkbox', { name: 'Select group Series 1' })).toBeNull()
    );

    // The per-group keep choice did not slide: merging Series 4 on its own
    // keeps Series 4's own series. With index keys it read Series 3's keep
    // list and merged nothing.
    const card4 = (await box(4)).closest('.MuiCard-root') as HTMLElement;
    await user.click(within(card4).getByRole('button', { name: /^Merge$/ }));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenLastCalledWith(41, [42]));

    // And the selection stayed on Series 3.
    await user.click(screen.getByRole('button', { name: 'Merge Selected (1)' }));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(3));
    expect(api.mergeSeriesGroup).toHaveBeenLastCalledWith(31, [32]);
  });
});

describe('AIReviewTab', () => {
  const scan = {
    id: 5,
    status: 'complete',
    mode: 'batch',
    models: {},
    author_count: 0,
    created_at: '',
  };
  const res = (id: number, applied = false, agreement = 'agreed') =>
    ({
      id,
      scan_id: 5,
      agreement,
      applied,
      suggestion: {
        action: 'merge',
        canonical_name: `Name ${id}`,
        reason: '',
        confidence: 'high',
        author_ids: [id],
        source: 'full_scan',
      },
    }) as unknown as api.AIScanResult;

  it('header selects every unapplied result; shift range skips applied ones', async () => {
    const user = userEvent.setup();
    vi.mocked(api.listAIScans).mockResolvedValue([scan as unknown as api.AIScan]);
    vi.mocked(api.getAIScan).mockResolvedValue({
      ...scan,
      phases: [],
    } as unknown as api.AIScanDetail);
    vi.mocked(api.getAIScanResults).mockResolvedValue([
      res(1),
      res(2, true),
      res(3),
      res(4, false, 'disagreed'),
    ]);
    vi.mocked(api.applyAIScanResults).mockResolvedValue(undefined as never);
    renderIn(<AIReviewTab />);
    await user.click(screen.getByRole('button', { name: /scan history/i }));
    await user.click(await screen.findByText(/Scan #5/));
    await screen.findByText('Name 1');

    // The history drawer's close transition hides the page until it ends.
    await user.click(await screen.findByRole('checkbox', { name: 'Select result 1' }));
    await user.keyboard('{Shift>}');
    await user.click(screen.getByRole('checkbox', { name: 'Select result 3' }));
    await user.keyboard('{/Shift}');
    expect(screen.getByRole('button', { name: 'Apply Selected (2)' })).toBeInTheDocument();

    await user.click(screen.getByRole('checkbox', { name: 'Select all 4 results shown' }));
    await user.click(screen.getByRole('button', { name: 'Apply Selected (3)' }));
    await waitFor(() => expect(api.applyAIScanResults).toHaveBeenCalledTimes(1));
    expect([...vi.mocked(api.applyAIScanResults).mock.calls[0][1]].sort()).toEqual([1, 3, 4]);
  });
});

describe('ReconcileTab', () => {
  const match = (id: string, confidence: 'high' | 'low') => ({
    book_id: id,
    book_title: `Book ${id}`,
    old_path: `/a/${id}.m4b`,
    new_path: `/b/${id}.m4b`,
    match_type: 'hash' as const,
    confidence,
    score: 1,
  });

  it('keeps the high-confidence auto-selection, selects all from the header, and shift-selects a range', async () => {
    const user = userEvent.setup();
    vi.mocked(api.getLatestReconcileScan).mockResolvedValue({
      operation: null,
      preview: {
        broken_records: [],
        untracked_files: [],
        unmatched_books: [],
        matches: [match('m1', 'high'), match('m2', 'low'), match('m3', 'low'), match('m4', 'low')],
      },
    } as never);
    renderIn(<ReconcileTab />);
    expect(await screen.findByRole('button', { name: /Apply 1 Fix$/ })).toBeInTheDocument();

    await user.click(screen.getByRole('checkbox', { name: 'Select match m2' }));
    await user.keyboard('{Shift>}');
    await user.click(screen.getByRole('checkbox', { name: 'Select match m4' }));
    await user.keyboard('{/Shift}');
    expect(screen.getByRole('button', { name: /Apply 4 Fixes/ })).toBeInTheDocument();

    await user.click(screen.getByRole('checkbox', { name: 'Select all 4 matches' }));
    expect(screen.getByRole('button', { name: /Apply 0 Fixes/ })).toBeDisabled();
  });
});
