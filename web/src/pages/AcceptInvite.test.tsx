// file: web/src/pages/AcceptInvite.test.tsx
// version: 1.3.1
// guid: 3c9e5a17-8b42-4d6f-a1e0-7f2b8d4c9e53
// last-edited: 2026-11-07

// /invite#token=... on core's native routes (F4-08 B8): the token is read from the fragment, the
// fragment is removed with history.replaceState before the first request, and the token travels
// in a request body only.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { coreJson, coreProblem, stubCoreFetch, type CoreHandler } from '../test/coreFetch';
import { AcceptInvite } from './AcceptInvite';

const TOKEN = 'mcl_' + 'a'.repeat(40);

const preview = (extra: object = {}) =>
  coreJson({ username: 'guest', role: 'viewer', expires_at: '2026-10-22T12:00:00Z', ...extra });

const session = (extra: object = {}) =>
  coreJson({ public_id: 's1', expires_at: '2026-10-23T12:00:00Z', ...extra });

function serve(overrides: Record<string, CoreHandler | Response | (() => Response)> = {}) {
  return stubCoreFetch({
    'POST /auth/invites/lookup': () => preview(),
    'POST /auth/invites/accept': () => session(),
    ...overrides,
  });
}

function renderPage(hash = `#token=${TOKEN}`) {
  window.history.replaceState(null, '', `/invite${hash}`);
  return renderWithProviders(<AcceptInvite />, { initialEntries: ['/invite'] });
}

beforeEach(() => window.history.replaceState(null, '', '/'));
afterEach(() => vi.unstubAllGlobals());

async function fillAndSubmit(user: ReturnType<typeof userEvent.setup>, pw = 'long-enough-pw') {
  await user.type(await screen.findByLabelText(/choose a password/i), pw);
  await user.type(screen.getByLabelText(/confirm password/i), pw);
  await user.click(screen.getByRole('button', { name: /create my account/i }));
}

