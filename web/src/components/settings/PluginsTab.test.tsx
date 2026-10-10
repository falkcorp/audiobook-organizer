// file: web/src/components/settings/PluginsTab.test.tsx
// version: 1.0.0
// guid: 8de4b5c7-6c91-4eb0-8c09-15b4df408d96
// last-edited: 2026-10-10

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { loginPageResponse } from '../../test/loginRedirect';
import PluginsTab from './PluginsTab';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('PluginsTab', () => {
  it('shows an error, not "No plugins registered", when the session has expired', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => loginPageResponse()));
    renderWithProviders(<PluginsTab />);
    expect(await screen.findByText(/redirected to a login page/i)).toBeInTheDocument();
    expect(screen.queryByText(/no plugins registered/i)).not.toBeInTheDocument();
  });
});
