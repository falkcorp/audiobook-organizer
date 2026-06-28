// file: web/src/components/dedup/__tests__/UnifiedDedupTab.test.tsx
// version: 1.3.0
// guid: d4e5f6a7-b8c9-0123-defa-444567890123
// last-edited: 2026-06-28

import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import { UnifiedDedupTab } from '../UnifiedDedupTab';
import * as api from '../../../services/api';
import type { Book, DedupBookDetail, DedupCandidate } from '../../../services/api';

// Mock the full api module.
vi.mock('../../../services/api', () => ({
  getDedupCandidates: vi.fn(),
  getDedupStats: vi.fn(),
  mergeDedupCandidate: vi.fn(),
  dismissDedupCandidate: vi.fn(),
  bulkMergeDedupCandidates: vi.fn(),
  getDedupCandidateBreakdown: vi.fn(),
  compareAcoustID: vi.fn(),
  getConfig: vi.fn(),
  rescoreDedupCandidates: vi.fn(),
  triggerDedupScan: vi.fn(),
}));

// Mock the operations store used inside trackOp.
vi.mock('../../../stores/useOperationsStore', () => ({
  useOperationsStore: {
    getState: () => ({ startPolling: vi.fn() }),
  },
}));

// The reworked UnifiedDedupTab renders rich book cards (title / author / path)
// from the inline book_a/book_b objects the API now returns with
// include_books=true — not the bare entity ULIDs. Tests query by these titles.
const bookATitle = 'Dune (FLAC rip)';
const bookBTitle = 'Dune (MP3 rip)';

const mockCandidate: DedupCandidate = {
  id: 1,
  entity_type: 'book' as const,
  entity_a_id: '01ABCDEFGHIJKLMNOPQRSTUV01',
  entity_b_id: '01ABCDEFGHIJKLMNOPQRSTUV02',
  layer: 'embedding' as const,
  similarity: 0.95,
  status: 'pending' as const,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  band: 'HIGH' as const,
  score: 92.5,
  book_a: {
    id: '01ABCDEFGHIJKLMNOPQRSTUV01',
    title: bookATitle,
    author_name: 'Frank Herbert',
    file_path: '/audiobooks/dune-flac.m4b',
  } as Book,
  book_b: {
    id: '01ABCDEFGHIJKLMNOPQRSTUV02',
    title: bookBTitle,
    author_name: 'Frank Herbert',
    file_path: '/audiobooks/dune-mp3.m4b',
  } as Book,
};

const secondMockCandidate: DedupCandidate = {
  ...mockCandidate,
  id: 2,
  entity_a_id: '01ABCDEFGHIJKLMNOPQRSTUV03',
  entity_b_id: '01ABCDEFGHIJKLMNOPQRSTUV04',
  score: 96.2,
  book_a: {
    id: '01ABCDEFGHIJKLMNOPQRSTUV03',
    title: 'Neuromancer (Archive)',
    author_name: 'William Gibson',
    file_path: '/audiobooks/neuromancer-archive.m4b',
  } as Book,
  book_b: {
    id: '01ABCDEFGHIJKLMNOPQRSTUV04',
    title: 'Neuromancer (Tagged)',
    author_name: 'William Gibson',
    file_path: '/audiobooks/neuromancer-tagged.m4b',
    asin: 'B000000001',
  } as Book,
};

function renderInRouter() {
  return render(
    <MemoryRouter>
      <UnifiedDedupTab />
    </MemoryRouter>
  );
}

function mockCandidatesResponse(candidates: DedupCandidate[]) {
  (globalThis.fetch as ReturnType<typeof vi.fn>).mockImplementation((url: string) => {
    if (url.includes('/dedup/stats')) {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ data: { stats: [] } }),
      });
    }
    if (url.includes('/dedup/candidates')) {
      return Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            data: { candidates, total: candidates.length },
          }),
      });
    }
    return Promise.resolve({ ok: false, json: () => Promise.resolve({}) });
  });
}