describe('AcceptInvite', () => {
  it('reads the token from the fragment, removes it, and looks the invite up with it in a body', async () => {
    const replace = vi.spyOn(window.history, 'replaceState');
    const { calls, fetchMock } = serve();
    renderPage();
    expect((await screen.findAllByText('guest', { selector: 'strong' })).length).toBeGreaterThan(0);
    expect(window.location.hash).toBe('');
    expect(replace).toHaveBeenCalled();
    expect(calls[0]).toMatchObject({ method: 'POST', path: '/auth/invites/lookup', body: { token: TOKEN } });
    for (const [input] of fetchMock.mock.calls) {
      expect(String((input as Request).url)).not.toContain(TOKEN);
    }
    replace.mockRestore();
  });

  it('shows the invalid-link message and makes no request when there is no fragment token', async () => {
    const { fetchMock } = serve();
    renderPage('');
    expect(await screen.findByText(/invalid, has already been used, or has expired/i)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('does not read a token from the query string (the old link form)', async () => {
    const { fetchMock } = serve();
    window.history.replaceState(null, '', `/invite?token=${TOKEN}`);
    renderWithProviders(<AcceptInvite />, { initialEntries: ['/invite'] });
    expect(await screen.findByText(/invalid, has already been used, or has expired/i)).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('shows the Cloudflare email step when email_login is set, and no phone-app steps', async () => {
    serve({ 'POST /auth/invites/lookup': () => preview({ email_login: true }) });
    renderPage();
    expect((await screen.findAllByText('guest', { selector: 'strong' })).length).toBeGreaterThan(0);
    expect(screen.getByText(/one-time code/i)).toBeInTheDocument();
    expect(screen.getByText(/phone app access will be added to your account soon/i)).toBeInTheDocument();
  });

  it('leaves out the Cloudflare email step otherwise', async () => {
    serve();
    renderPage();
    await screen.findByLabelText(/choose a password/i);
    expect(screen.queryByText(/one-time code/i)).not.toBeInTheDocument();
    expect(screen.getByText(/sign in with your username/i)).toBeInTheDocument();
  });

  it('never tells a guest to install headers or copy a shared credential', async () => {
    serve();
    renderPage();
    await screen.findByLabelText(/choose a password/i);
    expect(screen.queryByText(/CF-Access-Client/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/custom headers/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/shared guest credentials/i)).not.toBeInTheDocument();
  });

  it('explains an invalid, used or expired token (core answers link_invalid or invite_invalid)', async () => {
    serve({ 'POST /auth/invites/lookup': coreProblem(400, 'invite_invalid', 'the invite is not valid') });
    renderPage();
    expect(await screen.findByText(/invalid, has already been used, or has expired/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/choose a password/i)).not.toBeInTheDocument();
  });

  it('checks the password locally, then accepts with the token in the body', async () => {
    const { calls } = serve();
    const user = userEvent.setup();
    renderPage();

    await fillAndSubmit(user, 'short');
    expect(await screen.findByText(/at least 8 characters\./i)).toBeInTheDocument();
    expect(calls.some((c) => c.path === '/auth/invites/accept')).toBe(false);

    await user.clear(screen.getByLabelText(/choose a password/i));
    await user.clear(screen.getByLabelText(/confirm password/i));
    await user.type(screen.getByLabelText(/choose a password/i), 'long-enough-pw');
    await user.type(screen.getByLabelText(/confirm password/i), 'long-enough-pw');
    await user.click(screen.getByRole('button', { name: /create my account/i }));
    expect(await screen.findByText(/is ready/i)).toBeInTheDocument();
    const accept = calls.find((c) => c.path === '/auth/invites/accept');
    expect(accept?.body).toEqual({ token: TOKEN, password: 'long-enough-pw' });
  });

  it('shows the server refusal when accept fails', async () => {
    serve({ 'POST /auth/invites/accept': coreProblem(422, 'weak_password', 'that password is too common') });
    const user = userEvent.setup();
    renderPage();
    await fillAndSubmit(user);
    await waitFor(() => expect(screen.getByText('that password is too common')).toBeInTheDocument());
  });

  it('promises a one-time key before accept when the lookup says one will be minted', async () => {
    serve({ 'POST /auth/invites/lookup': () => preview({ phone_app_token: true }) });
    renderPage();
    await screen.findByLabelText(/choose a password/i);
    expect(screen.getByText(/shows your own phone-app key once/i)).toBeInTheDocument();
    expect(screen.queryByText(/CF-Access-Client/i)).not.toBeInTheDocument();
  });

  it('shows the guest their own key exactly once after accept', async () => {
    serve({
      'POST /auth/invites/lookup': () => preview({ phone_app_token: true }),
      'POST /auth/invites/accept': () =>
        session({
          phone_app: {
            status: 'issued',
            client_id: 'abc123.access',
            client_secret: 'test-secret-value',
            id_header: 'CF-Access-Client-Id',
            secret_header: 'CF-Access-Client-Secret',
          },
        }),
    });
    const user = userEvent.setup();
    renderPage();
    await fillAndSubmit(user);
    expect(await screen.findByTestId('phone-app-client-id')).toHaveTextContent('abc123.access');
    expect(screen.getByTestId('phone-app-client-secret')).toHaveTextContent('test-secret-value');
    expect(screen.getByText(/only once/i)).toBeInTheDocument();
    expect(screen.queryByText(/will be added to your account soon/i)).not.toBeInTheDocument();
  });

  it('says so when the key could not be created, without blocking the account', async () => {
    serve({
      'POST /auth/invites/lookup': () => preview({ phone_app_token: true }),
      'POST /auth/invites/accept': () =>
        session({
          phone_app: {
            status: 'unavailable',
            message: 'Your phone-app key could not be created. Ask the person who invited you.',
          },
        }),
    });
    const user = userEvent.setup();
    renderPage();
    await fillAndSubmit(user);
    expect(await screen.findByText(/is ready/i)).toBeInTheDocument();
    expect(screen.getByText(/phone-app key could not be created/i)).toBeInTheDocument();
    expect(screen.queryByTestId('phone-app-client-secret')).not.toBeInTheDocument();
  });
});
