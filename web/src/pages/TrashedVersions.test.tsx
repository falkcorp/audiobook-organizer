// file: web/src/pages/TrashedVersions.test.tsx
// version: 1.1.0
// guid: d74a1824-3f19-49fe-9a4f-fdbbea124f4b
// last-edited: 2026-10-10

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
});
