// file: web/src/components/GuestPhoneKeys.test.tsx
// version: 1.1.0
// guid: 5e1a7c93-2d48-4b6f-a0c7-8f3d9b2e6a41
// last-edited: 2026-11-07

import { afterEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '../test/renderWithProviders';
import GuestPhoneKeys from './GuestPhoneKeys';

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });

const row = {
  user_id: 'u-guest',
  username: 'guest',
  token_id: 'tok-123',
  status: 'active',
  created_at: '2026-10-23T10:00:00Z',
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('GuestPhoneKeys', () => {
  it('renders nothing while the feature is off and no key exists', async () => {
    const fetchMock = vi.fn(async () => jsonResponse({ data: { flag_on: false, tokens: [] } }));
    vi.stubGlobal('fetch', fetchMock);
    renderWithProviders(<GuestPhoneKeys />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    // Let the response settle before asserting absence.
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText(/phone app keys/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/could not be loaded/i)).not.toBeInTheDocument();
  });

  it('renders the panel as soon as the feature is switched on, even with no key yet', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse({ data: { flag_on: true, enabled: true, tokens: [] } }))
    );
    renderWithProviders(<GuestPhoneKeys />);
    expect(await screen.findByText(/phone app keys/i)).toBeInTheDocument();
  });

  it('lists each guest key and says when Cloudflare does not report last seen', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse({ data: { flag_on: true, enabled: true, tokens: [row] } }))
    );
    renderWithProviders(<GuestPhoneKeys />);
    expect(await screen.findByText('guest')).toBeInTheDocument();
    expect(screen.getByText('tok-123')).toBeInTheDocument();
    expect(screen.getByText('Active')).toBeInTheDocument();
    expect(screen.getByText('Not reported')).toBeInTheDocument();
  });

  it('warns when the feature is on but the Cloudflare settings are incomplete', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse({ data: { flag_on: true, enabled: false, tokens: [] } }))
    );
    renderWithProviders(<GuestPhoneKeys />);
    expect(await screen.findByText(/settings they need are incomplete/i)).toBeInTheDocument();
  });

  it('revokes one guest after confirmation and reloads the list', async () => {
    let revoked = false;
    const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url.endsWith('/revoke')) {
        expect(init?.method).toBe('POST');
        expect(url).toBe('/api/v1/admin/guest-service-tokens/u-guest/revoke');
        revoked = true;
        return jsonResponse({ data: { revoked: true, user_id: 'u-guest' } });
      }
      return jsonResponse({
        data: {
          flag_on: true,
          enabled: true,
          tokens: [{ ...row, status: revoked ? 'revoked' : 'active' }],
        },
      });
    });
    vi.stubGlobal('fetch', fetchMock);
    const user = userEvent.setup();
    renderWithProviders(<GuestPhoneKeys />);
    await user.click(
      await screen.findByRole('button', { name: /revoke phone-app key for guest/i })
    );
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: 'Revoke' }));
    expect(await screen.findByText(/phone-app key revoked for guest/i)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Revoked')).toBeInTheDocument());
    expect(
      screen.queryByRole('button', { name: /revoke phone-app key for guest/i })
    ).not.toBeInTheDocument();
  });

  it('shows the server refusal when Cloudflare does not confirm the revoke', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) =>
        url.endsWith('/revoke')
          ? jsonResponse({ error: 'Cloudflare did not confirm the revoke', status: 502 }, 502)
          : jsonResponse({ data: { flag_on: true, enabled: true, tokens: [row] } })
      )
    );
    const user = userEvent.setup();
    renderWithProviders(<GuestPhoneKeys />);
    await user.click(
      await screen.findByRole('button', { name: /revoke phone-app key for guest/i })
    );
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: 'Revoke' }));
    expect(
      await within(dialog).findByText('Cloudflare did not confirm the revoke')
    ).toBeInTheDocument();
  });

  it('says the list could not be loaded instead of hiding the failure', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse({ error: 'boom', status: 500 }, 500))
    );
    renderWithProviders(<GuestPhoneKeys />);
    expect(await screen.findByText(/phone-app keys could not be loaded/i)).toBeInTheDocument();
  });

  it('shows the new revoke wording that points at Reissue', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => jsonResponse({ data: { flag_on: true, enabled: true, tokens: [row] } }))
    );
    const user = userEvent.setup();
    renderWithProviders(<GuestPhoneKeys />);
    await user.click(
      await screen.findByRole('button', { name: /revoke phone-app key for guest/i })
    );
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/use Reissue/)).toBeInTheDocument();
  });

  describe('issue and reissue', () => {
    const listBody = (tokens: unknown[]) => ({ data: { flag_on: true, enabled: true, tokens } });

    it.each(['active', 'revoked', 'failed', 'revoke_failed'])(
      'offers Reissue on a %s key',
      async (status) => {
        vi.stubGlobal(
          'fetch',
          vi.fn(async () => jsonResponse(listBody([{ ...row, status }])))
        );
        renderWithProviders(<GuestPhoneKeys />);
        expect(
          await screen.findByRole('button', { name: /^reissue phone-app key for guest$/i })
        ).toBeInTheDocument();
      }
    );

    it.each(['pending', 'minted', 'revoking'])(
      'offers neither action on a %s key',
      async (status) => {
        vi.stubGlobal(
          'fetch',
          vi.fn(async () => jsonResponse(listBody([{ ...row, status }])))
        );
        renderWithProviders(<GuestPhoneKeys />);
        await screen.findByText('guest');
        expect(screen.queryByRole('button', { name: /reissue phone-app key/i })).toBeNull();
        expect(screen.queryByRole('button', { name: /issue phone-app key/i })).toBeNull();
      }
    );

    it('lists a user with no key as "Issue key" and issues a first key', async () => {
      const noKey = { user_id: 'u-robin', username: 'robin', status: 'none' };
      let posted = '';
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string, init?: RequestInit) => {
          if (url.endsWith('/reissue')) {
            expect(init?.method).toBe('POST');
            posted = url;
            return jsonResponse({ data: { reissued: true, user_id: 'u-robin' } });
          }
          return jsonResponse(listBody([noKey]));
        })
      );
      const user = userEvent.setup();
      renderWithProviders(<GuestPhoneKeys />);
      expect(await screen.findByText('No key')).toBeInTheDocument();
      expect(screen.queryByRole('button', { name: /revoke phone-app key/i })).toBeNull();
      await user.click(
        await screen.findByRole('button', { name: 'Issue phone-app key for robin' })
      );
      const dialog = await screen.findByRole('dialog');
      expect(within(dialog).getByText(/Issue phone-app key/)).toBeInTheDocument();
      await user.click(within(dialog).getByRole('button', { name: 'Issue and email' }));
      expect(await screen.findByText(/phone-app key was emailed to robin/i)).toBeInTheDocument();
      expect(posted).toBe('/api/v1/admin/guest-service-tokens/u-robin/reissue');
    });

    it('disables the button with the server reason as tooltip when blocked', async () => {
      const reason = 'This guest has no verified email address.';
      vi.stubGlobal(
        'fetch',
        vi.fn(async () =>
          jsonResponse(listBody([{ ...row, status: 'active', reissue_blocked: reason }]))
        )
      );
      const user = userEvent.setup();
      renderWithProviders(<GuestPhoneKeys />);
      const btn = await screen.findByRole('button', { name: /reissue phone-app key for guest/i });
      expect(btn).toBeDisabled();
      await user.hover(btn.parentElement as HTMLElement);
      expect(await screen.findByText(reason)).toBeInTheDocument();
    });

    it('reissues after confirmation, says the old key is dead, and reloads', async () => {
      let posted = '';
      let reloads = 0;
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string, init?: RequestInit) => {
          if (url.endsWith('/reissue')) {
            expect(init?.method).toBe('POST');
            posted = url;
            return jsonResponse({ data: { reissued: true, user_id: 'u-guest' } });
          }
          reloads++;
          return jsonResponse(listBody([row]));
        })
      );
      const user = userEvent.setup();
      renderWithProviders(<GuestPhoneKeys />);
      await user.click(
        await screen.findByRole('button', { name: /reissue phone-app key for guest/i })
      );
      const dialog = await screen.findByRole('dialog');
      expect(within(dialog).getByText(/current key stops working/i)).toBeInTheDocument();
      await user.click(within(dialog).getByRole('button', { name: 'Reissue and email' }));
      expect(
        await screen.findByText(
          /new phone-app key was emailed to guest\. Their old key no longer works/i
        )
      ).toBeInTheDocument();
      expect(posted).toBe('/api/v1/admin/guest-service-tokens/u-guest/reissue');
      await waitFor(() => expect(reloads).toBeGreaterThanOrEqual(2));
    });

    it('shows the refusal in the dialog and reloads the list', async () => {
      let reloads = 0;
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string) => {
          if (url.endsWith('/reissue')) {
            return jsonResponse(
              { error: 'The key could not be emailed; it was revoked', status: 502 },
              502
            );
          }
          reloads++;
          return jsonResponse(listBody([row]));
        })
      );
      const user = userEvent.setup();
      renderWithProviders(<GuestPhoneKeys />);
      await user.click(
        await screen.findByRole('button', { name: /reissue phone-app key for guest/i })
      );
      const dialog = await screen.findByRole('dialog');
      await user.click(within(dialog).getByRole('button', { name: 'Reissue and email' }));
      expect(
        await within(dialog).findByText('The key could not be emailed; it was revoked')
      ).toBeInTheDocument();
      await waitFor(() => expect(reloads).toBeGreaterThanOrEqual(2));
    });

    it('shows an info notice on 202 and closes the dialog', async () => {
      vi.stubGlobal(
        'fetch',
        vi.fn(async (url: string) =>
          url.endsWith('/reissue')
            ? jsonResponse({ data: { status: 'running' } }, 202)
            : jsonResponse(listBody([row]))
        )
      );
      const user = userEvent.setup();
      renderWithProviders(<GuestPhoneKeys />);
      await user.click(
        await screen.findByRole('button', { name: /reissue phone-app key for guest/i })
      );
      const dialog = await screen.findByRole('dialog');
      await user.click(within(dialog).getByRole('button', { name: 'Reissue and email' }));
      expect(await screen.findByText(/still working/i)).toBeInTheDocument();
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    });
  });
});
