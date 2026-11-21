// file: web/src/components/diagnostics/diagnosticsSplit.test.tsx
// version: 0.1.0
// guid: 14b353f9-b15d-4f65-9e4a-466f131a8294
// last-edited: 2026-11-21

// F9-04 slice 3: the Diagnostics page is split into the shell frame (heading and slot) and four
// books pieces (the export/AI diagnostics, the AI jobs panel, the database health card and the
// cache stats panel). The composed page must render them in the order it always did.

import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, it, expect, vi } from 'vitest';
import * as api from '../../services/api';
import { Diagnostics } from '../../pages/Diagnostics';
import { AIDiagnosticsWidget } from '../../modules/books/pages/AIDiagnosticsWidget';
import { DBHealthWidget } from '../../modules/books/pages/DBHealthWidget';
import { DiagnosticsFrame } from './DiagnosticsFrame';
import frameSource from './DiagnosticsFrame.tsx?raw';

vi.mock('../../services/api', async () => ({
  ...(await vi.importActual('../../services/api')),
  getDBHealthStats: vi.fn(),
}));
vi.mock('../AIJobsPanel', () => ({ AIJobsPanel: () => <div data-testid="ai-jobs" /> }));
vi.mock('../CacheStatsPanel', () => ({ CacheStatsPanel: () => <div data-testid="cache-stats" /> }));

const follows = (a: HTMLElement, b: HTMLElement) => Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);

describe('Diagnostics composition (F9-04 slice 3)', () => {
  it('renders heading, intro, categories, AI jobs, database health and cache stats in order', () => {
    render(
      <MemoryRouter>
        <Diagnostics />
      </MemoryRouter>,
    );
    const ordered: HTMLElement[] = [
      screen.getByRole('heading', { name: 'Diagnostics' }),
      screen.getByText(/Export library data for analysis/),
      screen.getByText('Select Category'),
      screen.getByText('Error Analysis'),
      screen.getByText('Deduplication'),
      screen.getByText('Metadata Quality'),
      screen.getByText('General'),
      screen.getByTestId('ai-jobs'),
      screen.getByText('Database Health'),
      screen.getByTestId('cache-stats'),
    ];
    for (let i = 1; i < ordered.length; i++) {
      expect(follows(ordered[i - 1], ordered[i]), `element ${i} follows element ${i - 1}`).toBe(true);
    }
  });

  it('the frame alone is the heading and its slot', () => {
    render(
      <DiagnosticsFrame>
        <p data-testid="slot">slot</p>
      </DiagnosticsFrame>,
    );
    expect(screen.getByRole('heading', { name: 'Diagnostics' })).toBeInTheDocument();
    expect(screen.getByTestId('slot')).toBeInTheDocument();
    expect(screen.queryByText('Select Category')).toBeNull();
    expect(screen.queryByText('Database Health')).toBeNull();
  });

  it('the AI diagnostics widget alone has the intro and categories, and no heading or database card', () => {
    render(
      <MemoryRouter initialEntries={['/diagnostics?category=dedup']}>
        <AIDiagnosticsWidget />
      </MemoryRouter>,
    );
    expect(screen.getByText(/Export library data for analysis/)).toBeInTheDocument();
    expect(screen.getByText('Deduplication')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Diagnostics' })).toBeNull();
    expect(screen.queryByText('Database Health')).toBeNull();
    // ?category= still selects a card, so the deep link survives the cut.
    expect(screen.getByRole('button', { name: /Submit to AI/ })).toBeEnabled();
  });

  it('the database health widget alone loads on first open and nothing else', async () => {
    vi.mocked(api.getDBHealthStats).mockResolvedValue({
      embeddings: { vector_count: 0, size_bytes: 0 },
      ai_scans: { job_count: 0, pending_count: 0, size_bytes: 0 },
      metadata_cache: { total_entries: 7, estimated: false, ttl_days: 30, expired_entries: -1, expired_entries_computed: false },
    });
    render(<DBHealthWidget />);
    expect(api.getDBHealthStats).not.toHaveBeenCalled();
    await userEvent.setup().click(screen.getByText('Database Health'));
    await screen.findByText('Metadata Cache');
    expect(api.getDBHealthStats).toHaveBeenCalledTimes(1);
    expect(screen.queryByText('Select Category')).toBeNull();
  });

  it('the shell frame imports no books code', () => {
    const imports = frameSource.split('\n').filter((l) => /from '/.test(l));
    expect(imports.length).toBeGreaterThan(1);
    for (const l of imports) expect(l).not.toMatch(/modules\/books|services\/api|AIJobsPanel|CacheStats/);
  });
});
