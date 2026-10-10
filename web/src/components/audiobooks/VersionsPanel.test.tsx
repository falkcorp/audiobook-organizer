// file: web/src/components/audiobooks/VersionsPanel.test.tsx
// version: 1.0.0
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
    expect(await screen.findByText(/redirected to a login page/i)).toBeInTheDocument();
  });
});
