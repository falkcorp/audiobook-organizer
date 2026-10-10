// file: web/src/components/AnnouncementBanner.test.tsx
// version: 1.2.0
// guid: f7313ac4-8fc8-49db-a24e-4902b36b8f3b
// last-edited: 2026-10-10

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { loginPageWithJsonBody } from '../test/loginRedirect';
import { AnnouncementBanner } from './AnnouncementBanner';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('AnnouncementBanner', () => {
  it('shows an announcement from the { data: { announcements } } envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              data: { announcements: [{ id: 'a1', severity: 'info', message: 'Synthetic notice' }] },
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
      )
    );
    renderWithProviders(<AnnouncementBanner />);
    expect(await screen.findByText('Synthetic notice')).toBeInTheDocument();
  });

  it('renders nothing when the announcements call is answered with a login page, even one whose body parses', async () => {
    const fetchMock = vi.fn(async () =>
      loginPageWithJsonBody({
        data: { announcements: [{ id: 'a1', severity: 'info', message: 'Synthetic notice' }] },
      })
    );
    vi.stubGlobal('fetch', fetchMock);
    const { container } = renderWithProviders(<AnnouncementBanner />);

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    // Announcements are non-critical: an auth bounce must not be parsed as
    // JSON or rendered, and must not throw.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(screen.queryByText('Synthetic notice')).not.toBeInTheDocument();
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });
});
