// file: web/src/components/dedup/__tests__/DedupAIReviewTab.test.tsx
// version: 1.0.0
// guid: 2ccccd0b-a2c1-4085-bf94-2c0f78c800bf
// last-edited: 2026-09-19

import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AIReviewTab } from '../DedupAIReviewTab';
import * as api from '../../../services/api';
import type { AIScan, AIScanDetail, AIScanResult } from '../../../services/api';

vi.mock('../../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../../services/api')>();
  return {
    ...actual,
    listAIScans: vi.fn(),
    getAIScan: vi.fn(),
    getAIScanResults: vi.fn(),
  };
});

const superseded: AIScan = {
  id: 9,
  status: 'superseded',
  superseded_by: 12,
  mode: 'batch',
  models: { groups: 'g', full: 'f' },
  author_count: 0,
  created_at: '2026-09-18T00:00:00Z',
};

const result: AIScanResult = {
  id: 1,
  scan_id: 9,
  agreement: 'full_only',
  suggestion: {
    action: 'merge',
    canonical_name: 'A. B. Smith',
    reason: 'same person',
    confidence: 'high',
    author_ids: [1, 2],
    source: 'full_scan',
  },
  applied: false,
};

describe('AIReviewTab — superseded scan', () => {
  beforeEach(() => {
    vi.mocked(api.listAIScans).mockResolvedValue([superseded]);
    vi.mocked(api.getAIScan).mockResolvedValue({ ...superseded, phases: [] } as AIScanDetail);
    vi.mocked(api.getAIScanResults).mockResolvedValue([result]);
  });

  // A superseded scan's list must stay readable — a reviewer reloading it
  // should see what it held — but not applyable, and it must say which newer
  // scan replaced it.
  it('shows its results read-only and names the scan that replaced it', async () => {
    render(
      <MemoryRouter>
        <AIReviewTab />
      </MemoryRouter>
    );
    fireEvent.click(screen.getByRole('button', { name: /scan history/i }));
    fireEvent.click(await screen.findByText(/Scan #9/));

    expect(await screen.findByText('A. B. Smith')).toBeInTheDocument();
    expect(screen.getByText(/superseded by scan #12/i)).toBeInTheDocument();
    // The result card's selection box (the page also has a batch-mode switch).
    const card = screen.getByText('A. B. Smith').closest('.MuiCard-root');
    expect(card).not.toBeNull();
    const boxes = card!.querySelectorAll('input[type="checkbox"]');
    expect(boxes.length).toBeGreaterThan(0);
    boxes.forEach((box) => expect(box).toBeDisabled());
    await waitFor(() => expect(api.getAIScanResults).toHaveBeenCalledWith(9));
  });
});
