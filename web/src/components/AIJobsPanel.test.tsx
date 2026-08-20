// file: web/src/components/AIJobsPanel.test.tsx
// version: 1.0.1
// guid: 5b8c9d0e-1f2a-3b4c-5d6e-7f8a9b0c1d2e

import { render, screen, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, afterEach } from 'vitest';
import { AIJobsPanel } from './AIJobsPanel';
import * as api from '../services/api';

vi.mock('../services/api', () => ({
  listAIJobs: vi.fn(),
}));

describe('AIJobsPanel', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('renders in-flight count + table rows', async () => {
    vi.mocked(api.listAIJobs).mockResolvedValue([
      {
        id: 'j1',
        type: 'dedup_review',
        custom_id_prefix: 'x',
        status: 'submitted',
        item_count: 5,
        success_count: 0,
        error_count: 0,
        created_at: '2026-04-24T19:00:00Z',
      },
      {
        id: 'j2',
        type: 'dedup_review',
        custom_id_prefix: 'x',
        status: 'completed',
        item_count: 5,
        success_count: 5,
        error_count: 0,
        created_at: '2026-04-24T18:00:00Z',
        submitted_at: '2026-04-24T18:00:01Z',
        completed_at: '2026-04-24T18:30:00Z',
      },
    ]);
    render(<AIJobsPanel />);
    await waitFor(() => {
      expect(screen.getByText(/1 in flight/)).toBeInTheDocument();
      expect(screen.getByText('submitted')).toBeInTheDocument();
      expect(screen.getByText('completed')).toBeInTheDocument();
    });
  });

  it('renders empty state', async () => {
    vi.mocked(api.listAIJobs).mockResolvedValue([]);
    render(<AIJobsPanel />);
    await waitFor(() => {
      expect(screen.getByText(/No AI jobs recorded/)).toBeInTheDocument();
    });
  });

  it('renders error message on fetch failure', async () => {
    vi.mocked(api.listAIJobs).mockRejectedValue(new Error('Network error'));
    render(<AIJobsPanel />);
    await waitFor(() => {
      expect(screen.getByText(/Network error/)).toBeInTheDocument();
    });
  });
});
