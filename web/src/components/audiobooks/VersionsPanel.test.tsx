// file: web/src/components/audiobooks/VersionsPanel.test.tsx
// version: 1.1.0
// guid: 686a1f6e-d9f1-43c4-9dca-1a77f89cd996
// last-edited: 2026-10-10

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { loginPageResponse } from '../../test/loginRedirect';
import VersionsPanel from './VersionsPanel';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('VersionsPanel', () => {
  it('shows an error instead of an empty panel when the session has expired', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => loginPageResponse()));
    renderWithProviders(<VersionsPanel bookId="book-1" />);
    expect(await screen.findByText(/session has expired/i)).toBeInTheDocument();
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
    renderWithProviders(<VersionsPanel bookId="book-1" />);
    expect(await screen.findByText(/m4b/i)).toBeInTheDocument();
  });
});
