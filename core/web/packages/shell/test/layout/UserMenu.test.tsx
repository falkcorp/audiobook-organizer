// file: core/web/packages/shell/test/layout/UserMenu.test.tsx
// version: 0.1.0
// guid: 4a7d3c91-0e58-4b26-9f13-c8e2b6a5d704
// last-edited: 2026-11-22

// The shell's UserMenu with no host callbacks: the profile and password saves go
// to core's own routes (PATCH /me, PUT /me/password), with the bodies core.json
// declares, and a refusal reaches the dialog instead of being dropped. A host's
// own callbacks, when given, still win (ABO's pre-flip binding passes its own).

import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { createShellClient } from '../../src/api/client';
import { AuthClientContext, AuthContext } from '../../src/auth/context';
import type { AuthState } from '../../src/contracts';
import { UserMenu } from '../../src/layout/UserMenu';
import { renderWithProviders } from './render';

interface Seen {
  method: string;
  path: string;
  body: unknown;
}

function setup(answer: (seen: Seen) => Response) {
  const calls: Seen[] = [];
  const client = createShellClient({
    buildId: 'test',
    shell: true,
    baseUrl: 'http://192.0.2.1',
    fetch: async (input, init) => {
      const req = input instanceof Request ? input : new Request(input, init);
      const text = await req.clone().text();
      const seen = { method: req.method, path: new URL(req.url).pathname, body: text ? JSON.parse(text) : undefined };
      calls.push(seen);
      return answer(seen);
    },
  });
  const refresh = vi.fn().mockResolvedValue(undefined);
  const auth: AuthState = {
    status: 'authenticated',
    user: { id: 'u1', username: 'robin', email: 'dick@wayne.example' },
    permissions: [],
    method: 'session',
    ownerVerified: false,
    authDisabled: false,
    setupRequired: false,
    error: null,
    refresh,
    logout: vi.fn().mockResolvedValue(undefined),
  };
  return { calls, refresh, client, auth };
}

function mount(s: ReturnType<typeof setup>, props: Parameters<typeof UserMenu>[0] = {}) {
  return renderWithProviders(
    <AuthClientContext value={s.client}>
      <AuthContext value={s.auth}>
        <UserMenu {...props} />
      </AuthContext>
    </AuthClientContext>,
  );
}

function openProfile() {
  fireEvent.click(screen.getByRole('button', { name: /robin/ }));
  fireEvent.click(screen.getByRole('menuitem', { name: 'Edit profile' }));
  return screen.getByRole('dialog');
}

function openPassword() {
  fireEvent.click(screen.getByRole('button', { name: /robin/ }));
  fireEvent.click(screen.getByRole('menuitem', { name: 'Change password' }));
  return screen.getByRole('dialog');
}

describe('UserMenu default saves', () => {
  it('PATCHes /me with the email, then refreshes auth', async () => {
    const s = setup(() => Response.json({ id: 'u1', username: 'robin', email_verified: false, roles: [], status: 'active' }));
    mount(s);
    const dialog = openProfile();
    fireEvent.change(within(dialog).getByLabelText('Email'), { target: { value: 'tim@wayne.example' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(s.calls).toEqual([{ method: 'PATCH', path: '/api/core/v1/me', body: { email: 'tim@wayne.example' } }]);
    expect(s.refresh).toHaveBeenCalledTimes(1);
  });

  it('PUTs /me/password with current and new, the body core.json declares', async () => {
    const s = setup(() => new Response(null, { status: 204 }));
    mount(s);
    const dialog = openPassword();
    fireEvent.change(within(dialog).getByLabelText('Current password'), { target: { value: 'old-signal' } });
    fireEvent.change(within(dialog).getByLabelText('New password'), { target: { value: 'new-signal-1' } });
    fireEvent.change(within(dialog).getByLabelText('Confirm new password'), { target: { value: 'new-signal-1' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(s.calls).toEqual([
      { method: 'PUT', path: '/api/core/v1/me/password', body: { current: 'old-signal', new: 'new-signal-1' } },
    ]);
  });

  it('shows core\'s refusal of a wrong current password in the dialog and keeps it open', async () => {
    const s = setup(() =>
      Response.json(
        { type: 'about:blank', title: 'Invalid', status: 422, code: 'validation_failed', request_id: 'r1', detail: 'the current password is wrong' },
        { status: 422, headers: { 'Content-Type': 'application/problem+json' } },
      ),
    );
    mount(s);
    const dialog = openPassword();
    fireEvent.change(within(dialog).getByLabelText('Current password'), { target: { value: 'wrong-signal' } });
    fireEvent.change(within(dialog).getByLabelText('New password'), { target: { value: 'new-signal-1' } });
    fireEvent.change(within(dialog).getByLabelText('Confirm new password'), { target: { value: 'new-signal-1' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/the current password is wrong/)).toBeInTheDocument();
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('uses a host callback instead of the route when one is given', async () => {
    const s = setup(() => new Response(null, { status: 500 }));
    const onSaveProfile = vi.fn().mockResolvedValue(undefined);
    mount(s, { onSaveProfile });
    const dialog = openProfile();
    fireEvent.change(within(dialog).getByLabelText('Email'), { target: { value: 'tim@wayne.example' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(onSaveProfile).toHaveBeenCalledExactlyOnceWith('tim@wayne.example'));
    expect(s.calls).toEqual([]);
  });
});
