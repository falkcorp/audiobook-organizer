// file: web/src/pages/__tests__/DedupLabels.test.tsx
// version: 1.2.0
// guid: 4e0c7a92-8b15-4d63-9f20-3a6e1c8d5b09
// last-edited: 2026-07-11

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import DedupLabels from '../DedupLabels';

const LIBROOT = '/mnt/bigdata/books/audiobook-organizer';

function jsonResponse(body: unknown) {
  return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) } as Response);
}

const sampleRow = {
  candidate_id: 42,
  entity_a_id: 'BOOKA',
  entity_b_id: 'BOOKB',
  layer: 'exact',
  band: 'title_author',
  label: 'unsure',
  label_source: 'rule',
  label_reason: 'part_vs_whole',
  a: { title: 'Mistborn', primary_path: `${LIBROOT}/Sanderson/Mistborn/01.m4b` },
  b: { title: 'Mistborn (CD)', primary_path: `${LIBROOT}/Sanderson/Mistborn-CD/01.m4b` },
};

const suspiciousRow = {
  candidate_id: 77,
  entity_a_id: 'SUSA',
  entity_b_id: 'SUSB',
  layer: 'embedding',
  band: 'CERTAIN',
  label: 'not_dup',
  label_source: 'rule',
  label_reason: 'part_vs_whole',
  suspicion_reasons: ['duplicate-likely band (CERTAIN)', 'cosine similarity >= 0.95'],
  a: { title: 'The Way of Kings', primary_path: `${LIBROOT}/Sanderson/WoK/01.m4b`, total_duration_sec: 3000 },
  b: { title: 'The Way of Kings', primary_path: `${LIBROOT}/Sanderson/WoK-dup/01.m4b`, total_duration_sec: 3010 },
};

describe('DedupLabels page', () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    localStorage.clear();
    fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/config')) return jsonResponse({ data: { config: { root_dir: LIBROOT } } });
      if (url.includes('/dedup/labels/stats')) {
        return jsonResponse({ data: { total: 1, by_label: { unsure: 1 }, by_source: { rule: 1 } } });
      }
      if (url.includes('/override')) return jsonResponse({ data: { status: 'updated', label: 'not_dup', label_source: 'human' } });
      if (url.includes('/dedup/labels/suspicious')) return jsonResponse({ data: { labels: [suspiciousRow], total: 1 } });
      if (url.includes('/dedup/labels')) return jsonResponse({ data: { labels: [sampleRow], total: 1 } });
      return jsonResponse({ data: {} });
    });
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it('renders the abbreviated library path for a labeled pair', async () => {
    render(
      <MemoryRouter>
        <DedupLabels />
      </MemoryRouter>
    );
    expect(await screen.findByText('$(libroot)/Sanderson/Mistborn/01.m4b')).toBeInTheDocument();
  });

  it('sends label, source, and band filters as query params', async () => {
    render(
      <MemoryRouter>
        <DedupLabels />
      </MemoryRouter>
    );
    await screen.findByText('Mistborn');

    fireEvent.mouseDown(screen.getByLabelText('Label'));
    fireEvent.click(screen.getByRole('option', { name: 'true_dup' }));
    fireEvent.mouseDown(screen.getByLabelText('Source'));
    fireEvent.click(screen.getByRole('option', { name: 'human (gold)' }));
    fireEvent.change(screen.getByLabelText('Band'), { target: { value: 'title_author' } });

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([u]) => {
        const url = String(u);
        return url.includes('/dedup/labels?')
          && url.includes('label=true_dup')
          && url.includes('label_source=human')
          && url.includes('band=title_author');
      });
      expect(call).toBeTruthy();
    });
  });

  it('posts an override when a label toggle is clicked', async () => {
    render(
      <MemoryRouter>
        <DedupLabels />
      </MemoryRouter>
    );
    await screen.findByText('Mistborn');
    fireEvent.click(screen.getByRole('button', { name: /^not$/i }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/dedup/labels/42/override'));
      expect(call).toBeTruthy();
      expect(JSON.parse((call![1] as RequestInit).body as string)).toMatchObject({ label: 'not_dup' });
    });
    expect(await screen.findByText('human')).toBeInTheDocument();
  });

  it('renders suspicious rows and reasons when the Suspicious tab is opened', async () => {
    render(
      <MemoryRouter>
        <DedupLabels />
      </MemoryRouter>
    );
    await screen.findByText('Mistborn');

    fireEvent.click(screen.getByRole('tab', { name: 'Suspicious' }));

    // The suspicious pair renders with its reason chips (the pair title appears
    // for both Book A and Book B, so assert on the unique reason text).
    expect(await screen.findByText('cosine similarity >= 0.95')).toBeInTheDocument();
    expect(screen.getByText('duplicate-likely band (CERTAIN)')).toBeInTheDocument();
    expect(screen.getAllByText('The Way of Kings').length).toBeGreaterThan(0);

    // The queue was fetched from the suspicious endpoint.
    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/dedup/labels/suspicious'));
      expect(call).toBeTruthy();
    });
  });

  it('posts an override to the existing route when "Mark true_dup" is clicked', async () => {
    render(
      <MemoryRouter>
        <DedupLabels />
      </MemoryRouter>
    );
    await screen.findByText('Mistborn');
    fireEvent.click(screen.getByRole('tab', { name: 'Suspicious' }));
    await screen.findByText('cosine similarity >= 0.95');

    fireEvent.click(screen.getByRole('button', { name: /Mark true_dup/i }));

    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/dedup/labels/77/override'));
      expect(call).toBeTruthy();
      expect(JSON.parse((call![1] as RequestInit).body as string)).toMatchObject({ label: 'true_dup' });
    });
    // On success the row leaves the queue (its reason chip is gone).
    await waitFor(() => {
      expect(screen.queryByText('cosine similarity >= 0.95')).not.toBeInTheDocument();
    });
  });
});
