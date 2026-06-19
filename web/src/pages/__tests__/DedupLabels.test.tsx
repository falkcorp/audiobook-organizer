// file: web/src/pages/__tests__/DedupLabels.test.tsx
// version: 1.0.0
// guid: 4e0c7a92-8b15-4d63-9f20-3a6e1c8d5b09
// last-edited: 2026-06-19

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
  label: 'unsure',
  label_source: 'rule',
  label_reason: 'part_vs_whole',
  a: { title: 'Mistborn', primary_path: `${LIBROOT}/Sanderson/Mistborn/01.m4b` },
  b: { title: 'Mistborn (CD)', primary_path: `${LIBROOT}/Sanderson/Mistborn-CD/01.m4b` },
};

describe('DedupLabels page', () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/config')) return jsonResponse({ data: { config: { root_dir: LIBROOT } } });
      if (url.includes('/dedup/labels/stats')) {
        return jsonResponse({ data: { total: 1, by_label: { unsure: 1 }, by_source: { rule: 1 } } });
      }
      if (url.includes('/override')) return jsonResponse({ data: { status: 'updated' } });
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
  });
});
