// file: web/src/pages/Settings.factoryReset.test.tsx
// version: 1.0.0
// guid: bce8e8e3-e6b5-4fcb-be91-709f898b3372
// last-edited: 2026-11-08

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithProviders } from '../test/renderWithProviders';

vi.mock('../components/common/ServerFileBrowser', () => ({
  ServerFileBrowser: () => null,
}));
vi.mock('../components/system/SystemInfoTab', () => ({
  SystemInfoTab: () => null,
}));

import { Settings } from './Settings';

// The reset no longer runs through the API: it is an offline command, so the
// System Info tab shows how to run it instead of a button and two dialogs.
describe('Settings factory reset', () => {
  beforeEach(() => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ data: {} }), { status: 200 }))
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('replaces the reset button with the command-line note', async () => {
    renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=system'] });

    expect(await screen.findByText('Danger Zone')).toBeInTheDocument();
    expect(screen.getByText(/audiobook-organizer storage reset/)).toBeInTheDocument();
    expect(
      screen.getByText(/runs from the command line with the service stopped/)
    ).toBeInTheDocument();

    expect(screen.queryByRole('button', { name: /factory reset/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /reset everything/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('never posts to a reset route', async () => {
    renderWithProviders(<Settings />, { initialEntries: ['/settings?tab=system'] });
    await screen.findByText('Danger Zone');

    const urls = vi.mocked(fetch).mock.calls.map((c) => String(c[0]));
    expect(urls.some((u) => u.includes('/system/reset') || u.includes('factory-reset'))).toBe(
      false
    );
  });
});
