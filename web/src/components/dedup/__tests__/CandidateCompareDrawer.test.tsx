// file: web/src/components/dedup/__tests__/CandidateCompareDrawer.test.tsx
// version: 1.0.0
// guid: c4d5e6f7-a8b9-0123-cdef-cd4567890123
// last-edited: 2026-06-28

import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { CandidateCompareDrawer } from '../CandidateCompareDrawer';
import * as api from '../../../services/api';
import type { DedupCandidateBreakdownResponse } from '../../../services/api';

vi.mock('../../../services/api', () => ({
  compareAcoustID: vi.fn(),
  dismissDedupCandidate: vi.fn(),
  getConfig: vi.fn().mockResolvedValue({ root_dir: '/library' }),
  getDedupCandidateBreakdown: vi.fn(),
  mergeDedupCandidate: vi.fn(),
}));

const breakdown: DedupCandidateBreakdownResponse = {
  candidate: {
    id: 42,
    entity_type: 'book',
    entity_a_id: 'book-a',
    entity_b_id: 'book-b',
    layer: 'embedding',
    similarity: 0.94,
    status: 'pending',
    created_at: '2026-06-28T00:00:00Z',
    updated_at: '2026-06-28T00:00:00Z',
    band: 'HIGH',
    score: 91.4,
    score_breakdown: {
      score: 91.4,
      band: 'HIGH',
      formula: 'v2',
      signals: [
        {
          kind: 'metadata_fuzzy',
          value: 0.91,
          weight: 80,
          evidence: 'Title, narrator, and series are close matches',
          primary: true,
        },
        {
          kind: 'duration',
          value: 0.73,
          weight: 20,
          evidence: 'Durations are close',
          primary: false,
        },
      ],
    },
  },
  book_a: {
    id: 'book-a',
    title: 'The City We Became',
    author_name: 'N. K. Jemisin',
    series_id: 12,
    series_name: 'Great Cities',
    duration: 3610,
    files: [
      {
        id: 'file-a-1',
        file_path: '/library/city-a.m4b',
        file_size: 734003200,
        duration: 3610,
      },
      {
        id: 'file-a-2',
        file_path: '/library/city-a-bonus.m4b',
        file_size: 1048576,
        duration: 60,
      },
    ],
  },
  book_b: {
    id: 'book-b',
    title: 'The City We Became',
    author_name: 'N. K. Jemisin',
    series_id: 13,
    series_name: 'Great Cities: Deluxe',
    duration: 4200,
    files: [
      {
        id: 'file-b-1',
        file_path: '/library/city-b.m4b',
        file_size: 838860800,
        duration: 4200,
      },
    ],
  },
};

function renderDrawer() {
  return render(
    <MemoryRouter>
      <CandidateCompareDrawer candidateId={42} onClose={vi.fn()} />
    </MemoryRouter>
  );
}

describe('CandidateCompareDrawer', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.getDedupCandidateBreakdown).mockResolvedValue(breakdown);
  });

  it('renders a Metadata tab with side-by-side metadata and primary signals', async () => {
    renderDrawer();

    expect(await screen.findByTestId('drawer-tab-metadata')).toBeInTheDocument();
    expect(api.getDedupCandidateBreakdown).toHaveBeenCalledWith(42, expect.any(AbortSignal));

    fireEvent.click(screen.getByTestId('drawer-tab-metadata'));

    expect(await screen.findByTestId('metadata-compare-panel')).toBeInTheDocument();
    expect(screen.getByText('Metadata fuzzy')).toBeInTheDocument();
    expect(screen.getByText('Duration match')).toBeInTheDocument();
    expect(screen.getByText('Great Cities')).toBeInTheDocument();
    expect(screen.getByText('Great Cities: Deluxe')).toBeInTheDocument();
    expect(screen.getByText('2 parts')).toBeInTheDocument();
    expect(screen.getByText('1 part')).toBeInTheDocument();
    expect(screen.getByText('1h 0m')).toBeInTheDocument();
    expect(screen.getByText('1h 10m')).toBeInTheDocument();
    expect(screen.getByText('701.0MB')).toBeInTheDocument();
    expect(screen.getByText('800.0MB')).toBeInTheDocument();

    const seriesRow = screen.getByTestId('metadata-row-series');
    expect(seriesRow).toHaveAttribute('data-different', 'true');
    expect(within(seriesRow).getByText('Series')).toBeInTheDocument();
  });
});
