// file: web/src/pages/TempLogin.test.tsx
// version: 0.1.0
// guid: b291dca4-886b-47d0-87e4-9d3b912fb9a0
// last-edited: 2026-11-22

// /login/temp#token=... (F4-08 B8): the fragment token is spent in one POST body; a reset link
// (core answers the password-less call with a 400 and keeps the token) asks for a new password.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { coreJson, coreProblem, stubCoreFetch, type CoreHandler } from '../test/coreFetch';
import { TempLogin } from './TempLogin';

const TOKEN = 'mcl_zatanna-zatara-temp-login-0001';
const goHome = vi.fn();

function renderAt(hash = `#token=${TOKEN}`) {
  window.history.replaceState(null, '', `/login/temp${hash}`);
  return renderWithProviders(<TempLogin onSignedIn={goHome} />, { initialEntries: ['/login/temp'] });
}

const opened = () => coreJson({ public_id: 's1', expires_at: '2026-11-22T12:00:00Z' });
const serve = (h: CoreHandler | Response) => stubCoreFetch({ 'POST /auth/temp-login/consume': h });

beforeEach(() => window.history.replaceState(null, '', '/'));
afterEach(() => {
  vi.unstubAllGlobals();
  goHome.mockClear();
});

describe('TempLogin', () => {
  it('spends the token in one POST body, removes the fragment, and goes to the app', async () => {
    const { calls, fetchMock } = serve(() => opened());
    renderAt();
    await waitFor(() => expect(goHome).toHaveBeenCalled());
    expect(calls).toHaveLength(1);
    expect(calls[0]).toMatchObject({ method: 'POST', path: '/auth/temp-login/consume', body: { token: TOKEN } });
    expect(window.location.hash).toBe('');
    for (const [input] of fetchMock.mock.calls) expect(String((input as Request).url)).not.toContain(TOKEN);
  });

  it('says the link is invalid when core answers 401 link_invalid, and still removes the fragment', async () => {
    serve(() => coreProblem(401, 'link_invalid', 'the link is not valid'));
    renderAt();
    expect(await screen.findByText(/invalid, has already been used, or has expired/i)).toBeInTheDocument();
    expect(goHome).not.toHaveBeenCalled();
    expect(window.location.hash).toBe('');
  });

  it('makes no request without a fragment token (the old query form is not read)', async () => {
    const { fetchMock } = serve(() => opened());
    window.history.replaceState(null, '', `/login/temp?token=${TOKEN}`);
    renderWithProviders(<TempLogin onSignedIn={goHome} />, { initialEntries: ['/login/temp'] });
    expect(await screen.findByText(/invalid, has already been used, or has expired/i)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('asks for a new password when the link is a reset link, then sends it with the same token', async () => {
    let n = 0;
    const { calls } = serve(() =>
      ++n === 1 ? coreProblem(400, 'bad_request', 'a reset link takes a new password') : opened()
    );
    const user = userEvent.setup();
    renderAt();
    await user.type(await screen.findByLabelText(/^new password/i), 'long-enough-pw');
    await user.type(screen.getByLabelText(/confirm password/i), 'long-enough-pw');
    await user.click(screen.getByRole('button', { name: /set password and sign in/i }));
    await waitFor(() => expect(goHome).toHaveBeenCalled());
    expect(calls.map((c) => c.body)).toEqual([
      { token: TOKEN },
      { token: TOKEN, new_password: 'long-enough-pw' },
    ]);
  });
});
