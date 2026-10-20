// file: web/src/pages/Users.test.tsx
// version: 1.4.1
// guid: 26b39541-b8d3-4fc8-b131-190f15f5a5ab
// last-edited: 2026-10-19

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import { loginPageResponse } from '../test/loginRedirect';
import Users from './Users';

// The server wraps every success as { data: ... } (httputil.SuccessResponse) and
// every failure as { error, code, status }; the helpers below use those shapes.
const envelope = (data: unknown) => jsonResponse({ data });

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const activeUser = {
  id: 'u1',
  username: 'test-user',
  role_id: 'editor',
  status: 'active',
  created_at: '2026-01-01T00:00:00Z',
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('Users', () => {
  it('shows an error, not an empty list, when the session has expired on load', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => loginPageResponse())
    );
    renderWithProviders(<Users />);
    expect(await screen.findByText(/session has expired/i)).toBeInTheDocument();
  });

  it('surfaces a failed deactivate instead of silently reloading', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url.endsWith('/deactivate'))
        return jsonResponse({ error: 'cannot deactivate last admin', status: 409 }, 409);
      if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
      return envelope({ users: [activeUser], count: 1 });
    });
    vi.stubGlobal('fetch', fetchMock);
    const user = userEvent.setup();
    renderWithProviders(<Users />);

    await screen.findByText('test-user');
    await user.click(screen.getByRole('button', { name: /deactivate/i }));

    expect(await screen.findByText('cannot deactivate last admin')).toBeInTheDocument();
    // No reload after the failed write: one /users fetch from the initial load only.
    const userListCalls = fetchMock.mock.calls.filter(([u]) => String(u).endsWith('/users'));
    expect(userListCalls).toHaveLength(1);
  });

  it('shows an expired-session error when deactivate is answered with a login page', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (url.endsWith('/deactivate')) return loginPageResponse();
        if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
        return envelope({ users: [activeUser], count: 1 });
      })
    );
    const user = userEvent.setup();
    renderWithProviders(<Users />);

    await screen.findByText('test-user');
    await user.click(screen.getByRole('button', { name: /deactivate/i }));

    expect(await screen.findByText(/session has expired/i)).toBeInTheDocument();
  });

  it('surfaces a failed reactivate', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (url.endsWith('/reactivate')) return jsonResponse({}, 500);
        if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
        return envelope({ users: [{ ...activeUser, status: 'locked' }], count: 1 });
      })
    );
    const user = userEvent.setup();
    renderWithProviders(<Users />);

    await screen.findByText('test-user');
    await user.click(screen.getByRole('button', { name: /reactivate/i }));

    await waitFor(() =>
      expect(screen.getByText('Failed to reactivate user (HTTP 500)')).toBeInTheDocument()
    );
  });

  it('renders users from the real { data: { users } } envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/users/invites')
          ? envelope({ invites: [], count: 0 })
          : envelope({ users: [activeUser], count: 1 })
      )
    );
    renderWithProviders(<Users />);
    expect(await screen.findByText('test-user')).toBeInTheDocument();
  });

  it('shows an error when the user list answers with a server error', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/users/invites')
          ? envelope({ invites: [], count: 0 })
          : jsonResponse({ error: 'boom', status: 500 }, 500)
      )
    );
    renderWithProviders(<Users />);
    expect(await screen.findByText('Failed to load users')).toBeInTheDocument();
  });

  it('reloads the list after a successful deactivate', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      if (url.endsWith('/deactivate')) return envelope({ status: 'ok' });
      if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
      return envelope({ users: [activeUser], count: 1 });
    });
    vi.stubGlobal('fetch', fetchMock);
    const user = userEvent.setup();
    renderWithProviders(<Users />);

    await screen.findByText('test-user');
    await user.click(screen.getByRole('button', { name: /deactivate/i }));

    await waitFor(() => {
      const listCalls = fetchMock.mock.calls.filter(([u]) => String(u).endsWith('/users'));
      expect(listCalls).toHaveLength(2);
    });
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('says why invitation email is unavailable', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (url.endsWith('/users/invites'))
          return envelope({
            invites: [],
            count: 0,
            mail_configured: false,
            mail_unavailable_reason: "the server's public address is not set (PUBLIC_BASE_URL)",
          });
        return envelope({ users: [activeUser], count: 1 });
      })
    );
    const user = userEvent.setup();
    renderWithProviders(<Users />);
    await screen.findByText('test-user');
    await user.click(screen.getByRole('button', { name: /create invite/i }));
    expect(await screen.findByText(/PUBLIC_BASE_URL/)).toBeInTheDocument();
    expect(screen.getByRole('checkbox')).toBeDisabled();
  });

  describe('set email', () => {
    const withEmail = { ...activeUser, email: 'old@example.com' };

    it('PUTs the address and reloads with a notice', async () => {
      const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
        if (url.endsWith('/users/u1/email')) {
          expect(init?.method).toBe('PUT');
          expect(JSON.parse(String(init?.body))).toEqual({ email: 'new@example.com' });
          return envelope({ user: { ...withEmail, email: 'new@example.com' } });
        }
        if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
        return envelope({ users: [withEmail], count: 1 });
      });
      vi.stubGlobal('fetch', fetchMock);
      const user = userEvent.setup();
      renderWithProviders(<Users />);

      await screen.findByText('test-user');
      await user.click(screen.getByRole('button', { name: /set email for test-user/i }));
      const field = screen.getByLabelText(/^email/i);
      await user.clear(field);
      await user.type(field, 'new@example.com');
      await user.click(screen.getByRole('button', { name: /^save$/i }));

      expect(await screen.findByText(/set to new@example.com/i)).toBeInTheDocument();
      const userListCalls = fetchMock.mock.calls.filter(([u]) => String(u).endsWith('/users'));
      expect(userListCalls).toHaveLength(2);
    });

    it('clears the address with an empty string', async () => {
      const bodies: unknown[] = [];
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string, init?: RequestInit) => {
          if (url.endsWith('/users/u1/email')) {
            bodies.push(JSON.parse(String(init?.body)));
            return envelope({ user: { ...withEmail, email: '' } });
          }
          if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
          return envelope({ users: [withEmail], count: 1 });
        })
      );
      const user = userEvent.setup();
      renderWithProviders(<Users />);

      await screen.findByText('test-user');
      await user.click(screen.getByRole('button', { name: /set email for test-user/i }));
      await user.click(screen.getByRole('button', { name: /clear email/i }));

      expect(await screen.findByText(/cleared/i)).toBeInTheDocument();
      expect(bodies).toEqual([{ email: '' }]);
    });

    it('keeps the dialog open and shows the server refusal', async () => {
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string) => {
          if (url.endsWith('/users/u1/email'))
            return jsonResponse(
              { error: "setting another user's email needs the admin role", status: 403 },
              403
            );
          if (url.endsWith('/users/invites')) return envelope({ invites: [], count: 0 });
          return envelope({ users: [withEmail], count: 1 });
        })
      );
      const user = userEvent.setup();
      renderWithProviders(<Users />);

      await screen.findByText('test-user');
      await user.click(screen.getByRole('button', { name: /set email for test-user/i }));
      await user.click(screen.getByRole('button', { name: /^save$/i }));

      expect(await screen.findByText(/needs the admin role/i)).toBeInTheDocument();
      expect(screen.getByRole('dialog')).toBeInTheDocument();
    });
  });

  describe('invitations', () => {
    const pending = {
      token: 'b'.repeat(64),
      username: 'pending-guest',
      email: 'pending@example.com',
      role_id: 'viewer',
      expires_at: '2026-10-22T12:00:00Z',
    };

    it('creates an emailed invite with the email and send flag, and shows the result', async () => {
      const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
        if (url.endsWith('/users/invite')) {
          expect(JSON.parse(String(init?.body))).toEqual({
            username: 'newguest',
            role_id: 'editor',
            email: 'new@example.com',
            send_email: true,
          });
          return jsonResponse({ data: { token: 'c'.repeat(64), email_sent: true } }, 201);
        }
        if (url.endsWith('/users/invites'))
          return envelope({ invites: [], count: 0, mail_configured: true });
        return envelope({ users: [activeUser], count: 1 });
      });
      vi.stubGlobal('fetch', fetchMock);
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('test-user');
      await user.click(screen.getByRole('button', { name: /create invite/i }));
      await user.type(screen.getByLabelText(/username/i), 'newguest');
      await user.type(screen.getByLabelText(/^email/i), 'new@example.com');
      await user.click(screen.getAllByRole('button', { name: /create invite/i }).at(-1)!);
      expect(await screen.findByText(/invitation emailed to new@example.com/i)).toBeInTheDocument();
      expect(screen.getByText(new RegExp(`/invite/${'c'.repeat(64)}`))).toBeInTheDocument();
    });

    it('falls back to the link and shows why when the email was not sent', async () => {
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string) => {
          if (url.endsWith('/users/invite'))
            return jsonResponse(
              {
                data: {
                  token: 'd'.repeat(64),
                  email_sent: false,
                  email_error: 'sending failed: 550',
                },
              },
              201
            );
          if (url.endsWith('/users/invites'))
            return envelope({ invites: [], count: 0, mail_configured: true });
          return envelope({ users: [activeUser], count: 1 });
        })
      );
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('test-user');
      await user.click(screen.getByRole('button', { name: /create invite/i }));
      await user.type(screen.getByLabelText(/username/i), 'g');
      await user.type(screen.getByLabelText(/^email/i), 'g@example.com');
      await user.click(screen.getAllByRole('button', { name: /create invite/i }).at(-1)!);
      expect(
        await screen.findByText(/the email was not sent: sending failed: 550/i)
      ).toBeInTheDocument();
      expect(screen.getByText(/send this link to g yourself/i)).toBeInTheDocument();
    });

    it('disables the send checkbox when email is not configured and sends send_email=false', async () => {
      const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
        if (url.endsWith('/users/invite')) {
          expect(JSON.parse(String(init?.body)).send_email).toBe(false);
          return jsonResponse({ data: { token: 'e'.repeat(64), email_sent: false } }, 201);
        }
        if (url.endsWith('/users/invites'))
          return envelope({ invites: [], count: 0, mail_configured: false });
        return envelope({ users: [activeUser], count: 1 });
      });
      vi.stubGlobal('fetch', fetchMock);
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      await screen.findByText('test-user');
      await user.click(screen.getByRole('button', { name: /create invite/i }));
      expect(screen.getByRole('checkbox', { name: /email is not configured/i })).toBeDisabled();
      await user.type(screen.getByLabelText(/username/i), 'g');
      await user.type(screen.getByLabelText(/^email/i), 'g@example.com');
      await user.click(screen.getAllByRole('button', { name: /create invite/i }).at(-1)!);
      await screen.findByText(/send this link to g yourself/i);
    });

    it('resends a pending invitation and reports a rate limit', async () => {
      let resends = 0;
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string) => {
          if (url.endsWith('/resend')) {
            resends++;
            return resends === 1
              ? envelope({ email_sent: true })
              : jsonResponse(
                  {
                    error:
                      'this invitation was emailed less than a minute ago; wait a minute and try again',
                    status: 429,
                  },
                  429
                );
          }
          if (url.endsWith('/users/invites'))
            return envelope({ invites: [pending], count: 1, mail_configured: true });
          return envelope({ users: [activeUser], count: 1 });
        })
      );
      const user = userEvent.setup();
      renderWithProviders(<Users />);
      const resend = await screen.findByRole('button', {
        name: /resend invitation email to pending@example.com/i,
      });
      await user.click(resend);
      expect(
        await screen.findByText(/invitation emailed again to pending@example.com/i)
      ).toBeInTheDocument();
      await user.click(
        screen.getByRole('button', { name: /resend invitation email to pending@example.com/i })
      );
      expect(await screen.findByText(/less than a minute ago/i)).toBeInTheDocument();
    });

    it('shows accepted invites with their Cloudflare outcome', async () => {
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string) => {
          if (url.endsWith('/users/invites'))
            return envelope({
              invites: [],
              count: 0,
              accepted: [
                {
                  username: 'ok-guest',
                  email: 'ok@example.com',
                  accepted_at: '2026-10-19T12:00:00Z',
                  email_binding: 'bound',
                  sso_grant: 'granted',
                  guest_policy_status: 'added',
                },
                {
                  username: 'failed-guest',
                  email: 'failed@example.com',
                  accepted_at: '2026-10-19T12:00:00Z',
                  email_binding: 'bound',
                  sso_grant: 'granted',
                  guest_policy_status: 'failed',
                  guest_policy_detail: 'HTTP 403',
                },
                {
                  username: 'revoked-guest',
                  email: 'revoked@example.com',
                  accepted_at: '2026-10-19T12:00:00Z',
                  email_binding: 'bound',
                  sso_grant: 'granted',
                  guest_policy_status: 'removed',
                },
                {
                  username: 'manual-guest',
                  email: 'manual@example.com',
                  accepted_at: '2026-10-19T12:00:00Z',
                  email_binding: 'not_emailed',
                  guest_policy_status: 'skipped',
                },
              ],
            });
          return envelope({ users: [activeUser], count: 1 });
        })
      );
      renderWithProviders(<Users />);
      expect(await screen.findByText('Added to Cloudflare guest policy')).toBeInTheDocument();
      expect(screen.getByText('Cloudflare update failed: add manually')).toBeInTheDocument();
      expect(screen.getByText('Email not verified: not granted')).toBeInTheDocument();
      expect(
        screen.getByText('Access revoked during the update: removed from Cloudflare')
      ).toBeInTheDocument();
      expect(screen.getByText(/manual@example.com \(not verified/i)).toBeInTheDocument();
    });
  });
});
