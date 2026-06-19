// file: web/src/components/dedup/__tests__/UnifiedDedupTab.test.tsx
// version: 1.2.1
// guid: d4e5f6a7-b8c9-0123-defa-444567890123
// last-edited: 2026-06-19

import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import { UnifiedDedupTab } from '../UnifiedDedupTab';
import * as api from '../../../services/api';
import type { Book } from '../../../services/api';

// Mock the full api module.
vi.mock('../../../services/api', () => ({
  getDedupCandidates: vi.fn(),
  getDedupStats: vi.fn(),
  mergeDedupCandidate: vi.fn(),
  dismissDedupCandidate: vi.fn(),
  bulkMergeDedupCandidates: vi.fn(),
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

const mockCandidate = {
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

function renderInRouter() {
  return render(
    <MemoryRouter>
      <UnifiedDedupTab />
    </MemoryRouter>
  );
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
});
