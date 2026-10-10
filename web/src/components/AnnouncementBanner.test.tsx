// file: web/src/components/AnnouncementBanner.test.tsx
// version: 1.0.0
// guid: f7313ac4-8fc8-49db-a24e-4902b36b8f3b
// last-edited: 2026-10-10

import { afterEach, describe, expect, it, vi } from 'vitest';
import { waitFor } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';
import { loginPageResponse } from '../test/loginRedirect';
import { AnnouncementBanner } from './AnnouncementBanner';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('AnnouncementBanner', () => {
  it('renders nothing when the announcements call is answered with a login page', async () => {
    const fetchMock = vi.fn(async () => loginPageResponse());
    vi.stubGlobal('fetch', fetchMock);
    const { container } = renderWithProviders(<AnnouncementBanner />);

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    // Announcements are non-critical: an auth bounce must not be parsed as
    // JSON or rendered, and must not throw.
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });
});
