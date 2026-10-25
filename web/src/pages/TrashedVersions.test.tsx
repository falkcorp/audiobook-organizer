// file: web/src/pages/TrashedVersions.test.tsx
// version: 1.3.0
// guid: d74a1824-3f19-49fe-9a4f-fdbbea124f4b
// last-edited: 2026-10-24

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { loginPageResponse } from '../test/loginRedirect';
import TrashedVersions from './TrashedVersions';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('TrashedVersions', () => {
  it('shows an error, not "No trashed versions.", when the session has expired', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => loginPageResponse()));
    renderWithProviders(<TrashedVersions />);
    expect(await screen.findByText(/session has expired/i)).toBeInTheDocument();
    expect(screen.queryByText('No trashed versions.')).not.toBeInTheDocument();
  });

  it('lists versions from the { data: { versions } } envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              data: {
                versions: [
                  {
                    id: 'v1',
                    book_id: 'book-1',
                    status: 'trash',
                    format: 'm4b',
                    source: 'test',
                    ingest_date: '2026-01-01T00:00:00Z',
                    created_at: '2026-01-01T00:00:00Z',
                  },
                ],
              },
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
      )
    );
    renderWithProviders(<TrashedVersions />);
    expect(await screen.findByText(/m4b/i)).toBeInTheDocument();
  });

  it('dates a trash row by trashed_at, and shows a dash when a legacy row has none', async () => {
    const row = (id: string, extra: Record<string, string>) => ({
      id,
      book_id: `book-${id}`,
      status: 'trash',
      format: id === 'stamped' ? 'm4b' : 'mp3',
      source: 'test',
      ingest_date: '2026-01-01T00:00:00Z',
      created_at: '2026-01-01T00:00:00Z',
      ...extra,
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              data: {
                versions: [
                  row('stamped', { trashed_at: '2026-03-15T12:00:00Z' }),
                  row('legacy', {}),
                ],
              },
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
      )
    );
    renderWithProviders(<TrashedVersions />);
    await screen.findByText(/m4b/i);
    expect(screen.getByText(new Date('2026-03-15T12:00:00Z').toLocaleDateString())).toBeInTheDocument();
    expect(screen.getByText('\u2014')).toBeInTheDocument();
    // The legacy row's created date must not stand in for a trash date.
    expect(screen.queryByText(new Date('2026-01-01T00:00:00Z').toLocaleDateString())).not.toBeInTheDocument();
  });

  it('offers no Restore for a purging row, only a retry of the purge', async () => {
    const row = (id: string, status: string, format: string) => ({
      id,
      book_id: `book-${id}`,
      status,
      format,
      source: 'test',
      ingest_date: '2026-01-01T00:00:00Z',
      created_at: '2026-01-01T00:00:00Z',
    });
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              data: { versions: [row('t', 'trash', 'm4b'), row('p', 'purging', 'mp3')] },
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
      )
    );
    renderWithProviders(<TrashedVersions />);
    await screen.findByText(/m4b/i);
    // The trash row keeps Restore (the control: the button is not simply gone).
    expect(screen.getAllByRole('button', { name: 'Restore' })).toHaveLength(1);
    expect(screen.getByRole('button', { name: 'Purge Now' })).toBeInTheDocument();
    // The purging row can be retried but never restored.
    expect(screen.getByRole('button', { name: 'Retry Purge' })).toBeInTheDocument();
    expect(screen.getByText('purging')).toBeInTheDocument();
    // Its created date must not stand in for a trash date.
    expect(screen.queryByText(new Date('2026-01-01T00:00:00Z').toLocaleDateString())).not.toBeInTheDocument();
  });
});
