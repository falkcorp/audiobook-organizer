// file: web/src/components/settings/ITunesTransfer.test.tsx
// version: 1.0.0
// guid: 588dcf60-74d5-4d61-b252-e322dec48e53
// last-edited: 2026-10-10

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../../test/renderWithProviders';
import { loginPageResponse } from '../../test/loginRedirect';
import { ToastProvider } from '../toast/ToastProvider';
import { ITunesTransfer } from './ITunesTransfer';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('ITunesTransfer', () => {
  it('reports a failed download, not success, when the session has expired', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => loginPageResponse()));
    const user = userEvent.setup();
    renderWithProviders(
      <ToastProvider>
        <ITunesTransfer />
      </ToastProvider>
    );

    await user.click(screen.getByRole('button', { name: /download/i }));

    expect(await screen.findByText(/Download failed/)).toBeInTheDocument();
    expect(screen.queryByText('ITL file downloaded')).not.toBeInTheDocument();
  });
});