function toDedupBookDetail(book: Book): DedupBookDetail {
  return {
    id: book.id,
    title: book.title ?? '',
    author_name: book.author_name,
    file_path: book.file_path,
    cover_url: book.cover_url,
    files: [],
  };
}

describe('UnifiedDedupTab', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // UnifiedDedupTab persists multi-select / page-size to localStorage and
    // reads them on init; clear between tests so the suite is order-independent.
    localStorage.clear();
    // Default: fetch returns an empty candidate list.
    vi.mocked(api.getDedupCandidates).mockResolvedValue({
      candidates: [],
      total: 0,
    });
    vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
    vi.mocked(api.getConfig).mockResolvedValue({
      root_dir: '/audiobooks',
    } as Awaited<ReturnType<typeof api.getConfig>>);
    vi.mocked(api.mergeDedupCandidate).mockResolvedValue();
    vi.mocked(api.dismissDedupCandidate).mockResolvedValue();
    vi.mocked(api.getDedupCandidateBreakdown).mockResolvedValue({
      candidate: mockCandidate,
      book_a: toDedupBookDetail(mockCandidate.book_a as Book),
      book_b: toDedupBookDetail(mockCandidate.book_b as Book),
    });
    vi.mocked(api.compareAcoustID).mockResolvedValue({
      book_a: mockCandidate.book_a as Book,
      book_b: mockCandidate.book_b as Book,
      overall_score: 0,
      segment_scores: [],
    });

    // Mock the global fetch used inside the component for AbortController-aware calls.
    globalThis.fetch = vi.fn().mockImplementation((url: string) => {
      if (url.includes('/dedup/stats')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ data: { stats: [] } }),
        });
      }
      if (url.includes('/dedup/candidates')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ data: { candidates: [], total: 0 } }),
        });
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) });
    }) as typeof fetch;
  });

  it('renders the band filter bar', async () => {
    renderInRouter();
    expect(screen.getByTestId('band-filter-bar')).toBeInTheDocument();
  });

  it('renders empty state when no candidates', async () => {
    renderInRouter();
    await waitFor(() => {
      expect(
        screen.getByText(/No candidates found for the current filter/i)
      ).toBeInTheDocument();
    });
  });

  it('renders candidates in the table', async () => {
    (globalThis.fetch as ReturnType<typeof vi.fn>).mockImplementation((url: string) => {
      if (url.includes('/dedup/stats')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ data: { stats: [] } }),
        });
      }
      if (url.includes('/dedup/candidates')) {
        return Promise.resolve({
          ok: true,
          json: () =>
            Promise.resolve({
              data: { candidates: [mockCandidate], total: 1 },
            }),
        });
      }
      return Promise.resolve({ ok: false, json: () => Promise.resolve({}) });
    });

    renderInRouter();
    await waitFor(() => {
      expect(screen.getByText(bookATitle)).toBeInTheDocument();
    });
  });

  it('shows bulk action bar when a candidate is selected via row click', async () => {
    (globalThis.fetch as ReturnType<typeof vi.fn>).mockImplementation((url: string) => {
      if (url.includes('/dedup/stats')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ data: { stats: [] } }),
        });
      }
      return Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({ data: { candidates: [mockCandidate], total: 1 } }),
      });
    });

    renderInRouter();

    await waitFor(() => {
      expect(screen.getByText(bookATitle)).toBeInTheDocument();
    });

    // Checkboxes are hidden by default (behind the Multi-select toggle).
    // Click the row itself — the new click-to-select behavior — to select it.
    const row = screen.getByText(bookATitle).closest('tr');
    expect(row).not.toBeNull();
    fireEvent.click(row as HTMLElement);

    await waitFor(() => {
      expect(screen.getByTestId('bulk-action-bar')).toBeInTheDocument();
    });
  });

  it('shows checkboxes when multi-select toggle is enabled', async () => {
    (globalThis.fetch as ReturnType<typeof vi.fn>).mockImplementation((url: string) => {
      if (url.includes('/dedup/stats')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ data: { stats: [] } }),
        });
      }
      return Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({ data: { candidates: [mockCandidate], total: 1 } }),
      });
    });

    renderInRouter();

    await waitFor(() => {
      expect(screen.getByText(bookATitle)).toBeInTheDocument();
    });

    // Row-selection checkboxes are hidden by default. (The toolbar's
    // "Both need manual matching" filter checkbox is always present, so we
    // assert the count grows once multi-select adds per-row checkboxes rather
    // than asserting zero checkboxes.)
    const baseline = screen.queryAllByRole('checkbox').length;

    // Enable multi-select — per-row checkboxes should appear.
    fireEvent.click(screen.getByTestId('multi-select-toggle'));
    expect(screen.getAllByRole('checkbox').length).toBeGreaterThan(baseline);
  });

  it('shows rescore dialog when rescore button clicked', async () => {
    renderInRouter();
    fireEvent.click(screen.getByTestId('rescore-btn'));
    expect(screen.getByText(/Rescore dedup candidates/i)).toBeInTheDocument();
  });

  it('uses keyboard navigation to merge and dismiss the focused candidate', async () => {
    mockCandidatesResponse([mockCandidate, secondMockCandidate]);

    renderInRouter();

    await waitFor(() => {
      expect(screen.getByText(bookATitle)).toBeInTheDocument();
      expect(screen.getByText('Neuromancer (Archive)')).toBeInTheDocument();
    });

    fireEvent.keyDown(window, { key: 'j' });
    fireEvent.keyDown(window, { key: 'm' });

    await waitFor(() => {
      expect(api.mergeDedupCandidate).toHaveBeenCalledWith(2, secondMockCandidate.entity_b_id);
    });

    fireEvent.keyDown(window, { key: 'k' });
    fireEvent.keyDown(window, { key: 'd' });

    await waitFor(() => {
      expect(api.dismissDedupCandidate).toHaveBeenCalledWith(1);
    });
  });

  it('supports select, select all, compare drawer, escape, and help shortcuts', async () => {
    mockCandidatesResponse([mockCandidate, secondMockCandidate]);

    renderInRouter();

    await waitFor(() => {
      expect(screen.getByText(bookATitle)).toBeInTheDocument();
    });

    fireEvent.keyDown(window, { key: 's' });
    await waitFor(() => {
      expect(screen.getByTestId('bulk-action-bar')).toBeInTheDocument();
    });

    fireEvent.keyDown(window, { key: 'A', shiftKey: true });
    expect(screen.getByText(/2 selected/i)).toBeInTheDocument();

    fireEvent.keyDown(window, { key: 'Enter' });
    await waitFor(() => {
      expect(screen.getByTestId('candidate-compare-drawer')).toBeInTheDocument();
    });

    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByText(/Candidate #1/i)).not.toBeInTheDocument();
    });

    fireEvent.keyDown(window, { key: '?' });
    expect(screen.getByText('Dedup Keyboard Shortcuts')).toBeInTheDocument();
    expect(screen.getByText('Merge the focused candidate')).toBeInTheDocument();

    fireEvent.keyDown(window, { key: '?' });
    await waitFor(() => {
      expect(screen.queryByText('Dedup Keyboard Shortcuts')).not.toBeInTheDocument();
    });
  });

  it('does not run row shortcuts while typing in the search field', async () => {
    mockCandidatesResponse([mockCandidate]);

    renderInRouter();

    const search = await screen.findByPlaceholderText(/Search by book ID/i);
    search.focus();
    expect(search).toHaveFocus();
    fireEvent.keyDown(window, { key: 'd' });
    fireEvent.keyDown(window, { key: '?' });

    expect(api.dismissDedupCandidate).not.toHaveBeenCalled();
    expect(screen.queryByText('Dedup Keyboard Shortcuts')).not.toBeInTheDocument();
  });
});
