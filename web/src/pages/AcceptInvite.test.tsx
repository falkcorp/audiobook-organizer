// file: web/src/pages/AcceptInvite.test.tsx
// version: 1.3.0
// guid: 3c9e5a17-8b42-4d6f-a1e0-7f2b8d4c9e53
// last-edited: 2026-10-23

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Route, Routes } from 'react-router-dom';
import { renderWithProviders } from '../test/renderWithProviders';
import { AcceptInvite } from './AcceptInvite';

const TOKEN = 'a'.repeat(64);

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

function renderPage() {
  return renderWithProviders(
    <Routes>
      <Route path="/invite/:token" element={<AcceptInvite />} />
    </Routes>,
    { initialEntries: [`/invite/${TOKEN}`] }
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('AcceptInvite', () => {
  it('shows the invitation, the Cloudflare email step when email_login is set, and no phone-app steps', async () => {
    const fetchMock = vi.fn(async (_url: string) =>
      jsonResponse({
        data: { username: 'guest', expires_at: '2026-10-22T12:00:00Z', email_login: true },
      })
    );
    vi.stubGlobal('fetch', fetchMock);
    renderPage();
    expect((await screen.findAllByText('guest', { selector: 'strong' })).length).toBeGreaterThan(0);
    expect(screen.getByText(/one-time code/i)).toBeInTheDocument();
    expect(
      screen.getByText(/phone app access will be added to your account soon/i)
    ).toBeInTheDocument();
    expect(screen.queryByText(/audiobooth/i)).not.toBeInTheDocument();
    expect(String(fetchMock.mock.calls[0][0])).toBe(`/api/v1/auth/invite/${TOKEN}`);
  });

  it('leaves out the Cloudflare email step when the invite will not be added to the guest policy', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        jsonResponse({
          data: { username: 'guest', expires_at: '2026-10-22T12:00:00Z', email_login: false },
        })
      )
    );
    renderPage();
    await screen.findByLabelText(/choose a password/i);
    expect(screen.queryByText(/one-time code/i)).not.toBeInTheDocument();
    expect(screen.getByText(/sign in with your username/i)).toBeInTheDocument();
    expect(screen.getByText(/phone app access will be added/i)).toBeInTheDocument();
  });

  it('never tells a guest to install headers or copy a shared credential', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        jsonResponse({ data: { username: 'guest', expires_at: '2026-10-22T12:00:00Z' } })
      )
    );
    renderPage();
    await screen.findByLabelText(/choose a password/i);
    expect(screen.queryByText(/CF-Access-Client/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/custom headers/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/shared guest credentials/i)).not.toBeInTheDocument();
  });

  it('explains an invalid, used or expired token', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        jsonResponse(
          { error: 'this invitation is invalid, already used or expired', status: 404 },
          404
        )
      )
    );
    renderPage();
    expect(
      await screen.findByText(/invalid, has already been used, or has expired/i)
    ).toBeInTheDocument();
    expect(screen.queryByLabelText(/choose a password/i)).not.toBeInTheDocument();
  });

  it('checks the password locally, then accepts', async () => {
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/accept-invite')) {
        expect(JSON.parse(String(init?.body))).toEqual({
          token: TOKEN,
          password: 'long-enough-pw',
        });
        return jsonResponse({ data: { user: { username: 'guest' } } }, 201);
      }
      return jsonResponse({ data: { username: 'guest', expires_at: '2026-10-22T12:00:00Z' } });
    });
    vi.stubGlobal('fetch', fetchMock);
    const user = userEvent.setup();
    renderPage();

    await user.type(await screen.findByLabelText(/choose a password/i), 'short');
    await user.type(screen.getByLabelText(/confirm password/i), 'short');
    await user.click(screen.getByRole('button', { name: /create my account/i }));
    expect(await screen.findByText(/at least 8 characters\./i)).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([u]) => String(u).endsWith('/accept-invite'))).toBe(false);

    await user.clear(screen.getByLabelText(/choose a password/i));
    await user.clear(screen.getByLabelText(/confirm password/i));
    await user.type(screen.getByLabelText(/choose a password/i), 'long-enough-pw');
    await user.type(screen.getByLabelText(/confirm password/i), 'long-enough-pw');
    await user.click(screen.getByRole('button', { name: /create my account/i }));
    expect(await screen.findByText(/is ready/i)).toBeInTheDocument();
  });

  it('shows the server refusal when accept fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/accept-invite')
          ? jsonResponse({ error: 'invite already used', status: 400 }, 400)
          : jsonResponse({ data: { username: 'guest', expires_at: '2026-10-22T12:00:00Z' } })
      )
    );
    const user = userEvent.setup();
    renderPage();
    await user.type(await screen.findByLabelText(/choose a password/i), 'long-enough-pw');
    await user.type(screen.getByLabelText(/confirm password/i), 'long-enough-pw');
    await user.click(screen.getByRole('button', { name: /create my account/i }));
    await waitFor(() => expect(screen.getByText('invite already used')).toBeInTheDocument());
  });

  it('promises a one-time key before accept when the lookup says one will be minted', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        jsonResponse({
          data: { username: 'guest', expires_at: '2026-10-22T12:00:00Z', phone_app_token: true },
        })
      )
    );
    renderPage();
    await screen.findByLabelText(/choose a password/i);
    expect(screen.getByText(/shows your own phone-app key once/i)).toBeInTheDocument();
    expect(screen.queryByText(/CF-Access-Client/i)).not.toBeInTheDocument();
  });

  it('shows the guest their own key exactly once after accept', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/accept-invite')
          ? jsonResponse(
              {
                data: {
                  user: { username: 'guest' },
                  phone_app: {
                    status: 'issued',
                    client_id: 'abc123.access',
                    client_secret: 'test-secret-value',
                    id_header: 'CF-Access-Client-Id',
                    secret_header: 'CF-Access-Client-Secret',
                  },
                },
              },
              201
            )
          : jsonResponse({
              data: {
                username: 'guest',
                expires_at: '2026-10-22T12:00:00Z',
                phone_app_token: true,
              },
            })
      )
    );
    const user = userEvent.setup();
    renderPage();
    await user.type(await screen.findByLabelText(/choose a password/i), 'long-enough-pw');
    await user.type(screen.getByLabelText(/confirm password/i), 'long-enough-pw');
    await user.click(screen.getByRole('button', { name: /create my account/i }));
    expect(await screen.findByTestId('phone-app-client-id')).toHaveTextContent('abc123.access');
    expect(screen.getByTestId('phone-app-client-secret')).toHaveTextContent('test-secret-value');
    expect(screen.getByText(/only once/i)).toBeInTheDocument();
    expect(screen.getAllByText(/CF-Access-Client-Id/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/CF-Access-Client-Secret/).length).toBeGreaterThan(0);
    expect(screen.queryByText(/will be added to your account soon/i)).not.toBeInTheDocument();
  });

  it('says so when the key could not be created, without blocking the account', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/accept-invite')
          ? jsonResponse(
              {
                data: {
                  user: { username: 'guest' },
                  phone_app: {
                    status: 'unavailable',
                    message:
                      'Your phone-app key could not be created. Ask the person who invited you.',
                  },
                },
              },
              201
            )
          : jsonResponse({
              data: {
                username: 'guest',
                expires_at: '2026-10-22T12:00:00Z',
                phone_app_token: true,
              },
            })
      )
    );
    const user = userEvent.setup();
    renderPage();
    await user.type(await screen.findByLabelText(/choose a password/i), 'long-enough-pw');
    await user.type(screen.getByLabelText(/confirm password/i), 'long-enough-pw');
    await user.click(screen.getByRole('button', { name: /create my account/i }));
    expect(await screen.findByText(/is ready/i)).toBeInTheDocument();
    expect(screen.getByText(/phone-app key could not be created/i)).toBeInTheDocument();
    expect(screen.queryByTestId('phone-app-client-secret')).not.toBeInTheDocument();
  });
});
