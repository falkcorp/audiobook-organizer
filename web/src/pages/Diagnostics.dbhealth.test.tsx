// file: web/src/pages/Diagnostics.dbhealth.test.tsx
// version: 1.0.0
// guid: f8487a85-3b15-4508-bef2-89cc3d3e84c9
// last-edited: 2026-10-04

import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { Diagnostics } from './Diagnostics';
import * as api from '../services/api';

vi.mock('../services/api', async () => ({
  ...(await vi.importActual('../services/api')),
  getDBHealthStats: vi.fn(),
}));

// Both panels poll their own endpoints on mount; the DB-health card does not
// depend on them.
vi.mock('../components/AIJobsPanel', () => ({ AIJobsPanel: () => null }));
vi.mock('../components/CacheStatsPanel', () => ({ CacheStatsPanel: () => null }));

const mockGetDBHealth = vi.mocked(api.getDBHealthStats);

function health(
  metadataCache: Partial<api.DBHealthStats['metadata_cache']> = {}
): api.DBHealthStats {
  return {
    embeddings: { vector_count: 0, size_bytes: 0 },
    ai_scans: { job_count: 0, pending_count: 0, size_bytes: 0 },
    metadata_cache: {
      total_entries: 1234,
      estimated: true,
      ttl_days: 30,
      expired_entries: -1,
      expired_entries_computed: false,
      ...metadataCache,
    },
  };
}

async function openCard(user: ReturnType<typeof userEvent.setup>) {
  render(<Diagnostics />);
  await user.click(screen.getByText('Database Health'));
  await screen.findByText('Metadata Cache');
}

describe('Diagnostics DB-health expired count', () => {
  beforeEach(() => {
    mockGetDBHealth.mockReset();
  });

  it('offers a Count expired button and shows the count with its walk measurements', async () => {
    const user = userEvent.setup();
    mockGetDBHealth.mockResolvedValueOnce(health());
    mockGetDBHealth.mockResolvedValueOnce(
      health({
        expired_entries: 42,
        expired_entries_computed: true,
        expired_entries_elapsed_ms: 2500,
        expired_entries_pages_walked: 7,
      })
    );
    await openCard(user);
    expect(screen.getByText('30 days')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Count expired' }));
    expect(await screen.findByText('42')).toBeInTheDocument();
    expect(screen.getByText('Counted in 2.5 s (7 pages)')).toBeInTheDocument();
    expect(mockGetDBHealth).toHaveBeenLastCalledWith(true);
    expect(screen.queryByRole('button', { name: 'Count expired' })).not.toBeInTheDocument();
  });

  it.each([0, -1])('shows TTL off in both TTL and Expired when ttl_days is %d', async (ttl) => {
    const user = userEvent.setup();
    mockGetDBHealth.mockResolvedValueOnce(health({ ttl_days: ttl }));
    await openCard(user);
    expect(screen.getAllByText('TTL off')).toHaveLength(2);
    expect(screen.queryByText(/-?\d+ days/)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Count expired' })).not.toBeInTheDocument();
  });

  it('clears a failed-count caption on refresh', async () => {
    const user = userEvent.setup();
    mockGetDBHealth.mockResolvedValueOnce(health());
    mockGetDBHealth.mockResolvedValueOnce(
      health({ expired_entries_error: 'timed out after 2m, 812 pages walked' })
    );
    mockGetDBHealth.mockResolvedValueOnce(health());
    await openCard(user);

    await user.click(screen.getByRole('button', { name: 'Count expired' }));
    expect(
      await screen.findByText('Expired count failed: timed out after 2m, 812 pages walked')
    ).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(mockGetDBHealth).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(screen.queryByText(/Expired count failed/)).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Count expired' })).toBeInTheDocument();
  });
});
